package estate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/housefold/runtime/internal/action"
	"github.com/housefold/runtime/internal/durable"
	"github.com/housefold/runtime/internal/execution"
	"github.com/housefold/runtime/internal/module"
	"github.com/housefold/runtime/internal/timeline"
)

type unit struct {
	engine                         *Engine
	process                        *module.Process
	identity                       module.Identity
	stores                         map[string]*module.StateStore
	ctx                            context.Context
	cancel                         context.CancelFunc
	readDone                       chan struct{}
	ready                          chan struct{}
	readyOnce                      sync.Once
	workers                        sync.WaitGroup
	mu                             sync.Mutex
	service, ui, frozen, streaming bool
	phase                          module.Lifecycle
	negotiated                     module.Hello
	lastHealth                     time.Time
	waiter                         chan module.Frame
	waiterID                       string
	rpcGate                        chan struct{}
	actions                        chan struct{}
	workersDone                    chan struct{}
	joined                         bool
	schedules                      map[string]timeline.Schedule
	canceled                       map[string]time.Time
}

func (u *unit) Stop(ctx context.Context) error {
	u.cancel()
	err := u.process.Stop(ctx)
	select {
	case <-u.readDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-u.workersDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	u.mu.Lock()
	u.joined = true
	u.service = false
	u.ui = false
	u.mu.Unlock()
	return err
}
func (u *unit) send(ctx context.Context, kind, id string, value any) error {
	frame, err := module.FrameOf(kind, value)
	if err != nil {
		return err
	}
	frame.ID = id
	return u.process.Session.Send(ctx, frame)
}
func (u *unit) reply(frame module.Frame, value any, err error) error {
	// Response errors are stable codes, never underlying paths/payloads/secrets.
	code := ""
	if err != nil {
		code = "unavailable"
	}
	return u.send(u.ctx, "result", frame.ID, struct {
		Success bool   `json:"success"`
		Data    any    `json:"data,omitempty"`
		Error   string `json:"error,omitempty"`
	}{err == nil, value, code})
}
func (e *Engine) startUnit(ctx context.Context, id module.Identity, b Bundle) (*unit, error) {
	stores := map[string]*module.StateStore{}
	for _, scope := range []string{"persistent", "cache", "generation", "temp"} {
		s, err := module.AllocateState(e.stateRoot(scope), id)
		if err != nil {
			return nil, err
		}
		stores[scope] = s
	}
	childCtx, cancel := context.WithCancel(e.ctx)
	limit := b.declaration().Resources.MemoryKiB
	if limit == 0 {
		limit = 128 * 1024
	}
	p, err := module.LaunchLocal(childCtx, module.LocalLaunch{Path: e.archivePath(b.declaration().ArtifactDigest), Identity: id, RSSLimitKiB: limit})
	if err != nil {
		cancel()
		return nil, err
	}
	u := &unit{engine: e, process: p, identity: id, stores: stores, ctx: childCtx, cancel: cancel, readDone: make(chan struct{}), workersDone: make(chan struct{}), ready: make(chan struct{}), rpcGate: make(chan struct{}, 1), actions: make(chan struct{}, 2), phase: module.Preparing, schedules: map[string]timeline.Schedule{}, canceled: map[string]time.Time{}}
	e.mu.Lock()
	e.units[id.Generation] = u
	e.mu.Unlock()
	if err = e.router.Track(id, u); err != nil {
		close(u.readDone)
		close(u.workersDone)
		join, c := context.WithTimeout(context.Background(), module.JoinTimeout)
		_ = u.Stop(join)
		c()
		return u, err
	}
	supported := module.Hello{Major: module.Major, Capabilities: []string{"health", "state", "storage", "execution", "actions", "handover"}, Required: []string{"health"}}
	negotiate, cancelNegotiation := context.WithTimeout(ctx, module.IOTimeout)
	u.negotiated, err = p.Session.Negotiate(negotiate, supported)
	cancelNegotiation()
	if err != nil {
		close(u.readDone)
		close(u.workersDone)
		join, c := context.WithTimeout(context.Background(), module.JoinTimeout)
		_ = u.Stop(join)
		c()
		return u, err
	}
	// Reader owns every Receive, including RPC replies. No handover call steals
	// frames from module health, action or execution handling.
	u.workers.Add(1)
	go u.read()
	go u.heartbeat()
	select {
	case <-u.ready:
		if err = e.router.Ready(id, true, u); err == nil {
			return u, nil
		}
	case <-u.readDone:
		err = ErrOperation
	case <-ctx.Done():
		err = ctx.Err()
	}
	join, c := context.WithTimeout(context.Background(), module.JoinTimeout)
	_ = u.Stop(join)
	c()
	return u, err
}
func (u *unit) heartbeat() {
	defer u.workers.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-u.ctx.Done():
			return
		case now := <-ticker.C:
			u.mu.Lock()
			last := u.lastHealth
			u.mu.Unlock()
			if !last.IsZero() && now.Sub(last) > 4*time.Second {
				u.cancel()
				return
			}
			if u.send(u.ctx, "health_request", "", struct{}{}) != nil {
				u.cancel()
				return
			}
		}
	}
}
func (u *unit) read() {
	defer func() { close(u.readDone); go func() { u.workers.Wait(); close(u.workersDone) }() }()
	defer u.cancel()
	defer func() { u.mu.Lock(); u.service = false; u.ui = false; u.mu.Unlock() }()
	defer u.process.Session.Close()
	window := time.Now()
	frames, bytes := 0, 0
	for {
		f, err := u.process.Session.Receive(u.ctx)
		if err != nil {
			return
		}
		now := time.Now()
		if now.Sub(window) >= time.Second {
			window = now
			frames = 0
			bytes = 0
		}
		frames++
		bytes += len(f.Body)
		if frames > 64 || bytes > 2<<20 {
			return
		}
		u.mu.Lock()
		waiter, waitID := u.waiter, u.waiterID
		u.mu.Unlock()
		if waiter != nil && f.ID == waitID {
			select {
			case waiter <- f:
			default:
				return
			}
			continue
		}
		if f.Type == "state_exported" || f.Type == "state_imported" || f.Type == "state_frozen" {
			continue
		} // bounded late RPC replies
		switch f.Type {
		case "ready":
			var h struct {
				Accepting bool `json:"accepting"`
				Service   bool `json:"service_healthy"`
				UI        bool `json:"ui_healthy"`
			}
			if json.Unmarshal(f.Body, &h) != nil || !h.Accepting || !h.Service {
				return
			}
			u.mu.Lock()
			if u.phase != module.Preparing {
				u.mu.Unlock()
				return
			}
			u.service = h.Service
			u.ui = h.UI
			u.lastHealth = now
			u.mu.Unlock()
			u.readyOnce.Do(func() { close(u.ready) })
		case "health":
			var h struct {
				Service bool `json:"service_healthy"`
				UI      bool `json:"ui_healthy"`
			}
			if json.Unmarshal(f.Body, &h) != nil || !h.Service {
				return
			}
			u.mu.Lock()
			u.service = h.Service
			u.ui = h.UI
			u.lastHealth = now
			u.mu.Unlock()
		case "subscribe_state":
			u.mu.Lock()
			already := u.streaming
			u.streaming = true
			u.mu.Unlock()
			if already {
				if u.reply(f, nil, ErrOperation) != nil {
					return
				}
				continue
			}
			u.workers.Add(1)
			go u.streamState()
			if u.reply(f, nil, nil) != nil {
				return
			}
		case "storage_read", "storage_write":
			if u.storage(f) != nil {
				return
			}
		case "definition":
			var def execution.Definition
			if json.Unmarshal(f.Body, &def) != nil {
				return
			}
			def.Module = u.identity.Module
			def.ID, err = definitionsPrefix(u.identity.Module, def.ID)
			if err == nil {
				existing, ok := u.engine.exec.Snapshot().Definitions[def.ID]
				u.mu.Lock()
				preparing := u.phase == module.Preparing
				u.mu.Unlock()
				if preparing && ok && existing != def {
					err = ErrOperation
				} else {
					err = u.engine.exec.Configure(def)
				}
			}
			if u.reply(f, nil, err) != nil {
				return
			}
		case "schedule":
			var schedule timeline.Schedule
			if json.Unmarshal(f.Body, &schedule) != nil {
				return
			}
			local := schedule.ID
			schedule.ID, err = definitionsPrefix(u.identity.Module, local)
			if err == nil {
				if !timeline.ValidateSchedule(schedule) {
					err = timeline.ErrLimit
				} else {
					u.mu.Lock()
					preparing := u.phase == module.Preparing
					if preparing {
						if len(u.schedules) >= timeline.MaxSchedules {
							err = timeline.ErrLimit
						} else {
							u.schedules[schedule.ID] = schedule
						}
					}
					u.mu.Unlock()
					if !preparing {
						select {
						case u.actions <- struct{}{}:
							u.workers.Add(1)
							go func(frame module.Frame, registration timeline.Schedule) {
								defer u.workers.Done()
								defer func() { <-u.actions }()
								err := u.engine.router.WithAuthority(u.identity, "", func() error { return u.engine.timeline.Put(registration) })
								_ = u.reply(frame, nil, err)
							}(f, schedule)
							continue
						default:
							err = ErrOperation
						}
					}
				}
			}
			if u.reply(f, nil, err) != nil {
				return
			}
		case "trigger":
			select {
			case u.actions <- struct{}{}:
			default:
				if u.reply(f, nil, ErrOperation) != nil {
					return
				}
				continue
			}
			u.workers.Add(1)
			go func(frame module.Frame) { defer u.workers.Done(); defer func() { <-u.actions }(); u.trigger(frame) }(f)
		case "execution_finished":
			var finish struct {
				ID       string `json:"id"`
				Canceled bool   `json:"canceled"`
			}
			if json.Unmarshal(f.Body, &finish) != nil {
				return
			}
			if !u.engine.exec.Owns(finish.ID, u.identity.Module, u.identity.Generation) {
				err = module.ErrFenced
			} else {
				err = u.engine.exec.Finish(finish.ID, finish.Canceled, now)
			}
			if u.reply(f, nil, err) != nil {
				return
			}
		case "action_request":
			select {
			case u.actions <- struct{}{}:
			default:
				if u.send(u.ctx, "action_result", f.ID, action.Record{Identity: u.identity, ID: f.ID, Outcome: action.NotSent}) != nil {
					return
				}
				continue
			}
			u.workers.Add(1)
			go func(frame module.Frame) {
				defer u.workers.Done()
				defer func() { <-u.actions }()
				_ = u.engine.actions.Handle(u.ctx, u.process.Session, frame)
			}(f)
		case "log":
			var code struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(f.Body, &code) != nil {
				return
			}
			switch code.Code {
			case "ready", "starting", "state_stale", "action_unknown", "work_completed", "error":
				u.engine.Log(u.identity, code.Code)
			default:
				if u.reply(f, nil, module.ErrProtocol) != nil {
					return
				}
			}
		default:
			return
		}
	}
}
func (u *unit) trigger(f module.Frame) {
	var trigger struct {
		Definition string              `json:"definition"`
		Occurrence timeline.Occurrence `json:"occurrence"`
	}
	if json.Unmarshal(f.Body, &trigger) != nil {
		_ = u.reply(f, nil, module.ErrProtocol)
		return
	}
	var record execution.Record
	var err error
	trigger.Occurrence.ID, err = definitionsPrefix(u.identity.Module, trigger.Occurrence.ID)
	if err == nil {
		trigger.Definition, err = definitionsPrefix(u.identity.Module, trigger.Definition)
	}
	if err == nil {
		record, err = u.engine.router.AdmitFrom(u.identity, trigger.Definition, trigger.Occurrence, time.Now().UTC())
	}
	_ = u.reply(f, record, err)
}

