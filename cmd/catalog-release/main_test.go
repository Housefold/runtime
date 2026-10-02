package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIRejectsUnconfiguredAuthorityAndDoesNotOutputKey(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	key := filepath.Join(external, "key")
	if err := os.WriteFile(key, []byte(strings.Repeat("0", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(external, "out")
	if run([]string{"plan.json", root, key, out}) == nil {
		t.Fatal("synthetic key trusted by production signer")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("unconfigured signer created output", err)
	}
}
