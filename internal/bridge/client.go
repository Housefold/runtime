// Package bridge negotiates optional capabilities on a separate authenticated
// Core WebSocket-style transport. It never owns native state or process health.
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/housefold/runtime/internal/discovery"
	"sync"
	"time"
)

const MaxFrame = 64 << 10
const MaxChunks = 16
const MaxTotal = 1 << 20
const Timeout = 2 * time.Second

var ErrProtocol = errors.New("invalid Bridge response")
var ErrBusy = errors.New("Bridge operation busy or in backoff")

type Status string

const (
	Absent       Status = "absent"
	Incompatible Status = "incompatible"
	Available    Status = "available"
	Unavailable  Status = "temporarily_unavailable"
)

type Version struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
}
type Limits struct {
	Frame  int `json:"frame"`
	Chunks int `json:"chunks"`
	Total  int `json:"total"`
}
type Capability struct {
	Name     string   `json:"name"`
	Version  Version  `json:"version"`
	Required []string `json:"required"`
	Limits   Limits   `json:"limits"`
}
type Hello struct {
	Protocol      Version      `json:"protocol"`
	BridgeVersion string       `json:"bridge_version"`
	CoreVersion   string       `json:"core_version"`
	Required      []string     `json:"required"`
	Capabilities  []Capability `json:"capabilities"`
}
type Command struct {
	ID   uint64 `json:"id"`
	Type string `json:"type"`
	Body any    `json:"body"`
}
type Response struct {
	ID      uint64          `json:"id"`
	Type    string          `json:"type"`
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
	Error   *CoreError      `json:"error,omitempty"`
}
type CoreError struct {
	Code string `json:"code"`
}

// Exchange must enforce maxResponse before allocating received frames and honor
// context. It uses a private Core session, never the native state WebSocket.
type Transport interface {
	Exchange(context.Context, []byte, int) ([]byte, error)
}
type Result struct {
	Status        Status
	BridgeVersion string
	CoreVersion   string
	Capabilities  map[string]Capability
	NextProbe     time.Time
}
type Client struct {
	lastDiscovery  *discovery.Snapshot
	discoveryFresh bool
	transport      Transport
	gate           chan struct{}
	mu             sync.Mutex
	result         Result
	nextID         uint64
	failures       int
}

