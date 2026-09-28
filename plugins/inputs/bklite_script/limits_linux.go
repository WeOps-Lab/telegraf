//go:build linux

package bklite_script

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/influxdata/telegraf"
	"golang.org/x/sys/unix"
)

func applyResourceLimits(pid int, timeout time.Duration, log telegraf.Logger) func() {
	var cleanup func()
	if fn, err := applyCgroupLimits(pid); err != nil {
		if log != nil {
			log.Debugf("cgroup v2 limits not applied: %v", err)
		}
	} else {
		cleanup = fn
	}

	mem := unix.Rlimit{Cur: platformMemoryLimitBytes, Max: platformMemoryLimitBytes}
	if err := unix.Prlimit(pid, unix.RLIMIT_AS, &mem, nil); err != nil && log != nil {
		log.Debugf("prlimit RLIMIT_AS: %v", err)
	}

	cpu := uint64(cpuLimitSeconds(timeout))
	cpulim := unix.Rlimit{Cur: cpu, Max: cpu}
	if err := unix.Prlimit(pid, unix.RLIMIT_CPU, &cpulim, nil); err != nil && log != nil {
		log.Debugf("prlimit RLIMIT_CPU: %v", err)
	}
	return cleanup
}

func applyCgroupLimits(pid int) (func(), error) {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		return nil, err
	}
	dir := filepath.Join("/sys/fs/cgroup/bklite_script", strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.Remove(dir) }

	memPath := filepath.Join(dir, "memory.max")
	if err := os.WriteFile(memPath, []byte(strconv.FormatInt(platformMemoryLimitBytes, 10)), 0640); err != nil {
		cleanup()
		return nil, err
	}
	pidsPath := filepath.Join(dir, "pids.max")
	if err := os.WriteFile(pidsPath, []byte(strconv.Itoa(platformNprocLimit)), 0640); err != nil {
		cleanup()
		return nil, err
	}
	procs := filepath.Join(dir, "cgroup.procs")
	if err := os.WriteFile(procs, []byte(strconv.Itoa(pid)), 0640); err != nil {
		cleanup()
		return nil, err
	}
	return cleanup, nil
}
