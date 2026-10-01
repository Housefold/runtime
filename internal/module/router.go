package module

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/housefold/runtime/internal/durable"
	"github.com/housefold/runtime/internal/execution"
	"github.com/housefold/runtime/internal/timeline"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"
)

const MaxModules = 32
const MaxGenerations = 128
const DrainBudget = 5 * time.Second

var ErrFenced = errors.New("module generation fenced or not ready")

type Lifecycle string

const (
	Preparing   Lifecycle = "PREPARING"
	Active      Lifecycle = "ACTIVE"
	Draining    Lifecycle = "DRAINING"
	Retired     Lifecycle = "RETIRED"
	Quarantined Lifecycle = "QUARANTINED"
)

type Generation struct {
	ArtifactDigest string
	StatePath      string
	Crashed        bool
	Replaces       uint64
	Number         uint64
	Module         string
	Version        string
	Phase          Lifecycle
	DrainUntil     time.Time
}
type Selection struct {
	Failures    int
	NextRestart time.Time
	Active      uint64
	Candidate   uint64
	Previous    uint64
}
type RouterData struct {
	Version     int
	Next        uint64
	Modules     map[string]Selection
	Generations map[uint64]Generation
}
type Child interface{ Stop(context.Context) error }
type Router struct {
	mu       sync.Mutex
	store    durable.Store
	exec     *execution.Manager
	boot     string
	data     RouterData
	ready    map[uint64]bool
	children map[uint64]Child
	poisoned bool
}

