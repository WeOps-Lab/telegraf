package bklite_script

import (
	"errors"
	"fmt"
	"os"
)

func (b *BkliteScript) prepareArgv() ([]string, func(), error) {
	if !b.usesScriptBody() {
		return nil, nil, errors.New("must set script or script_env")
	}
	path, cleanup, err := b.materializeScript()
	if err != nil {
		return nil, cleanup, err
	}
	argv := append([]string{b.Interpreter}, b.Params...)
	argv = append(argv, path)
	return argv, cleanup, nil
}

func (b *BkliteScript) materializeScript() (string, func(), error) {
	body := b.Script
	if b.ScriptEnv != "" {
		if v := os.Getenv(b.ScriptEnv); v != "" {
			body = v
		}
	}
	if body == "" {
		if b.ScriptEnv != "" {
			return "", nil, fmt.Errorf("script_env %q is empty", b.ScriptEnv)
		}
		return "", nil, errors.New("script body is empty")
	}

	f, err := os.CreateTemp(b.RunDir, "script-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp script: %w", err)
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }

	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, fmt.Errorf("writing temp script: %w", err)
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, fmt.Errorf("chmod temp script: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("closing temp script: %w", err)
	}
	if err := b.chownScript(path); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}
