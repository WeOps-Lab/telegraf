package bklite_script

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

func interpreterBase(interpreter string) string {
	base := interpreter
	if i := strings.LastIndexAny(interpreter, `/\`); i >= 0 {
		base = interpreter[i+1:]
	}
	return strings.ToLower(base)
}

func isPowerShell(interpreter string) bool {
	switch interpreterBase(interpreter) {
	case "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		return true
	default:
		return false
	}
}

func isCmd(interpreter string) bool {
	switch interpreterBase(interpreter) {
	case "cmd", "cmd.exe":
		return true
	default:
		return false
	}
}

func isPython(interpreter string) bool {
	switch interpreterBase(interpreter) {
	case "python", "python.exe", "python3", "python3.exe", "py", "py.exe":
		return true
	default:
		return false
	}
}

// scriptFileSuffix returns the temp-file extension required so the
// interpreter will actually execute the materialized script. An empty
// suffix leaves CreateTemp's default extensionless name (Unix shells).
func scriptFileSuffix(interpreter string) string {
	switch {
	case isPowerShell(interpreter):
		return ".ps1"
	case isCmd(interpreter):
		return ".cmd"
	case isPython(interpreter):
		return ".py"
	default:
		return ""
	}
}

func hasFlag(params []string, names ...string) bool {
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		want[strings.ToLower(n)] = struct{}{}
	}
	for _, p := range params {
		if _, ok := want[strings.ToLower(p)]; ok {
			return true
		}
	}
	return false
}

// prepareInterpreterArgs builds argv for the materialized script path.
// PowerShell must see -File <path>, otherwise an extensionless or even a
// .ps1 path passed as a bare argument is treated as a native command.
// cmd.exe needs /C so it runs the batch file and exits.
func prepareInterpreterArgs(interpreter string, params []string, path string) []string {
	argv := []string{interpreter}
	switch {
	case isPowerShell(interpreter):
		if !hasFlag(params, "-NoProfile") {
			argv = append(argv, "-NoProfile")
		}
		if !hasFlag(params, "-ExecutionPolicy") {
			argv = append(argv, "-ExecutionPolicy", "Bypass")
		}
		argv = append(argv, params...)
		if !hasFlag(params, "-File", "-Command") {
			argv = append(argv, "-File")
		}
		argv = append(argv, path)
	case isCmd(interpreter):
		argv = append(argv, params...)
		if !hasFlag(params, "/C") {
			argv = append(argv, "/C")
		}
		argv = append(argv, path)
	default:
		argv = append(argv, params...)
		argv = append(argv, path)
	}
	return argv
}

func splitPathElems(path string) []string {
	path = strings.ReplaceAll(path, "/", `\`)
	var elems []string
	for _, e := range strings.Split(path, `\`) {
		if e == "" {
			continue
		}
		// Drop a drive prefix like "C:" so "C:\Windows\..." compares cleanly.
		if len(e) == 2 && e[1] == ':' {
			continue
		}
		elems = append(elems, e)
	}
	return elems
}

// isRestrictedWindowsTemp reports whether path is under Windows\SystemTemp
// or Windows\Temp. The check is OS-agnostic so Darwin tests can use
// Windows-style paths (filepath would not split on '\').
func isRestrictedWindowsTemp(path string) bool {
	elems := splitPathElems(path)
	for i := 0; i < len(elems)-1; i++ {
		if !strings.EqualFold(elems[i], "windows") && !strings.EqualFold(elems[i], "winnt") {
			continue
		}
		next := elems[i+1]
		if strings.EqualFold(next, "systemtemp") || strings.EqualFold(next, "temp") {
			return true
		}
	}
	return false
}

func joinRunDir(base string) string {
	const name = "bklite_script"
	if strings.Contains(base, `\`) && !strings.Contains(base, `/`) {
		return strings.TrimRight(base, `\`) + `\` + name
	}
	return filepath.Join(base, name)
}

// resolveRunDir keeps a usable run directory. An empty value defaults to
// $TMPDIR/bklite_script. If that lands under Windows\SystemTemp or
// Windows\Temp (typical for a Telegraf service running as SYSTEM), move
// to %ProgramData%\bklite_script, then the user cache dir.
func resolveRunDir(runDir string) string {
	if runDir == "" {
		runDir = joinRunDir(os.TempDir())
	}
	if !isRestrictedWindowsTemp(runDir) {
		return runDir
	}
	if v := os.Getenv("ProgramData"); v != "" {
		return joinRunDir(v)
	}
	if dir, err := os.UserCacheDir(); err == nil && dir != "" && !isRestrictedWindowsTemp(dir) {
		return joinRunDir(dir)
	}
	return runDir
}

func removeWindowsCarriageReturns(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\r"), nil)
}
