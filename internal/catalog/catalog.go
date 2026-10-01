// Package catalog owns one official, signed, bounded remote catalog. It never
// launches code or changes module desired state: explicit reviewed install does.
package catalog

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/housefold/runtime/internal/durable"
	"github.com/housefold/runtime/internal/packageverify"
)

const OfficialURL = "https://housefold.github.io/modules/v1/"
const MaxCache = 2 << 20
const MaxTransfer = 64 << 20
const MaxPackages = 15

var ErrUnavailable = errors.New("official catalog unavailable")
var ErrReview = errors.New("catalog changed; review required")
var ErrDependency = errors.New("catalog dependency unavailable or cyclic")

// Filled only from a provisioned Housefold signing authority. A missing key is
// explicitly unavailable, never replaced with a synthetic or remotely fetched key.
var officialPublicKey = ""

func OfficialAuthority() packageverify.Authority {
	raw, err := hex.DecodeString(officialPublicKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return packageverify.Authority{}
	}
	return packageverify.Authority{KeyID: "housefold-modules-v1", PublicKey: raw}
}

type cache struct {
	Version    int
	Sequence   uint64
	AcceptedAt time.Time
	Catalog    packageverify.Signed
	Manifests  map[string]packageverify.Signed
}
type Item struct {
	Manifest   packageverify.Manifest
	Compatible bool
}
type Snapshot struct {
	Status   string
	Sequence uint64
	Digest   string
	Expires  time.Time
	Items    []Item
}
type Package struct {
	Catalog, Manifest   packageverify.Signed
	Artifact, Signature []byte
}
type Client struct {
	op        sync.Mutex
	mu        sync.Mutex
	authority packageverify.Authority
	arch      string
	store     durable.Store
	http      *http.Client
	base      string
	data      cache
	status    string
	poisoned  bool
}

