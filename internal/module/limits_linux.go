//go:build linux

package module

import (
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"
)

// Memory/CPU/thread limits are sampled over the whole owned process group;
// descriptors and a UID-wide fork ceiling are additionally kernel enforced.
type ProcessLimits struct {
	MemoryKiB  uint64
	CPUPercent uint64
	Threads    uint64
	FDs        uint64
	NProc      uint64
	Priority   string
}

func DefaultLimits() ProcessLimits {
	return ProcessLimits{MemoryKiB: 128 * 1024, CPUPercent: 50, Threads: 64, FDs: 64, NProc: 256, Priority: "normal"}
}
func ValidLimits(l ProcessLimits) bool {
	return l.MemoryKiB >= 4096 && l.MemoryKiB <= 512*1024 && l.CPUPercent >= 1 && l.CPUPercent <= 100 && l.Threads >= 4 && l.Threads <= 128 && l.FDs >= 16 && l.FDs <= 256 && l.NProc >= 32 && l.NProc <= 256 && (l.Priority == "essential" || l.Priority == "normal" || l.Priority == "background")
}

type launchSettings struct {
	Limits ProcessLimits
	Args   []string
	Name   string
}

// LaunchEntry is entered only by the packaged launcher with parent-opened fd4.
// It consumes no inherited credentials, installs guards, then replaces itself.
func LaunchEntry() error {
	// prctl/seccomp/nice are thread-local. Keep the installing thread through exec;
	// a Go scheduler migration would otherwise execute an unguarded artifact.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	raw := os.Getenv("HOUSEFOLD_LAUNCH_SETTINGS")
	if len(raw) > 8192 {
		return ErrProtocol
	}
	var settings launchSettings
	if json.Unmarshal([]byte(raw), &settings) != nil || !ValidLimits(settings.Limits) || len(settings.Args) > 32 || settings.Name == "" {
		return ErrProtocol
	}
	artifact := os.NewFile(4, "verified-elf")
	defer artifact.Close() // Keep fd4 owned/live until native exec.
	if info, err := artifact.Stat(); err != nil || !info.Mode().IsRegular() {
		return ErrProtocol
	}
	if err := os.WriteFile("/proc/self/oom_score_adj", []byte("1000"), 0); err != nil {
		return err
	}
	nice := 10
	if settings.Limits.Priority == "essential" {
		nice = 5
	}
	if settings.Limits.Priority == "background" {
		nice = 15
	}
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, 0, nice); err != nil {
		return err
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: settings.Limits.FDs, Max: settings.Limits.FDs}); err != nil {
		return err
	}
	if err := syscall.Setrlimit(6 /* RLIMIT_NPROC */, &syscall.Rlimit{Cur: settings.Limits.NProc, Max: settings.Limits.NProc}); err != nil {
		return err
	}
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, 38 /* NO_NEW_PRIVS */, 1, 0, 0, 0, 0); errno != 0 {
		return errno
	}
	if err := processGuards(); err != nil {
		return err
	}
	// No launch metadata/credential or artifact descriptor survives native exec.
	syscall.CloseOnExec(4)
	heapMiB := max(uint64(2), settings.Limits.MemoryKiB/2048)
	env := []string{"HOUSEFOLD_IPC_FD=3", "GOMAXPROCS=1", "GOMEMLIMIT=" + strconv.FormatUint(heapMiB, 10) + "MiB"}
	return syscall.Exec("/proc/self/fd/4", append([]string{settings.Name}, settings.Args...), env)
}
func processGuards() error {
	arch := uint32(0xc000003e)
	setns := uint32(308)
	if runtime.GOARCH == "arm64" {
		arch = 0xc00000b7
		setns = 268
	} else if runtime.GOARCH != "amd64" {
		return errors.New("unsupported process guard architecture")
	}
	const load = 0x20
	const equal = 0x15
	const bits = 0x45
	const ret = 0x06
	filters := []syscall.SockFilter{{Code: load, K: 4}, {Code: equal, K: arch, Jt: 1}, {Code: ret, K: 0x80000000}, {Code: load, K: 0},
		// Reject x32/alternate syscall numbering even on an amd64 audit architecture.
		{Code: bits, K: 0x40000000, Jt: 0, Jf: 1}, {Code: ret, K: 0x00050001},
	}
	// Children cannot escape the owned group/namespace or serve a LAN endpoint.
	for _, nr := range []uint32{syscall.SYS_SETSID, syscall.SYS_SETPGID, syscall.SYS_UNSHARE, setns, syscall.SYS_BIND, syscall.SYS_LISTEN} {
		filters = append(filters, syscall.SockFilter{Code: equal, K: nr, Jf: 1}, syscall.SockFilter{Code: ret, K: 0x00050001})
	}
	filters = append(filters, syscall.SockFilter{Code: ret, K: 0x7fff0000})
	program := syscall.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	_, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, 22 /* SET_SECCOMP */, 2 /* FILTER */, uintptr(unsafe.Pointer(&program)), 0, 0, 0)
	runtime.KeepAlive(filters)
	if errno != 0 {
		return errno
	}
	return nil
}
