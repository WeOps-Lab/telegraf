//go:build unix

package bklite_script

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var osGeteuid = os.Geteuid

func defaultInterpreter() (string, error) {
	return "/bin/sh", nil
}

func (b *BkliteScript) initUser() error {
	if err := checkRootPermission(osGeteuid(), b.User, b.AllowRoot); err != nil {
		return err
	}
	if b.User == "" {
		return nil
	}
	if osGeteuid() != 0 {
		return fmt.Errorf("cannot switch to user %q: telegraf is not running as root", b.User)
	}
	u, err := user.Lookup(b.User)
	if err != nil {
		return wrapUserLookup(b.User, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return fmt.Errorf("parsing uid for %q: %w", b.User, err)
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return fmt.Errorf("parsing gid for %q: %w", b.User, err)
	}
	b.uid = uint32(uid)
	b.gid = uint32(gid)
	b.hasCredential = true
	return nil
}

func (b *BkliteScript) chownScript(path string) error {
	if !b.hasCredential {
		return nil
	}
	if err := os.Chown(path, int(b.uid), int(b.gid)); err != nil {
		return fmt.Errorf("chown temp script: %w", err)
	}
	return nil
}

func (b *BkliteScript) runCommand(
	argv []string,
	env []string,
	timeout time.Duration,
	memoryBytes int64,
	cpuSeconds int,
	maxOut int,
) runResult {
	if len(argv) == 0 {
		return startErrorResult(errors.New("empty command"))
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	sys := &syscall.SysProcAttr{Setpgid: true}
	if b.hasCredential {
		sys.Credential = &syscall.Credential{Uid: b.uid, Gid: b.gid}
	}
	cmd.SysProcAttr = sys

	var stdout cappedBuffer
	stdout.max = maxOut
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return startErrorResult(err)
	}
	cleanup := applyResourceLimits(cmd.Process.Pid, memoryBytes, cpuSeconds, b.Log)
	if cleanup != nil {
		defer cleanup()
	}
	return waitAndCollect(cmd, timeout, &stdout, &stderr)
}

func (b *BkliteScript) withLock(fn func()) (bool, error) {
	f, err := os.OpenFile(b.lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return false, fmt.Errorf("opening lock file: %w", err)
	}
	defer f.Close()

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return false, nil
		}
		return false, fmt.Errorf("flock: %w", err)
	}
	defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()
	fn()
	return true, nil
}
