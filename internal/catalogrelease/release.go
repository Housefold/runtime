// Package catalogrelease prepares offline signed official catalog distributions.
// It has no network, deployment or Runtime authority; key custody remains external.
package catalogrelease

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/housefold/runtime/internal/packageverify"
)

var ErrRelease = errors.New("invalid catalog release")

const MaxReleaseBytes = 128 << 20
const MaxPlanBytes = 2 << 20

type Package struct {
	Manifest packageverify.Manifest `json:"manifest"`
	Artifact string                 `json:"artifact"`
}
type Plan struct {
	KeyID    string    `json:"key_id"`
	Sequence uint64    `json:"sequence"`
	Expires  time.Time `json:"expires"`
	Packages []Package `json:"packages"`
}

// ReadOwned rejects links, special files, traversal and excessive input before
// reading release inputs. No artifact path can reference the external key file.
func ReadOwned(root, path string, limit int64) ([]byte, error) {
	if filepath.IsAbs(path) || path == "" || path != filepath.Clean(path) {
		return nil, ErrRelease
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == "." || part == ".." {
			return nil, ErrRelease
		}
	}
	at := root
	info, err := os.Lstat(at)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrRelease
	}
	parts := strings.Split(path, string(filepath.Separator))
	for i, part := range parts {
		at = filepath.Join(at, part)
		info, err = os.Lstat(at)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrRelease
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, ErrRelease
		}
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
		return nil, ErrRelease
	}
	file, err := os.Open(at)
	if err != nil {
		return nil, ErrRelease
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return nil, ErrRelease
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, ErrRelease
	}
	return raw, nil
}
func Prepare(root string, planRaw []byte, key ed25519.PrivateKey, previous *packageverify.Signed, now time.Time) (map[string][]byte, error) {
	if len(planRaw) > MaxPlanBytes || len(key) != ed25519.PrivateKeySize || !bytes.Equal(ed25519.NewKeyFromSeed(key.Seed()), key) {
		return nil, ErrRelease
	}
	var plan Plan
	decoder := json.NewDecoder(bytes.NewReader(planRaw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&plan) != nil || !json.Valid(planRaw) || len(plan.Packages) > packageverify.MaxEntries || plan.KeyID == "" || plan.Sequence == 0 || !plan.Expires.After(now) || plan.Expires.After(now.Add(30*24*time.Hour)) {
		return nil, ErrRelease
	}
	authority := packageverify.Authority{KeyID: plan.KeyID, PublicKey: key.Public().(ed25519.PublicKey)}
	old := packageverify.Catalog{}
	if previous != nil {
		if json.Unmarshal(previous.Data, &old) != nil {
			return nil, ErrRelease
		}
		checked, err := packageverify.VerifyCatalog(authority, *previous, old.Expires.Add(-time.Nanosecond))
		if err != nil || plan.Sequence <= checked.Sequence {
			return nil, ErrRelease
		}
		old = checked
	}
	type prepared struct {
		m        packageverify.Manifest
		signed   packageverify.Signed
		artifact []byte
	}
	packages := []prepared{}
	total := 0
	cat := packageverify.Catalog{Schema: 1, KeyID: plan.KeyID, Sequence: plan.Sequence, Expires: plan.Expires.UTC()}
	seen := map[string]bool{}
	for _, p := range plan.Packages {
		raw, err := ReadOwned(root, p.Artifact, packageverify.MaxArtifact)
		if err != nil {
			return nil, err
		}
		total += len(raw)
		if total > MaxReleaseBytes {
			return nil, ErrRelease
		}
		m := p.Manifest
		if m.ArtifactDigest != "" && m.ArtifactDigest != packageverify.Digest(raw) {
			return nil, ErrRelease
		}
		m.ArtifactDigest = packageverify.Digest(raw)
		machine := uint16(62)
		if m.Arch == "aarch64" {
			machine = 183
		}
		if len(raw) < 20 || string(raw[:4]) != "\x7fELF" || raw[4] != 2 || raw[5] != 1 || uint16(raw[18])|uint16(raw[19])<<8 != machine {
			return nil, ErrRelease
		}
		declaration, err := json.Marshal(m)
		if err != nil {
			return nil, ErrRelease
		}
		signed := packageverify.Signed{Data: declaration, Signature: ed25519.Sign(key, declaration)}
		identity := m.Identity + "/" + m.Version + "/" + m.Arch
		if seen[identity] {
			return nil, ErrRelease
		}
		seen[identity] = true
		md := packageverify.Digest(declaration)
		for _, prior := range old.Entries {
			if prior.Identity == m.Identity && prior.Version == m.Version && prior.Arch == m.Arch && (prior.ManifestDigest != md || prior.ArtifactDigest != m.ArtifactDigest) {
				return nil, ErrRelease
			}
		}
		cat.Entries = append(cat.Entries, packageverify.Entry{Identity: m.Identity, Version: m.Version, Arch: m.Arch, ManifestDigest: md, ArtifactDigest: m.ArtifactDigest})
		packages = append(packages, prepared{m, signed, raw})
	}
	sort.Slice(cat.Entries, func(i, j int) bool {
		a, b := cat.Entries[i], cat.Entries[j]
		return a.Identity+"/"+a.Version+"/"+a.Arch < b.Identity+"/"+b.Version+"/"+b.Arch
	})
	catRaw, _ := json.Marshal(cat)
	signedCat := packageverify.Signed{Data: catRaw, Signature: ed25519.Sign(key, catRaw)}
	if _, err := packageverify.VerifyCatalog(authority, signedCat, now); err != nil {
		return nil, ErrRelease
	}
	files := map[string][]byte{}
	encoded, _ := json.Marshal(signedCat)
	files["catalog.json"] = encoded
	for _, p := range packages {
		md := packageverify.Digest(p.signed.Data)
		signature := ed25519.Sign(key, packageverify.ArtifactStatement(md, p.m.ArtifactDigest))
		if _, err := packageverify.Verify(authority, signedCat, p.signed, p.artifact, signature, packageverify.Expected{Identity: p.m.Identity, Version: p.m.Version, Arch: p.m.Arch, RuntimeMajor: 1, ProtocolMajor: 1}, now); err != nil {
			return nil, ErrRelease
		}
		encoded, _ = json.Marshal(p.signed)
		files["objects/"+md+".manifest.json"] = encoded
		files["objects/"+md+".artifact.sig"] = signature
		files["objects/"+p.m.ArtifactDigest+".bin"] = p.artifact
	}
	return files, nil
}

// Write creates a new distribution atomically; never removes/replaces an
// existing release or includes private signing input in the published tree.
func Write(out string, files map[string][]byte) error {
	if !filepath.IsAbs(out) {
		return ErrRelease
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		return ErrRelease
	}
	parent := filepath.Dir(out)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrRelease
	}
	stage, err := os.MkdirTemp(parent, ".catalog-release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err = os.Mkdir(filepath.Join(stage, "objects"), 0755); err != nil {
		return err
	}
	for path, raw := range files {
		if path != "catalog.json" && (!strings.HasPrefix(path, "objects/") || filepath.Clean(path) != path || strings.Contains(strings.TrimPrefix(path, "objects/"), "/")) {
			return ErrRelease
		}
		if err = os.WriteFile(filepath.Join(stage, path), raw, 0644); err != nil {
			return err
		}
	}
	if err = os.Chmod(stage, 0755); err != nil {
		return err
	}
	return os.Rename(stage, out)
}