func New(transport Transport) *Client {
	return &Client{transport: transport, gate: make(chan struct{}, 1), result: Result{Status: Absent, Capabilities: map[string]Capability{}}}
}
func cloneResult(r Result) Result {
	raw, _ := json.Marshal(r)
	var copy Result
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func (c *Client) Snapshot() Result { c.mu.Lock(); defer c.mu.Unlock(); return cloneResult(c.result) }
func boundedJSON(raw []byte, limit int) bool {
	if len(raw) == 0 || len(raw) > limit || !json.Valid(raw) {
		return false
	}
	depth := 0
	quoted, escape := false, false
	for _, b := range raw {
		if quoted {
			if escape {
				escape = false
			} else if b == '\\' {
				escape = true
			} else if b == '"' {
				quoted = false
			}
			continue
		}
		switch b {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			if depth > 32 {
				return false
			}
		case '}', ']':
			depth--
		}
	}
	return true
}
func (c *Client) exchange(ctx context.Context, kind string, body any, limit int) (Response, error) {
	if ctx.Err() != nil {
		return Response{}, ctx.Err()
	}
	if c.transport == nil {
		return Response{}, ErrProtocol
	}
	c.nextID++
	if c.nextID == 0 {
		return Response{}, ErrProtocol
	}
	raw, err := json.Marshal(Command{ID: c.nextID, Type: kind, Body: body})
	if err != nil || len(raw) > MaxFrame {
		return Response{}, ErrProtocol
	}
	raw, err = c.transport.Exchange(ctx, raw, limit)
	if err != nil {
		return Response{}, err
	}
	if ctx.Err() != nil {
		return Response{}, ctx.Err()
	}
	var response Response
	if !boundedJSON(raw, limit) || json.Unmarshal(raw, &response) != nil || response.ID != c.nextID || response.Type != "result" {
		return Response{}, ErrProtocol
	}
	return response, nil
}
func (c *Client) Probe(ctx context.Context, now time.Time) (Result, error) {
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	default:
		return c.Snapshot(), ErrBusy
	}
	current := c.Snapshot()
	if now.Before(current.NextProbe) {
		return current, ErrBusy
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	result := Result{Status: Unavailable, Capabilities: map[string]Capability{}}
	response, err := c.exchange(ctx, "housefold/hello", struct {
		Protocol Version `json:"protocol"`
	}{Version{Major: 1}}, MaxFrame)
	if err == nil && !response.Success {
		err = ErrProtocol
		if response.Error != nil && response.Error.Code == "unknown_command" {
			result.Status = Absent
		}
	} else if err == nil {
		var hello Hello
		if !boundedJSON(response.Result, MaxFrame) || json.Unmarshal(response.Result, &hello) != nil || len(hello.Capabilities) > 32 || len(hello.BridgeVersion) > 64 || len(hello.CoreVersion) > 64 || hello.BridgeVersion == "" || hello.CoreVersion == "" {
			err = ErrProtocol
		} else if hello.Protocol.Major != 1 || hello.Protocol.Minor < 0 || len(hello.Required) > 0 {
			result.Status = Incompatible
			err = ErrProtocol
		} else {
			result.Status = Available
			result.BridgeVersion = hello.BridgeVersion
			result.CoreVersion = hello.CoreVersion
			seen := map[string]bool{}
			for _, cap := range hello.Capabilities {
				if cap.Name == "" || len(cap.Name) > 64 || seen[cap.Name] || len(cap.Required) > 16 || cap.Version.Minor < 0 {
					result.Status = Unavailable
					err = ErrProtocol
					break
				}
				seen[cap.Name] = true
				if cap.Name == "ordered_state" {
					if orderedCompatible(cap) {
						cap.Limits.Frame = min(cap.Limits.Frame, MaxFrame)
						cap.Limits.Chunks = min(cap.Limits.Chunks, MaxChunks)
						cap.Limits.Total = min(cap.Limits.Total, MaxTotal)
						result.Capabilities[cap.Name] = cap
					}
					continue
				}
				if cap.Name != "discovery" || cap.Version.Major != 1 {
					continue
				}
				compatible := true
				for _, required := range cap.Required {
					if required != "bounded_chunks" {
						compatible = false
					}
				}
				if !compatible || cap.Limits.Frame <= 0 || cap.Limits.Chunks <= 0 || cap.Limits.Total <= 0 {
					continue
				}
				cap.Limits.Frame = min(cap.Limits.Frame, MaxFrame)
				cap.Limits.Chunks = min(cap.Limits.Chunks, MaxChunks)
				cap.Limits.Total = min(cap.Limits.Total, MaxTotal)
				result.Capabilities[cap.Name] = cap
			}
		}
	}
	if err != nil {
		result.Capabilities = map[string]Capability{}
		c.failures = min(c.failures+1, 3)
		delay := time.Second * time.Duration(1<<uint(c.failures-1))
		if result.Status == Absent || result.Status == Incompatible || c.failures == 3 {
			delay = 30 * time.Second
		}
		result.NextProbe = now.Add(delay)
	} else {
		c.failures = 0
		result.NextProbe = now.Add(30 * time.Second)
	}
	c.mu.Lock()
	c.result = cloneResult(result)
	if _, ok := result.Capabilities["discovery"]; result.Status != Available || !ok {
		c.discoveryFresh = false
	}
	c.mu.Unlock()
	return result, err
}

// Invalidate forgets capability liveness on HA transport/generation loss. Only
// negotiation's normal bounded worker may restore readiness; state is untouched.
func (c *Client) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.result.Status = Unavailable
	c.result.Capabilities = map[string]Capability{}
	c.result.NextProbe = time.Time{}
	c.discoveryFresh = false
}
