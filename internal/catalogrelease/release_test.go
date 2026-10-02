package catalogrelease

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/housefold/runtime/internal/packageverify"
)

func fixture(t *testing.T) (string, Plan, ed25519.PrivateKey, time.Time) {
	t.Helper()
	root := t.TempDir()
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	raw := make([]byte, 40)
	copy(raw, "\x7fELF")
	raw[4] = 2
	raw[5] = 1
	raw[18] = 62
	copy(raw[20:], "SYNTHETIC_ONLY")
	if err := os.WriteFile(filepath.Join(root, "reference.bin"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	m := packageverify.Manifest{Schema: 1, Identity: "synthetic", Version: "1.0.0", Arch: "amd64", RuntimeMajor: 1, ProtocolMajor: 1, Capabilities: []string{"ha.observe"}}
	return root, Plan{KeyID: "synthetic", Sequence: 1, Expires: now.Add(time.Hour), Packages: []Package{{m, "reference.bin"}}}, key, now
}
func encode(p Plan) []byte { raw, _ := json.Marshal(p); return raw }
func TestSignedCatalogObjectsVerifyDeterministicAndKeyNeverPublished(t *testing.T) {
	root, plan, key, now := fixture(t)
	files, err := Prepare(root, encode(plan), key, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Prepare(root, encode(plan), key, nil, now)
	if err != nil || len(files) != len(again) {
		t.Fatal(err)
	}
	for name, raw := range files {
		if !bytes.Equal(raw, again[name]) || bytes.Contains(raw, key) {
			t.Fatal("non-deterministic or private key in distribution", name)
		}
	}
	var cat packageverify.Signed
	if json.Unmarshal(files["catalog.json"], &cat) != nil {
		t.Fatal("catalog envelope")
	}
	authority := packageverify.Authority{KeyID: "synthetic", PublicKey: key.Public().(ed25519.PublicKey)}
	catalog, err := packageverify.VerifyCatalog(authority, cat, now)
	if err != nil {
		t.Fatal(err)
	}
	e := catalog.Entries[0]
	var m packageverify.Signed
	_ = json.Unmarshal(files["objects/"+e.ManifestDigest+".manifest.json"], &m)
	if _, err = packageverify.Verify(authority, cat, m, files["objects/"+e.ArtifactDigest+".bin"], files["objects/"+e.ManifestDigest+".artifact.sig"], packageverify.Expected{Identity: "synthetic", Version: "1.0.0", Arch: "amd64", RuntimeMajor: 1, ProtocolMajor: 1}, now); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "catalog")
	if err = Write(out, files); err != nil {
		t.Fatal(err)
	}
	if Write(out, files) == nil {
		t.Fatal("existing release replaced")
	}
}
func TestCatalogReleaseRejectsTamperRollbackImmutabilityAndUnsafeInputs(t *testing.T) {
	root, plan, key, now := fixture(t)
	files, err := Prepare(root, encode(plan), key, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	var previous packageverify.Signed
	_ = json.Unmarshal(files["catalog.json"], &previous)
	if _, err = Prepare(root, encode(plan), key, &previous, now); err == nil {
		t.Fatal("sequence rollback accepted")
	}
	plan.Sequence = 2
	plan.Packages[0].Manifest.Capabilities = append(plan.Packages[0].Manifest.Capabilities, "ha.actions")
	if _, err = Prepare(root, encode(plan), key, &previous, now); err == nil {
		t.Fatal("published version changed declaration")
	}
	plan.Packages[0].Manifest.Version = "2.0.0"
	if _, err = Prepare(root, encode(plan), key, &previous, now); err != nil {
		t.Fatal("independent compatible module release rejected", err)
	}
	previous.Signature[0] ^= 1
	if _, err = Prepare(root, encode(plan), key, &previous, now); err == nil {
		t.Fatal("tampered history accepted")
	}
	if _, err = Prepare(root, encode(plan), nil, nil, now); err == nil {
		t.Fatal("missing custody")
	}
	for _, path := range []string{"../foreign", "/tmp/foreign", "reference.bin/../reference.bin"} {
		bad := plan
		bad.Packages = append([]Package(nil), plan.Packages...)
		bad.Packages[0].Artifact = path
		if _, err = Prepare(root, encode(bad), key, nil, now); err == nil {
			t.Fatal("unsafe artifact", path)
		}
	}
	linked := filepath.Join(root, "linked")
	if err = os.Symlink("reference.bin", linked); err != nil {
		t.Fatal(err)
	}
	plan.Packages[0].Artifact = "linked"
	if _, err = Prepare(root, encode(plan), key, nil, now); err == nil {
		t.Fatal("linked artifact accepted")
	}
	plan.Packages[0].Artifact = "reference.bin"
	plan.Packages[0].Manifest.Arch = "aarch64"
	if _, err = Prepare(root, encode(plan), key, nil, now); err == nil {
		t.Fatal("wrong ELF target accepted")
	}
	plan.Packages[0].Manifest.Arch = "amd64"
	plan.Packages[0].Manifest.RuntimeMajor = 2
	if _, err = Prepare(root, encode(plan), key, nil, now); err == nil {
		t.Fatal("incompatible release accepted")
	}
}
