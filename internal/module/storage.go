package module

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/housefold/runtime/internal/durable"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

const MaxStateValue = 1 << 20
const MaxStateBytes = 8 << 20
const MaxStateKeys = 64

var stateKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type StateStore struct {
	mu   sync.Mutex
	file *durable.File
	path string
}

func AllocateState(root string, id Identity) (*StateStore, error) {
	if !filepath.IsAbs(root) || id.Module == "" || id.Generation == 0 {
		return nil, ErrFenced
	}
	sum := sha256.Sum256([]byte(id.Module))
	moduleDir := filepath.Join(root, hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(moduleDir, 0700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(moduleDir)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(moduleDir, fmt.Sprint(id.Generation))
	if len(entries) >= MaxGenerations {
		if _, err = os.Stat(path); err != nil {
			return nil, ErrFenced
		}
	}
	if err = os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	s := &StateStore{file: durable.NewFile(filepath.Join(path, "state.json")), path: path}
	if _, err = s.file.Load(); errors.Is(err, os.ErrNotExist) {
		if err = s.file.Save([]byte(`{}`)); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return s, nil
}
func (s *StateStore) read() (map[string][]byte, error) {
	raw, err := s.file.Load()
	if err != nil {
		return nil, err
	}
	var d map[string][]byte
	if json.Unmarshal(raw, &d) != nil || d == nil || len(d) > MaxStateKeys {
		return nil, durable.ErrCorrupt
	}
	total := 0
	for key, value := range d {
		total += len(value)
		if !stateKey.MatchString(key) || len(value) > MaxStateValue {
			return nil, durable.ErrCorrupt
		}
	}
	if total > MaxStateBytes {
		return nil, durable.ErrCorrupt
	}
	return d, nil
}
func (s *StateStore) Read(key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !stateKey.MatchString(key) {
		return nil, ErrProtocol
	}
	d, err := s.read()
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), d[key]...), nil
}
func (s *StateStore) Write(key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !stateKey.MatchString(key) || len(value) > MaxStateValue {
		return ErrProtocol
	}
	d, err := s.read()
	if err != nil {
		return err
	}
	d[key] = append([]byte(nil), value...)
	if len(d) > MaxStateKeys {
		return ErrProtocol
	}
	total := 0
	for _, v := range d {
		total += len(v)
	}
	if total > MaxStateBytes {
		return ErrProtocol
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return s.file.Save(raw)
}
