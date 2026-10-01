//go:build linux

package module

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const MaxProcEntries = 4096

var ErrUsage = errors.New("owned process usage unavailable or exceeds bound")

type procStat struct {
	PID, Group               int
	State                    string
	RSS, Threads, CPU, Start uint64
}

func boundedRead(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(raw)) > max {
		return nil, ErrUsage
	}
	return raw, nil
}
func parseProc(raw []byte) (procStat, error) {
	var out procStat
	end := strings.LastIndex(string(raw), ")")
	if end < 1 {
		return out, ErrUsage
	}
	first := strings.IndexByte(string(raw), ' ')
	if first < 0 {
		return out, ErrUsage
	}
	pid, err := strconv.Atoi(string(raw[:first]))
	if err != nil {
		return out, ErrUsage
	}
	out.PID = pid
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 22 {
		return out, ErrUsage
	}
	out.State = fields[0]
	parse := func(i int) (uint64, error) { return strconv.ParseUint(fields[i], 10, 64) }
	value, err := parse(2)
	if err != nil {
		return out, ErrUsage
	}
	out.Group = int(value)
	user, err := parse(11)
	if err != nil {
		return out, ErrUsage
	}
	system, err := parse(12)
	if err != nil {
		return out, ErrUsage
	}
	out.CPU = user + system
	if out.Threads, err = parse(17); err != nil {
		return out, ErrUsage
	}
	if out.Start, err = parse(19); err != nil {
		return out, ErrUsage
	}
	rss, err := strconv.ParseInt(fields[21], 10, 64)
	if err != nil || rss < 0 {
		return out, ErrUsage
	}
	out.RSS = uint64(rss) * uint64(os.Getpagesize()) / 1024
	return out, nil
}
func readProc(pid int) (procStat, error) {
	raw, err := boundedRead(fmt.Sprintf("/proc/%d/stat", pid), 65536)
	if err != nil {
		return procStat{}, err
	}
	return parseProc(raw)
}
func groupMembers(group int, birth uint64) ([]procStat, error) {
	if leader, err := readProc(group); err == nil && leader.Start != birth {
		return nil, nil
	}
	dir, err := os.Open("/proc")
	if err != nil {
		return nil, ErrUsage
	}
	defer dir.Close()
	entries, err := dir.Readdirnames(MaxProcEntries + 1)
	if (err != nil && err != io.EOF) || len(entries) > MaxProcEntries {
		return nil, ErrUsage
	}
	out := []procStat{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry)
		if err != nil {
			continue
		}
		s, err := readProc(pid)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if s.Group == group && s.Start >= birth && s.State != "Z" && s.State != "X" {
			out = append(out, s)
		}
	}
	return out, nil
}
func fdCount(pid int) (uint64, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	names, err := f.Readdirnames(4097)
	if err != nil && err != io.EOF {
		return 0, err
	}
	if len(names) > 4096 {
		return 0, ErrUsage
	}
	return uint64(len(names)), nil
}

// SampleGroup accounts direct child plus confined descendants. A process that
// disappears during sampling is retried next tick; it cannot fabricate zero use.
func (p *Process) SampleGroup() (Usage, error) {
	select {
	case <-p.done:
		return Usage{}, os.ErrProcessDone
	default:
	}
	rows, err := groupMembers(p.cmd.Process.Pid, p.birth)
	if err != nil {
		return Usage{}, err
	}
	var usage Usage
	for _, row := range rows {
		count, err := fdCount(row.PID)
		if err != nil {
			return Usage{}, err
		}
		usage.RSSKiB += row.RSS
		usage.Threads += row.Threads
		usage.FDs += count
		usage.CPUTicks += row.CPU
		usage.Processes++
	}
	p.mu.Lock()
	now := time.Now()
	if !p.sampleAt.IsZero() && usage.CPUTicks >= p.sampleCPU {
		elapsed := now.Sub(p.sampleAt)
		if elapsed > 0 {
			usage.CPUPercent = (usage.CPUTicks - p.sampleCPU) * uint64(time.Second) / uint64(elapsed)
		}
	}
	p.sampleAt = now
	p.sampleCPU = usage.CPUTicks
	p.mu.Unlock()
	return usage, nil
}
func RuntimeUsage() (Usage, error) {
	row, err := readProc(os.Getpid())
	if err != nil {
		return Usage{}, err
	}
	fds, err := fdCount(os.Getpid())
	return Usage{RSSKiB: row.RSS, Threads: row.Threads, FDs: fds, CPUTicks: row.CPU, Processes: 1}, err
}
