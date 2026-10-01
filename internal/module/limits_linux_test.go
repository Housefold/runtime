//go:build linux

package module

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGuardedProbe(t *testing.T) {
	if os.Getenv("HOUSEFOLD_IPC_FD") != "3" || os.Getenv("GOMAXPROCS") != "1" {
		return
	}
	stage := 50
	fail := func() { os.Exit(stage) }
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "HOUSEFOLD_IPC_FD=") && !strings.HasPrefix(v, "GOMAXPROCS=") && !strings.HasPrefix(v, "GOMEMLIMIT=") {
			fail()
		}
	}
	stage = 51
	var nofile, nproc syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &nofile) != nil || syscall.Getrlimit(6, &nproc) != nil || nofile.Max != 32 || nproc.Max != 256 {
		fail()
	}
	stage = 52
	if _, err := syscall.Setsid(); err != syscall.EPERM {
		fail()
	}
	if err := syscall.Setpgid(0, 0); err != syscall.EPERM {
		fail()
	}
	if err := syscall.Unshare(syscall.CLONE_NEWNS); err != syscall.EPERM {
		fail()
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		fail()
	}
	if syscall.Bind(fd, &syscall.SockaddrInet4{}) != syscall.EPERM {
		fail()
	}
	if syscall.Listen(fd, 1) != syscall.EPERM {
		fail()
	}
	syscall.Close(fd)
	stage = 53
	status, _ := os.ReadFile("/proc/self/status")
	if !strings.Contains(string(status), "NoNewPrivs:\t1") || !strings.Contains(string(status), "Seccomp:\t2") {
		fail()
	}
	oom, _ := os.ReadFile("/proc/self/oom_score_adj")
	if strings.TrimSpace(string(oom)) != "1000" {
		fail()
	}
	stage = 54
	nice, err := syscall.Getpriority(syscall.PRIO_PROCESS, 0)
	if err != nil || nice != 5 {
		fail()
	} // Linux Getpriority returns 20 - nice(15).
	stage = 55
	open := []*os.File{}
	for len(open) < 64 {
		f, err := os.Open(os.DevNull)
		if err != nil {
			if !os.IsNotExist(err) && !strings.Contains(err.Error(), "too many open files") {
				fail()
			}
			break
		}
		open = append(open, f)
	}
	if len(open) >= 32 {
		fail()
	}
	for _, f := range open {
		f.Close()
	}
	f := os.NewFile(3, "ipc")
	conn, err := net.FileConn(f)
	f.Close()
	if err != nil {
		fail()
	}
	s := NewSession(conn, Identity{})
	report, _ := FrameOf("guard_report", struct{ Limits bool }{true})
	if s.Send(context.Background(), report) != nil {
		fail()
	}
	frame, _ := s.Receive(context.Background())
	if frame.Type == "spawn" {
		path, _ := os.Executable()
		cmd := exec.Command(path, "-test.run=^TestSyntheticDescendant$")
		cmd.Env = []string{"HOUSEFOLD_DESCENDANT=1", "GOMAXPROCS=1"}
		if cmd.Start() != nil {
			fail()
		}
		report, _ := FrameOf("descendant", struct{ PID int }{cmd.Process.Pid})
		if s.Send(context.Background(), report) != nil {
			fail()
		}
		_, _ = s.Receive(context.Background())
	}
	os.Exit(0)
}
func TestPackagedTrampolineGuardsAndLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launcher")
	cmd := exec.Command("go", "build", "-o", path, "../../cmd/module-launcher")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	t.Setenv("SUPERVISOR_TOKEN", "SYNTHETIC_SECRET")
	binary, _ := os.Executable()
	limits := DefaultLimits()
	limits.FDs = 32
	limits.Priority = "background"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := LaunchLocal(ctx, LocalLaunch{Path: binary, Args: []string{"-test.run=^TestGuardedProbe$"}, Identity: Identity{Module: "synthetic", Version: "1", Boot: "boot", Generation: 1}, Trampoline: path, Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())
	f, err := p.Session.Receive(ctx)
	if err != nil || f.Type != "guard_report" {
		waitErr := p.Wait(ctx)
		t.Fatal(f, err, waitErr)
	}
	var report struct{ Limits bool }
	if json.Unmarshal(f.Body, &report) != nil || !report.Limits {
		t.Fatal(f)
	}
	usage, err := p.SampleGroup()
	if err != nil || usage.RSSKiB == 0 || usage.Processes != 1 || usage.Threads == 0 || usage.FDs > 32 {
		t.Fatal(usage, err)
	}
	frame, _ := FrameOf("spawn", struct{}{})
	if err = p.Session.Send(ctx, frame); err != nil {
		t.Fatal(err)
	}
	f, err = p.Session.Receive(ctx)
	if err != nil || f.Type != "descendant" {
		t.Fatal(f, err)
	}
	var descendant struct{ PID int }
	_ = json.Unmarshal(f.Body, &descendant)
	usage, err = p.SampleGroup()
	if err != nil || usage.Processes != 2 {
		t.Fatal("descendant usage omitted", usage, err)
	}
	_ = p.Stop(ctx)
	if row, err := readProc(descendant.PID); err == nil && row.State != "Z" && row.State != "X" {
		t.Fatal("owned descendant survived join", row)
	}
	if err = p.Wait(ctx); err == context.DeadlineExceeded {
		t.Fatal(err)
	}
}
func TestParseProcHandlesSpacesAndParentheses(t *testing.T) {
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Fatal(err)
	}
	end := strings.LastIndex(string(raw), ")")
	first := strings.IndexByte(string(raw), ' ')
	modified := string(raw[:first]) + " (synthetic (name))" + string(raw[end+1:])
	row, err := parseProc([]byte(modified))
	if err != nil || row.PID != os.Getpid() || row.Threads == 0 || row.Start == 0 {
		t.Fatal(row, err)
	}
}