func (u *unit) storage(f module.Frame) error {
	if !u.engine.available() {
		return u.reply(f, nil, ErrRecovery)
	}
	var request struct {
		Scope string `json:"scope"`
		Key   string `json:"key"`
		Value []byte `json:"value,omitempty"`
	}
	if json.Unmarshal(f.Body, &request) != nil {
		return module.ErrProtocol
	}
	store := u.stores[request.Scope]
	if store == nil {
		return u.reply(f, nil, module.ErrProtocol)
	}
	u.mu.Lock()
	frozen, phase := u.frozen, u.phase
	u.mu.Unlock()
	if phase != module.Preparing && phase != module.Active && phase != module.Draining {
		return u.reply(f, nil, module.ErrFenced)
	}
	var value []byte
	var err error
	if f.Type == "storage_write" {
		if frozen || len(request.Value) > 512<<10 {
			err = module.ErrFenced
		} else {
			err = store.Write(request.Key, request.Value)
		}
	} else {
		value, err = store.Read(request.Key)
	}
	if (request.Scope == "persistent" || request.Scope == "generation") && (errors.Is(err, durable.ErrCorrupt) || errors.Is(err, durable.ErrUncertain)) {
		u.engine.fail()
	}
	return u.reply(f, value, err)
}
func (u *unit) streamState() {
	defer u.workers.Done()
	defer func() { u.mu.Lock(); u.streaming = false; u.mu.Unlock() }()
	source := u.engine.config.Source
	if source == nil {
		_ = u.send(u.ctx, "state_unavailable", "", struct {
			Reason string `json:"reason"`
		}{"unavailable"})
		return
	}
	delay := time.Second
	for u.ctx.Err() == nil {
		sub, err := source.Subscribe(u.ctx)
		if err == nil {
			for {
				event, nextErr := sub.Next(u.ctx)
				if nextErr != nil {
					err = nextErr
					break
				}
				if err = u.process.Session.SendEvent(u.ctx, event); err != nil {
					sub.Close()
					return
				}
			}
			sub.Close()
		}
		if u.ctx.Err() != nil {
			return
		}
		// HA-side overflow/large view is capability loss, not module/Runtime health.
		if u.send(u.ctx, "state_unavailable", "", struct {
			Reason string `json:"reason"`
		}{"resync_required"}) != nil {
			return
		}
		timer := time.NewTimer(delay)
		select {
		case <-u.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
}
func (u *unit) rpc(ctx context.Context, kind string, value any) (module.Frame, error) {
	select {
	case u.rpcGate <- struct{}{}:
		defer func() { <-u.rpcGate }()
	case <-ctx.Done():
		return module.Frame{}, ctx.Err()
	}
	waiter := make(chan module.Frame, 1)
	id := fmt.Sprintf("runtime-%d-%d", u.identity.Generation, time.Now().UnixNano())
	u.mu.Lock()
	u.waiter = waiter
	u.waiterID = id
	u.mu.Unlock()
	defer func() { u.mu.Lock(); u.waiter = nil; u.waiterID = ""; u.mu.Unlock() }()
	if err := u.send(ctx, kind, id, value); err != nil {
		return module.Frame{}, err
	}
	select {
	case f := <-waiter:
		return f, nil
	case <-u.readDone:
		return module.Frame{}, ErrOperation
	case <-ctx.Done():
		return module.Frame{}, ctx.Err()
	}
}
func (u *unit) Export(ctx context.Context, final bool) (module.StatePacket, error) {
	f, err := u.rpc(ctx, "state_export", struct {
		Final bool `json:"final"`
	}{final})
	var p module.StatePacket
	if err == nil && (f.Type != "state_exported" || json.Unmarshal(f.Body, &p) != nil) {
		err = module.ErrHandover
	}
	return p, err
}
func (u *unit) Adopt(ctx context.Context, p module.StatePacket, final bool) (module.Adoption, error) {
	f, err := u.rpc(ctx, "state_import", struct {
		Packet module.StatePacket `json:"packet"`
		Final  bool               `json:"final"`
	}{p, final})
	var r struct {
		Adoption module.Adoption `json:"adoption"`
	}
	if err == nil && (f.Type != "state_imported" || json.Unmarshal(f.Body, &r) != nil) {
		err = module.ErrHandover
	}
	return r.Adoption, err
}
func (u *unit) Freeze(ctx context.Context) (func(), error) {
	u.mu.Lock()
	u.frozen = true
	u.mu.Unlock()
	unfreeze := func() {
		u.mu.Lock()
		u.frozen = false
		u.mu.Unlock()
		resume, cancel := context.WithTimeout(u.ctx, module.HandoverBudget)
		defer cancel()
		_ = u.send(resume, "state_resume", "", struct{}{})
	}
	f, err := u.rpc(ctx, "state_freeze", struct{}{})
	if err != nil || f.Type != "state_frozen" {
		unfreeze()
		if err == nil {
			err = module.ErrHandover
		}
		return nil, err
	}
	return unfreeze, nil
}
func (u *unit) supports(cap string) bool {
	for _, c := range u.negotiated.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}
func childStopped(err error) bool {
	var exit interface{ ExitCode() int }
	return err == nil || errors.As(err, &exit)
}

var _ action.Transport = offlineActions{}

func (u *unit) schedulesSnapshot() []timeline.Schedule {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := []timeline.Schedule{}
	for _, s := range u.schedules {
		out = append(out, s)
	}
	return out
}
func (u *unit) cancelExecutions(now time.Time) error {
	records := u.engine.exec.Snapshot().Records
	for id, record := range records {
		if record.Module != u.identity.Module || record.Generation != u.identity.Generation || record.Phase != execution.Canceling {
			continue
		}
		if sent, ok := u.canceled[id]; ok {
			if now.Sub(sent) > module.JoinTimeout {
				u.cancel()
				return ErrOperation
			}
			continue
		}
		u.canceled[id] = now
		if err := u.send(u.ctx, "cancel", id, record); err != nil {
			return err
		}
	}
	for id := range u.canceled {
		if r := records[id]; r.Phase != execution.Canceling {
			delete(u.canceled, id)
		}
	}
	return nil
}
