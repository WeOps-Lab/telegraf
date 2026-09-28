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

	"golang.org/x/sys/unix"
)

var osGeteuid = os.Geteuid
var lookupUser = user.Lookup
var osChown = os.Chown

func defaultInterpreter() (string, error) {
	return "/bin/sh", nil
}

func (b *BkliteScript) initUser() error {
	if err := checkRootPermission(osGeteuid(), b.RunAs); err != nil {
		return err
	}
	if b.RunAs == "" {
		// Already refused if euid is 0; run as the current non-root user.
		return nil
	}

	u, err := lookupUser(b.RunAs)
	if err != nil {
		return wrapUserLookup(b.RunAs, err)
	}
	uid64, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return fmt.Errorf("parsing uid for %q: %w", b.RunAs, err)
	}
	uid := uint32(uid64)
	if err := refuseIfRootUID(uid, b.RunAs); err != nil {
		return err
	}
	gid64, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return fmt.Errorf("parsing gid for %q: %w", b.RunAs, err)
	}

	if osGeteuid() == int(uid) {
		return nil
	}
	if osGeteuid() != 0 {
		return fmt.Errorf("cannot switch to user %q: telegraf is not running as root", b.RunAs)
	}
	b.uid = uid
	b.gid = uint32(gid64)
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

func (b *BkliteScript) chownRunDir() error {
	if !b.hasCredential {
		return nil
	}
	if err := osChown(b.RunDir, int(b.uid), int(b.gid)); err != nil {
		return fmt.Errorf("chown run_dir %q: %w", b.RunDir, err)
	}
	if err := os.Chmod(b.RunDir, 0700); err != nil {
		return fmt.Errorf("chmod run_dir %q: %w", b.RunDir, err)
	}
	return nil
}

func (b *BkliteScript) runCommand(argv []string, env []string) runResult {
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
	stdout.max = platformMaxOutputBytes
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return startErrorResult(err)
	}
	cleanup := applyResourceLimits(cmd.Process.Pid, b.runTimeout, b.Log)
	if cleanup != nil {
		defer cleanup()
	}
	return waitAndCollect(cmd, b.runTimeout, &stdout, &stderr)
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