func New(store durable.Store, arch string) *Client {
	return newClient(store, OfficialAuthority(), arch, OfficialURL, &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrUnavailable }})
}
func newClient(store durable.Store, authority packageverify.Authority, arch, base string, client *http.Client) *Client {
	bounded := *client
	bounded.Timeout = 10 * time.Second
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return ErrUnavailable }
	status := "initializing"
	if len(authority.PublicKey) != ed25519.PublicKeySize || authority.KeyID == "" {
		status = "unconfigured"
	}
	return &Client{store: store, authority: authority, arch: arch, base: base, http: &bounded, status: status}
}
func (c *Client) Run(ctx context.Context) {
	if c.Open() != nil {
		return
	}
	_ = c.Refresh(ctx, time.Now().UTC())
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			_ = c.Refresh(ctx, now.UTC())
		}
	}
}
func copyCache(d cache) cache {
	raw, _ := json.Marshal(d)
	var out cache
	_ = json.Unmarshal(raw, &out)
	return out
}
func (c *Client) Open() error {
	c.op.Lock()
	defer c.op.Unlock()
	if len(c.authority.PublicKey) != ed25519.PublicKeySize {
		return ErrUnavailable
	}
	raw, err := c.store.Load()
	if errors.Is(err, os.ErrNotExist) {
		c.mu.Lock()
		c.status = "unavailable"
		c.mu.Unlock()
		return nil
	}
	var d cache
	if err != nil || len(raw) > MaxCache || json.Unmarshal(raw, &d) != nil || d.Version != 1 || d.AcceptedAt.IsZero() || d.Manifests == nil {
		return c.corrupt()
	}
	catalog, err := packageverify.VerifyCatalog(c.authority, d.Catalog, d.AcceptedAt)
	if err != nil || catalog.Sequence != d.Sequence || len(d.Manifests) != len(catalog.Entries) {
		return c.corrupt()
	}
	for _, entry := range catalog.Entries {
		signed := d.Manifests[entry.ManifestDigest]
		if packageverify.Digest(signed.Data) != entry.ManifestDigest {
			return c.corrupt()
		}
		if review, inspectErr := packageverify.Inspect(c.authority, d.Catalog, signed, d.AcceptedAt); inspectErr != nil || !matches(review, entry) {
			return c.corrupt()
		}
	}
	c.mu.Lock()
	c.data = copyCache(d)
	c.status = "cached"
	c.mu.Unlock()
	return nil
}
func (c *Client) corrupt() error {
	c.mu.Lock()
	c.status = "corrupt"
	c.poisoned = true
	c.mu.Unlock()
	return ErrUnavailable
}
func (c *Client) fetch(ctx context.Context, path string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	// No Supervisor credential or inherited authorization is ever attached.
	response, err := c.http.Do(request)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > limit {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, ErrUnavailable
	}
	return raw, nil
}
func decodeSigned(raw []byte) (packageverify.Signed, error) {
	var signed packageverify.Signed
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&signed) != nil || !json.Valid(raw) || len(signed.Data) > packageverify.MaxMetadata || len(signed.Signature) != ed25519.SignatureSize {
		return signed, ErrUnavailable
	}
	return signed, nil
}
func (c *Client) Refresh(ctx context.Context, now time.Time) error {
	c.op.Lock()
	defer c.op.Unlock()
	c.mu.Lock()
	old := copyCache(c.data)
	blocked := c.poisoned
	c.mu.Unlock()
	if blocked || len(c.authority.PublicKey) != ed25519.PublicKeySize {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := c.fetch(ctx, "catalog.json", 2*packageverify.MaxMetadata+4096)
	if err != nil {
		return c.unavailable(err)
	}
	signed, err := decodeSigned(raw)
	if err != nil {
		return c.unavailable(err)
	}
	authority := c.authority
	authority.MinimumSequence = max(authority.MinimumSequence, old.Sequence)
	catalog, err := packageverify.VerifyCatalog(authority, signed, now)
	if err != nil || (catalog.Sequence == old.Sequence && packageverify.Digest(signed.Data) != packageverify.Digest(old.Catalog.Data)) {
		return c.unavailable(ErrUnavailable)
	}
	d := cache{Version: 1, Sequence: catalog.Sequence, AcceptedAt: now.UTC(), Catalog: signed, Manifests: map[string]packageverify.Signed{}}
	for _, entry := range catalog.Entries {
		// Immutable content addressing avoids URLs supplied by catalog metadata.
		manifest := old.Manifests[entry.ManifestDigest]
		if len(manifest.Data) == 0 {
			raw, err = c.fetch(ctx, "objects/"+entry.ManifestDigest+".manifest.json", 2*packageverify.MaxMetadata+4096)
			if err != nil {
				return c.unavailable(err)
			}
			manifest, err = decodeSigned(raw)
		}
		if err != nil || packageverify.Digest(manifest.Data) != entry.ManifestDigest {
			return c.unavailable(ErrUnavailable)
		}
		if review, inspectErr := packageverify.Inspect(c.authority, signed, manifest, now); inspectErr != nil || !matches(review, entry) {
			return c.unavailable(ErrUnavailable)
		}
		// A signed version is immutable, including declarations, across refreshes.
		for _, prior := range old.Manifests {
			var previous, next packageverify.Manifest
			_ = json.Unmarshal(prior.Data, &previous)
			_ = json.Unmarshal(manifest.Data, &next)
			if previous.Identity == next.Identity && previous.Version == next.Version && previous.Arch == next.Arch && packageverify.Digest(prior.Data) != entry.ManifestDigest {
				return c.unavailable(ErrUnavailable)
			}
		}
		d.Manifests[entry.ManifestDigest] = manifest
	}
	raw, err = json.Marshal(d)
	if err != nil || len(raw) > MaxCache {
		return c.unavailable(ErrUnavailable)
	}
	if err = c.store.Save(raw); err != nil {
		if errors.Is(err, durable.ErrUncertain) {
			return c.corrupt()
		}
		return c.unavailable(err)
	}
	c.mu.Lock()
	c.data = copyCache(d)
	c.status = "fresh"
	c.mu.Unlock()
	return nil
}

func matches(review packageverify.Review, entry packageverify.Entry) bool {
	return review.Identity == entry.Identity && review.Version == entry.Version && review.Architecture == entry.Arch && review.ArtifactDigest == entry.ArtifactDigest
}
func (c *Client) unavailable(err error) error {
	c.mu.Lock()
	if c.data.Sequence > 0 {
		c.status = "cached"
	} else {
		c.status = "unavailable"
	}
	c.mu.Unlock()
	return ErrUnavailable
}
func compatible(m packageverify.Manifest, arch string) bool {
	return m.Arch == arch && m.RuntimeMajor == 1 && m.RuntimeMinMinor == 0 && m.ProtocolMajor == 1 && m.ProtocolMinMinor == 0
}
func (c *Client) Snapshot(now time.Time) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := Snapshot{Status: c.status, Sequence: c.data.Sequence, Digest: packageverify.Digest(c.data.Catalog.Data)}
	var catalog packageverify.Catalog
	_ = json.Unmarshal(c.data.Catalog.Data, &catalog)
	out.Expires = catalog.Expires
	if !out.Expires.IsZero() && !now.Before(out.Expires) && !c.poisoned {
		out.Status = "expired"
	}
	for _, signed := range c.data.Manifests {
		var m packageverify.Manifest
		_ = json.Unmarshal(signed.Data, &m)
		if m.Arch == c.arch {
			out.Items = append(out.Items, Item{Manifest: m, Compatible: compatible(m, c.arch)})
		}
	}
	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i].Manifest, out.Items[j].Manifest
		if a.Identity == b.Identity {
			return a.Version < b.Version
		}
		return a.Identity < b.Identity
	})
	return out
}
func (c *Client) plan(d cache, id, version string) ([]packageverify.Signed, error) {
	byID := map[string]packageverify.Signed{}
	for _, signed := range d.Manifests {
		var m packageverify.Manifest
		_ = json.Unmarshal(signed.Data, &m)
		if m.Arch == c.arch {
			byID[m.Identity+"\x00"+m.Version] = signed
		}
	}
	visiting := map[string]int{}
	out := []packageverify.Signed{}
	var visit func(string, string) error
	visit = func(id, version string) error {
		key := id + "\x00" + version
		if visiting[key] == 1 {
			return ErrDependency
		}
		if visiting[key] == 2 {
			return nil
		}
		signed, ok := byID[key]
		if !ok {
			return ErrDependency
		}
		var m packageverify.Manifest
		_ = json.Unmarshal(signed.Data, &m)
		if !compatible(m, c.arch) {
			return ErrDependency
		}
		visiting[key] = 1
		for _, dep := range m.Dependencies {
			if !dep.Optional {
				if err := visit(dep.Identity, dep.Version); err != nil {
					return err
				}
			}
		}
		visiting[key] = 2
		out = append(out, signed)
		if len(out) > MaxPackages {
			return ErrDependency
		}
		return nil
	}
	if err := visit(id, version); err != nil {
		return nil, err
	}
	return out, nil
}

