//go:build linux

package bklite_script

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// linuxPrlimitPath is the only wrapper binary used to apply rlimits in the
// child before exec. It is a fixed absolute path so PATH cannot hijack it.
// util-linux prlimit sets the given limits on itself and then execs COMMAND.
// After SysProcAttr.Credential has already dropped the child, prlimit can
// still lower hard limits; it cannot raise them or gain caps.
//
// A telegraf self-reexec helper is intentionally not used: the Go runtime
// has already reserved far more than 256MiB of virtual address space, so
// setrlimit(RLIMIT_AS, 256MiB) in a Go process is unreliable and can abort
// the helper. Dash (/bin/sh on Ubuntu) also cannot set RLIMIT_NPROC
// (`ulimit -u` is illegal). Hand-rolled fork after runtime start is unsafe.
var linuxPrlimitPath = "/usr/bin/prlimit"

// linuxLimitLoaderVars are stripped from the child environment so they cannot
// interpose the prlimit wrapper. The script inherits the same sanitized env
// after exec. This is a tightening, not a loosening of production limits.
var linuxLimitLoaderVars = []string{
	"LD_PRELOAD",
	"LD_LIBRARY_PATH",
	"LD_AUDIT",
	"LD_DYNAMIC_WEAK",
	"LD_USE_LOAD_BIAS",
	"LD_PROFILE",
}

// wrapLinuxChildLimits prefixes argv with prlimit flags that apply the
// promised Linux rlimits, then "--", then the original interpreter argv.
// User-controlled strings are never placed in prlimit flags. includeNproc
// is true only for an explicit credential drop: the parent cannot prlimit a
// different UID after Start (EPERM). Same-UID (empty run_as inherit) keeps
// the historical NPROC fallback in applyResourceLimits when cgroup fails,
// so a busy test UID is not hard-capped at 256 threads by the wrapper.
func wrapLinuxChildLimits(argv []string, timeout time.Duration, includeNproc bool) []string {
	if len(argv) == 0 {
		return argv
	}
	flags := []string{
		linuxPrlimitPath,
		"--as=" + strconv.FormatInt(platformMemoryLimitBytes, 10),
		"--cpu=" + strconv.Itoa(cpuLimitSeconds(timeout)),
	}
	if includeNproc {
		flags = append(flags, "--nproc="+strconv.Itoa(rlimitNprocFallback))
	}
	wrapped := make([]string, 0, len(flags)+1+len(argv))
	wrapped = append(wrapped, flags...)
	wrapped = append(wrapped, "--")
	wrapped = append(wrapped, argv...)
	return wrapped
}

func sanitizeLinuxLimitEnv(env []string) []string {
	if len(env) == 0 {
		return env
	}
	blocked := make(map[string]struct{}, len(linuxLimitLoaderVars))
	for _, name := range linuxLimitLoaderVars {
		blocked[name] = struct{}{}
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, found := strings.Cut(kv, "=")
		if !found {
			out = append(out, kv)
			continue
		}
		if _, ok := blocked[name]; ok {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// linuxLimitFlagValues documents the exact numeric flags for tests and
// operators. Values come from the existing sandbox constants only.
func linuxLimitFlagValues(timeout time.Duration, includeNproc bool) []string {
	flags := []string{
		fmt.Sprintf("--as=%d", platformMemoryLimitBytes),
		fmt.Sprintf("--cpu=%d", cpuLimitSeconds(timeout)),
	}
	if includeNproc {
		flags = append(flags, fmt.Sprintf("--nproc=%d", rlimitNprocFallback))
	}
	return flags
}
