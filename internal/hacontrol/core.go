package hacontrol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

var ErrCore = errors.New("local Home Assistant command unavailable")

const CoreTimeout = 3 * time.Second

// Core owns a separate authenticated command socket, never the state socket.
// One bounded exchange reads replies; no module receives this handle or token.
type Core struct {
	mu         sync.Mutex
	gate       chan struct{}
	token, url string
	http       *http.Client
	conn       *websocket.Conn
	last       time.Time
	next       uint64
}

func NewCore(token string) *Core {
	return newCore(token, "ws://supervisor/core/websocket", localHTTP())
}
func newCore(token, url string, client *http.Client) *Core {
	return &Core{token: token, url: url, http: client, gate: make(chan struct{}, 1)}
}
func (c *Core) Close() {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn != nil {
		_ = conn.CloseNow()
	}
	if transport, ok := c.http.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}
func (c *Core) connection(ctx context.Context) (*websocket.Conn, error) {
	c.mu.Lock()
	conn, last := c.conn, c.last
	c.mu.Unlock()
	if conn != nil && time.Since(last) < 15*time.Second {
		return conn, nil
	}
	c.Close()
	if c.token == "" {
		return nil, ErrCore
	}
	conn, response, err := websocket.Dial(ctx, c.url, &websocket.DialOptions{HTTPClient: c.http})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, ErrCore
	}
	conn.SetReadLimit(4096)
	valid := false
	defer func() {
		if !valid {
			_ = conn.CloseNow()
		}
	}()
	_, raw, err := conn.Read(ctx)
	var hello struct {
		Type string `json:"type"`
	}
	if err != nil || json.Unmarshal(raw, &hello) != nil || hello.Type != "auth_required" {
		return nil, ErrCore
	}
	if err = writeJSON(ctx, conn, map[string]any{"type": "auth", "access_token": c.token}); err != nil {
		return nil, ErrCore
	}
	_, raw, err = conn.Read(ctx)
	if err != nil || json.Unmarshal(raw, &hello) != nil || hello.Type != "auth_ok" {
		return nil, ErrCore
	}
	c.mu.Lock()
	c.conn = conn
	c.last = time.Now()
	c.next = 0
	c.mu.Unlock()
	valid = true
	return conn, nil
}

// Exchange translates caller IDs to a socket-owned increasing ID and validates
// the reply before restoring it. A timed-out/invalid exchange closes the socket.
func (c *Core) Exchange(ctx context.Context, request []byte, limit int) (out []byte, err error) {
	if len(request) == 0 || len(request) > 64<<10 || limit < 1 || limit > 2<<20 {
		return nil, ErrCore
	}
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	default:
		return nil, ErrCore
	}
	ctx, cancel := context.WithTimeout(ctx, CoreTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return nil, ErrCore
	}
	var command map[string]json.RawMessage
	if json.Unmarshal(request, &command) != nil {
		return nil, ErrCore
	}
	var original uint64
	var kind string
	if json.Unmarshal(command["id"], &original) != nil || original == 0 || json.Unmarshal(command["type"], &kind) != nil || kind == "auth" || len(kind) > 128 {
		return nil, ErrCore
	}
	conn, err := c.connection(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			c.Close()
		}
	}()
	c.mu.Lock()
	c.next++
	wire := c.next
	c.mu.Unlock()
	if wire == 0 {
		return nil, ErrCore
	}
	command["id"], _ = json.Marshal(wire)
	// Bridge's private command envelope has Body; Core expects flat HA commands.
	if body, ok := command["body"]; ok {
		var fields map[string]json.RawMessage
		if json.Unmarshal(body, &fields) != nil {
			return nil, ErrCore
		}
		for key, value := range fields {
			if key == "id" || key == "type" {
				return nil, ErrCore
			}
			command[key] = value
		}
		delete(command, "body")
	}
	conn.SetReadLimit(int64(limit))
	if err = writeJSON(ctx, conn, command); err != nil {
		return nil, ErrCore
	}
	_, raw, readErr := conn.Read(ctx)
	if readErr != nil || len(raw) > limit {
		return nil, ErrCore
	}
	var reply map[string]json.RawMessage
	var id uint64
	var typ string
	if json.Unmarshal(raw, &reply) != nil || json.Unmarshal(reply["id"], &id) != nil || id != wire || json.Unmarshal(reply["type"], &typ) != nil || typ != "result" {
		return nil, ErrCore
	}
	reply["id"], _ = json.Marshal(original)
	out, err = json.Marshal(reply)
	if err != nil || len(out) > limit {
		return nil, ErrCore
	}
	c.mu.Lock()
	c.last = time.Now()
	c.mu.Unlock()
	return out, nil
}
func (c *Core) command(ctx context.Context, kind string, limit int) (json.RawMessage, string, error) {
	request, _ := json.Marshal(map[string]any{"id": 1, "type": kind})
	raw, err := c.Exchange(ctx, request, limit)
	if err != nil {
		return nil, "", err
	}
	var reply struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &reply) != nil {
		return nil, "", ErrCore
	}
	if !reply.Success {
		code := "unsupported"
		if reply.Error != nil {
			code = reply.Error.Code
		}
		return nil, code, ErrCore
	}
	return reply.Result, "", nil
}

func writeJSON(ctx context.Context, conn *websocket.Conn, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, raw)
}
