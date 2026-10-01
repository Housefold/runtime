package packageverify

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"
)

type fixture struct {
	authority           Authority
	catalog, manifest   Signed
	artifact, signature []byte
	expected            Expected
	now                 time.Time
	key                 ed25519.PrivateKey
}

func sign(key ed25519.PrivateKey, value any) Signed {
	raw, _ := json.Marshal(value)
	return Signed{Data: raw, Signature: ed25519.Sign(key, raw)}
}
func makeFixture() fixture {
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	artifact := []byte("synthetic non-executable artifact")
	m := Manifest{Schema: 1, Identity: "synthetic", Version: "1.2.3", Arch: "amd64", RuntimeMajor: 1, ProtocolMajor: 1, Capabilities: []string{"ha.observe", "ha.actions"}, ArtifactDigest: Digest(artifact)}
	manifest := sign(key, m)
	catalog := sign(key, Catalog{Schema: 1, KeyID: "synthetic-official", Sequence: 2, Expires: now.Add(time.Hour), Entries: []Entry{{Identity: m.Identity, Version: m.Version, Arch: m.Arch, ManifestDigest: Digest(manifest.Data), ArtifactDigest: m.ArtifactDigest}}})
	return fixture{authority: Authority{KeyID: "synthetic-official", PublicKey: key.Public().(ed25519.PublicKey), MinimumSequence: 2}, catalog: catalog, manifest: manifest, artifact: artifact, signature: ed25519.Sign(key, ArtifactStatement(Digest(manifest.Data), Digest(artifact))), expected: Expected{Identity: m.Identity, Version: m.Version, Arch: m.Arch, RuntimeMajor: 1, ProtocolMajor: 1}, now: now, key: key}
}
func (f fixture) verify() (Review, error) {
	return Verify(f.authority, f.catalog, f.manifest, f.artifact, f.signature, f.expected, f.now)
}
func TestOfficialReview(t *testing.T) {
	f := makeFixture()
	review, err := f.verify()
	if err != nil || !review.Compatible || len(review.Capabilities) != 2 || review.SigningAuthority != "synthetic-official" {
		t.Fatal(review, err)
	}
	review.Capabilities[0] = "mutated"
	again, _ := f.verify()
	if again.Capabilities[0] != "ha.observe" {
		t.Fatal("review shared declaration ownership")
	}
}
func TestRejectTamperingAndMismatch(t *testing.T) {
	for _, mutation := range []func(*fixture){func(f *fixture) { f.catalog.Data[0] = 'X' }, func(f *fixture) { f.manifest.Data[0] = 'X' }, func(f *fixture) { f.artifact[0] = 'X' }, func(f *fixture) { f.signature[0] ^= 1 }, func(f *fixture) { f.expected.Identity = "other" }, func(f *fixture) { f.expected.Version = "2.0.0" }, func(f *fixture) { f.expected.Arch = "aarch64" }, func(f *fixture) { f.expected.ProtocolMajor = 2 }, func(f *fixture) { f.expected.RuntimeMajor = 2 }, func(f *fixture) { f.authority.MinimumSequence = 3 }, func(f *fixture) { f.now = f.now.Add(2 * time.Hour) }, func(f *fixture) { f.manifest.Signature = nil }, func(f *fixture) { f.artifact = make([]byte, MaxArtifact+1) }} {
		f := makeFixture()
		mutation(&f)
		if _, err := f.verify(); err != ErrInvalid {
			t.Fatal("invalid accepted", err)
		}
	}
}
func TestSignedInvalidMetadata(t *testing.T) {
	f := makeFixture()
	var m Manifest
	json.Unmarshal(f.manifest.Data, &m)
	m.Capabilities = append(m.Capabilities, m.Capabilities[0])
	f.manifest = sign(f.key, m)
	if _, err := f.verify(); err != ErrInvalid {
		t.Fatal(err)
	}
	f = makeFixture()
	var c Catalog
	json.Unmarshal(f.catalog.Data, &c)
	c.Entries = append(c.Entries, c.Entries[0])
	f.catalog = sign(f.key, c)
	if _, err := f.verify(); err != ErrInvalid {
		t.Fatal(err)
	}
}

func TestSignedUnsafeDependenciesAndResourcesRejected(t *testing.T) {
	for _, mutate := range []func(*Manifest){
		func(m *Manifest) { m.Dependencies = []Dependency{{Identity: m.Identity, Version: m.Version}} },
		func(m *Manifest) {
			m.Dependencies = []Dependency{{Identity: "peer", Version: "1.0.0"}, {Identity: "peer", Version: "1.0.0", Optional: true}}
		},
		func(m *Manifest) { m.Dependencies = []Dependency{{Identity: "../peer", Version: "1.0.0"}} },
		func(m *Manifest) { m.Resources.MemoryKiB = 512*1024 + 1 },
		func(m *Manifest) { m.Resources.CPUPercent = 101 },
		func(m *Manifest) { m.Resources.Threads = 129 },
		func(m *Manifest) { m.Resources.FDs = 257 },
		func(m *Manifest) { m.Resources.MemoryKiB = 1 },
		func(m *Manifest) { m.Resources.Threads = 1 },
		func(m *Manifest) { m.Resources.FDs = 1 },
		func(m *Manifest) { m.Requests.MemoryKiB = 512*1024 + 1 },
		func(m *Manifest) { m.Requests.CPUPercent = 101 },
		func(m *Manifest) { m.Requests.Threads = 129 },
		func(m *Manifest) { m.Requests.FDs = 257 },
		func(m *Manifest) { m.Priority = "root" },
		func(m *Manifest) { m.Outbound = []string{"supervisor"} },
		func(m *Manifest) { m.Outbound = []string{"https://synthetic.invalid"} },
	} {
		f := makeFixture()
		var m Manifest
		_ = json.Unmarshal(f.manifest.Data, &m)
		mutate(&m)
		f.manifest = sign(f.key, m)
		var c Catalog
		_ = json.Unmarshal(f.catalog.Data, &c)
		c.Entries[0].ManifestDigest = Digest(f.manifest.Data)
		f.catalog = sign(f.key, c)
		f.signature = ed25519.Sign(f.key, ArtifactStatement(Digest(f.manifest.Data), Digest(f.artifact)))
		if _, err := f.verify(); err != ErrInvalid {
			t.Fatal("unsafe signed metadata accepted", m, err)
		}
	}
}
