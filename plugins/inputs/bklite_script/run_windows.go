//go:build windows

package bklite_script

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func defaultInterpreter() (string, error) {
	return "", errors.New("interpreter is required on windows when using script or script_env")
}

func (b *BkliteScript) initUser() error {
	if b.RunAs != "" {
		return errors.New("run_as is not supported on windows; run the Telegraf service under the desired account")
	}
	return nil
}

func (b *BkliteScript) chownScript(string) error {
	return nil
}

func (b *BkliteScript) chownRunDir() error {
	return nil
}

func (b *BkliteScript) runCommand(argv []string, env []string) runResult {
	if len(argv) == 0 {
		return startErrorResult(errors.New("empty command"))
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}

	var stdout cappedBuffer
	stdout.max = platformMaxOutputBytes
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return startErrorResult(err)
	}
	return waitAndCollect(cmd, b.runTimeout, &stdout, &stderr)
}

func (b *BkliteScript) withLock(fn func()) (bool, error) {
	f, err := os.OpenFile(b.lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return false, fmt.Errorf("opening lock file: %w", err)
	}
	defer f.Close()

	var ol windows.Overlapped
	err = windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&ol,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return false, nil
		}
		return false, fmt.Errorf("lockfile: %w", err)
	}
	defer func() {
		var uol windows.Overlapped
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &uol)
	}()
	fn()
	return true, nil
}
