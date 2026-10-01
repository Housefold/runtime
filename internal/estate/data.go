// Package estate owns the production module estate. Only verified official
// bundles enter inventory; there is no arbitrary-path/binary install operation.
package estate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/housefold/runtime/internal/action"
	"github.com/housefold/runtime/internal/durable"
	"github.com/housefold/runtime/internal/execution"
	"github.com/housefold/runtime/internal/ha"
	"github.com/housefold/runtime/internal/module"
	"github.com/housefold/runtime/internal/packageverify"
	"github.com/housefold/runtime/internal/timeline"
)

var ErrRecovery = errors.New("Runtime estate recovery required")
var ErrDependency = errors.New("required module dependency unavailable or cyclic")
var ErrOperation = errors.New("module lifecycle operation unavailable")

const MaxInstalled = 15 // One of 16 process slots stays available for preparation.

type Bundle struct {
	Catalog           packageverify.Signed
	Manifest          packageverify.Signed
	ArtifactSignature []byte
	AcceptedAt        time.Time
}

func (b Bundle) declaration() (m packageverify.Manifest) {
	_ = json.Unmarshal(b.Manifest.Data, &m)
	return
}

type Entry struct {
	Installed   bool
	Desired     bool
	Current     *Bundle
	Previous    *Bundle
	Pending     *Bundle
	UpdateError string
}
type Inventory struct {
	Version int
	Modules map[string]Entry
}
type ModuleStatus struct {
	Identity                  string
	Installed, Desired        bool
	Version                   string
	Generation                uint64
	Phase                     string
	ServiceHealthy, UIHealthy bool
	Accepting                 bool
	Error                     string
}
type Snapshot struct {
	StorageDegraded bool
	Phase           string
	Modules         []ModuleStatus
}
type Config struct {
	Root         string
	Source       *ha.StateSession
	Authority    packageverify.Authority
	Actions      action.Transport
	Logger       *slog.Logger
	OnRecovery   func()
	StoreFactory func(string) durable.Store
}
type Engine struct {
	op              sync.Mutex
	mu              sync.Mutex
	config          Config
	phase           string
	inv             Inventory
	store           durable.Store
	exec            *execution.Manager
	router          *module.Router
	timeline        *timeline.Timeline
	actions         *action.Gateway
	boot            string
	units           map[uint64]*unit
	ctx             context.Context
	cancel          context.CancelFunc
	initialized     chan struct{}
	storageDegraded bool
	ownedStores     map[string]durable.Store
}

