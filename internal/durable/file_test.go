package durable

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestAtomicFaults(t *testing.T) {
	for _, stage := range []string{"write", "partial", "sync", "rename", "dirsync"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state")
			f := NewFile(path)
			if err := f.Save([]byte(`{"value":1}`)); err != nil {
				t.Fatal(err)
			}
			f.fault = func(s string) error {
				if s == stage {
					return syscall.ENOSPC
				}
				return nil
			}
			if err := f.Save([]byte(`{"value":2}`)); err == nil {
				t.Fatal("fault ignored")
			}
			raw, err := NewFile(path).Load()
			if err != nil {
				t.Fatal(err)
			}
			want := `{"value":1}`
			if stage == "dirsync" {
				want = `{"value":2}`
				if !errors.Is(f.Save([]byte(`{}`)), ErrUncertain) {
					t.Fatal("unfenced uncertainty")
				}
			}
			if string(raw) != want {
				t.Fatal(string(raw))
			}
			stat, _ := os.Stat(path)
			if stat.Mode().Perm() != 0600 {
				t.Fatal(stat.Mode())
			}
			matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".runtime-*"))
			if len(matches) != 0 {
				t.Fatal(matches)
			}
		})
	}
}
func TestCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	f := NewFile(path)
	for _, raw := range []string{`{`, `{"version":1,"digest":"bad","data":{}}`, `{"version":2,"data":{}}`} {
		os.WriteFile(path, []byte(raw), 0600)
		if _, err := f.Load(); !errors.Is(err, ErrCorrupt) {
			t.Fatal(err)
		}
	}
}
func TestPrettyRoundTrip(t *testing.T) {
	f := NewFile(filepath.Join(t.TempDir(), "state"))
	if err := f.Save([]byte("{\n \"value\": 1\n}")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Load(); err != nil {
		t.Fatal(err)
	}
}
func TestSymlinkStateRejected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	file := NewFile(target)
	file.Save([]byte(`{}`))
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFile(link).Load(); err != ErrCorrupt {
		t.Fatal("symlink followed", err)
	}
}
func TestRawJSONRoundTrip(t *testing.T) {
	for _, raw := range []string{`{"value":"<>&"}`, `{"value":"\u003c"}`, `{"value":"é"}`, `{"value":1e-3}`} {
		f := NewFile(filepath.Join(t.TempDir(), "state"))
		if err := f.Save([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		loaded, err := f.Load()
		if err != nil || string(loaded) != raw {
			t.Fatal(raw, string(loaded), err)
		}
	}
}
