// Package action fences module action admission and durably retains uncertainty.
// Production HA transport is composed by Runtime outside this package.
package action

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/housefold/runtime/internal/durable"
	"github.com/housefold/runtime/internal/module"
	"os"
	"regexp"
	"sync"
	"time"
)

const MaxRequests = 1024
const MaxInFlight = 16
const MaxPayload = 64 << 10
const RequestTimeout = 5 * time.Second

type Outcome string

const (
	NotSent  Outcome = "not_sent"
	Accepted Outcome = "accepted"
	Rejected Outcome = "rejected_by_ha"
	Unknown  Outcome = "unknown"
)

type Observation string

const (
	Matches     Observation = "observed_matches"
	NotObserved Observation = "not_observed"
)

type Request struct {
	ID      string          `json:"id"`
	Work    string          `json:"work,omitempty"`
	Domain  string          `json:"domain"`
	Service string          `json:"service"`
	Data    json.RawMessage `json:"data"`
}
type Record struct {
	Key         string
	Identity    module.Identity
	ID          string
	Work        string
	Digest      string
	Outcome     Outcome
	Observation Observation
}
type Transport interface {
	Call(context.Context, Request) (Outcome, error)
}
type AttributedTransport interface {
	CallAttributed(context.Context, module.Identity, Request) (Outcome, error)
}
type data struct {
	Version int
	Records map[string]Record
}
type Gateway struct {
	mu        sync.Mutex
	store     durable.Store
	router    *module.Router
	transport Transport
	data      data
	slots     chan struct{}
	poisoned  bool
}

var token = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

func Open(store durable.Store, router *module.Router, transport Transport) (*Gateway, error) {
	if router == nil || transport == nil {
		return nil, module.ErrProtocol
	}
	raw, err := store.Load()
	d := data{Version: 1, Records: map[string]Record{}}
	fresh := errors.Is(err, os.ErrNotExist)
	if !fresh {
		if err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &d) != nil || d.Version != 1 || d.Records == nil || len(d.Records) > MaxRequests {
			return nil, durable.ErrCorrupt
		}
	}
	for key, r := range d.Records {
		if key != r.Key || key != requestKey(r.Identity.Module, r.ID) || r.Digest == "" || (r.Outcome != NotSent && r.Outcome != Accepted && r.Outcome != Rejected && r.Outcome != Unknown) {
			return nil, durable.ErrCorrupt
		}
	}
	g := &Gateway{store: store, router: router, transport: transport, data: d, slots: make(chan struct{}, MaxInFlight)}
	if fresh {
		if err = g.commit(d); err != nil {
			return nil, err
		}
	}
	return g, nil
}
func requestKey(module, id string) string {
	sum := sha256.Sum256([]byte(module + "\x00" + id))
	return hex.EncodeToString(sum[:])
}
func (g *Gateway) commit(d data) error {
	if g.poisoned {
		return durable.ErrUncertain
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if err = g.store.Save(raw); err != nil {
		if errors.Is(err, durable.ErrUncertain) {
			g.poisoned = true
		}
		return err
	}
	g.data = d
	return nil
}
func (g *Gateway) copy() data {
	c := data{Version: 1, Records: map[string]Record{}}
	for k, v := range g.data.Records {
		c.Records[k] = v
	}
	return c
}
func valid(r Request) bool {
	if len(r.ID) == 0 || len(r.ID) > 128 || len(r.Work) > 128 || !token.MatchString(r.Domain) || !token.MatchString(r.Service) || len(r.Data) > MaxPayload || !json.Valid(r.Data) {
		return false
	} // bounded depth through the canonical module codec
	depth := 0
	quoted, escape := false, false
	for _, c := range r.Data {
		if quoted {
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '[', '{':
			depth++
			if depth > module.MaxDepth-2 {
				return false
			}
		case ']', '}':
			depth--
		}
	}
	trimmed := bytes.TrimSpace(r.Data)
	return len(trimmed) > 0 && trimmed[0] == '{'
}
func ValidRequest(request Request) bool { return valid(request) }
func (g *Gateway) Submit(ctx context.Context, id module.Identity, request Request) (Record, error) {
	notSent := Record{Identity: id, ID: request.ID, Work: request.Work, Outcome: NotSent}
	if !valid(request) {
		return notSent, module.ErrProtocol
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return notSent, ctx.Err()
	}
	raw, _ := json.Marshal(request)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	key := requestKey(id.Module, request.ID)
	g.mu.Lock()
	if old, ok := g.data.Records[key]; ok {
		g.mu.Unlock()
		if old.Digest != digest {
			return notSent, module.ErrProtocol
		}
		return old, nil
	}
	if len(g.data.Records) >= MaxRequests || g.poisoned {
		g.mu.Unlock()
		return notSent, module.ErrFenced
	}
	select {
	case g.slots <- struct{}{}:
	default:
		g.mu.Unlock()
		return notSent, module.ErrFenced
	}
	defer func() { <-g.slots }()
	record := Record{Key: key, Identity: id, ID: request.ID, Work: request.Work, Digest: digest, Outcome: Unknown}
	err := g.router.WithAuthority(id, request.Work, func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d := g.copy()
		d.Records[key] = record
		return g.commit(d)
	})
	g.mu.Unlock()
	if err != nil {
		return notSent, err
	}
	// Once the durable unknown marker exists, invocation may reach HA. No
	// automatic retries are performed, including timeout and Runtime recovery.
	request.Data = append(json.RawMessage(nil), request.Data...)
	var outcome Outcome
	var callErr error
	if attributed, ok := g.transport.(AttributedTransport); ok {
		outcome, callErr = attributed.CallAttributed(ctx, id, request)
	} else {
		outcome, callErr = g.transport.Call(ctx, request)
	}
	if outcome == NotSent {
		record.Outcome = NotSent
	} else if callErr == nil && (outcome == Accepted || outcome == Rejected) {
		record.Outcome = outcome
	}
	g.mu.Lock()
	d := g.copy()
	d.Records[key] = record
	err = g.commit(d)
	g.mu.Unlock()
	if err != nil {
		record.Outcome = Unknown
		return record, err
	}
	return record, callErr
}
func (g *Gateway) Observe(moduleID, id string, observation Observation) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if observation != Matches && observation != NotObserved {
		return module.ErrProtocol
	}
	key := requestKey(moduleID, id)
	r, ok := g.data.Records[key]
	if !ok {
		return module.ErrProtocol
	}
	d := g.copy()
	r.Observation = observation
	d.Records[key] = r
	return g.commit(d)
}
func (g *Gateway) Handle(ctx context.Context, s *module.Session, f module.Frame) error {
	var request Request
	if f.Type != "action_request" || json.Unmarshal(f.Body, &request) != nil || request.ID != f.ID {
		return module.ErrProtocol
	}
	record, err := g.Submit(ctx, s.Identity(), request)
	reply, encodeErr := module.FrameOf("action_result", record)
	if encodeErr != nil {
		return encodeErr
	}
	reply.ID = f.ID
	if sendErr := s.Send(ctx, reply); sendErr != nil {
		return sendErr
	}
	return err
}
