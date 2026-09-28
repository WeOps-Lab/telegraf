package bklite_script

import (
	"errors"
	"fmt"
	"time"
)

const (
	minInterval = 60 * time.Second

	// Fixed BKLite sandbox defaults; not user-configurable.
	platformMemoryLimitBytes = 256 * 1024 * 1024
	platformNprocLimit       = 64
	platformMaxSeries        = 50
	platformMaxOutputBytes   = 64 * 1024

	// rlimitNprocFallback is used only when cgroup v2 pids.max cannot be
	// applied. RLIMIT_NPROC is a per-UID total (not a per-child tree), so
	// 256 allows /bin/sh plus helpers on a typical telegraf host while
	// still bounding a fork bomb.
	rlimitNprocFallback = 256

	// GNU timeout-style codes so operators can tell timeout/skip from the script.
	exitTimeout  = 124
	exitLockBusy = 125
	exitNoStart  = 127
)

func derivedTimeout(interval time.Duration) time.Duration {
	if interval <= time.Second {
		return interval
	}
	return interval - time.Second
}

func validateInterval(interval time.Duration) error {
	if interval < minInterval {
		return fmt.Errorf("interval must be at least %s (got %s)", minInterval, interval)
	}
	return nil
}

func cpuLimitSeconds(timeout time.Duration) int {
	sec := int(timeout / time.Second)
	if sec < 1 {
		return 1
	}
	return sec
}

// checkRootPermission refuses to run a child as root. euid is Telegraf's
// effective uid; username is the configured run_as target.
func checkRootPermission(euid int, username string) error {
	if username == "root" {
		return errors.New("refusing to run as root; set run_as to a non-root account")
	}
	if euid == 0 && username == "" {
		return errors.New("refusing to run as root; set run_as to a non-root account")
	}
	return nil
}

func wrapUserLookup(username string, err error) error {
	return fmt.Errorf("looking up user %q: %w", username, err)
}

func refuseIfRootUID(uid uint32, username string) error {
	if uid == 0 {
		return fmt.Errorf("refusing to run as root; user %q has uid 0", username)
	}
	return nil
}
