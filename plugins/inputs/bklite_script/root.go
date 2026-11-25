package bklite_script

import (
	"errors"
	"fmt"
	"strings"
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

// effectiveTimeout is min(configured, interval-1s). Unset or zero configured
// keeps the historical interval-1s default. Interval <= 1s is guarded the
// same way as derivedTimeout.
func effectiveTimeout(configured, interval time.Duration) time.Duration {
	limit := derivedTimeout(interval)
	if configured <= 0 || configured > limit {
		return limit
	}
	return configured
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

// normalizedRunAs trims user-facing run_as so blank values inherit the
// collector process user. Explicit "root" is detected after trim.
func normalizedRunAs(username string) string {
	return strings.TrimSpace(username)
}

// checkRootPermission refuses an explicit root target. Empty or blank
// run_as is allowed and means inherit the Telegraf process user, including
// when that user is root. uid 0 aliases are refused separately after lookup.
func checkRootPermission(username string) error {
	if normalizedRunAs(username) == "root" {
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