func OpenRouter(store durable.Store, exec *execution.Manager, boot string) (*Router, error) {
	if boot == "" || exec == nil {
		return nil, ErrFenced
	}
	raw, err := store.Load()
	d := RouterData{Version: 1, Modules: map[string]Selection{}, Generations: map[uint64]Generation{}}
	fresh := errors.Is(err, os.ErrNotExist)
	if !fresh {
		if err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &d) != nil || d.Version != 1 || d.Modules == nil || d.Generations == nil || len(d.Modules) > MaxModules || len(d.Generations) > MaxGenerations {
			return nil, durable.ErrCorrupt
		}
	}
	for n, g := range d.Generations {
		if n == 0 || n != g.Number || n > d.Next || g.Module == "" || g.Version == "" || (g.Phase != Preparing && g.Phase != Active && g.Phase != Draining && g.Phase != Retired && g.Phase != Quarantined) {
			return nil, durable.ErrCorrupt
		}
	}
	for module, sel := range d.Modules {
		for _, n := range []uint64{sel.Active, sel.Candidate, sel.Previous} {
			if n != 0 && d.Generations[n].Module != module {
				return nil, durable.ErrCorrupt
			}
		}
		if sel.Active != 0 && (d.Generations[sel.Active].Phase != Active && d.Generations[sel.Active].Phase != Quarantined) {
			return nil, durable.ErrCorrupt
		}
		if sel.Candidate != 0 && d.Generations[sel.Candidate].Phase != Preparing {
			return nil, durable.ErrCorrupt
		}
	}
	r := &Router{store: store, exec: exec, boot: boot, data: d, ready: map[uint64]bool{}, children: map[uint64]Child{}}
	if fresh {
		if err = r.commit(d); err != nil {
			return nil, err
		}
	}
	return r, nil
}
func routerCopy(d RouterData) RouterData {
	raw, _ := json.Marshal(d)
	var c RouterData
	_ = json.Unmarshal(raw, &c)
	return c
}
func (r *Router) commit(d RouterData) error {
	if r.poisoned {
		return durable.ErrUncertain
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if err = r.store.Save(raw); err != nil {
		if errors.Is(err, durable.ErrUncertain) {
			r.poisoned = true
		}
		return err
	}
	r.data = d
	return nil
}
func (r *Router) Snapshot() RouterData { r.mu.Lock(); defer r.mu.Unlock(); return routerCopy(r.data) }
func (r *Router) identity(g Generation) Identity {
	return Identity{Module: g.Module, Version: g.Version, Boot: r.boot, Generation: g.Number}
}
func (r *Router) matches(id Identity) bool {
	g, ok := r.data.Generations[id.Generation]
	return ok && r.identity(g) == id && !r.poisoned && r.exec.Healthy()
}
func (r *Router) Prepare(module, version string) (Identity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prepareLocked(module, version)
}
func (r *Router) prepareLocked(module, version string) (Identity, error) {
	if module == "" || version == "" || len(module) > 128 || len(version) > 128 || r.poisoned || !r.exec.Healthy() {
		return Identity{}, ErrFenced
	}
	sel, ok := r.data.Modules[module]
	if (!ok && len(r.data.Modules) >= MaxModules) || sel.Candidate != 0 || len(r.data.Generations) >= MaxGenerations {
		return Identity{}, ErrFenced
	}
	for _, g := range r.data.Generations {
		if g.Module == module && g.Phase == Draining {
			return Identity{}, ErrFenced
		}
	}
	d := routerCopy(r.data)
	d.Next++
	g := Generation{Number: d.Next, Module: module, Version: version, Phase: Preparing}
	d.Generations[g.Number] = g
	sel.Candidate = g.Number
	d.Modules[module] = sel
	if err := r.commit(d); err != nil {
		return Identity{}, err
	}
	return r.identity(g), nil
}

// Acknowledgement must come from the launcher-bound session identity. Recovery
// never reuses old boot authority: every selected generation must handshake anew.
func (r *Router) Ready(id Identity, accepting bool, child Child) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.matches(id) || !accepting {
		return ErrFenced
	}
	g := r.data.Generations[id.Generation]
	if g.Crashed || (g.Phase != Preparing && g.Phase != Active) || (g.Phase == Active && r.data.Modules[id.Module].Active != id.Generation) || (g.Phase == Preparing && r.data.Modules[id.Module].Candidate != id.Generation) {
		return ErrFenced
	}
	r.ready[id.Generation] = true
	if child != nil {
		r.children[id.Generation] = child
	}
	return nil
}
func (r *Router) Cutover(id Identity, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cutoverLocked(id, now)
}
func (r *Router) cutoverLocked(id Identity, now time.Time) error {
	if !r.matches(id) || !r.ready[id.Generation] {
		return ErrFenced
	}
	sel := r.data.Modules[id.Module]
	if sel.Candidate != id.Generation {
		return ErrFenced
	}
	d := routerCopy(r.data)
	g := d.Generations[id.Generation]
	if g.Replaces != 0 {
		if err := r.exec.RebindPending(id.Module, g.Replaces, g.Number); err != nil {
			return err
		}
	}
	if sel.Active != 0 {
		old := d.Generations[sel.Active]
		old.Phase = Draining
		old.DrainUntil = now.Add(DrainBudget)
		d.Generations[old.Number] = old
		sel.Previous = old.Number
	}
	g.Phase = Active
	d.Generations[g.Number] = g
	sel.Active = g.Number
	sel.Candidate = 0
	d.Modules[g.Module] = sel
	return r.commit(d)
}
func (r *Router) Admit(definition string, o timeline.Occurrence, now time.Time) (execution.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	def, ok := r.exec.Snapshot().Definitions[definition]
	if !ok || r.poisoned || !r.exec.Healthy() {
		return execution.Record{}, ErrFenced
	}
	sel := r.data.Modules[def.Module]
	g := r.data.Generations[sel.Active]
	if g.Phase != Active || !r.ready[g.Number] {
		return execution.Record{}, ErrFenced
	}
	return r.exec.AdmitGeneration(definition, o, now, g.Number)
}
func (r *Router) FailCandidate(id Identity) error {
	return r.FailCandidateAt(id, time.Now())
}
func (r *Router) FailCandidateAt(id Identity, now time.Time) error {
	r.mu.Lock()
	if !r.matches(id) || r.data.Modules[id.Module].Candidate != id.Generation {
		r.mu.Unlock()
		return ErrFenced
	}
	d := routerCopy(r.data)
	sel := d.Modules[id.Module]
	sel.Candidate = 0
	if d.Generations[id.Generation].Replaces != 0 {
		sel.Failures++
		active := d.Generations[sel.Active]
		if sel.Failures > MaxRestarts {
			active.Phase = Quarantined
			sel.NextRestart = time.Time{}
		} else {
			sel.NextRestart = now.Add(time.Second * time.Duration(1<<uint(sel.Failures-1)))
		}
		d.Generations[active.Number] = active
	}
	d.Modules[id.Module] = sel
	failed := d.Generations[id.Generation]
	failed.Phase = Draining
	failed.DrainUntil = now
	d.Generations[id.Generation] = failed
	err := r.commit(d)
	if err == nil {
		delete(r.ready, id.Generation)
	}
	r.mu.Unlock()
	if err == nil {
		return r.Drain(now)
	}
	return err
}
func (r *Router) Drain(now time.Time) error {
	r.mu.Lock()
	var numbers []uint64
	for n, g := range r.data.Generations {
		if g.Phase == Draining {
			numbers = append(numbers, n)
		}
	}
	r.mu.Unlock()
	sort.Slice(numbers, func(i, j int) bool { return numbers[i] < numbers[j] })
	for _, n := range numbers {
		r.mu.Lock()
		g := r.data.Generations[n]
		if g.Phase != Draining {
			r.mu.Unlock()
			continue
		}
		if r.exec.InFlight(g.Module, n) > 0 {
			if now.Before(g.DrainUntil) {
				r.mu.Unlock()
				continue
			}
			if err := r.exec.InterruptGeneration(g.Module, n); err != nil {
				r.mu.Unlock()
				return err
			}
		}
		delete(r.ready, n)
		child := r.children[n]
		r.mu.Unlock()
		// Keep DRAINING references until child join succeeds; a failed join may be
		// retried and must never make live state/artifacts collectable.
		if child != nil {
			ctx, cancel := context.WithTimeout(context.Background(), JoinTimeout)
			err := child.Stop(ctx)
			cancel()
			var exited *exec.ExitError
			if err != nil && !errors.As(err, &exited) {
				return err
			}
		}
		r.mu.Lock()
		d := routerCopy(r.data)
		g = d.Generations[n]
		if g.Phase != Draining {
			r.mu.Unlock()
			continue
		}
		g.Phase = Retired
		d.Generations[n] = g
		err := r.commit(d)
		if err == nil {
			delete(r.children, n)
		}
		r.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// WithAuthority serializes action admission with cutover. Work IDs bind draining
// generations to pre-cutover executions. The callback must be short and durable.
func (r *Router) WithAuthority(id Identity, work string, f func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.matches(id) || !r.ready[id.Generation] {
		return ErrFenced
	}
	g := r.data.Generations[id.Generation]
	if (g.Phase != Active && g.Phase != Draining) || g.Crashed || (g.Phase == Active && r.data.Modules[id.Module].Active != id.Generation) {
		return ErrFenced
	}
	if work == "" {
		if g.Phase != Active {
			return ErrFenced
		}
	} else if !r.exec.Owns(work, id.Module, id.Generation) {
		return ErrFenced
	}
	return f()
}
func (r *Router) Dispatch(ctx context.Context, session *Session) (bool, error) {
	id := session.Identity()
	r.mu.Lock()
	if !r.matches(id) || !r.ready[id.Generation] {
		r.mu.Unlock()
		return false, ErrFenced
	}
	g := r.data.Generations[id.Generation]
	if (g.Phase != Active && g.Phase != Draining) || g.Crashed || (g.Phase == Active && r.data.Modules[id.Module].Active != id.Generation) {
		r.mu.Unlock()
		return false, ErrFenced
	}
	record, ok, err := r.exec.ClaimNext(id.Module, id.Generation)
	r.mu.Unlock()
	if err != nil || !ok {
		return false, err
	}
	frame, err := FrameOf("execute", record)
	if err != nil {
		return false, err
	}
	return true, session.Send(ctx, frame)
}
