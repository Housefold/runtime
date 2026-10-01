//go:build linux

package module

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const JoinTimeout = 2 * time.Second
const MaxChildren = 16

var childSlots = make(chan struct{}, MaxChildren)

// LocalLaunch is an explicit local artifact boundary. Production catalog and
// operator approval wiring are deliberately absent. No parent env is copied.
type LocalLaunch struct {
	Path        string
	Args        []string
	Identity    Identity
	RSSLimitKiB uint64
}
type Process struct {
	Session *Session
	cmd     *exec.Cmd
	done    chan struct{}
	mu      sync.Mutex
	err     error
	limit   uint64
}

func LaunchLocal(ctx context.Context, spec LocalLaunch) (*Process, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !filepath.IsAbs(spec.Path) || spec.Identity.Module == "" || spec.Identity.Version == "" || spec.Identity.Boot == "" || spec.Identity.Generation == 0 || len(spec.Args) > 32 {
		return nil, ErrProtocol
	}
	select {
	case childSlots <- struct{}{}:
	default:
		return nil, errors.New("child capacity exhausted")
	}
	success := false
	defer func() {
		if !success {
			<-childSlots
		}
	}()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(fds[0]), "runtime-ipc")
	child := os.NewFile(uintptr(fds[1]), "module-ipc")
	defer parent.Close()
	defer child.Close()
	conn, err := net.FileConn(parent)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Env = []string{"HOUSEFOLD_IPC_FD=3"}
	cmd.ExtraFiles = []*os.File{child}
	cmd.Stdin = nil
	output, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		conn.Close()
		return nil, err
	}
	defer output.Close()
	// File-backed discard avoids os/exec copier pipes that descendants could
	// keep open after the direct child exits, preventing Wait from joining.
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err = markDescriptors(); err != nil {
		conn.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		conn.Close()
		return nil, err
	}
	p := &Process{Session: NewSession(conn, spec.Identity), cmd: cmd, done: make(chan struct{}), limit: spec.RSSLimitKiB}
	stop := context.AfterFunc(ctx, func() { p.kill() })
	success = true
	go func() {
		err := cmd.Wait()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		p.Session.Close()
		stop()
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		close(p.done)
		<-childSlots
	}()
	return p, nil
}
func (p *Process) kill() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	p.Session.Close()
}
func (p *Process) Done() <-chan struct{} { return p.done }
func (p *Process) Wait(ctx context.Context) error {
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *Process) Stop(ctx context.Context) error {
	p.kill()
	bounded, cancel := context.WithTimeout(ctx, JoinTimeout)
	defer cancel()
	return p.Wait(bounded)
}

type Usage struct {
	RSSKiB  uint64
	Threads uint64
}

// Observe uses a bounded Linux proc read. An observed RSS violation kills the
// process; polling is a caller responsibility and is not a hard kernel limit.
func (p *Process) Observe() (Usage, error) {
	var usage Usage
	select {
	case <-p.done:
		return usage, os.ErrProcessDone
	default:
	}
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", p.cmd.Process.Pid))
	if err != nil {
		return usage, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(raw) > 65536 {
		return usage, ErrProtocol
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 {
			switch fields[0] {
			case "VmRSS:":
				usage.RSSKiB, _ = strconv.ParseUint(fields[1], 10, 64)
			case "Threads:":
				usage.Threads, _ = strconv.ParseUint(fields[1], 10, 64)
			}
		}
	}
	if p.limit > 0 && usage.RSSKiB > p.limit {
		p.kill()
		return usage, errors.New("observed module RSS limit exceeded")
	}
	return usage, nil
}

// Go-created descriptors are CLOEXEC. Also close the inheritance gap for
// preexisting descriptors opened by external libraries without that flag.
// Parent handles remain usable; explicitly passed ExtraFiles are duplicated by
// os/exec into the child's allowed descriptors. Runtime owns descriptor creation.
func markDescriptors() error {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return err
	}
	if len(entries) > 1024 {
		return errors.New("parent descriptor bound exceeded")
	}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil || fd < 3 {
			continue
		}
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
		if errno == syscall.EBADF {
			continue
		}
		if errno != 0 {
			return errno
		}
		_, _, errno = syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFD, flags|syscall.FD_CLOEXEC)
		if errno != 0 && errno != syscall.EBADF {
			return errno
		}
	}
	return nil
}
