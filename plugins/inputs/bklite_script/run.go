package bklite_script

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/influxdata/telegraf/internal"
)

type runResult struct {
	stdout    []byte
	stderr    string
	truncated bool
	exitCode  int
	err       error
}

type cappedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.max <= 0 {
		return c.buf.Write(p)
	}
	remaining := c.max - c.buf.Len()
	if remaining <= 0 {
		c.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		c.truncated = true
		if _, err := c.buf.Write(p[:remaining]); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

func exitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, internal.ErrTimeout) {
		return exitTimeout
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

func waitAndCollect(cmd *exec.Cmd, timeout time.Duration, stdout *cappedBuffer, stderr *bytes.Buffer) runResult {
	err := internal.WaitTimeout(cmd, timeout)
	res := runResult{
		stdout:    stdout.buf.Bytes(),
		truncated: stdout.truncated,
		err:       err,
		exitCode:  exitCodeFromError(err),
	}
	if stderr.Len() > 0 {
		msg := stderr.Bytes()
		if len(msg) > 512 {
			msg = msg[:512]
		}
		res.stderr = string(msg)
	}
	if err != nil && res.exitCode == 0 {
		res.exitCode = 1
	}
	return res
}

func startErrorResult(err error) runResult {
	return runResult{
		err:      err,
		exitCode: exitNoStart,
		stderr:   fmt.Sprintf("start: %v", err),
	}
}
