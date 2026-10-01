package catalog

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/housefold/runtime/internal/durable"
	"github.com/housefold/runtime/internal/packageverify"
)

type serverFixture struct {
	mu          sync.Mutex
	objects     map[string][]byte
	credentials bool
	server      *httptest.Server
	key         ed25519.PrivateKey
	now         time.Time
	catalog     packageverify.Signed
	manifests   []packageverify.Signed
	artifact    []byte
}

func signature(key ed25519.PrivateKey, value any) packageverify.Signed {
	raw, _ := json.Marshal(value)
	return packageverify.Signed{Data: raw, Signature: ed25519.Sign(key, raw)}
}
func fixture(t *testing.T, declarations ...packageverify.Manifest) (*Client, *serverFixture, string) {
	t.Helper()
	f := &serverFixture{objects: map[string][]byte{}, key: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)), now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), artifact: []byte("synthetic native bytes; execution tested by estate")}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			f.credentials = true
		}
		raw, ok := f.objects[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if string(raw) == "redirect" {
			http.Redirect(w, r, "/target", http.StatusFound)
			return
		}
		_, _ = w.Write(raw)
	}))
	t.Cleanup(f.server.Close)
	for _, m := range declarations {
		m.Schema = 1
		m.Arch = "amd64"
		m.RuntimeMajor = 1
		m.ProtocolMajor = 1
		m.ArtifactDigest = packageverify.Digest(f.artifact)
		signed := signature(f.key, m)
		f.manifests = append(f.manifests, signed)
		raw, _ := json.Marshal(signed)
		digest := packageverify.Digest(signed.Data)
		f.objects["/objects/"+digest+".manifest.json"] = raw
		f.objects["/objects/"+digest+".artifact.sig"] = ed25519.Sign(f.key, packageverify.ArtifactStatement(digest, m.ArtifactDigest))
		f.objects["/objects/"+m.ArtifactDigest+".bin"] = f.artifact
	}
	f.publish(1)
	path := filepath.Join(t.TempDir(), "catalog")
	c := newClient(durable.NewFile(path), packageverify.Authority{KeyID: "synthetic", PublicKey: f.key.Public().(ed25519.PublicKey)}, "amd64", f.server.URL+"/", f.server.Client())
	if err := c.Open(); err != nil {
		t.Fatal(err)
	}
	return c, f, path
}
func (f *serverFixture) publish(sequence uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	catalog := packageverify.Catalog{Schema: 1, KeyID: "synthetic", Sequence: sequence, Expires: f.now.Add(time.Hour), Entries: []packageverify.Entry{}}
	for _, signed := range f.manifests {
		var m packageverify.Manifest
		_ = json.Unmarshal(signed.Data, &m)
		catalog.Entries = append(catalog.Entries, packageverify.Entry{Identity: m.Identity, Version: m.Version, Arch: m.Arch, ArtifactDigest: m.ArtifactDigest, ManifestDigest: packageverify.Digest(signed.Data)})
	}
	f.catalog = signature(f.key, catalog)
	raw, _ := json.Marshal(f.catalog)
	f.objects["/catalog.json"] = raw
}
func TestSignedRemoteReviewDependencyPlanAndOfflineCache(t *testing.T) {
	c, f, path := fixture(t, packageverify.Manifest{Identity: "dependent", Version: "1.0.0", Dependencies: []packageverify.Dependency{{Identity: "dependency", Version: "1.0.0"}, {Identity: "optional", Version: "1.0.0", Optional: true}}}, packageverify.Manifest{Identity: "dependency", Version: "1.0.0"})
	if err := c.Refresh(context.Background(), f.now); err != nil {
		t.Fatal(err)
	}
	digest, review, err := c.Review("dependent", "1.0.0", f.now)
	if err != nil || len(review) != 2 || review[0].Identity != "dependency" {
		t.Fatal(review, err)
	}
	packages, err := c.Prepare(context.Background(), "dependent", "1.0.0", digest, f.now)
	if err != nil || len(packages) != 2 {
		t.Fatal(err)
	}
	packages[0].Artifact[0] = 'X'
	if string(f.artifact) == string(packages[0].Artifact) {
		t.Fatal("shared bytes")
	}
	if f.credentials {
		t.Fatal("credential attached to catalog request")
	}
	f.server.Close()
	if err = c.Refresh(context.Background(), f.now); err != ErrUnavailable {
		t.Fatal(err)
	}
	restarted := newClient(durable.NewFile(path), c.authority, c.arch, c.base, c.http)
	if err = restarted.Open(); err != nil {
		t.Fatal(err)
	}
	snapshot := restarted.Snapshot(f.now)
	if snapshot.Status != "cached" || len(snapshot.Items) != 2 || snapshot.Sequence != 1 {
		t.Fatal(snapshot)
	}
	snapshot.Items[0].Manifest.Dependencies = nil
	if _, review, err = restarted.Review("dependent", "1.0.0", f.now); err != nil || len(review) != 2 {
		t.Fatal("mutated cached declarations", err)
	}
	if _, _, err = restarted.Review("dependent", "1.0.0", f.now.Add(2*time.Hour)); err != ErrUnavailable {
		t.Fatal("expired catalog admitted install", err)
	}
}
func TestReplayEquivocationRedirectAndTamperPreserveVerifiedCache(t *testing.T) {
	for _, fault := range []string{"replay", "equivocation", "redirect", "signature", "manifest", "oversize"} {
		t.Run(fault, func(t *testing.T) {
			c, f, path := fixture(t, packageverify.Manifest{Identity: "synthetic", Version: "1.0.0"})
			f.publish(2)
			if err := c.Refresh(context.Background(), f.now); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			switch fault {
			case "replay":
				f.publish(1)
			case "equivocation":
				f.mu.Lock()
				var cat packageverify.Catalog
				_ = json.Unmarshal(f.catalog.Data, &cat)
				cat.Expires = cat.Expires.Add(time.Minute)
				raw, _ := json.Marshal(signature(f.key, cat))
				f.objects["/catalog.json"] = raw
				f.mu.Unlock()
			case "redirect":
				f.mu.Lock()
				f.objects["/catalog.json"] = []byte("redirect")
				f.mu.Unlock()
			case "signature":
				f.mu.Lock()
				s := f.catalog
				s.Signature = append([]byte(nil), s.Signature...)
				s.Signature[0] ^= 1
				raw, _ := json.Marshal(s)
				f.objects["/catalog.json"] = raw
				f.mu.Unlock()
			case "manifest":
				f.publish(3)
				f.mu.Lock()
				var m packageverify.Manifest
				_ = json.Unmarshal(f.manifests[0].Data, &m)
				m.Priority = "essential"
				signed := signature(f.key, m)
				var cat packageverify.Catalog
				_ = json.Unmarshal(f.catalog.Data, &cat)
				cat.Entries[0].ManifestDigest = packageverify.Digest(signed.Data)
				raw, _ := json.Marshal(signature(f.key, cat))
				f.objects["/catalog.json"] = raw
				raw, _ = json.Marshal(signed)
				f.objects["/objects/"+packageverify.Digest(signed.Data)+".manifest.json"] = raw
				f.mu.Unlock()
			case "oversize":
				f.mu.Lock()
				f.objects["/catalog.json"] = make([]byte, 2*packageverify.MaxMetadata+4097)
				f.mu.Unlock()
			}
			if err := c.Refresh(context.Background(), f.now); err != ErrUnavailable {
				t.Fatal("unsafe refresh", err)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) || c.Snapshot(f.now).Sequence != 2 {
				t.Fatal("bad remote input replaced cache")
			}
		})
	}
}
func TestExplicitReviewAndInterruptedDownload(t *testing.T) {
	c, f, _ := fixture(t, packageverify.Manifest{Identity: "synthetic", Version: "1.0.0"})
	if err := c.Refresh(context.Background(), f.now); err != nil {
		t.Fatal(err)
	}
	digest, _, err := c.Review("synthetic", "1.0.0", f.now)
	if err != nil {
		t.Fatal(err)
	}
	f.publish(2)
	if err = c.Refresh(context.Background(), f.now); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Prepare(context.Background(), "synthetic", "1.0.0", digest, f.now); err != ErrReview {
		t.Fatal("stale consent", err)
	}
	digest, _, _ = c.Review("synthetic", "1.0.0", f.now)
	f.mu.Lock()
	f.objects["/objects/"+packageverify.Digest(f.artifact)+".bin"] = []byte("tampered")
	f.mu.Unlock()
	if _, err = c.Prepare(context.Background(), "synthetic", "1.0.0", digest, f.now); err != ErrUnavailable {
		t.Fatal("artifact tamper", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = c.Prepare(ctx, "synthetic", "1.0.0", digest, f.now); err != ErrUnavailable {
		t.Fatal("canceled download", err)
	}
	if c.Snapshot(f.now).Sequence != 2 {
		t.Fatal("download mutated metadata")
	}
}
func TestEmptyCatalogCorruptionAndMissingAuthority(t *testing.T) {
	c, f, path := fixture(t)
	if err := c.Refresh(context.Background(), f.now); err != nil {
		t.Fatal(err)
	}
	if len(c.Snapshot(f.now).Items) != 0 {
		t.Fatal("functional module preinstalled")
	}
	if err := os.WriteFile(path, []byte("preserve corrupt cache"), 0600); err != nil {
		t.Fatal(err)
	}
	restarted := newClient(durable.NewFile(path), c.authority, c.arch, c.base, c.http)
	if err := restarted.Open(); err != ErrUnavailable || restarted.Snapshot(f.now).Status != "corrupt" {
		t.Fatal(err)
	}
	if err := restarted.Refresh(context.Background(), f.now); err != ErrUnavailable {
		t.Fatal("corrupt cache overwritten", err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "preserve corrupt cache" {
		t.Fatal("discarded corruption")
	}
	unconfigured := New(durable.NewFile(filepath.Join(t.TempDir(), "absent")), "amd64")
	if unconfigured.Snapshot(f.now).Status != "unconfigured" || unconfigured.Open() != ErrUnavailable {
		t.Fatal("missing authority silently trusted")
	}
}
func TestDependencyCyclesAndRequiredMissingRejectReview(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		declarations := []packageverify.Manifest{{Identity: "a", Version: "1.0.0", Dependencies: []packageverify.Dependency{{Identity: "b", Version: "1.0.0"}}}}
		if cycle {
			declarations = append(declarations, packageverify.Manifest{Identity: "b", Version: "1.0.0", Dependencies: []packageverify.Dependency{{Identity: "a", Version: "1.0.0"}}})
		}
		c, f, _ := fixture(t, declarations...)
		if err := c.Refresh(context.Background(), f.now); err != nil {
			t.Fatal(err)
		}
		if _, _, err := c.Review("a", "1.0.0", f.now); err != ErrDependency {
			t.Fatal(err)
		}
	}
}
