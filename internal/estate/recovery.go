package estate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

var ErrRestartRequired = errors.New("explicit estate reset complete; Supervisor restart required")

const FactoryResetConfirmation = "DELETE ALL HOUSEFOLD DATA"

type checkpoint struct {
	Version int
	Files   map[string]string
}

// Cold Supervisor backup stops this process before archiving /data. The optional
// stopped-estate checkpoint detects mismatched/partial restores before any write.
// Crash recovery without a checkpoint still validates every authoritative store.
func (e *Engine) treeHashes(ctx context.Context) (map[string]string, error) {
	files := map[string]string{}
	count := 0
	var total int64
	var walk func(string, int) error
	walk = func(path string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 4 {
			return ErrRecovery
		}
		entries, err := readEntries(path, 256)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			count++
			if count > MaxAccountingEntries {
				return ErrRecovery
			}
			rel, _ := filepath.Rel(e.config.Root, filepath.Join(path, entry.Name()))
			if rel == "backup.json" {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.IsDir() {
				if err = walk(filepath.Join(path, entry.Name()), depth+1); err != nil {
					return err
				}
				continue
			}
			if !info.Mode().IsRegular() {
				return ErrRecovery
			}
			total += info.Size()
			if total > MaxEstateBytes+128<<20 {
				return ErrRecovery
			}
			f, err := os.Open(filepath.Join(path, entry.Name()))
			if err != nil {
				return err
			}
			h := sha256.New()
			buf := make([]byte, 64<<10)
			for {
				if err = ctx.Err(); err != nil {
					f.Close()
					return err
				}
				n, readErr := f.Read(buf)
				if n > 0 {
					_, _ = h.Write(buf[:n])
				}
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					f.Close()
					return readErr
				}
			}
			if err = f.Close(); err != nil {
				return err
			}
			files[rel] = hex.EncodeToString(h.Sum(nil))
		}
		return nil
	}
	err := walk(e.config.Root, 0)
	return files, err
}
func (e *Engine) writeCheckpoint(ctx context.Context) error {
	e.io.Lock()
	defer e.io.Unlock()
	files, err := e.treeHashes(ctx)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(checkpoint{Version: 1, Files: files})
	return e.file("backup").Save(raw)
}
func (e *Engine) consumeCheckpoint() error {
	raw, err := e.file("backup").Load()
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved checkpoint
	if json.Unmarshal(raw, &saved) != nil || saved.Version != 1 || saved.Files == nil || len(saved.Files) > MaxAccountingEntries {
		return ErrRecovery
	}
	for path, digest := range saved.Files {
		if path == "." || path == "backup.json" || filepath.Clean(path) != path || filepath.IsAbs(path) || strings.HasPrefix(path, "..") || len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
			return ErrRecovery
		}
	}
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	files, err := e.treeHashes(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(saved.Files, files) {
		return ErrRecovery
	}
	if err = os.Remove(filepath.Join(e.config.Root, "backup.json")); err != nil {
		return err
	}
	return syncDirectory(e.config.Root)
}
func syncDirectory(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Cleanup deletes only reference-unprotected retired/orphan data under V1P08.
func (e *Engine) Cleanup() error {
	e.op.Lock()
	defer e.op.Unlock()
	if !e.available() {
		return ErrRecovery
	}
	return e.collectStorage(false)
}

// ClearVolatile intentionally stops all owned generations of this module before
// clearing cache/temp. Selected/previous persistent state and uncertainty stay.
func (e *Engine) ClearVolatile(ctx context.Context, id string) error {
	e.op.Lock()
	defer e.op.Unlock()
	if !e.available() {
		return ErrRecovery
	}
	if _, ok := e.inv.Modules[id]; !ok {
		return ErrOperation
	}
	e.mu.Lock()
	units := []*unit{}
	for _, u := range e.units {
		if u.identity.Module == id {
			units = append(units, u)
		}
	}
	e.mu.Unlock()
	if err := e.router.StopSelected(id); err != nil {
		return err
	}
	for _, u := range units {
		if err := u.Stop(ctx); err != nil && !childStopped(err) {
			return err
		}
		e.forget(u.identity.Generation)
	}
	e.io.Lock()
	defer e.io.Unlock()
	sum := sha256.Sum256([]byte(id))
	name := hex.EncodeToString(sum[:])
	for _, scope := range []string{"cache", "temp"} {
		path := filepath.Join(e.stateRoot(scope), name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrRecovery
		}
		if err = walkBounded(path, func(_ string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return ErrRecovery
			}
			return nil
		}); err != nil {
			return err
		}
	}
	for _, scope := range []string{"cache", "temp"} {
		if err := os.RemoveAll(filepath.Join(e.stateRoot(scope), name)); err != nil {
			return err
		}
		if err := syncDirectory(e.stateRoot(scope)); err != nil {
			return err
		}
	}
	return nil
}

// FactoryReset is called only by the HA-admin plane after explicit destructive
// confirmation. It works with corrupt state; no normal recovery path calls it.
// Interrupted reset keeps its intent and requires explicit operator resumption.
func (e *Engine) FactoryReset(ctx context.Context, confirmation string) error {
	if confirmation != FactoryResetConfirmation {
		return ErrOperation
	}
	e.cancel()
	e.mu.Lock()
	e.phase = "reset_required"
	e.mu.Unlock()
	if e.config.OnRecovery != nil {
		e.config.OnRecovery()
	}
	e.op.Lock()
	defer e.op.Unlock()
	e.mu.Lock()
	e.phase = "reset_required"
	units := []*unit{}
	for _, u := range e.units {
		units = append(units, u)
	}
	catalogDone := e.catalogDone
	e.mu.Unlock()
	for _, u := range units {
		if err := u.Stop(ctx); err != nil && !childStopped(err) {
			return err
		}
	}
	if catalogDone != nil {
		select {
		case <-catalogDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	e.io.Lock()
	defer e.io.Unlock()
	info, err := os.Lstat(e.config.Root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrRecovery
	}
	// Validate bounded shape, allowing regular corrupt bytes but never links or
	// unlimited directories. Explicit reset can discard corrupt owned files.
	if err = e.validateResetTree(ctx); err != nil {
		return err
	}
	raw, _ := json.Marshal(struct {
		Version  int
		Explicit bool
	}{1, true})
	if err = e.file("reset-intent").Save(raw); err != nil {
		return err
	}
	entries, err := readEntries(e.config.Root, 256)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.Name() == "reset-intent.json" {
			continue
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = os.RemoveAll(filepath.Join(e.config.Root, entry.Name())); err != nil {
			return err
		}
	}
	if err = syncDirectory(e.config.Root); err != nil {
		return err
	}
	if err = os.Remove(filepath.Join(e.config.Root, "reset-intent.json")); err != nil {
		return err
	}
	if err = syncDirectory(e.config.Root); err != nil {
		return err
	}
	return ErrRestartRequired
}

func (e *Engine) validateResetTree(ctx context.Context) error {
	count := 0
	var walk func(string, int) error
	walk = func(path string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 4 {
			return ErrRecovery
		}
		rows, err := readEntries(path, 256)
		if err != nil {
			return err
		}
		for _, d := range rows {
			count++
			if count > MaxAccountingEntries {
				return ErrRecovery
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.IsDir() {
				if err = walk(filepath.Join(path, d.Name()), depth+1); err != nil {
					return err
				}
			} else if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
				return ErrRecovery
			}
		}
		return nil
	}
	return walk(e.config.Root, 0)
}
