package estate

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/housefold/runtime/internal/durable"
	"sort"
	"time"

	"github.com/housefold/runtime/internal/module"
	"github.com/housefold/runtime/internal/packageverify"
	"github.com/housefold/runtime/internal/timeline"
)

func (e *Engine) Run(ctx context.Context) {
	e.op.Lock()
	err := e.initialize(time.Now().UTC())
	e.op.Unlock()
	if err != nil {
		e.fail()
	}
	if err == nil {
		if client := e.config.Catalog; client != nil {
			e.mu.Lock()
			e.catalogDone = make(chan struct{})
			done := e.catalogDone
			e.mu.Unlock()
			go func() { defer close(done); client.Run(e.ctx) }()
		}
	}
	close(e.initialized)
	if err != nil {
		<-ctx.Done()
		return
	}
	defer e.Close()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err = e.Reconcile(now.UTC()); err != nil && (errors.Is(err, ErrRecovery) || errors.Is(err, durable.ErrUncertain) || errors.Is(err, durable.ErrCorrupt)) {
				e.fail()
			}
		}
	}
}
func (e *Engine) Close() {
	e.cancel()
	e.mu.Lock()
	healthy := e.phase == "running"
	e.phase = "stopped"
	e.mu.Unlock()
	e.op.Lock()
	defer e.op.Unlock()
	e.mu.Lock()
	healthy = healthy && e.phase == "stopped"
	e.mu.Unlock()
	e.mu.Lock()
	units := make([]*unit, 0, len(e.units))
	for _, u := range e.units {
		units = append(units, u)
	}
	catalogDone := e.catalogDone
	e.mu.Unlock()
	// Every child has already received cancellation; joins share one budget.
	ctx, cancel := context.WithTimeout(context.Background(), module.JoinTimeout)
	defer cancel()
	for _, u := range units {
		if err := u.Stop(ctx); err != nil && !childStopped(err) {
			healthy = false
		}
	}
	if catalogDone != nil {
		select {
		case <-catalogDone:
		case <-ctx.Done():
			healthy = false
		}
	}
	if healthy {
		checkpointCtx, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		if err := e.writeCheckpoint(checkpointCtx); err != nil {
			e.config.Logger.Error("cold snapshot checkpoint unavailable")
		}
	}
}
func (e *Engine) Ready(ctx context.Context) error {
	select {
	case <-e.initialized:
		if !e.available() {
			return ErrRecovery
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (e *Engine) plan(d Inventory) ([]string, error) {
	if err := resourceAdmission(d); err != nil {
		return nil, err
	}
	count := 0
	for _, row := range d.Modules {
		if row.Installed {
			count++
		}
	}
	if count > MaxInstalled {
		return nil, ErrOperation
	}
	marked := map[string]int{}
	order := []string{}
	var visit func(string) error
	visit = func(id string) error {
		if marked[id] == 1 {
			return ErrDependency
		}
		if marked[id] == 2 {
			return nil
		}
		entry, ok := d.Modules[id]
		if !ok || !entry.Installed {
			return ErrDependency
		}
		b := entry.Current
		if entry.Pending != nil && entry.UpdateError == "" {
			b = entry.Pending
		}
		if b == nil {
			return ErrDependency
		}
		marked[id] = 1
		for _, dep := range b.declaration().Dependencies {
			other, ok := d.Modules[dep.Identity]
			if dep.Optional {
				continue
			}
			if !ok || !other.Installed || !other.Desired {
				return ErrDependency
			}
			selected := other.Current
			if other.Pending != nil && other.UpdateError == "" {
				selected = other.Pending
			}
			// Exact dependency versions are explicit, loosely coupled by release. Do
			// not invent compatibility for a different signed version.
			if selected == nil || selected.declaration().Version != dep.Version {
				return ErrDependency
			}
			if err := visit(dep.Identity); err != nil {
				return err
			}
		}
		marked[id] = 2
		order = append(order, id)
		return nil
	}
	ids := []string{}
	for id, row := range d.Modules {
		if row.Installed && row.Desired {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return order, nil
}
func (e *Engine) enableDependencies(d *Inventory, id string, visiting map[string]bool) error {
	if visiting[id] {
		return ErrDependency
	}
	row, ok := d.Modules[id]
	if !ok || !row.Installed {
		return ErrDependency
	}
	b := row.Current
	if row.Pending != nil && row.UpdateError == "" {
		b = row.Pending
	}
	if b == nil {
		return ErrDependency
	}
	visiting[id] = true
	for _, dep := range b.declaration().Dependencies {
		if dep.Optional {
			continue
		}
		if err := e.enableDependencies(d, dep.Identity, visiting); err != nil {
			return err
		}
	}
	delete(visiting, id)
	row.Desired = true
	d.Modules[id] = row
	return nil
}

// Stage transactionally verifies all reviewed dependency packages before
// changing desired inventory. Downloads occur elsewhere; no network is used.
func (e *Engine) Stage(ctx context.Context, bundles []Bundle, artifacts [][]byte, now time.Time) error {
	e.op.Lock()
	defer e.op.Unlock()
	if !e.available() {
		return ErrRecovery
	}
	if len(bundles) == 0 || len(bundles) > MaxInstalled || len(bundles) != len(artifacts) {
		return ErrOperation
	}
	d := cloneInventory(e.inv)
	original := cloneInventory(e.inv)
	seen := map[string]bool{}
	for i, b := range bundles {
		b.AcceptedAt = now.UTC()
		review, err := e.verify(b, artifacts[i], now)
		if err != nil {
			return err
		}
		if seen[review.Identity] {
			return ErrOperation
		}
		seen[review.Identity] = true
		if err = e.writeArtifact(review.ArtifactDigest, artifacts[i]); err != nil {
			return err
		}
		row := d.Modules[review.Identity]
		if row.Current != nil && row.Current.declaration().Version == review.Version && packageverify.Digest(row.Current.Manifest.Data) != packageverify.Digest(b.Manifest.Data) {
			return ErrOperation
		}
		row.Installed = true
		row.Pending = &b
		row.UpdateError = ""
		d.Modules[review.Identity] = row
	}
	for id := range seen {
		if err := e.enableDependencies(&d, id, map[string]bool{}); err != nil {
			return err
		}
	}
	rawInventory, _ := json.Marshal(d)
	if len(rawInventory) > durable.MaxBytes-1024 {
		return ErrOperation
	}
	if len(d.Modules) > module.MaxModules {
		return ErrOperation
	}
	order, err := e.plan(d)
	if err != nil {
		return err
	}
	if err = e.save(d); err != nil {
		return err
	}
	// Prepare every pending dependency first. No candidate accepts new work
	// until every reviewed package has prepared successfully.
	prepared := map[string]*unit{}
	ids := map[string]module.Identity{}
	selectedBundles := map[string]Bundle{}
	cleanup := func() {
		for id, row := range d.Modules {
			if !seen[id] && row.Desired && !original.Modules[id].Desired {
				row.Desired = false
				d.Modules[id] = row
			}
		}
		for id, u := range prepared {
			join, cancel := context.WithTimeout(context.Background(), module.JoinTimeout)
			_ = u.Stop(join)
			cancel()
			_ = e.router.FailCandidateAt(ids[id], now)
			e.forget(ids[id].Generation)
		}
	}
	for _, id := range order {
		row := d.Modules[id]
		selected := row.Pending
		restoring := selected == nil || row.UpdateError != ""
		if restoring {
			selected = row.Current
		}
		if selected == nil || (restoring && e.router.Accepting(id)) {
			continue
		}
		var identity module.Identity
		var err error
		if restoring {
			previous := e.router.Snapshot().Modules[id].Active
			identity, err = e.router.RestoreSelected(id, e.stateRoot("generation"))
			if err == nil {
				err = e.copyPersistent(previous, identity)
			}
		} else {
			identity, err = e.router.Prepare(id, selected.declaration().Version)
			if err == nil {
				err = e.bindNew(identity, *selected)
			}
			if err == nil && e.currentUnit(id) == nil {
				err = e.copyPersistent(e.router.Snapshot().Modules[id].Active, identity)
			}
		}
		if identity.Generation != 0 {
			ids[id] = identity
		}
		selectedBundles[id] = *selected
		var u *unit
		if err == nil {
			u, err = e.startUnit(ctx, identity, *selected)
		}
		if err != nil {
			if identity.Generation != 0 {
				join, c := context.WithTimeout(context.Background(), module.JoinTimeout)
				if u != nil {
					_ = u.Stop(join)
				}
				c()
				_ = e.router.FailCandidateAt(identity, now)
				e.forget(identity.Generation)
			}
			cleanup()
			for failed := range seen {
				entry := d.Modules[failed]
				entry.UpdateError = "preparation failed: explicit retry required"
				d.Modules[failed] = entry
			}
			_ = e.save(d)
			return err
		}
		prepared[id] = u
	}
	activations := []module.Activation{}
	oldUnits := map[string]*unit{}
	for _, id := range order {
		u := prepared[id]
		if u == nil {
			continue
		}
		a, old, prepErr := e.warm(ctx, u, selectedBundles[id])
		if prepErr != nil {
			cleanup()
			for failed := range seen {
				entry := d.Modules[failed]
				entry.UpdateError = "handover failed: explicit retry required"
				d.Modules[failed] = entry
			}
			_ = e.save(d)
			return prepErr
		}
		activations = append(activations, a)
		oldUnits[id] = old
	}
	registrations := []timeline.Schedule{}
	existingSchedules := e.timeline.Snapshot().Schedules
	for _, u := range prepared {
		for _, registration := range u.schedulesSnapshot() {
			registrations = append(registrations, registration)
			existingSchedules[registration.ID] = registration
		}
	}
	if len(existingSchedules) > timeline.MaxSchedules {
		cleanup()
		return timeline.ErrLimit
	}
	if err = e.router.ActivateMany(ctx, activations, now); err != nil {
		if errors.Is(err, durable.ErrUncertain) {
			e.fail()
			return err
		}
		cleanup()
		for failed := range seen {
			entry := d.Modules[failed]
			entry.UpdateError = "activation failed: explicit retry required"
			d.Modules[failed] = entry
		}
		_ = e.save(d)
		return err
	}
	for _, id := range order {
		u := prepared[id]
		if u == nil {
			continue
		}
		u.mu.Lock()
		u.phase = module.Active
		u.mu.Unlock()
		if old := oldUnits[id]; old != nil {
			old.mu.Lock()
			old.phase = module.Draining
			old.mu.Unlock()
		}
		entry := d.Modules[id]
		if entry.Pending != nil && entry.UpdateError == "" {
			if entry.Current == nil || entry.Current.declaration().Version != entry.Pending.declaration().Version {
				entry.Previous = entry.Current
			}
			entry.Current = entry.Pending
			entry.Pending = nil
			entry.UpdateError = ""
		}
		d.Modules[id] = entry
	}
	if err = e.save(d); err != nil {
		return err
	}
	if len(registrations) > 0 {
		if err = e.timeline.PutMany(registrations); err != nil {
			return err
		}
	}
	for _, u := range prepared {
		if err = u.send(u.ctx, "activated", "", u.identity); err != nil {
			u.cancel()
		}
	}
	return e.router.Drain(now)
}
func (e *Engine) bindNew(id module.Identity, b Bundle) error {
	s, err := module.AllocateState(e.stateRoot("generation"), id)
	if err != nil {
		return err
	}
	if err = e.router.BindReferences(id, b.declaration().ArtifactDigest, s); err != nil {
		return err
	}
	reqs := []module.Requirement{}
	for _, dep := range b.declaration().Dependencies {
		if !dep.Optional {
			reqs = append(reqs, module.Requirement{Module: dep.Identity, Version: dep.Version})
		}
	}
	return e.router.BindRequirements(id, reqs)
}
func (e *Engine) forget(n uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	u := e.units[n]
	if u == nil {
		return
	}
	u.mu.Lock()
	joined := u.joined
	u.mu.Unlock()
	if joined {
		delete(e.units, n)
	}
}
func (e *Engine) currentUnit(id string) *unit {
	data := e.router.Snapshot()
	e.mu.Lock()
	defer e.mu.Unlock()
	u := e.units[data.Modules[id].Active]
	if u != nil {
		u.mu.Lock()
		joined := u.joined
		u.mu.Unlock()
		if joined {
			return nil
		}
	}
	return u
}

type finalStateSource struct{ source, target *unit }

func (s finalStateSource) Freeze(ctx context.Context) (func(), error) { return s.source.Freeze(ctx) }
func (s finalStateSource) Export(ctx context.Context, final bool) (module.StatePacket, error) {
	p, err := s.source.Export(ctx, final)
	if err == nil && final {
		err = s.target.stores["persistent"].CopyFrom(s.source.stores["persistent"])
	}
	return p, err
}
func (e *Engine) warm(ctx context.Context, u *unit, b Bundle) (module.Activation, *unit, error) {
	old := e.currentUnit(u.identity.Module)
	a := module.Activation{Identity: u.identity, Candidate: u, Warm: module.CleanStart}
	if old == nil {
		return a, nil, nil
	}
	negotiated := old.supports("handover") && u.supports("handover")
	if !negotiated && !b.declaration().AllowClean {
		return a, old, module.ErrHandover
	}
	a.Source = finalStateSource{old, u}
	var err error
	a.Warm, err = module.WarmHandover(ctx, a.Source, u, old.identity.Version, module.HandoverPolicy{Negotiated: negotiated, Required: b.declaration().HandoverRequired, AllowClean: b.declaration().AllowClean})
	return a, old, err
}
func (e *Engine) SetDesired(id string, desired bool) error {
	e.op.Lock()
	defer e.op.Unlock()
	if !e.available() {
		return ErrRecovery
	}
	d := cloneInventory(e.inv)
	row, ok := d.Modules[id]
	if !ok || !row.Installed {
		return ErrOperation
	}
	if desired {
		if err := e.enableDependencies(&d, id, map[string]bool{}); err != nil {
			return err
		}
	} else {
		row.Desired = false
		d.Modules[id] = row
	}
	if _, err := e.plan(d); err != nil {
		return err
	}
	if err := e.save(d); err != nil {
		return err
	}
	if !desired {
		n := e.router.Snapshot().Modules[id].Active
		err := e.router.StopSelected(id)
		e.forget(n)
		return err
	}
	return nil
}
func (e *Engine) Remove(id string) error {
	e.op.Lock()
	defer e.op.Unlock()
	if !e.available() {
		return ErrRecovery
	}
	d := cloneInventory(e.inv)
	row, ok := d.Modules[id]
	if !ok || !row.Installed {
		return ErrOperation
	}
	row.Installed = false
	row.Desired = false
	d.Modules[id] = row
	if _, err := e.plan(d); err != nil {
		return err
	}
	if err := e.save(d); err != nil {
		return err
	}
	// Artifacts and all module-owned data are retained. Destructive reset is a
	// separate explicit recovery operation, never part of ordinary removal.
	n := e.router.Snapshot().Modules[id].Active
	err := e.router.StopSelected(id)
	e.forget(n)
	return err
}
func (e *Engine) Recover(id string, now time.Time) error {
	e.op.Lock()
	defer e.op.Unlock()
	if !e.available() {
		return ErrRecovery
	}
	return e.router.OperatorRecover(id, now)
}
func (e *Engine) Restart(id string) error {
	e.op.Lock()
	defer e.op.Unlock()
	if !e.available() {
		return ErrRecovery
	}
	row := e.inv.Modules[id]
	if !row.Installed || !row.Desired {
		return ErrOperation
	}
	n := e.router.Snapshot().Modules[id].Active
	err := e.router.StopSelected(id)
	e.forget(n)
	return err
}
func (e *Engine) Rollback(ctx context.Context, id string, now time.Time) error {
	e.op.Lock()
	defer e.op.Unlock()
	if !e.available() {
		return ErrRecovery
	}
	row := e.inv.Modules[id]
	if row.Previous == nil || !row.Installed || !row.Desired {
		return ErrOperation
	}
	if _, err := e.verify(*row.Previous, nil, row.Previous.AcceptedAt); err != nil {
		return err
	}
	dCheck := cloneInventory(e.inv)
	check := dCheck.Modules[id]
	check.Current = check.Previous
	check.Pending = nil
	dCheck.Modules[id] = check
	if _, err := e.plan(dCheck); err != nil {
		return err
	}
	candidate, err := e.router.PrepareRollback(id, e.stateRoot("generation"))
	if err != nil {
		return err
	}
	previousUnit := e.currentUnit(id)
	retained := e.router.Snapshot().Modules[id].Previous
	err = e.copyPersistent(retained, candidate)
	var u *unit
	if err == nil {
		u, err = e.startUnit(ctx, candidate, *row.Previous)
	}
	if err == nil {
		err = e.router.Cutover(candidate, now)
	}
	if err != nil {
		if errors.Is(err, durable.ErrUncertain) {
			e.fail()
			return err
		}
		if u != nil {
			join, c := context.WithTimeout(context.Background(), module.JoinTimeout)
			_ = u.Stop(join)
			c()
		}
		_ = e.router.FailCandidateAt(candidate, now)
		e.forget(candidate.Generation)
		return err
	}
	u.mu.Lock()
	u.phase = module.Active
	u.mu.Unlock()
	if previousUnit != nil {
		previousUnit.mu.Lock()
		previousUnit.phase = module.Draining
		previousUnit.mu.Unlock()
	}
	if registrations := u.schedulesSnapshot(); len(registrations) > 0 {
		if err = e.timeline.PutMany(registrations); err != nil {
			return err
		}
	}
	if err = u.send(u.ctx, "activated", "", candidate); err != nil {
		u.cancel()
	}
	old := row.Current
	row.Current = row.Previous
	row.Previous = old
	row.Pending = nil
	row.UpdateError = ""
	d := cloneInventory(e.inv)
	d.Modules[id] = row
	if err = e.save(d); err != nil {
		return err
	}
	return e.router.Drain(now)
}
func (e *Engine) copyPersistent(from uint64, to module.Identity) error {
	if from == 0 {
		return nil
	}
	src, err := module.OpenState(e.stateRoot("persistent"), module.Identity{Module: to.Module, Generation: from})
	if err != nil {
		return err
	}
	dst, err := module.AllocateState(e.stateRoot("persistent"), to)
	if err != nil {
		return err
	}
	return dst.CopyFrom(src)
}
func (e *Engine) Reconcile(now time.Time) error {
	e.op.Lock()
	defer e.op.Unlock()
	if !e.available() {
		return ErrRecovery
	}
	// Continue only previously admitted draining work until the router's
	// deadline. Never promote disabled/quarantined generation queues.
	snapshot := e.router.Snapshot()
	for n, g := range snapshot.Generations {
		if g.Phase != module.Draining {
			continue
		}
		e.mu.Lock()
		old := e.units[n]
		e.mu.Unlock()
		if old == nil {
			continue
		}
		if err := e.exec.ResumeGeneration(g.Module, n, now); err != nil {
			return err
		}
		if err := old.cancelExecutions(now); err != nil {
			old.cancel()
		}
		if _, err := e.router.Dispatch(old.ctx, old.process.Session); err != nil {
			old.cancel()
		}
	}
	if err := e.router.Drain(now); err != nil {
		return err
	}
	if err := e.resourceTick(now); err != nil {
		return err
	}
	order, err := e.plan(e.inv)
	if err != nil {
		return err
	}
	data := e.router.Snapshot()
	for _, id := range order {
		row := e.inv.Modules[id]
		if row.Current == nil || row.PressurePaused {
			continue
		}
		sel := data.Modules[id]
		g := data.Generations[sel.Active]
		if g.Phase == module.Quarantined {
			continue
		}
		e.mu.Lock()
		u := e.units[g.Number]
		e.mu.Unlock()
		if u != nil {
			u.mu.Lock()
			joined := u.joined
			u.mu.Unlock()
			if joined {
				e.forget(g.Number)
				u = nil
			}
		}
		if u != nil {
			select {
			case <-u.readDone:
				if err = e.router.Crash(u.identity, now); err != nil {
					return err
				}
				e.forget(g.Number)
			case <-u.process.Done():
				if err = e.router.Crash(u.identity, now); err != nil {
					return err
				}
				e.forget(g.Number)
			default:
				if err = e.observeUnit(u); err != nil {
					if err = e.router.Crash(u.identity, now); err != nil {
						return err
					}
					e.forget(g.Number)
					continue
				}
				if err = u.cancelExecutions(now); err != nil {
					u.cancel()
					continue
				}
				if err = e.exec.ResumeGeneration(id, g.Number, now); err != nil {
					return err
				}
				if _, err = e.router.Dispatch(u.ctx, u.process.Session); err != nil {
					u.cancel()
				}
				continue
			}
			data = e.router.Snapshot()
			sel = data.Modules[id]
			g = data.Generations[sel.Active]
		}
		if g.Phase == module.Quarantined {
			continue
		}
		// Required dependencies must be healthy before dependent startup. Existing
		// modules stay alive during dependency/HA loss; health is separately visible.
		dependenciesReady := true
		for _, dep := range row.Current.declaration().Dependencies {
			if !dep.Optional {
				if !e.router.Accepting(dep.Identity) {
					dependenciesReady = false
					break
				}
			}
		}
		if !dependenciesReady {
			continue
		}
		var candidate module.Identity
		if g.Crashed {
			if sel.NextRestart.IsZero() || now.Before(sel.NextRestart) {
				continue
			}
			candidate, err = e.router.RestartCandidate(id, e.stateRoot("generation"), now)
		} else {
			candidate, err = e.router.RestoreSelected(id, e.stateRoot("generation"))
		}
		if err != nil {
			continue
		}
		if err = e.copyPersistent(g.Number, candidate); err != nil {
			_ = e.router.FailCandidateAt(candidate, now)
			return err
		}
		prep, cancel := context.WithTimeout(e.ctx, 30*time.Second)
		next, launchErr := e.startUnit(prep, candidate, *row.Current)
		if launchErr == nil {
			launchErr = e.router.Cutover(candidate, now)
		}
		cancel()
		if launchErr != nil {
			if errors.Is(launchErr, durable.ErrUncertain) {
				e.fail()
				return launchErr
			}
			if next != nil {
				join, c := context.WithTimeout(context.Background(), module.JoinTimeout)
				_ = next.Stop(join)
				c()
			}
			_ = e.router.FailCandidateAt(candidate, now)
			e.forget(candidate.Generation)
			continue
		}
		if registrations := next.schedulesSnapshot(); len(registrations) > 0 {
			if err = e.timeline.PutMany(registrations); err != nil {
				return err
			}
		}
		next.mu.Lock()
		next.phase = module.Active
		next.mu.Unlock()
		_ = next.send(next.ctx, "activated", "", candidate)
	}
	// Timeline acknowledgments follow generation-fenced durable admission.
	if len(e.timeline.Snapshot().Schedules) != 0 {
		if _, err = e.timeline.Advance(now, false); err != nil {
			return err
		}
	}
	pending := e.timeline.Snapshot().Pending
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := pending[ids[i]], pending[ids[j]]
		if a.Logical.Equal(b.Logical) {
			return a.ID < b.ID
		}
		return a.Logical.Before(b.Logical)
	})
	for _, id := range ids {
		o := pending[id]
		if _, err = e.router.Admit(o.Schedule, o, now); err != nil {
			if errors.Is(err, module.ErrFenced) {
				continue
			}
			return err
		}
		if err = e.timeline.Ack(id); err != nil {
			return err
		}
	}

	// Coordination records can be pruned only after owned children have joined.
	// Physical state/artifact deletion belongs to the explicit storage policy.
	for id := range e.inv.Modules {
		retired, err := e.router.Collectable(id)
		if err != nil {
			return err
		}
		for _, g := range retired {
			e.forget(g.Number)
		}
	}
	return e.collectStorage(false)
}