func New(config Config) *Engine {
	if config.Logger == nil {
		config.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{config: config, phase: "initializing", units: map[uint64]*unit{}, ctx: ctx, cancel: cancel, initialized: make(chan struct{}), ownedStores: map[string]durable.Store{}}
}
func (e *Engine) fail() {
	e.mu.Lock()
	e.phase = "recovery_required"
	e.mu.Unlock()
	e.config.Logger.Error("Runtime estate recovery required")
	e.cancel()
	if e.config.OnRecovery != nil {
		e.config.OnRecovery()
	}
}
func (e *Engine) file(name string) *durable.File {
	return durable.NewFile(filepath.Join(e.config.Root, name+".json"))
}

type ownedStore struct {
	engine *Engine
	store  durable.Store
}

func (s ownedStore) Load() ([]byte, error) { return s.store.Load() }
func (s ownedStore) Save(raw []byte) error {
	s.engine.mu.Lock()
	blocked := s.engine.storageDegraded
	s.engine.mu.Unlock()
	if blocked {
		return ErrStorage
	}
	err := s.store.Save(raw)
	if errors.Is(err, durable.ErrUncertain) || errors.Is(err, durable.ErrCorrupt) {
		s.engine.fail()
	} else if err != nil {
		s.engine.mu.Lock()
		first := !s.engine.storageDegraded
		s.engine.storageDegraded = true
		s.engine.mu.Unlock()
		if first {
			s.engine.config.Logger.Error("Runtime estate storage degraded")
		}
	}
	return err
}

var ErrStorage = errors.New("Runtime storage degraded; writes fenced")

func (e *Engine) owned(name string) durable.Store {
	if s := e.ownedStores[name]; s != nil {
		return s
	}
	var s durable.Store = e.file(name)
	if e.config.StoreFactory != nil {
		s = e.config.StoreFactory(filepath.Join(e.config.Root, name+".json"))
	}
	s = ownedStore{e, s}
	e.ownedStores[name] = s
	return s
}
func (e *Engine) stateRoot(scope string) string { return filepath.Join(e.config.Root, "states-"+scope) }
func (e *Engine) archivePath(digest string) string {
	return filepath.Join(e.config.Root, "artifacts", digest)
}
func architecture() string {
	if runtime.GOARCH == "arm64" {
		return "aarch64"
	}
	return runtime.GOARCH
}
func safeDir(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrRecovery
	}
	return nil
}
func (e *Engine) initialize(now time.Time) error {
	if !filepath.IsAbs(e.config.Root) {
		return ErrRecovery
	}
	if err := safeDir(e.config.Root); err != nil {
		return err
	}
	marker := e.file("estate")
	raw, err := marker.Load()
	fresh := errors.Is(err, os.ErrNotExist)
	var meta struct {
		Version     int
		Initialized bool
	}
	if fresh {
		entries, err := os.ReadDir(e.config.Root)
		if err != nil || len(entries) != 0 {
			return ErrRecovery
		}
		meta.Version = 1
		raw, _ = json.Marshal(meta)
		if err = marker.Save(raw); err != nil {
			return err
		}
	} else if err != nil || json.Unmarshal(raw, &meta) != nil || meta.Version != 1 {
		return ErrRecovery
	}
	required := []string{"inventory", "execution", "router", "timeline", "actions"}
	if meta.Initialized {
		for _, name := range required {
			if _, err = e.file(name).Load(); err != nil {
				return err
			}
		}
	}
	for _, scope := range []string{"generation", "persistent", "cache", "temp", "artifacts"} {
		path := e.stateRoot(scope)
		if scope == "artifacts" {
			path = filepath.Join(e.config.Root, scope)
		}
		if err = safeDir(path); err != nil {
			return err
		}
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	e.boot = hex.EncodeToString(nonce[:])
	e.store = e.owned("inventory")
	raw, err = e.store.Load()
	d := Inventory{Version: 1, Modules: map[string]Entry{}}
	if errors.Is(err, os.ErrNotExist) {
		raw, _ = json.Marshal(d)
		if err = e.store.Save(raw); err != nil {
			return err
		}
	} else if err != nil || json.Unmarshal(raw, &d) != nil || d.Version != 1 || d.Modules == nil || len(d.Modules) > module.MaxModules {
		return ErrRecovery
	}
	e.inv = d
	// Verify all inventory artifacts even for disabled/retained modules. Offline
	// boot uses the signed acceptance time, never current catalog availability.
	for id, entry := range d.Modules {
		for _, b := range []*Bundle{entry.Current, entry.Previous, entry.Pending} {
			if b != nil {
				m := b.declaration()
				if m.Identity != id || b.AcceptedAt.IsZero() {
					return ErrRecovery
				}
				if _, err = e.verify(*b, nil, b.AcceptedAt); err != nil {
					return err
				}
			}
		}
	}
	if e.exec, err = execution.Open(e.owned("execution")); err != nil {
		return err
	}
	if e.router, err = module.OpenRouter(e.owned("router"), e.exec, e.boot); err != nil {
		return err
	}
	if e.timeline, err = timeline.Open(e.owned("timeline"), now); err != nil {
		return err
	}
	transport := e.config.Actions
	if transport == nil {
		transport = offlineActions{}
	}
	if e.actions, err = action.Open(e.owned("actions"), e.router, transport); err != nil {
		return err
	}
	// Roll forward inventory only when the durable cutover already selected the
	// pending digest. Never roll back the router or automatically retry updates.
	data := e.router.Snapshot()
	for id, entry := range e.inv.Modules {
		sel := data.Modules[id]
		active := data.Generations[sel.Active]
		if entry.Pending != nil && active.Version == entry.Pending.declaration().Version && active.ArtifactDigest == entry.Pending.declaration().ArtifactDigest && sel.Candidate == 0 {
			if entry.Current == nil || entry.Current.declaration().Version != entry.Pending.declaration().Version {
				entry.Previous = entry.Current
			}
			entry.Current = entry.Pending
			entry.Pending = nil
			entry.UpdateError = ""
		} else if entry.Pending != nil {
			entry.UpdateError = "interrupted preparation: explicit retry required"
		}
		for _, n := range []uint64{sel.Active, sel.Previous} {
			if n == 0 {
				continue
			}
			g := data.Generations[n]
			state, checkErr := module.OpenState(e.stateRoot("generation"), module.Identity{Module: id, Generation: n})
			if checkErr != nil || state.Reference() != g.StatePath {
				return ErrRecovery
			}
			if _, checkErr = module.OpenState(e.stateRoot("persistent"), module.Identity{Module: id, Generation: n}); checkErr != nil {
				return ErrRecovery
			}
		}
		if sel.Active != 0 && (entry.Current == nil || active.ArtifactDigest != entry.Current.declaration().ArtifactDigest || active.Module != id || active.Version != entry.Current.declaration().Version) {
			return ErrRecovery
		}
		if sel.Previous != 0 {
			previous := data.Generations[sel.Previous]
			if entry.Previous == nil || previous.ArtifactDigest != entry.Previous.declaration().ArtifactDigest || previous.Version != entry.Previous.declaration().Version {
				return ErrRecovery
			}
		}
		if sel.Candidate != 0 {
			g := data.Generations[sel.Candidate]
			if err = e.router.FailCandidate(module.Identity{Module: g.Module, Version: g.Version, Boot: e.boot, Generation: g.Number}); err != nil {
				return err
			}
		}
		e.inv.Modules[id] = entry
	}
	for id := range data.Modules {
		if _, ok := e.inv.Modules[id]; !ok {
			return ErrRecovery
		}
	}
	if !meta.Initialized {
		if len(e.inv.Modules) != 0 || len(e.router.Snapshot().Modules) != 0 || len(e.exec.Snapshot().Records) != 0 || len(e.exec.Snapshot().Definitions) != 0 || len(e.timeline.Snapshot().Schedules) != 0 {
			return ErrRecovery
		}
	}
	// Pending cross-store rebinds roll forward only to the durable selection.
	// Prior running/canceling executions were already interrupted by Open.
	rd := e.router.Snapshot()
	for _, def := range e.exec.Snapshot().Definitions {
		if _, ok := e.inv.Modules[def.Module]; !ok {
			return ErrRecovery
		}
	}
	for _, record := range e.exec.Snapshot().Records {
		if record.Phase != "pending" {
			continue
		}
		g, ok := rd.Generations[record.Generation]
		if !ok || g.Module != record.Module {
			return ErrRecovery
		}
		selected := rd.Modules[record.Module].Active
		if selected != 0 && selected != record.Generation && (g.Phase == module.Retired || g.Phase == module.Draining) {
			if err = e.exec.RebindPending(record.Module, record.Generation, selected); err != nil {
				return err
			}
		}
	}
	if err = e.save(e.inv); err != nil {
		return err
	}
	if _, err = e.timeline.Advance(now, true); err != nil {
		return err
	}
	meta.Initialized = true
	raw, _ = json.Marshal(meta)
	if err = marker.Save(raw); err != nil {
		return err
	}
	e.mu.Lock()
	e.phase = "running"
	e.mu.Unlock()
	return nil
}

type offlineActions struct{}

func (offlineActions) Call(context.Context, action.Request) (action.Outcome, error) {
	return action.NotSent, ErrOperation
}
func (e *Engine) save(d Inventory) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if err = e.store.Save(raw); err != nil {
		return err
	}
	// Snapshots read inventory under this lock; lifecycle serializes writers.
	e.mu.Lock()
	e.inv = cloneInventory(d)
	e.mu.Unlock()
	return nil
}
func cloneInventory(d Inventory) Inventory {
	raw, _ := json.Marshal(d)
	var out Inventory
	_ = json.Unmarshal(raw, &out)
	return out
}
func (e *Engine) available() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.phase == "running" }
func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	if e.phase != "running" {
		phase := e.phase
		e.mu.Unlock()
		return Snapshot{Phase: phase}
	}
	out := Snapshot{Phase: e.phase, StorageDegraded: e.storageDegraded}
	d := cloneInventory(e.inv)
	units := make(map[uint64]*unit, len(e.units))
	for n, u := range e.units {
		units[n] = u
	}
	r := e.router
	e.mu.Unlock()
	var rd module.RouterData
	if r != nil && out.Phase == "running" {
		rd = r.Snapshot()
	}
	for id, entry := range d.Modules {
		sel := rd.Modules[id]
		g := rd.Generations[sel.Active]
		status := ModuleStatus{Identity: id, Installed: entry.Installed, Desired: entry.Desired, Version: g.Version, Generation: g.Number, Phase: string(g.Phase), Error: entry.UpdateError, Accepting: r != nil && r.Accepting(id)}
		if u := units[g.Number]; u != nil {
			u.mu.Lock()
			status.ServiceHealthy = u.service
			status.UIHealthy = u.ui
			u.mu.Unlock()
		}
		if status.Phase == "" {
			status.Phase = "STOPPED"
		}
		if !entry.Desired {
			status.Phase = "STOPPED"
		}
		out.Modules = append(out.Modules, status)
	}
	sort.Slice(out.Modules, func(i, j int) bool { return out.Modules[i].Identity < out.Modules[j].Identity })
	return out
}
func (e *Engine) verify(b Bundle, artifact []byte, now time.Time) (packageverify.Review, error) {
	m := b.declaration()
	if len(m.ArtifactDigest) != 64 || strings.Trim(m.ArtifactDigest, "0123456789abcdef") != "" {
		return packageverify.Review{}, packageverify.ErrInvalid
	}
	if artifact == nil {
		path := e.archivePath(m.ArtifactDigest)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > packageverify.MaxArtifact {
			return packageverify.Review{}, ErrRecovery
		}
		f, err := os.Open(path)
		if err != nil {
			return packageverify.Review{}, err
		}
		defer f.Close()
		artifact, err = io.ReadAll(io.LimitReader(f, packageverify.MaxArtifact+1))
		if err != nil {
			return packageverify.Review{}, err
		}
	}
	review, err := packageverify.Verify(e.config.Authority, b.Catalog, b.Manifest, artifact, b.ArtifactSignature, packageverify.Expected{Identity: m.Identity, Version: m.Version, Arch: architecture(), RuntimeMajor: 1, ProtocolMajor: module.Major}, now)
	if err != nil {
		return review, err
	}
	// Packages are raw native Linux executables, never archives/scripts. Verify
	// ELF target before any launch. Signed wrong-architecture bytes fail closed.
	machine := uint16(62)
	if architecture() == "aarch64" {
		machine = 183
	}
	if len(artifact) < 20 || string(artifact[:4]) != "\x7fELF" || artifact[4] != 2 || artifact[5] != 1 || uint16(artifact[18])|uint16(artifact[19])<<8 != machine {
		return review, packageverify.ErrInvalid
	}
	return review, nil
}
func (e *Engine) writeArtifact(digest string, raw []byte) error {
	path := e.archivePath(digest)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Size() > packageverify.MaxArtifact {
			return ErrRecovery
		}
		old, err := os.ReadFile(path)
		if err != nil || packageverify.Digest(old) != digest {
			return ErrRecovery
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".stage-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0500); err != nil {
		return err
	}
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func definitionsPrefix(id, local string) (string, error) {
	if local == "" || len(local) > 64 {
		return "", module.ErrProtocol
	}
	for _, r := range local {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "", module.ErrProtocol
		}
	}
	return id + "/" + local, nil
}
func (e *Engine) Log(id module.Identity, message string) {
	e.config.Logger.Info("module event", "module", id.Module, "version", id.Version, "generation", id.Generation, "code", message)
}
func (e *Engine) String() string {
	s := e.Snapshot()
	return fmt.Sprintf("estate %s (%d modules)", s.Phase, len(s.Modules))
}
