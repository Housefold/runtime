package estate

import (
	"errors"
	"time"

	"github.com/housefold/runtime/internal/audit"
	"github.com/housefold/runtime/internal/durable"
)

type AuditSnapshot struct {
	Status  string
	Entries []audit.Entry
}

func (e *Engine) AuditSnapshot() AuditSnapshot {
	e.mu.Lock()
	j := e.audit
	e.mu.Unlock()
	if j == nil {
		return AuditSnapshot{Status: "unavailable"}
	}
	return AuditSnapshot{Status: "available", Entries: j.Entries()}
}
func (e *Engine) BeginAdmin(user, op, id string, now time.Time) (uint64, error) {
	e.op.Lock()
	defer e.op.Unlock()
	e.mu.Lock()
	j := e.audit
	phase := e.phase
	e.mu.Unlock()
	if phase != "running" && (op != "factory_reset" || phase == "stopped") {
		return 0, audit.ErrUnavailable
	}
	if j == nil {
		// Confirmed factory reset is the explicit escape from corrupt/unwritable
		// audit/storage. It deletes the whole estate including its audit by design.
		if op == "factory_reset" && !e.available() {
			return 0, nil
		}
		return 0, audit.ErrUnavailable
	}
	sequence, err := j.Begin(user, op, id, now)
	if err != nil && op == "factory_reset" {
		return 0, nil
	} // Explicit reset may clear an unwritable journal too.
	return sequence, err
}
func (e *Engine) FinishAdmin(sequence uint64, success bool, reset bool) error {
	e.op.Lock()
	defer e.op.Unlock()
	e.mu.Lock()
	stopped := e.phase == "stopped"
	e.mu.Unlock()
	if stopped {
		return audit.ErrUnavailable
	}
	if reset {
		return nil
	} // A successful explicit reset deletes this journal too.
	e.mu.Lock()
	j := e.audit
	e.mu.Unlock()
	if j == nil {
		return audit.ErrUnavailable
	}
	outcome := "failed"
	if success {
		outcome = "completed"
	}
	return j.Finish(sequence, outcome)
}
func (e *Engine) openAudit(create bool) error {
	j, err := audit.Open(e.owned("audit"), create)
	if err != nil {
		if errors.Is(err, audit.ErrUnavailable) || errors.Is(err, durable.ErrCorrupt) || errors.Is(err, durable.ErrUncertain) {
			return ErrRecovery
		}
		return err
	}
	e.mu.Lock()
	e.audit = j
	e.mu.Unlock()
	return nil
}
