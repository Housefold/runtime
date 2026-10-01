// reference-module is a synthetic acceptance module, never preinstalled. It
// performs no HA actions and persists only a synthetic execution counter.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/housefold/runtime/internal/execution"
	"github.com/housefold/runtime/internal/module"
	"github.com/housefold/runtime/internal/timeline"
)

var buildMode = "normal"

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}
func run() error {
	if os.Getenv("SUPERVISOR_TOKEN") != "" {
		return errors.New("unexpected credential")
	}
	fd, err := strconv.Atoi(os.Getenv("HOUSEFOLD_IPC_FD"))
	if err != nil || fd != 3 {
		return module.ErrProtocol
	}
	file := os.NewFile(uintptr(fd), "ipc")
	conn, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx := context.Background()
	s := module.NewSession(conn, module.Identity{})
	send := func(kind, id string, value any) error {
		f, err := module.FrameOf(kind, value)
		if err != nil {
			return err
		}
		f.ID = id
		return s.Send(ctx, f)
	}
	if err = send("hello", "", module.Hello{Major: 1, Capabilities: []string{"health", "state", "storage", "execution", "handover", "actions"}}); err != nil {
		return err
	}
	hello, err := s.Receive(ctx)
	var identity struct {
		Identity module.Identity `json:"identity"`
	}
	if err != nil || hello.Type != "hello_ok" || json.Unmarshal(hello.Body, &identity) != nil {
		return module.ErrProtocol
	}
	if err = send("storage_read", "initial", struct {
		Scope string `json:"scope"`
		Key   string `json:"key"`
	}{"persistent", "counter"}); err != nil {
		return err
	}
	result, err := s.Receive(ctx)
	if err != nil {
		return err
	}
	var stored struct {
		Data []byte `json:"data"`
	}
	_ = json.Unmarshal(result.Body, &stored)
	var counter uint64
	_ = json.Unmarshal(stored.Data, &counter)
	mode := execution.Queued
	if buildMode == "hold_execution" {
		mode = execution.Restart
	}
	if err = send("definition", "definition", execution.Definition{ID: "tick", Mode: mode, Queue: 4, Concurrency: 1, TTL: time.Hour}); err != nil {
		return err
	}
	if _, err = s.Receive(ctx); err != nil {
		return err
	}
	if buildMode == "schedule" {
		if err = send("schedule", "schedule", timeline.Schedule{ID: "tick", Start: time.Now().UTC().Add(time.Minute)}); err != nil {
			return err
		}
		if _, err = s.Receive(ctx); err != nil {
			return err
		}
	}
	ready := struct {
		Accepting bool `json:"accepting"`
		Service   bool `json:"service_healthy"`
		UI        bool `json:"ui_healthy"`
	}{buildMode != "fail_ready", true, false}
	if err = send("ready", "", ready); err != nil {
		return err
	}
	if err = send("subscribe_state", "state", struct{}{}); err != nil {
		return err
	}
	persist := func() error {
		raw, _ := json.Marshal(counter)
		if err := send("storage_write", "counter", struct {
			Scope string `json:"scope"`
			Key   string `json:"key"`
			Value []byte `json:"value"`
		}{"persistent", "counter", raw}); err != nil {
			return err
		}
		// The normal reader below handles results. Handovers must reply only after
		// their persistent write has acknowledged; defer its RPC reply until result.
		return nil
	}
	pendingReplyKind, pendingReplyID := "", ""
	for {
		f, err := s.Receive(ctx)
		if err != nil {
			return err
		}
		switch f.Type {
		case "activated":
			if buildMode == "exit_after_activation" {
				return errors.New("synthetic crash")
			}
		case "health_request":
			if err = send("health", "", struct {
				Service bool `json:"service_healthy"`
				UI      bool `json:"ui_healthy"`
			}{true, false}); err != nil {
				return err
			}
		case "execute":
			var r execution.Record
			if json.Unmarshal(f.Body, &r) != nil {
				return module.ErrProtocol
			}
			if buildMode == "hold_execution" {
				continue
			}
			counter++
			if err = persist(); err != nil {
				return err
			}
			if err = send("execution_finished", r.ID, struct {
				ID string `json:"id"`
			}{r.ID}); err != nil {
				return err
			}
		case "state_export":
			raw, _ := json.Marshal(counter)
			if err = send("state_exported", f.ID, module.StatePacket{SourceVersion: identity.Identity.Version, Schema: "reference-v1", Data: raw}); err != nil {
				return err
			}
		case "state_import":
			var request struct {
				Packet module.StatePacket `json:"packet"`
			}
			if json.Unmarshal(f.Body, &request) != nil || request.Packet.Schema != "reference-v1" || json.Unmarshal(request.Packet.Data, &counter) != nil {
				return module.ErrHandover
			}
			if err = persist(); err != nil {
				return err
			}
			pendingReplyKind = "state_imported"
			pendingReplyID = f.ID
		case "result":
			if f.ID == "counter" && pendingReplyID != "" {
				if err = send(pendingReplyKind, pendingReplyID, struct {
					Adoption module.Adoption `json:"adoption"`
				}{module.Compatible}); err != nil {
					return err
				}
				pendingReplyKind, pendingReplyID = "", ""
			}
		case "state_freeze":
			if err = send("state_frozen", f.ID, struct{}{}); err != nil {
				return err
			}
		case "cancel":
			if err = send("execution_finished", f.ID, struct {
				ID       string `json:"id"`
				Canceled bool   `json:"canceled"`
			}{f.ID, true}); err != nil {
				return err
			}
		case "state_resume", "reset_begin", "reset_entity", "reset_end", "state_event", "state_unavailable":
		default:
			return module.ErrProtocol
		}
	}
}
