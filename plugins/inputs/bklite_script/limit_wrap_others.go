//go:build unix && !linux

package bklite_script

import "time"

func wrapLinuxChildLimits(argv []string, _ time.Duration, _ bool) []string {
	return argv
}

func sanitizeLinuxLimitEnv(env []string) []string {
	return env
}
