//go:build linux

package module

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSyntheticChild(t *testing.T) {
	if os.Getenv("HOUSEFOLD_IPC_FD") != "3" {
		return
	}
	f := os.NewFile(3, "ipc")
	conn, err := net.FileConn(f)
	f.Close()
	if err != nil {
		os.Exit(20)
	}
	s := NewSession(conn, Identity{})
	for _, entry := range os.Environ() {
		if entry != "HOUSEFOLD_IPC_FD=3" {
			os.Exit(21)
		}
	} // Only the IPC descriptor is inherited beyond standard IO.
	entries, _ := os.ReadDir("/proc/self/fd")
	for _, entry := range entries {
		if entry.Name() == "0" || entry.Name() == "1" || entry.Name() == "2" || entry.Name() == "3" {
			continue
		}
		target, err := os.Readlink("/proc/self/fd/" + entry.Name())
		if err == nil && strings.Contains(target, "descriptor-sentinel") {
			os.Exit(22)
		}
	}
	fmsg, _ := FrameOf("hello", Hello{Major: 1})
	if s.Send(context.Background(), fmsg) != nil {
		os.Exit(23)
	}
	if _, err = s.Receive(context.Background()); err != nil {
		os.Exit(24)
	}
	ready, _ := FrameOf("ready", struct{}{})
	if s.Send(context.Background(), ready) != nil {
		os.Exit(25)
	}
	frame, err := s.Receive(context.Background())
	if err != nil {
		os.Exit(26)
	}
	if frame.Type == "spawn_and_exit" {
		path, _ := os.Executable()
		cmd := exec.Command(path, "-test.run=^TestSyntheticDescendant$")
		cmd.Env = []string{"HOUSEFOLD_DESCENDANT=1"}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if cmd.Start() != nil {
			os.Exit(27)
		}
	}
	os.Exit(0)
}
func launchFixture(t *testing.T, ctx context.Context, limit uint64) *Process {
	t.Helper()
	path, _ := os.Executable()
	p, err := LaunchLocal(ctx, LocalLaunch{Path: path, Args: []string{"-test.run=^TestSyntheticChild$"}, Identity: Identity{Module: "synthetic", Version: "1", Boot: "boot", Generation: 1}, RSSLimitKiB: limit})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Stop(context.Background()) })
	if _, err = p.Session.Negotiate(ctx, Hello{Major: 1}); err != nil {
		t.Fatal(err)
	}
	if frame, err := p.Session.Receive(ctx); err != nil || frame.Type != "ready" {
		t.Fatal(frame, err)
	}
	return p
}
func TestChildIsolationCancelJoin(t *testing.T) {
	t.Setenv("SUPERVISOR_TOKEN", "synthetic-secret")
	sentinel, err := os.CreateTemp(t.TempDir(), "descriptor-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	defer sentinel.Close()
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, sentinel.Fd(), syscall.F_SETFD, 0)
	if errno != 0 {
		t.Fatal(errno)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := launchFixture(t, ctx, 0)
	usage, err := p.Observe()
	if err != nil || usage.Threads == 0 {
		t.Fatal(usage, err)
	}
	cancel()
	wait, c := context.WithTimeout(context.Background(), JoinTimeout)
	defer c()
	if err = p.Wait(wait); err == context.DeadlineExceeded {
		t.Fatal("child not joined")
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("not done")
	}
}
func TestChildPressureAndExit(t *testing.T) {
	p := launchFixture(t, context.Background(), 1)
	if _, err := p.Observe(); err == nil {
		t.Fatal("RSS budget ignored")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	defer cancel()
	if p.Wait(ctx) == context.DeadlineExceeded {
		t.Fatal("not killed")
	}
	p = launchFixture(t, context.Background(), 0)
	f, _ := FrameOf("exit", struct{}{})
	if err := p.Session.Send(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := p.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSyntheticDescendant(t *testing.T) {
	if os.Getenv("HOUSEFOLD_DESCENDANT") != "1" {
		return
	}
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	<-timer.C
}
func TestDescendantOutputCannotHoldChildJoin(t *testing.T) {
	p := launchFixture(t, context.Background(), 0)
	ctx, cancel := context.WithTimeout(context.Background(), JoinTimeout)
	defer cancel()
	frame, _ := FrameOf("spawn_and_exit", struct{}{})
	if err := p.Session.Send(ctx, frame); err != nil {
		t.Fatal(err)
	}
	if err := p.Wait(ctx); err != nil {
		t.Fatal("descendant prevented child join", err)
	}
}
