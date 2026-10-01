package module

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const MaxHandover = 512 << 10
const HandoverBudget = 250 * time.Millisecond

var ErrHandover = errors.New("state handover refused or invalid")

type StatePacket struct {
	SourceVersion string `json:"source_version"`
	Schema        string `json:"schema"`
	Data          []byte `json:"data"`
}
type Adoption string

const (
	Compatible Adoption = "compatible"
	CleanStart Adoption = "clean_start"
	Refused    Adoption = "refused"
)

type HandoverPolicy struct {
	Negotiated bool
	Required   bool
	AllowClean bool
}

// Injected peers must honor contexts; the IPC implementation below does so.
type StateSource interface {
	Export(context.Context, bool) (StatePacket, error)
	Freeze(context.Context) (func(), error)
}
type StateCandidate interface {
	Adopt(context.Context, StatePacket, bool) (Adoption, error)
}

func checkPacket(p StatePacket, version string) error {
	if p.SourceVersion != version || len(p.Schema) > 128 || len(p.Data) > MaxHandover {
		return ErrHandover
	}
	return nil
}
func WarmHandover(ctx context.Context, source StateSource, candidate StateCandidate, version string, policy HandoverPolicy) (Adoption, error) {
	if !policy.Negotiated {
		if policy.Required {
			return Refused, ErrHandover
		}
		return CleanStart, nil
	}
	p, err := source.Export(ctx, false)
	if err != nil {
		return Refused, err
	}
	if err = checkPacket(p, version); err != nil {
		return Refused, err
	}
	adoption, err := candidate.Adopt(ctx, p, false)
	if err != nil {
		return Refused, err
	}
	if adoption == Compatible {
		return adoption, nil
	}
	if adoption == CleanStart && !policy.Required && policy.AllowClean {
		return adoption, nil
	}
	return Refused, ErrHandover
}
func (r *Router) ActivateHandover(ctx context.Context, id Identity, now time.Time, source StateSource, candidate StateCandidate, warm Adoption) error {
	ctx, cancel := context.WithTimeout(ctx, HandoverBudget)
	defer cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.matches(id) || !r.ready[id.Generation] || r.data.Modules[id.Module].Candidate != id.Generation {
		return ErrFenced
	}
	if warm == Compatible {
		release, err := source.Freeze(ctx)
		if err != nil {
			return err
		}
		defer release()
		old := r.data.Generations[r.data.Modules[id.Module].Active]
		packet, err := source.Export(ctx, true)
		if err != nil {
			return err
		}
		if err = checkPacket(packet, old.Version); err != nil {
			return err
		}
		adoption, err := candidate.Adopt(ctx, packet, true)
		if err != nil {
			return err
		}
		if adoption != Compatible {
			return ErrHandover
		}
	} else if warm != CleanStart {
		return ErrHandover
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return r.cutoverLocked(id, now)
}

// SessionStatePeer translates broker operations into bounded private IPC.
type SessionStatePeer struct{ Session *Session }

func (p SessionStatePeer) exchange(ctx context.Context, kind string, value any) (Frame, error) {
	f, err := FrameOf(kind, value)
	if err != nil {
		return Frame{}, err
	}
	if err = p.Session.Send(ctx, f); err != nil {
		return Frame{}, err
	}
	return p.Session.Receive(ctx)
}
func (p SessionStatePeer) Export(ctx context.Context, final bool) (StatePacket, error) {
	f, err := p.exchange(ctx, "state_export", struct {
		Final bool `json:"final"`
	}{final})
	var packet StatePacket
	if err != nil {
		return packet, err
	}
	if f.Type != "state_exported" || json.Unmarshal(f.Body, &packet) != nil || len(packet.Data) > MaxHandover {
		return packet, ErrHandover
	}
	return packet, nil
}
func (p SessionStatePeer) Adopt(ctx context.Context, packet StatePacket, final bool) (Adoption, error) {
	f, err := p.exchange(ctx, "state_import", struct {
		Packet StatePacket `json:"packet"`
		Final  bool        `json:"final"`
	}{packet, final})
	if err != nil {
		return Refused, err
	}
	var result struct {
		Adoption Adoption `json:"adoption"`
	}
	if f.Type != "state_imported" || json.Unmarshal(f.Body, &result) != nil {
		return Refused, ErrHandover
	}
	return result.Adoption, nil
}
func (p SessionStatePeer) Freeze(ctx context.Context) (func(), error) {
	f, err := p.exchange(ctx, "state_freeze", struct{}{})
	if err != nil {
		return nil, err
	}
	if f.Type != "state_frozen" {
		return nil, ErrHandover
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), HandoverBudget)
		defer cancel()
		f, _ := FrameOf("state_resume", struct{}{})
		_ = p.Session.Send(ctx, f)
	}, nil
}
