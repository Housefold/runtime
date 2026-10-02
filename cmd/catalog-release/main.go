// catalog-release prepares signed catalog bytes offline. It never publishes.
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/housefold/runtime/internal/catalog"
	"github.com/housefold/runtime/internal/catalogrelease"
	"github.com/housefold/runtime/internal/packageverify"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "catalog release rejected")
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) != 4 && len(args) != 5 {
		return catalogrelease.ErrRelease
	}
	// plan/root/key/out/[previous signed catalog]. Key file is external to root,
	// private, regular and fixed length. No key generation/custody is implied.
	planPath, root, keyPath, out := args[0], args[1], args[2], args[3]
	info, err := os.Lstat(keyPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() != 64 {
		return catalogrelease.ErrRelease
	}
	keyRaw, err := os.ReadFile(keyPath)
	if err != nil {
		return catalogrelease.ErrRelease
	}
	seed, err := hex.DecodeString(string(keyRaw))
	if err != nil || len(seed) != ed25519.SeedSize {
		return catalogrelease.ErrRelease
	}
	defer clear(seed)
	defer clear(keyRaw)
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return catalogrelease.ErrRelease
	}
	keyAbsolute, err := filepath.Abs(keyPath)
	if err != nil {
		return catalogrelease.ErrRelease
	}
	relative, err := filepath.Rel(rootAbsolute, keyAbsolute)
	if err != nil || relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return catalogrelease.ErrRelease
	}
	authority := catalog.OfficialAuthority()
	if !bytes.Equal(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey), authority.PublicKey) {
		return catalogrelease.ErrRelease
	}
	plan, err := catalogrelease.ReadOwned(root, planPath, catalogrelease.MaxPlanBytes)
	if err != nil {
		return err
	}
	var declaration catalogrelease.Plan
	if json.Unmarshal(plan, &declaration) != nil || declaration.KeyID != authority.KeyID {
		return catalogrelease.ErrRelease
	}
	var previous *packageverify.Signed
	if len(args) == 5 {
		raw, err := catalogrelease.ReadOwned(root, args[4], 2*packageverify.MaxMetadata+4096)
		if err != nil {
			return err
		}
		previous = &packageverify.Signed{}
		if json.Unmarshal(raw, previous) != nil {
			return catalogrelease.ErrRelease
		}
	}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	files, err := catalogrelease.Prepare(root, plan, key, previous, time.Now().UTC())
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	return catalogrelease.Write(absolute, files)
}
