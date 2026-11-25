//go:build linux

package bklite_script

import (
	"bytes"
	"os"
	"strconv"
)

// currentUIDThreadCount counts threads whose real UID matches the caller.
// Used only to skip fork-dependent tests on a busy developer UID when the
// historical NPROC fallback (256) is in force. ok is false if /proc is unusable.
func currentUIDThreadCount() (int, bool) {
	want := strconv.Itoa(os.Geteuid())
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	n := 0
	for _, e := range ents {
		data, err := os.ReadFile("/proc/" + e.Name() + "/status")
		if err != nil {
			continue
		}
		if !statusRealUIDEquals(data, want) {
			continue
		}
		n += statusThreadCount(data)
	}
	return n, true
}

func statusRealUIDEquals(data []byte, want string) bool {
	for _, line := range bytes.Split(data, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("Uid:")) {
			continue
		}
		fields := bytes.Fields(line)
		// Uid: real effective saved fs
		return len(fields) >= 2 && string(fields[1]) == want
	}
	return false
}

func statusThreadCount(data []byte) int {
	for _, line := range bytes.Split(data, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("Threads:")) {
			continue
		}
		fields := bytes.Fields(line)
		if len(fields) < 2 {
			return 1
		}
		n, err := strconv.Atoi(string(fields[1]))
		if err != nil || n < 1 {
			return 1
		}
		return n
	}
	return 1
}