// Review returns the entire signed dependency declaration set for explicit admin
// review. Its catalog digest must accompany install; refresh cannot swap consent.
func (c *Client) Review(id, version string, now time.Time) (string, []packageverify.Manifest, error) {
	c.mu.Lock()
	d := copyCache(c.data)
	blocked := c.poisoned
	c.mu.Unlock()
	if blocked {
		return "", nil, ErrUnavailable
	}
	if _, err := packageverify.VerifyCatalog(c.authority, d.Catalog, now); err != nil {
		return "", nil, ErrUnavailable
	}
	plan, err := c.plan(d, id, version)
	if err != nil {
		return "", nil, err
	}
	out := []packageverify.Manifest{}
	for _, signed := range plan {
		var m packageverify.Manifest
		_ = json.Unmarshal(signed.Data, &m)
		out = append(out, m)
	}
	return packageverify.Digest(d.Catalog.Data), out, nil
}
func (c *Client) Prepare(ctx context.Context, id, version, reviewedDigest string, now time.Time) ([]Package, error) {
	c.op.Lock()
	defer c.op.Unlock()
	c.mu.Lock()
	d := copyCache(c.data)
	blocked := c.poisoned
	c.mu.Unlock()
	if blocked {
		return nil, ErrUnavailable
	}
	if reviewedDigest != packageverify.Digest(d.Catalog.Data) {
		return nil, ErrReview
	}
	if _, err := packageverify.VerifyCatalog(c.authority, d.Catalog, now); err != nil {
		return nil, ErrUnavailable
	}
	plan, err := c.plan(d, id, version)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out := []Package{}
	total := 0
	for _, manifest := range plan {
		var m packageverify.Manifest
		_ = json.Unmarshal(manifest.Data, &m)
		artifact, err := c.fetch(ctx, "objects/"+m.ArtifactDigest+".bin", int64(min(packageverify.MaxArtifact, MaxTransfer-total)))
		if err != nil {
			return nil, err
		}
		total += len(artifact)
		signature, err := c.fetch(ctx, "objects/"+packageverify.Digest(manifest.Data)+".artifact.sig", ed25519.SignatureSize)
		if err != nil {
			return nil, err
		}
		if _, err = packageverify.Verify(c.authority, d.Catalog, manifest, artifact, signature, packageverify.Expected{Identity: m.Identity, Version: m.Version, Arch: c.arch, RuntimeMajor: 1, ProtocolMajor: 1}, now); err != nil {
			return nil, ErrUnavailable
		}
		out = append(out, Package{Catalog: d.Catalog, Manifest: manifest, Artifact: artifact, Signature: signature})
	}
	return out, nil
}
