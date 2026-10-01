// Package durable provides bounded checksummed, atomic private JSON storage.
package durable

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const MaxBytes = 16 << 20

var ErrCorrupt = errors.New("corrupt durable state")
var ErrUncertain = errors.New("durable commit uncertain; reopen before further writes")

type Store interface {
	Load() ([]byte, error)
	Save([]byte) error
}
type File struct {
	path     string
	mu       sync.Mutex
	poisoned bool
	fault    func(string) error
}

func NewFile(path string) *File { return &File{path: path} }

type envelope struct {
	Version int             `json:"version"`
	Digest  string          `json:"digest"`
	Data    json.RawMessage `json:"data"`
}

func (f *File) Load() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, err := os.Lstat(f.path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrCorrupt
	}
	file, err := os.Open(f.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxBytes {
		return nil, ErrCorrupt
	}
	var e envelope
	if json.Unmarshal(raw, &e) != nil || e.Version != 1 || !json.Valid(e.Data) {
		return nil, ErrCorrupt
	}
	sum := sha256.Sum256(e.Data)
	if e.Digest != hex.EncodeToString(sum[:]) {
		return nil, ErrCorrupt
	}
	return append([]byte(nil), e.Data...), nil
}
func (f *File) step(stage string) error {
	if f.fault != nil {
		return f.fault(stage)
	}
	return nil
}
func (f *File) Save(data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.poisoned {
		return ErrUncertain
	}
	if !json.Valid(data) {
		return ErrCorrupt
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return ErrCorrupt
	}
	data = compact.Bytes()
	sum := sha256.Sum256(data)
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(envelope{Version: 1, Digest: hex.EncodeToString(sum[:]), Data: data})
	raw := bytes.TrimSpace(encoded.Bytes())
	if err != nil {
		return err
	}
	if len(raw) > MaxBytes {
		return ErrCorrupt
	}
	dir := filepath.Dir(f.path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".runtime-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	defer tmp.Close()
	if err = f.step("write"); err != nil {
		return err
	}
	if _, err = tmp.Write(raw[:len(raw)/2]); err != nil {
		return err
	}
	if err = f.step("partial"); err != nil {
		return err
	}
	if _, err = tmp.Write(raw[len(raw)/2:]); err != nil {
		return err
	}
	if err = f.step("sync"); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = f.step("rename"); err != nil {
		return err
	}
	if err = os.Rename(name, f.path); err != nil {
		return err
	}
	f.poisoned = true
	if err = f.step("dirsync"); err != nil {
		return errors.Join(ErrUncertain, err)
	}
	d, err := os.Open(dir)
	if err != nil {
		return errors.Join(ErrUncertain, err)
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return errors.Join(ErrUncertain, err)
	}
	f.poisoned = false
	return nil
}
