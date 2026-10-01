// Package bootstrap provisions only Runtime's private estate before dropping
// root privileges. It never recursively follows/chowns module-owned paths.
package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

var ErrData = errors.New("private Runtime data unavailable")

// Prepare validates both mount and estate against links. Existing corruption
// is preserved: a non-directory, link or unsafe ownership is not replaced.
func Prepare(path string, uid int) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || uid < 1 {
		return ErrData
	}
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrData
	}
	if err = os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return ErrData
	}
	info, err = os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrData
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != 0 && stat.Uid != uint32(uid)) {
		return ErrData
	}
	if os.Geteuid() == 0 {
		if err = os.Chown(path, uid, uid); err != nil {
			return ErrData
		}
	}
	if err = os.Chmod(path, 0700); err != nil {
		return ErrData
	}
	return nil
}

// Drop is required even when data validation fails: recovery must not become a
// privileged management plane. A privilege-drop failure is a process failure.
func Drop(uid int) error {
	// Parent memory, initial environment and descriptors must not be readable
	// through proc/ptrace by a same-UID child, including rootless launches.
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, 4 /* PR_SET_DUMPABLE */, 0, 0, 0, 0, 0); errno != 0 {
		return errno
	}
	if uid < 1 {
		return ErrData
	}
	if os.Geteuid() != 0 {
		if os.Geteuid() != uid || os.Getegid() != uid {
			return ErrData
		}
		return nil
	}
	if err := syscall.Setgroups([]int{}); err != nil {
		return err
	}
	if err := syscall.Setgid(uid); err != nil {
		return err
	}
	if err := syscall.Setuid(uid); err != nil {
		return err
	}
	// Linux resets dumpability when credentials change.
	_, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, 4, 0, 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
