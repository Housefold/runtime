package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

type fake struct {
	exchange func(context.Context, Command) ([]byte, error)
	calls    int
}

func (f *fake) Exchange(ctx context.Context, raw []byte, limit int) ([]byte, error) {
	f.calls++
	var command Command
	if json.Unmarshal(raw, &command) != nil {
		return nil, ErrProtocol
	}
	return f.exchange(ctx, command)
}
func helloFixture(t *testing.T) Response {
	t.Helper()
	raw, err := os.ReadFile("testdata/hello_response_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var r Response
	if json.Unmarshal(raw, &r) != nil {
		t.Fatal("fixture")
	}
	return r
}
func helloTransport(t *testing.T, mutate func(*Response)) *fake {
	return &fake{exchange: func(ctx context.Context, command Command) ([]byte, error) {
		r := helloFixture(t)
		r.ID = command.ID
		if mutate != nil {
			mutate(&r)
		}
		return json.Marshal(r)
	}}
}
func TestSharedHelloAndOptionalStatus(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		status Status
		mutate func(*Response)
	}{{Available, nil}, {Absent, func(r *Response) { r.Success = false; r.Error = &CoreError{Code: "unknown_command"} }}, {Unavailable, func(r *Response) { r.Success = false; r.Error = &CoreError{Code: "unauthorized"} }}, {Incompatible, func(r *Response) {
		var h Hello
		json.Unmarshal(r.Result, &h)
		h.Protocol.Major = 2
		r.Result, _ = json.Marshal(h)
	}}, {Unavailable, func(r *Response) { r.ID++ }}, {Unavailable, func(r *Response) { r.Result = []byte(`{}`) }}} {
		f := helloTransport(t, tc.mutate)
		c := New(f)
		result, _ := c.Probe(context.Background(), now)
		if result.Status != tc.status {
			t.Fatal(result, tc.status)
		}
		if _, err := c.Probe(context.Background(), now); !errors.Is(err, ErrBusy) || f.calls != 1 {
			t.Fatal("unbounded retry", err)
		}
		if tc.status == Available {
			delete(result.Capabilities, "discovery")
			if len(c.Snapshot().Capabilities) != 1 {
				t.Fatal("ownership")
			}
		}
	}
}
func TestRequiredSemanticsAndLimits(t *testing.T) {
	for _, required := range []bool{true, false} {
		f := helloTransport(t, func(r *Response) {
			var h Hello
			json.Unmarshal(r.Result, &h)
			if required {
				h.Required = []string{"new_semantics"}
			} else {
				h.Capabilities[0].Required = []string{"unknown"}
			}
			r.Result, _ = json.Marshal(h)
		})
		result, _ := New(f).Probe(context.Background(), time.Now())
		if required && result.Status != Incompatible || !required && len(result.Capabilities) != 0 {
			t.Fatal(result)
		}
	}
}
func TestTimeoutMalformedAndSeparateSession(t *testing.T) {
	entered := make(chan struct{})
	f := &fake{exchange: func(ctx context.Context, _ Command) ([]byte, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	c := New(f)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)
	go func() { r, _ := c.Probe(ctx, time.Now()); done <- r }()
	<-entered
	cancel()
	if (<-done).Status != Unavailable {
		t.Fatal("cancel")
	}
	for _, raw := range [][]byte{[]byte(`{`), make([]byte, MaxFrame+1)} {
		f := &fake{exchange: func(context.Context, Command) ([]byte, error) { return raw, nil }}
		if r, _ := New(f).Probe(context.Background(), time.Now()); r.Status != Unavailable {
			t.Fatal(r)
		}
	}
}
