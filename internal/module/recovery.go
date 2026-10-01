package module

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"regexp"
	"time"
)

const MaxRestarts = 3

var artifactDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (r *Router) BindReferences(id Identity, digest string, store *StateStore) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.matches(id) || r.data.Generations[id.Generation].Phase != Preparing || !artifactDigest.MatchString(digest) || store == nil || store.identity.Module != id.Module || store.identity.Generation != id.Generation {
		return ErrFenced
	}
	d := routerCopy(r.data)
	g := d.Generations[id.Generation]
	g.ArtifactDigest = digest
	g.StatePath = store.path
	d.Generations[g.Number] = g
	return r.commit(d)
}
func cloneState(root string, from Generation, to Identity) (*StateStore, error) {
	target, err := AllocateState(root, to)
	if err != nil {
		return nil, err
	}
	if from.StatePath == "" {
		return target, nil
	}
	source, err := OpenState(root, Identity{Module: from.Module, Generation: from.Number})
	if err != nil {
		return nil, err
	}
	if source.path != from.StatePath {
		return nil, ErrFenced
	}
	source.mu.Lock()
	data, err := source.read()
	source.mu.Unlock()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	if err = target.file.Save(raw); err != nil {
		return nil, err
	}
	return target, nil
}
func (r *Router) PrepareRollback(module, root string) (Identity, error) {
	r.mu.Lock()
	sel := r.data.Modules[module]
	old, ok := r.data.Generations[sel.Previous]
	if !ok || old.Phase != Retired {
		r.mu.Unlock()
		return Identity{}, ErrFenced
	}
	id, err := r.prepareLocked(module, old.Version)
	r.mu.Unlock()
	if err != nil {
		return id, err
	}
	store, err := cloneState(root, old, id)
	if err == nil && old.ArtifactDigest != "" {
		err = r.BindReferences(id, old.ArtifactDigest, store)
		if err == nil {
			err = r.BindRequirements(id, old.Required)
		}
	}
	if err != nil {
		_ = r.FailCandidate(id)
	}
	return id, err
}

