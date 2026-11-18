package bklite_script

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScriptFileSuffix(t *testing.T) {
	cases := map[string]string{
		`powershell.exe`: ".ps1",
		`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`: ".ps1",
		`pwsh`:                        ".ps1",
		`pwsh.exe`:                    ".ps1",
		`cmd.exe`:                     ".cmd",
		`C:\Windows\System32\cmd.exe`: ".cmd",
		`cmd`:                         ".cmd",
		`python.exe`:                  ".py",
		`python3`:                     ".py",
		`/bin/sh`:                     "",
		`/usr/bin/python3`:            ".py",
		`bash`:                        "",
	}
	for interp, want := range cases {
		require.Equal(t, want, scriptFileSuffix(interp), interp)
	}
}

func TestPrepareInterpreterArgsPowerShell(t *testing.T) {
	path := `C:\ProgramData\bklite_script\script-123.ps1`
	argv := prepareInterpreterArgs("powershell.exe", nil, path)
	require.Equal(t, []string{
		"powershell.exe",
		"-NoProfile",
		"-ExecutionPolicy", "Bypass",
		"-File",
		path,
	}, argv)

	argv = prepareInterpreterArgs("pwsh", []string{"-NoLogo"}, path)
	require.Equal(t, []string{
		"pwsh",
		"-NoProfile",
		"-ExecutionPolicy", "Bypass",
		"-NoLogo",
		"-File",
		path,
	}, argv)

	argv = prepareInterpreterArgs("powershell.exe", []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File"}, path)
	require.Equal(t, []string{
		"powershell.exe",
		"-NoProfile",
		"-ExecutionPolicy", "Bypass",
		"-File",
		path,
	}, argv)
}

func TestPrepareInterpreterArgsCmdAndUnix(t *testing.T) {
	path := `C:\ProgramData\bklite_script\script-123.cmd`
	require.Equal(t, []string{"cmd.exe", "/C", path}, prepareInterpreterArgs("cmd.exe", nil, path))
	require.Equal(t, []string{"cmd.exe", "/C", path}, prepareInterpreterArgs("cmd.exe", []string{"/C"}, path))
	require.Equal(t, []string{"/bin/sh", "-u", "/tmp/script-1"}, prepareInterpreterArgs("/bin/sh", []string{"-u"}, "/tmp/script-1"))
	require.Equal(t, []string{"python.exe", "-u", `C:\tmp\script-1.py`}, prepareInterpreterArgs("python.exe", []string{"-u"}, `C:\tmp\script-1.py`))
}

func TestIsRestrictedWindowsTemp(t *testing.T) {
	require.True(t, isRestrictedWindowsTemp(`C:\WINDOWS\SystemTemp\bklite_script`))
	require.True(t, isRestrictedWindowsTemp(`C:\Windows\Temp`))
	require.True(t, isRestrictedWindowsTemp(`C:/Windows/SystemTemp/bklite_script`))
	require.False(t, isRestrictedWindowsTemp(`C:\ProgramData\bklite_script`))
	require.False(t, isRestrictedWindowsTemp(`C:\Users\foo\AppData\Local\Temp\bklite_script`))
	require.False(t, isRestrictedWindowsTemp(`/var/folders/xx/T/bklite_script`))
}

func TestResolveRunDirAvoidsSystemTemp(t *testing.T) {
	t.Setenv("ProgramData", `C:\ProgramData`)
	got := resolveRunDir(`C:\WINDOWS\SystemTemp\bklite_script`)
	require.Equal(t, `C:\ProgramData\bklite_script`, got)

	got = resolveRunDir(`C:\Users\foo\AppData\Local\Temp\bklite_script`)
	require.Equal(t, `C:\Users\foo\AppData\Local\Temp\bklite_script`, got)

	got = resolveRunDir("")
	require.False(t, isRestrictedWindowsTemp(got))
	require.Equal(t, "bklite_script", filepath.Base(got))
}

func TestMaterializeScriptUsesSuffix(t *testing.T) {
	dir := t.TempDir()
	p := &BkliteScript{
		Interpreter: "powershell.exe",
		Script:      "Write-Output 'ok'",
		RunDir:      dir,
	}
	path, cleanup, err := p.materializeScript()
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.Equal(t, ".ps1", filepath.Ext(path))
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "Write-Output 'ok'", string(body))

	argv, cleanup2, err := p.prepareArgv()
	require.NoError(t, err)
	t.Cleanup(cleanup2)
	require.Equal(t, "powershell.exe", argv[0])
	require.Contains(t, argv, "-File")
	require.Equal(t, ".ps1", filepath.Ext(argv[len(argv)-1]))
}

func TestRemoveWindowsCarriageReturns(t *testing.T) {
	require.Equal(t, []byte("a\nb\n"), removeWindowsCarriageReturns([]byte("a\r\nb\r\n")))
}
