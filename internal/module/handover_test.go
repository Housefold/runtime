package module

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type sourceFixture struct {
	mu     sync.Mutex
	data   []byte
	frozen bool
	fail   bool
}

func (s *sourceFixture) Export(ctx context.Context, final bool) (StatePacket, error) {
	if ctx.Err() != nil {
		return StatePacket{}, ctx.Err()
	}
	if s.fail {
		return StatePacket{}, ErrHandover
	}
	if !final {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	return StatePacket{SourceVersion: "1", Schema: "schema1", Data: append([]byte(nil), s.data...)}, nil
}
func (s *sourceFixture) Freeze(context.Context) (func(), error) {
	s.mu.Lock()
	s.frozen = true
	return func() { s.frozen = false; s.mu.Unlock() }, nil
}

type candidateFixture struct {
	result     Adoption
	copies     [][]byte
	failFinal  bool
	delayFinal bool
}

func (c *candidateFixture) Adopt(ctx context.Context, p StatePacket, final bool) (Adoption, error) {
	if final && c.delayFinal {
		<-ctx.Done()
		return Refused, ctx.Err()
	}
	if final && c.failFinal {
		return Refused, ErrHandover
	}
	c.copies = append(c.copies, append([]byte(nil), p.Data...))
	return c.result, nil
}
func TestStateIsolation(t *testing.T) {
	root := t.TempDir()
	a, err := AllocateState(root, Identity{Module: "synthetic", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := AllocateState(root, Identity{Module: "synthetic", Generation: 2})
	a.Write("state", []byte("active"))
	b.Write("state", []byte("candidate"))
	raw, _ := a.Read("state")
	if string(raw) != "active" {
		t.Fatal("shared state")
	}
	raw[0] = 'X'
	again, _ := a.Read("state")
	if string(again) != "active" {
		t.Fatal("ownership")
	}
	if b.Write("../state", nil) == nil {
		t.Fatal("path escape")
	}
	reopen, _ := AllocateState(root, Identity{Module: "synthetic", Generation: 1})
	raw, _ = reopen.Read("state")
	if string(raw) != "active" {
		t.Fatal("lost state")
	}
}
func TestHandoverCompatibility(t *testing.T) {
	for _, tc := range []struct {
		adopt           Adoption
		required, allow bool
		ok              bool
	}{{Compatible, true, false, true}, {CleanStart, false, true, true}, {CleanStart, true, true, false}, {CleanStart, false, false, false}, {Refused, false, true, false}} {
		s := &sourceFixture{data: []byte("warm")}
		c := &candidateFixture{result: tc.adopt}
		_, err := WarmHandover(context.Background(), s, c, "1", HandoverPolicy{Negotiated: true, Required: tc.required, AllowClean: tc.allow})
		if (err == nil) != tc.ok {
			t.Fatal(tc, err)
		}
	}
	s := &sourceFixture{data: make([]byte, MaxHandover+1)}
	if _, err := WarmHandover(context.Background(), s, &candidateFixture{result: Compatible}, "1", HandoverPolicy{Negotiated: true}); err == nil {
		t.Fatal("oversized transfer")
	}
}
func TestFinalBarrierAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		r, _, _, now, v1 := routerFixture(t)
		v2, _ := r.Prepare("synthetic", "2")
		r.Ready(v2, true, nil)
		source := &sourceFixture{data: []byte("initial")}
		candidate := &candidateFixture{result: Compatible, failFinal: fail}
		warm, err := WarmHandover(context.Background(), source, candidate, "1", HandoverPolicy{Negotiated: true})
		if err != nil {
			t.Fatal(err)
		}
		mutated := make(chan struct{})
		go func() { source.mu.Lock(); source.data = []byte("final"); source.mu.Unlock(); close(mutated) }()
		<-mutated
		err = r.ActivateHandover(context.Background(), v2, now, source, candidate, warm)
		if fail {
			if err == nil || r.Snapshot().Modules["synthetic"].Active != v1.Generation {
				t.Fatal("failed candidate replaced v1")
			}
		} else if err != nil || string(candidate.copies[1]) != "final" {
			t.Fatal(err, candidate.copies)
		}
		if source.frozen {
			t.Fatal("barrier leaked")
		}
		if string(source.data) != "final" {
			t.Fatal("source mutated by candidate")
		}
	}
}
func TestHandoverDeadline(t *testing.T) {
	r, _, _, now, v1 := routerFixture(t)
	v2, _ := r.Prepare("synthetic", "2")
	r.Ready(v2, true, nil)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	err := r.ActivateHandover(ctx, v2, now, &sourceFixture{data: []byte("state")}, &candidateFixture{result: Compatible}, Compatible)
	if !errors.Is(err, context.DeadlineExceeded) || r.Snapshot().Modules["synthetic"].Active != v1.Generation {
		t.Fatal(err)
	}
}

func TestDependencyActivationIsAtomicAndReleasesAllBarriers(t *testing.T) {
	for _, fail := range []bool{true, false} {
		r, _, _, now, original := routerFixture(t)
		peer, err := r.Prepare("dependency", "1")
		if err != nil {
			t.Fatal(err)
		}
		if err = r.Ready(peer, true, nil); err != nil {
			t.Fatal(err)
		}
		if err = r.Cutover(peer, now); err != nil {
			t.Fatal(err)
		}
		next, _ := r.Prepare("synthetic", "2")
		nextPeer, _ := r.Prepare("dependency", "2")
		r.Ready(next, true, nil)
		r.Ready(nextPeer, true, nil)
		a, b := &sourceFixture{data: []byte("a")}, &sourceFixture{data: []byte("b")}
		err = r.ActivateMany(context.Background(), []Activation{
			{Identity: next, Source: a, Candidate: &candidateFixture{result: Compatible}, Warm: Compatible},
			{Identity: nextPeer, Source: b, Candidate: &candidateFixture{result: Compatible, failFinal: fail}, Warm: Compatible},
		}, now)
		data := r.Snapshot()
		if fail {
			if err == nil || data.Modules["synthetic"].Active != original.Generation || data.Modules["dependency"].Active != peer.Generation {
				t.Fatal("partial activation", data, err)
			}
		} else if err != nil || data.Modules["synthetic"].Active != next.Generation || data.Modules["dependency"].Active != nextPeer.Generation {
			t.Fatal(data, err)
		}
		if a.frozen || b.frozen {
			t.Fatal("leaked final barrier")
		}
	}
}