// Collectable removes only obsolete retired coordination records and returns
// their references to the owning staging layer. Selected/previous/draining
// artifacts and state stay retained; filesystem deletion is explicit.
func (r *Router) Collectable(module string) ([]Generation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sel := r.data.Modules[module]
	d := routerCopy(r.data)
	var out []Generation
	for n, g := range d.Generations {
		if g.Module == module && g.Phase == Retired && n != sel.Active && n != sel.Candidate && n != sel.Previous && r.exec.InFlight(module, n) == 0 {
			out = append(out, g)
			delete(d.Generations, n)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	if err := r.commit(d); err != nil {
		return nil, err
	}
	return out, nil
}
func (r *Router) Crash(id Identity, now time.Time) error {
	r.mu.Lock()
	if !r.matches(id) || r.data.Modules[id.Module].Active != id.Generation {
		r.mu.Unlock()
		return ErrFenced
	}
	g := r.data.Generations[id.Generation]
	if g.Crashed {
		r.mu.Unlock()
		return r.stopOwned(id.Generation)
	}
	delete(r.ready, id.Generation)
	if err := r.exec.PauseGeneration(id.Module, id.Generation); err != nil {
		r.mu.Unlock()
		return err
	}
	d := routerCopy(r.data)
	sel := d.Modules[id.Module]
	sel.Failures++
	g.Crashed = true
	if sel.Failures > MaxRestarts {
		g.Phase = Quarantined
		sel.NextRestart = time.Time{}
	} else {
		sel.NextRestart = now.Add(time.Second * time.Duration(1<<uint(sel.Failures-1)))
	}
	d.Modules[id.Module] = sel
	d.Generations[g.Number] = g
	err := r.commit(d)
	if err == nil {
		delete(r.ready, g.Number)
	}
	r.mu.Unlock()
	stopErr := r.stopOwned(id.Generation)
	if err != nil {
		return err
	}
	return stopErr
}
func (r *Router) RestartCandidate(module, root string, now time.Time) (Identity, error) {
	r.mu.Lock()
	sel := r.data.Modules[module]
	old := r.data.Generations[sel.Active]
	if r.children[old.Number] != nil || !old.Crashed || old.Phase != Active || sel.Failures > MaxRestarts || sel.NextRestart.IsZero() || now.Before(sel.NextRestart) {
		r.mu.Unlock()
		return Identity{}, ErrFenced
	}
	id, err := r.prepareLocked(module, old.Version)
	if err == nil {
		d := routerCopy(r.data)
		g := d.Generations[id.Generation]
		g.Replaces = old.Number
		d.Generations[g.Number] = g
		err = r.commit(d)
	}
	r.mu.Unlock()
	if err != nil {
		return id, err
	}
	store, err := cloneState(root, old, id)
	if err == nil && old.ArtifactDigest != "" {
		err = r.BindReferences(id, old.ArtifactDigest, store)
		if err == nil {
			err = r.BindRequirements(id, old.Required)
		}
	}
	if err != nil {
		_ = r.FailCandidateAt(id, now)
	}
	return id, err
}

// OperatorRecover resets the selected version's budget, not its readiness or
// epoch. The next restart still prepares a new generation and must cut over.
func (r *Router) OperatorRecover(module string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	sel := r.data.Modules[module]
	g := r.data.Generations[sel.Active]
	if g.Phase != Quarantined {
		return ErrFenced
	}
	d := routerCopy(r.data)
	sel.Failures = 0
	sel.NextRestart = now
	g.Phase = Active
	g.Crashed = true
	d.Modules[module] = sel
	d.Generations[g.Number] = g
	return r.commit(d)
}

func (r *Router) stopOwned(n uint64) error {
	r.mu.Lock()
	child := r.children[n]
	r.mu.Unlock()
	if child == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), JoinTimeout)
	err := child.Stop(ctx)
	cancel()
	var exited *exec.ExitError
	if err != nil && !errors.As(err, &exited) {
		return err
	}
	r.mu.Lock()
	delete(r.children, n)
	r.mu.Unlock()
	return nil
}

// StopSelected fences new work before stopping/joining the selected process.
// It changes neither version nor restart budget; desired-state ownership is in
// the estate. Failed joins remain owned and block replacement.
func (r *Router) StopSelected(moduleID string) error {
	r.mu.Lock()
	sel := r.data.Modules[moduleID]
	delete(r.ready, sel.Active)
	err := r.exec.PauseGeneration(moduleID, sel.Active)
	r.mu.Unlock()
	if err != nil {
		return err
	}
	return r.stopOwned(sel.Active)
}

// RestoreSelected prepares a fresh epoch from the selected isolated state after
// a deliberate stop or a Runtime reboot. It never clears terminal quarantine.
func (r *Router) RestoreSelected(moduleID, root string) (Identity, error) {
	r.mu.Lock()
	sel := r.data.Modules[moduleID]
	old, ok := r.data.Generations[sel.Active]
	if !ok || old.Phase != Active || old.Crashed || r.children[old.Number] != nil {
		r.mu.Unlock()
		return Identity{}, ErrFenced
	}
	id, err := r.prepareLocked(moduleID, old.Version)
	if err == nil {
		d := routerCopy(r.data)
		g := d.Generations[id.Generation]
		g.Replaces = old.Number
		d.Generations[g.Number] = g
		err = r.commit(d)
	}
	r.mu.Unlock()
	if err != nil {
		return id, err
	}
	store, err := cloneState(root, old, id)
	if err == nil {
		err = r.BindReferences(id, old.ArtifactDigest, store)
		if err == nil {
			err = r.BindRequirements(id, old.Required)
		}
	}
	if err != nil {
		_ = r.FailCandidate(id)
	}
	return id, err
}
