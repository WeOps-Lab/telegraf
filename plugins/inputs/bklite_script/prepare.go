package bklite_script

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kballard/go-shellquote"
)

func (b *BkliteScript) prepareCommands() ([][]string, func(), error) {
	if b.usesScriptBody() {
		path, cleanup, err := b.materializeScript()
		if err != nil {
			return nil, cleanup, err
		}
		argv := append([]string{b.Interpreter}, b.InterpreterArgs...)
		argv = append(argv, path)
		return [][]string{argv}, cleanup, nil
	}

	if b.ScriptFile != "" {
		if b.Interpreter == "" {
			return [][]string{{b.ScriptFile}}, nil, nil
		}
		argv := append([]string{b.Interpreter}, b.InterpreterArgs...)
		argv = append(argv, b.ScriptFile)
		return [][]string{argv}, nil, nil
	}

	commands := append([]string{}, b.Commands...)
	if b.Command != "" {
		commands = append(commands, b.Command)
	}

	var argvs [][]string
	for _, pattern := range commands {
		expanded, err := expandCommand(pattern)
		if err != nil {
			return nil, nil, err
		}
		argvs = append(argvs, expanded...)
	}
	if len(argvs) == 0 {
		return nil, nil, errors.New("no commands to run")
	}
	return argvs, nil, nil
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

func expandCommand(pattern string) ([][]string, error) {
	cmdAndArgs := strings.SplitN(pattern, " ", 2)
	if len(cmdAndArgs) == 0 || cmdAndArgs[0] == "" {
		return nil, nil
	}

	matches, err := filepath.Glob(cmdAndArgs[0])
	if err != nil {
		return nil, fmt.Errorf("glob %q: %w", cmdAndArgs[0], err)
	}

	var patterns []string
	if len(matches) == 0 {
		patterns = []string{pattern}
	} else {
		for _, match := range matches {
			if len(cmdAndArgs) == 1 {
				patterns = append(patterns, match)
			} else {
				patterns = append(patterns, strings.Join([]string{match, cmdAndArgs[1]}, " "))
			}
		}
	}

	argvs := make([][]string, 0, len(patterns))
	for _, p := range patterns {
		split, err := shellquote.Split(p)
		if err != nil || len(split) == 0 {
			return nil, fmt.Errorf("unable to parse command %q: %w", p, err)
		}
		argvs = append(argvs, split)
	}
	return argvs, nil
}
