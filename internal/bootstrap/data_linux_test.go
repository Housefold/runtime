package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateEstateAndPreservedCorruption(t *testing.T) {
	uid := os.Getuid()
	if uid == 0 {
		uid = 10001
	}
	root := t.TempDir()
	path := filepath.Join(root, "housefold")
	if err := Prepare(path, uid); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("mode: %v %v", info, err)
	}
	data := filepath.Join(path, "durable.json")
	if err = os.WriteFile(data, []byte("corrupt-preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Prepare(path, uid); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(data)
	if string(raw) != "corrupt-preserve" {
		t.Fatal("discarded durable state")
	}
	link := filepath.Join(root, "link")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(Prepare(link, uid), ErrData) {
		t.Fatal("accepted estate symlink")
	}
	if !errors.Is(Prepare(filepath.Join(link, "child"), uid), ErrData) {
		t.Fatal("accepted parent symlink")
	}
	file := filepath.Join(root, "file")
	_ = os.WriteFile(file, []byte("keep"), 0600)
	if !errors.Is(Prepare(file, uid), ErrData) {
		t.Fatal("replaced file")
	}
	raw, _ = os.ReadFile(file)
	if string(raw) != "keep" {
		t.Fatal("modified bad estate")
	}
}
