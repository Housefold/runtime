package estate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/housefold/runtime/internal/module"
	"github.com/housefold/runtime/internal/packageverify"
)

const MinFreeBytes = 64 << 20
const ResumeFreeBytes = 96 << 20
const MaxEstateBytes = 1 << 30
const MaxAccountingEntries = 20000
const ManagedRSSKiB = 512 * 1024
const RuntimeRSSKiB = 256 * 1024

var ErrResources = errors.New("Runtime resource admission unavailable")

type HostResources struct {
	AvailableKiB, TotalKiB               uint64
	CgroupMemoryBytes, CgroupMemoryLimit uint64
	CgroupPIDs, CgroupPIDLimit           uint64
	Runtime                              module.Usage
}
type ResourceStatus struct {
	Host                    HostResources
	Managed                 module.Usage
	StorageBytes, FreeBytes uint64
	Pressure                bool
	SampleError             bool
}

func limitsFor(m packageverify.Manifest) module.ProcessLimits {
	r := packageverify.EffectiveLimits(m.Resources)
	p := m.Priority
	if p == "" {
		p = "normal"
	}
	return module.ProcessLimits{MemoryKiB: r.MemoryKiB, CPUPercent: r.CPUPercent, Threads: r.Threads, FDs: r.FDs, NProc: 256, Priority: p}
}
func resourceAdmission(d Inventory) error {
	var memory, cpu, threads, fds uint64
	for _, row := range d.Modules {
		if !row.Installed || !row.Desired {
			continue
		}
		b := row.Current
		if row.Pending != nil && row.UpdateError == "" {
			b = row.Pending
		}
		if b == nil {
			continue
		}
		r := packageverify.EffectiveRequests(b.declaration().Requests)
		memory += r.MemoryKiB
		cpu += r.CPUPercent
		threads += r.Threads
		fds += r.FDs
	}
	if memory > ManagedRSSKiB || cpu > 100 || threads > 192 || fds > 768 {
		return ErrResources
	}
	return nil
}
func readSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if len(raw) > 65536 {
		return nil, ErrResources
	}
	return raw, err
}
func cgroupNumber(path string) uint64 {
	raw, err := readSmall(path)
	if err != nil || strings.TrimSpace(string(raw)) == "max" {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return n
}
func sampleHost(_ string) (HostResources, error) {
	var h HostResources
	raw, err := readSmall("/proc/meminfo")
	if err != nil {
		return h, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, _ := strconv.ParseUint(f[1], 10, 64)
		if f[0] == "MemAvailable:" {
			h.AvailableKiB = n
		}
		if f[0] == "MemTotal:" {
			h.TotalKiB = n
		}
	}
	if h.TotalKiB == 0 {
		return h, ErrResources
	}
	// Resolve only this App's cgroup; never inspect/control another HA service.
	group := ""
	raw, err = readSmall("/proc/self/cgroup")
	if err != nil {
		return h, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "0::/") {
			group = strings.TrimPrefix(line, "0::")
		}
	}
	if group != "" && !strings.Contains(group, "..") {
		base := filepath.Join("/sys/fs/cgroup", group)
		h.CgroupMemoryBytes = cgroupNumber(filepath.Join(base, "memory.current"))
		h.CgroupMemoryLimit = cgroupNumber(filepath.Join(base, "memory.max"))
		// Reclaimable inactive file cache is not module working-set pressure.
		if stat, readErr := readSmall(filepath.Join(base, "memory.stat")); readErr == nil {
			for _, line := range strings.Split(string(stat), "\n") {
				f := strings.Fields(line)
				if len(f) == 2 && f[0] == "inactive_file" {
					n, _ := strconv.ParseUint(f[1], 10, 64)
					if n < h.CgroupMemoryBytes {
						h.CgroupMemoryBytes -= n
					}
				}
			}
		}
		h.CgroupPIDs = cgroupNumber(filepath.Join(base, "pids.current"))
		h.CgroupPIDLimit = cgroupNumber(filepath.Join(base, "pids.max"))
	}
	h.Runtime, err = module.RuntimeUsage()
	return h, err
}
func freeSpace(root string) (uint64, error) {
	var st syscall.Statfs_t
	err := syscall.Statfs(root, &st)
	if err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
func (e *Engine) free() (uint64, error) {
	if e.config.FreeSpace != nil {
		return e.config.FreeSpace(e.config.Root)
	}
	return freeSpace(e.config.Root)
}
func (e *Engine) checkSpace(extra uint64) error {
	free, err := e.free()
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil || free < MinFreeBytes || extra > free-MinFreeBytes || e.resources.StorageBytes > MaxEstateBytes || e.storageDegraded {
		e.storageDegraded = true
		return ErrStorage
	}
	return nil
}
func (e *Engine) observeUnit(u *unit) error {
	sample := e.config.SampleUsage
	if sample == nil {
		sample = func(p *module.Process) (module.Usage, error) { return p.SampleGroup() }
	}
	usage, err := sample(u.process)
	u.mu.Lock()
	defer u.mu.Unlock()
	if err != nil {
		u.violations++
	} else {
		u.usage = usage
		l := u.limits
		if usage.RSSKiB > l.MemoryKiB || usage.CPUPercent > l.CPUPercent || usage.Threads > l.Threads || usage.FDs > l.FDs || usage.Processes > 32 {
			u.violations++
		} else {
			u.violations = 0
		}
	}
	if u.violations >= 3 {
		return ErrResources
	}
	return nil
}
func priority(p string) int {
	switch p {
	case "background":
		return 0
	case "essential":
		return 2
	}
	return 1
}
func (e *Engine) resourceTick(now time.Time) error {
	sample := e.config.SampleResources
	if sample == nil {
		sample = sampleHost
	}
	host, err := sample(e.config.Root)
	free, spaceErr := e.free()
	e.mu.Lock()
	units := make([]*unit, 0, len(e.units))
	for _, u := range e.units {
		units = append(units, u)
	}
	previous := e.resources
	e.mu.Unlock()
	var managed module.Usage
	for _, u := range units {
		u.mu.Lock()
		if !u.joined {
			managed.RSSKiB += u.usage.RSSKiB
			managed.Threads += u.usage.Threads
			managed.FDs += u.usage.FDs
			managed.CPUPercent += u.usage.CPUPercent
			managed.Processes += u.usage.Processes
		}
		u.mu.Unlock()
	}
	pressure := err != nil || spaceErr != nil || free < MinFreeBytes || previous.StorageBytes > MaxEstateBytes || (host.TotalKiB > 0 && host.AvailableKiB < max(uint64(64*1024), host.TotalKiB/20)) ||
		(host.CgroupMemoryLimit > 0 && host.CgroupMemoryBytes > host.CgroupMemoryLimit*85/100) ||
		(host.CgroupPIDLimit > 0 && host.CgroupPIDs+32 >= host.CgroupPIDLimit) || host.Runtime.RSSKiB > RuntimeRSSKiB || host.Runtime.FDs > 768 || host.Runtime.Threads > 128 || managed.RSSKiB > ManagedRSSKiB || managed.Threads > 192 || managed.FDs > 768
	e.mu.Lock()
	e.resources = ResourceStatus{Host: host, Managed: managed, FreeBytes: free, StorageBytes: previous.StorageBytes, Pressure: pressure, SampleError: err != nil || spaceErr != nil}
	e.mu.Unlock()
	if spaceErr != nil || free < MinFreeBytes {
		e.mu.Lock()
		e.storageDegraded = true
		e.mu.Unlock()
		if err = e.collectStorage(true); err != nil {
			return err
		}
	}
	if pressure {
		e.pressureClear = 0
		sort.Slice(units, func(i, j int) bool {
			a, b := priority(units[i].limits.Priority), priority(units[j].limits.Priority)
			if a == b {
				return units[i].identity.Module < units[j].identity.Module
			}
			return a < b
		})
		for _, u := range units {
			row := e.inv.Modules[u.identity.Module]
			if !row.Installed || !row.Desired || row.PressurePaused {
				continue
			}
			rd := e.router.Snapshot()
			if rd.Modules[u.identity.Module].Active != u.identity.Generation {
				continue
			}
			row.PressurePaused = true
			d := cloneInventory(e.inv)
			d.Modules[u.identity.Module] = row
			// Preserve intent if storage works. In-memory pause plus cancellation still
			// protects Runtime when commits are impossible; reboot resamples first.
			if err = e.save(d); err != nil {
				e.mu.Lock()
				e.inv.Modules[u.identity.Module] = row
				e.mu.Unlock()
			}
			stopErr := e.router.StopSelected(u.identity.Module)
			if stopErr != nil {
				u.cancel()
				return stopErr
			}
			e.forget(u.identity.Generation)
			return nil // one priority victim per tick
		}
		return nil
	}
	// Sustained recovery, not a single clear sample. Resume highest priority first,
	// one module per interval, with fresh authority and unchanged failure budget.
	e.pressureClear++
	if free >= ResumeFreeBytes {
		e.mu.Lock()
		degraded := e.storageDegraded
		e.mu.Unlock()
		if degraded {
			if probeStorage(e.config.Root) == nil {
				e.mu.Lock()
				e.storageDegraded = false
				e.mu.Unlock()
			}
		}
	}
	if e.pressureClear < 5 {
		return nil
	}
	ids := []string{}
	for id, row := range e.inv.Modules {
		if row.PressurePaused {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := priority(limitsFor(e.inv.Modules[ids[i]].Current.declaration()).Priority), priority(limitsFor(e.inv.Modules[ids[j]].Current.declaration()).Priority)
		if a == b {
			return ids[i] < ids[j]
		}
		return a > b
	})
	for _, id := range ids {
		row := e.inv.Modules[id]
		row.PressurePaused = false
		d := cloneInventory(e.inv)
		d.Modules[id] = row
		if err = e.save(d); err != nil {
			return err
		}
		e.pressureClear = 0
		break
	}
	return nil
}
func probeStorage(root string) error {
	f, err := os.CreateTemp(root, ".space-probe-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write([]byte("storage probe")); err != nil {
		return err
	}
	return f.Sync()
}

// collectStorage runs only under lifecycle serialization. The protected set is
// rebuilt from durable coordination plus still-owned children and executions.
// All tree entries are checked before any deletion. Unknown or linked entries
// fail closed; a corrupt tree is never cleared to get more space.
func (e *Engine) collectStorage(pressure bool) (result error) {
	defer func() {
		if result != nil {
			e.mu.Lock()
			e.storageDegraded = true
			e.mu.Unlock()
		}
	}()
	protected := map[string]bool{}
	artifacts := map[string]bool{}
	known := map[string]bool{}
	for id, row := range e.inv.Modules {
		sum := sha256.Sum256([]byte(id))
		known[hex.EncodeToString(sum[:])] = true
		for _, b := range []*Bundle{row.Current, row.Previous, row.Pending} {
			if b != nil {
				artifacts[b.declaration().ArtifactDigest] = true
			}
		}
	}
	protect := func(id string, n uint64) {
		sum := sha256.Sum256([]byte(id))
		protected[filepath.Join(hex.EncodeToString(sum[:]), strconv.FormatUint(n, 10))] = true
	}
	for _, g := range e.router.Snapshot().Generations {
		protect(g.Module, g.Number)
		artifacts[g.ArtifactDigest] = true
	}
	for _, r := range e.exec.Snapshot().Records {
		protect(r.Module, r.Generation)
	}
	e.mu.Lock()
	for n, u := range e.units {
		u.mu.Lock()
		joined := u.joined
		u.mu.Unlock()
		if !joined {
			protect(u.identity.Module, n)
		}
	}
	e.mu.Unlock()
	e.io.Lock()
	defer e.io.Unlock()
	remove := []string{}
	var used uint64
	rootEntries, err := readEntries(e.config.Root, 64)
	if err != nil || len(rootEntries) > 64 {
		return ErrStorage
	}
	for _, d := range rootEntries {
		info, err := d.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return ErrStorage
		}
		if info.Mode().IsRegular() {
			used += uint64(info.Size())
		} else if !info.IsDir() {
			return ErrStorage
		}
	}
	count := 0
	for _, scope := range []string{"persistent", "generation", "cache", "temp"} {
		root := e.stateRoot(scope)
		err := walkBounded(root, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			count++
			if count > MaxAccountingEntries {
				return ErrStorage
			}
			info, err := d.Info()
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return ErrStorage
			}
			if !info.IsDir() && !info.Mode().IsRegular() {
				return ErrStorage
			}
			if info.Mode().IsRegular() {
				used += uint64(info.Size())
			}
			rel, _ := filepath.Rel(root, path)
			parts := strings.Split(rel, string(os.PathSeparator))
			if rel == "." {
				return nil
			}
			if !known[parts[0]] {
				return ErrStorage
			}
			if len(parts) >= 2 {
				if _, err = strconv.ParseUint(parts[1], 10, 64); err != nil {
					return ErrStorage
				}
			}
			if len(parts) == 2 && info.IsDir() && !protected[rel] {
				remove = append(remove, path)
			}
			// Only disposable caches may be reset while their generation is retained;
			// live stores are never unlinked under IPC writers.
			return nil
		})
		if err != nil {
			return err
		}
	}
	root := filepath.Join(e.config.Root, "artifacts")
	entries, err := readEntries(root, 256)
	if err != nil {
		return err
	}
	for _, d := range entries {
		count++
		if count > MaxAccountingEntries {
			return ErrStorage
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return ErrStorage
		}
		used += uint64(info.Size())
		name := d.Name()
		if strings.HasPrefix(name, ".stage-") || (len(name) == 64 && strings.Trim(name, "0123456789abcdef") == "" && !artifacts[name]) {
			remove = append(remove, filepath.Join(root, name))
		} else if !artifacts[name] {
			return ErrStorage
		}
	}
	// Temp artifacts first, then obsolete cache/temp, then obsolete durable state
	// and artifacts. No selected/previous/pending reference enters this list.
	rank := func(p string) int {
		if strings.Contains(filepath.Base(p), ".stage-") {
			return 0
		}
		if strings.Contains(p, "states-cache") || strings.Contains(p, "states-temp") {
			return 1
		}
		return 2
	}
	sort.Slice(remove, func(i, j int) bool {
		if rank(remove[i]) == rank(remove[j]) {
			return remove[i] < remove[j]
		}
		return rank(remove[i]) < rank(remove[j])
	})
	for _, path := range remove {
		if err = os.RemoveAll(path); err != nil {
			return err
		}
	}
	e.mu.Lock()
	e.resources.StorageBytes = used
	e.mu.Unlock()
	if used > MaxEstateBytes {
		e.mu.Lock()
		e.storageDegraded = true
		e.mu.Unlock()
		return ErrStorage
	}
	return nil
}

// ReadDir(n) caps allocation before checking limits, unlike WalkDir/ReadDir(-1).
func readEntries(path string, limit int) ([]os.DirEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := f.ReadDir(limit + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(rows) > limit {
		return nil, ErrStorage
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name() < rows[j].Name() })
	return rows, nil
}
func walkBounded(root string, visit func(string, os.DirEntry, error) error) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	var walk func(string, os.DirEntry, int) error
	walk = func(path string, d os.DirEntry, depth int) error {
		if depth > 3 {
			return ErrStorage
		}
		if err := visit(path, d, nil); err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rows, err := readEntries(path, 256)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err = walk(filepath.Join(path, row.Name()), row, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root, fs.FileInfoToDirEntry(info), 0)
}
