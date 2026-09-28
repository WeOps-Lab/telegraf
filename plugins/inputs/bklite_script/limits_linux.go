//go:build linux

package bklite_script

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/influxdata/telegraf"
	"golang.org/x/sys/unix"
)

func applyResourceLimits(pid int, memoryBytes int64, cpuSeconds int, log telegraf.Logger) func() {
	var cleanup func()
	if memoryBytes > 0 {
		if fn, err := applyCgroupMemory(pid, memoryBytes); err != nil {
			if log != nil {
				log.Debugf("cgroup v2 memory limit not applied: %v", err)
			}
		} else {
			cleanup = fn
		}
		lim := unix.Rlimit{Cur: uint64(memoryBytes), Max: uint64(memoryBytes)}
		if err := unix.Prlimit(pid, unix.RLIMIT_AS, &lim, nil); err != nil && log != nil {
			log.Debugf("prlimit RLIMIT_AS: %v", err)
		}
	}
	if cpuSeconds > 0 {
		lim := unix.Rlimit{Cur: uint64(cpuSeconds), Max: uint64(cpuSeconds)}
		if err := unix.Prlimit(pid, unix.RLIMIT_CPU, &lim, nil); err != nil && log != nil {
			log.Debugf("prlimit RLIMIT_CPU: %v", err)
		}
	}
	return cleanup
}

func applyCgroupMemory(pid int, memoryBytes int64) (func(), error) {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		return nil, err
	}
	dir := filepath.Join("/sys/fs/cgroup/bklite_script", strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.Remove(dir) }
	maxPath := filepath.Join(dir, "memory.max")
	if err := os.WriteFile(maxPath, []byte(strconv.FormatInt(memoryBytes, 10)), 0640); err != nil {
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
