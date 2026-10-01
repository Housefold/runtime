// Package packageverify verifies official local packages; it never installs or runs code.
package packageverify

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

const MaxMetadata = 64 << 10
const MaxArtifact = 16 << 20
const MaxEntries = 128

var ErrInvalid = errors.New("invalid or incompatible official package")

type Signed struct {
	Data      []byte
	Signature []byte
}
type Authority struct {
	KeyID           string
	PublicKey       ed25519.PublicKey
	MinimumSequence uint64
}
type Catalog struct {
	Schema   int       `json:"schema"`
	KeyID    string    `json:"key_id"`
	Sequence uint64    `json:"sequence"`
	Expires  time.Time `json:"expires"`
	Entries  []Entry   `json:"entries"`
}
type Entry struct {
	Identity       string `json:"identity"`
	Version        string `json:"version"`
	Arch           string `json:"arch"`
	ManifestDigest string `json:"manifest_digest"`
	ArtifactDigest string `json:"artifact_digest"`
}
type Manifest struct {
	Schema           int      `json:"schema"`
	Identity         string   `json:"identity"`
	Version          string   `json:"version"`
	Arch             string   `json:"arch"`
	RuntimeMajor     int      `json:"runtime_major"`
	RuntimeMinMinor  int      `json:"runtime_min_minor"`
	ProtocolMajor    int      `json:"protocol_major"`
	ProtocolMinMinor int      `json:"protocol_min_minor"`
	Capabilities     []string `json:"capabilities"`
	ArtifactDigest   string   `json:"artifact_digest"`
}
type Expected struct {
	Identity      string
	Version       string
	Arch          string
	RuntimeMajor  int
	RuntimeMinor  int
	ProtocolMajor int
	ProtocolMinor int
}
type Review struct {
	SigningAuthority string
	CatalogSequence  uint64
	Identity         string
	Version          string
	Architecture     string
	Capabilities     []string
	ArtifactDigest   string
	Compatible       bool
}

var identity = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)
var version = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func Digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func metadata(authority Authority, signed Signed, value any) error {
	if len(authority.PublicKey) != ed25519.PublicKeySize || len(signed.Data) == 0 || len(signed.Data) > MaxMetadata || !ed25519.Verify(authority.PublicKey, signed.Data, signed.Signature) {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(signed.Data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || !json.Valid(signed.Data) {
		return ErrInvalid
	}
	return nil
}

// Artifact signature binds the bytes to this signed manifest, not just a bare
// digest usable under another identity/version/architecture.
func ArtifactStatement(manifestDigest, artifactDigest string) []byte {
	return []byte("housefold-package-v1\x00" + manifestDigest + "\x00" + artifactDigest)
}
func Verify(authority Authority, catalog, manifest Signed, artifact, artifactSignature []byte, expected Expected, now time.Time) (Review, error) {
	var review Review
	var c Catalog
	var m Manifest
	if authority.KeyID == "" || metadata(authority, catalog, &c) != nil || metadata(authority, manifest, &m) != nil {
		return review, ErrInvalid
	}
	if c.Schema != 1 || c.KeyID != authority.KeyID || c.Sequence == 0 || c.Sequence < authority.MinimumSequence || !now.Before(c.Expires) || c.Expires.After(now.Add(30*24*time.Hour)) || len(c.Entries) > MaxEntries || len(c.Entries) == 0 {
		return review, ErrInvalid
	}
	if m.Schema != 1 || !identity.MatchString(m.Identity) || !version.MatchString(m.Version) || len(m.Version) > 64 || (m.Arch != "amd64" && m.Arch != "aarch64") || m.RuntimeMajor < 1 || m.RuntimeMinMinor < 0 || m.ProtocolMajor < 1 || m.ProtocolMinMinor < 0 || len(m.Capabilities) > 32 || !digestPattern.MatchString(m.ArtifactDigest) {
		return review, ErrInvalid
	}
	seenCapabilities := map[string]bool{}
	for _, capability := range m.Capabilities {
		if len(capability) > 64 || !identity.MatchString(capability) || seenCapabilities[capability] {
			return review, ErrInvalid
		}
		seenCapabilities[capability] = true
	}
	if m.Identity != expected.Identity || m.Version != expected.Version || m.Arch != expected.Arch || m.RuntimeMajor != expected.RuntimeMajor || m.RuntimeMinMinor > expected.RuntimeMinor || m.ProtocolMajor != expected.ProtocolMajor || m.ProtocolMinMinor > expected.ProtocolMinor {
		return review, ErrInvalid
	}
	manifestDigest := Digest(manifest.Data)
	seen := map[string]bool{}
	found := false
	for _, entry := range c.Entries {
		key := entry.Identity + "\x00" + entry.Version + "\x00" + entry.Arch
		if seen[key] || !identity.MatchString(entry.Identity) || !version.MatchString(entry.Version) || !digestPattern.MatchString(entry.ManifestDigest) || !digestPattern.MatchString(entry.ArtifactDigest) || (entry.Arch != "amd64" && entry.Arch != "aarch64") {
			return review, ErrInvalid
		}
		seen[key] = true
		if entry.Identity == m.Identity && entry.Version == m.Version && entry.Arch == m.Arch {
			if entry.ManifestDigest != manifestDigest || entry.ArtifactDigest != m.ArtifactDigest {
				return review, ErrInvalid
			}
			found = true
		}
	}
	if !found || len(artifact) == 0 || len(artifact) > MaxArtifact || Digest(artifact) != m.ArtifactDigest || !ed25519.Verify(authority.PublicKey, ArtifactStatement(manifestDigest, m.ArtifactDigest), artifactSignature) {
		return review, ErrInvalid
	}
	return Review{SigningAuthority: authority.KeyID, CatalogSequence: c.Sequence, Identity: m.Identity, Version: m.Version, Architecture: m.Arch, Capabilities: append([]string(nil), m.Capabilities...), ArtifactDigest: m.ArtifactDigest, Compatible: true}, nil
}
