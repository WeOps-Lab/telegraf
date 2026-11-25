//go:build linux

package bklite_script

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/influxdata/telegraf/testutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestWrapLinuxChildLimitsArgvBoundary(t *testing.T) {
	secret := "secret_script_body_should_not_appear_in_ps"
	evilPath := "/tmp/limit-wrap-test/foo;touch evilstuff;#"
	argv := []string{"/bin/sh", evilPath, "--not-a-prlimit-flag"}
	got := wrapLinuxChildLimits(argv, 59*time.Second, true)

	require.Equal(t, linuxPrlimitPath, got[0])
	require.Equal(t, "--", got[4])
	require.Equal(t, argv, got[5:])
	require.Equal(t, []string{
		"--as=268435456",
		"--cpu=59",
		"--nproc=256",
	}, got[1:4])

	joinedFlags := strings.Join(got[:5], "\x00")
	require.NotContains(t, joinedFlags, secret)
	require.NotContains(t, joinedFlags, evilPath)
	require.NotContains(t, joinedFlags, "evilstuff")
	// User strings stay after "--" as intact argv elements.
	require.Equal(t, evilPath, got[6])
	require.NotContains(t, strings.Join(linuxLimitFlagValues(59*time.Second, true), " "), ";")
}

func TestWrapLinuxChildLimitsOmitsNprocForSameUID(t *testing.T) {
	got := wrapLinuxChildLimits([]string{"/bin/sh", "script"}, 59*time.Second, false)
	require.Equal(t, []string{
		linuxPrlimitPath,
		"--as=268435456",
		"--cpu=59",
		"--",
		"/bin/sh",
		"script",
	}, got)
}

func TestSanitizeLinuxLimitEnvStripsLoaderOverrides(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"LD_PRELOAD=/tmp/evil.so",
		"LD_LIBRARY_PATH=/tmp",
		"HOME=/tmp",
		"LD_AUDIT=/tmp/audit.so",
	}
	got := sanitizeLinuxLimitEnv(in)
	require.Equal(t, []string{"PATH=/usr/bin", "HOME=/tmp"}, got)
}

func TestNoEnvBypassOfLimitWrapper(t *testing.T) {
	t.Setenv("BKLITE_SKIP_LIMITS", "1")
	t.Setenv("BKLITE_PRLIMIT", "/bin/true")
	got := wrapLinuxChildLimits([]string{"/bin/sh", "script"}, 59*time.Second, false)
	require.Equal(t, linuxPrlimitPath, got[0])
	require.Contains(t, got, "--as=268435456")
}

func readRlimit(t *testing.T, resource int) unix.Rlimit {
	t.Helper()
	var lim unix.Rlimit
	require.NoError(t, unix.Getrlimit(resource, &lim))
	return lim
}

func limitsProbeScript() string {
	// Builtins only: dash forks for awk/id/grep, and a same-UID parent may
	// still apply the historical NPROC fallback after Start.
	return `while IFS= read -r line; do
  case "$line" in
    Uid:*) set -- $line; echo limit_uid $2 ;;
  esac
done < /proc/self/status
while IFS= read -r line; do
  case "$line" in
    "Max address space"*) set -- $line; echo limit_as $4 ;;
    "Max cpu time"*) set -- $line; echo limit_cpu $4 ;;
    "Max processes"*) set -- $line; echo limit_nproc $3 ;;
  esac
done < /proc/self/limits
`
}

func parseLimitField(t *testing.T, acc *testutil.Accumulator, name string) string {
	t.Helper()
	for _, m := range acc.Metrics {
		if m.Measurement == name {
			switch v := m.Fields["value"].(type) {
			case float64:
				if v == -1 {
					return "unlimited"
				}
				return strconv.FormatInt(int64(v), 10)
			case int64:
				return strconv.FormatInt(v, 10)
			case string:
				return v
			}
		}
	}
	// influx-style "name value" is parsed as measurement + field value
	t.Fatalf("missing metric %q, metrics=%v", name, acc.Metrics)
	return ""
}

func TestLinuxSameUIDChildGetsPromisedASAndCPU(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("same-UID inherit path is covered by the non-root test; root suite uses explicit drop tests")
	}

	beforeAS := readRlimit(t, unix.RLIMIT_AS)
	beforeCPU := readRlimit(t, unix.RLIMIT_CPU)
	beforeNPROC := readRlimit(t, unix.RLIMIT_NPROC)

	p := New()
	p.Log = testutil.Logger{Name: "bklite_script"}
	p.RunDir = t.TempDir()
	p.Interval = 0
	p.runTimeout = 59 * time.Second
	p.Interpreter = "/bin/sh"
	p.Script = limitsProbeScript()
	p.RunAs = ""
	require.NoError(t, p.Init())
	require.False(t, p.hasCredential)

	var acc testutil.Accumulator
	require.NoError(t, p.Gather(&acc))
	require.Empty(t, acc.Errors)
	h := health(t, &acc)
	require.Equal(t, int64(1), h["up"], "health=%v metrics=%v", h, acc.Metrics)
	require.Equal(t, int64(0), h["exit_code"])
	require.Equal(t, strconv.Itoa(os.Geteuid()), parseLimitField(t, &acc, "limit_uid"))
	require.Equal(t, "268435456", parseLimitField(t, &acc, "limit_as"))
	require.Equal(t, "59", parseLimitField(t, &acc, "limit_cpu"))
	// Same-UID path applies historical NPROC fallback in applyResourceLimits
	// when cgroup is unavailable (this box: pids controller not writable).
	require.Equal(t, "256", parseLimitField(t, &acc, "limit_nproc"))

	require.Equal(t, beforeAS, readRlimit(t, unix.RLIMIT_AS), "parent RLIMIT_AS changed")
	require.Equal(t, beforeCPU, readRlimit(t, unix.RLIMIT_CPU), "parent RLIMIT_CPU changed")
	require.Equal(t, beforeNPROC, readRlimit(t, unix.RLIMIT_NPROC), "parent RLIMIT_NPROC changed")
}

func TestLinuxSameUIDLimitsHoldAcrossRepeatedGathers(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("repeated same-UID gathers are for the non-root suite")
	}
	p := New()
	p.Log = testutil.Logger{Name: "bklite_script"}
	p.RunDir = t.TempDir()
	p.runTimeout = 59 * time.Second
	p.Interpreter = "/bin/sh"
	p.Script = limitsProbeScript()
	p.RunAs = ""
	require.NoError(t, p.Init())

	for i := 0; i < 5; i++ {
		var acc testutil.Accumulator
		require.NoError(t, p.Gather(&acc), "round %d", i+1)
		require.Empty(t, acc.Errors, "round %d", i+1)
		h := health(t, &acc)
		require.Equal(t, int64(1), h["up"], "round %d health=%v", i+1, h)
		require.Equal(t, "268435456", parseLimitField(t, &acc, "limit_as"))
		require.Equal(t, "59", parseLimitField(t, &acc, "limit_cpu"))
	}
}

func TestLinuxMissingPrlimitDoesNotRunScriptAndLeavesPluginUp(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	orig := linuxPrlimitPath
	linuxPrlimitPath = filepath.Join(dir, "no-such-prlimit")
	t.Cleanup(func() { linuxPrlimitPath = orig })

	p := New()
	p.Log = testutil.Logger{Name: "bklite_script"}
	p.RunDir = dir
	p.runTimeout = 59 * time.Second
	p.Interpreter = "/bin/sh"
	p.Script = "echo ran > " + marker + "\necho limit_ok 1\n"
	p.RunAs = ""
	require.NoError(t, p.Init())

	var acc testutil.Accumulator
	require.NoError(t, p.Gather(&acc), "Gather must not fail the agent")
	h := health(t, &acc)
	require.Equal(t, int64(0), h["up"])
	require.Equal(t, int64(exitNoStart), h["exit_code"])
	_, err := os.Stat(marker)
	require.True(t, os.IsNotExist(err), "script must not have executed")

	// Plugin remains usable once the wrapper path is restored.
	linuxPrlimitPath = orig
	p.Script = limitsProbeScript()
	require.NoError(t, p.Init())
	var acc2 testutil.Accumulator
	require.NoError(t, p.Gather(&acc2))
	require.Empty(t, acc2.Errors)
	h2 := health(t, &acc2)
	require.Equal(t, int64(1), h2["up"], "health=%v", h2)
	require.Equal(t, "268435456", parseLimitField(t, &acc2, "limit_as"))
}

func TestLinuxTimeoutStillKillsProcessGroup(t *testing.T) {
	token := fmt.Sprintf("bklite-limit-timeout-%d", os.Getpid())
	p := New()
	p.Log = testutil.Logger{Name: "bklite_script"}
	p.RunDir = t.TempDir()
	p.runTimeout = 200 * time.Millisecond
	p.Interpreter = "/bin/sh"
	// Unique token lives in the script file. Use a builtin loop so the
	// test does not need to fork sleep(1) after RLIMIT_NPROC=256.
	p.Script = "while :; do :; done # " + token + "\n"
	p.RunAs = ""
	require.NoError(t, p.Init())

	var acc testutil.Accumulator
	require.NoError(t, p.Gather(&acc))
	h := health(t, &acc)
	require.Equal(t, int64(0), h["up"])
	require.Equal(t, int64(exitTimeout), h["exit_code"])

	time.Sleep(50 * time.Millisecond)
	out, err := exec.Command("ps", "-eo", "pid,args").CombinedOutput()
	require.NoError(t, err)
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, token) && !strings.Contains(line, "ps -eo") {
			t.Fatalf("leftover process after timeout: %s", line)
		}
	}
}

func TestLinuxChildArgvOmitsScriptBody(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "cmdline")
	p := New()
	p.Log = testutil.Logger{Name: "bklite_script"}
	p.RunDir = dir
	p.runTimeout = 59 * time.Second
	p.Interpreter = "/bin/sh"
	marker := "secret_script_body_should_not_appear_in_ps"
	p.Script = "{ exec 3</proc/self/cmdline; IFS= read -r -d '' cmd <&3 || true; echo \"$cmd\" > " + out + "; } 2>/dev/null\necho 'ok_metric 1'\n# " + marker + "\n"
	p.RunAs = ""
	require.NoError(t, p.Init())

	var acc testutil.Accumulator
	require.NoError(t, p.Gather(&acc))
	require.Empty(t, acc.Errors)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	require.NotContains(t, string(data), marker)
	require.NotContains(t, string(data), "secret_script_body")
}

func droppedUIDForRootTest(t *testing.T) (string, uint32, uint32) {
	t.Helper()
	name := "nobody"
	if u, err := user.LookupId("450"); err == nil {
		name = u.Username
		uid, _ := strconv.ParseUint(u.Uid, 10, 32)
		gid, _ := strconv.ParseUint(u.Gid, 10, 32)
		return name, uint32(uid), uint32(gid)
	}
	u, err := user.Lookup("nobody")
	require.NoError(t, err)
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	require.NoError(t, err)
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	require.NoError(t, err)
	require.NotEqual(t, uint32(0), uint32(uid))
	return name, uint32(uid), uint32(gid)
}

func isolatedDropRunDir(t *testing.T) string {
	t.Helper()
	parent, err := os.MkdirTemp("/tmp", "bklite-script-limitrun-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	require.NoError(t, os.Chmod(parent, 0o755))
	return filepath.Join(parent, "run")
}

func TestLinuxExplicitDropAppliesASCPUAndNproc(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires an isolated default-cap root process; do not sudo or add caps; do not run on the shared collector")
	}
	name, wantUID, wantGID := droppedUIDForRootTest(t)
	if wantUID != 450 {
		t.Logf("uid 450 not present; using %s uid=%d (ops should rerun with telegraf uid 450 when available)", name, wantUID)
	}

	beforeAS := readRlimit(t, unix.RLIMIT_AS)
	beforeCPU := readRlimit(t, unix.RLIMIT_CPU)
	beforeNPROC := readRlimit(t, unix.RLIMIT_NPROC)

	p := New()
	p.Log = testutil.Logger{Name: "bklite_script"}
	p.RunDir = isolatedDropRunDir(t)
	p.runTimeout = 59 * time.Second
	p.Interpreter = "/bin/sh"
	p.Script = limitsProbeScript()
	p.RunAs = name
	require.NoError(t, p.Init())
	require.True(t, p.hasCredential)
	require.Equal(t, wantUID, p.uid)
	require.Equal(t, wantGID, p.gid)

	var acc testutil.Accumulator
	require.NoError(t, p.Gather(&acc))
	require.Empty(t, acc.Errors)
	h := health(t, &acc)
	require.Equal(t, int64(1), h["up"], "health=%v metrics=%v", h, acc.Metrics)
	require.Equal(t, int64(0), h["exit_code"])
	require.Equal(t, strconv.FormatUint(uint64(wantUID), 10), parseLimitField(t, &acc, "limit_uid"))
	require.Equal(t, "268435456", parseLimitField(t, &acc, "limit_as"))
	require.Equal(t, "59", parseLimitField(t, &acc, "limit_cpu"))
	require.Equal(t, "256", parseLimitField(t, &acc, "limit_nproc"))

	require.Equal(t, beforeAS, readRlimit(t, unix.RLIMIT_AS), "parent RLIMIT_AS changed")
	require.Equal(t, beforeCPU, readRlimit(t, unix.RLIMIT_CPU), "parent RLIMIT_CPU changed")
	require.Equal(t, beforeNPROC, readRlimit(t, unix.RLIMIT_NPROC), "parent RLIMIT_NPROC changed")
}

func TestLinuxEmptyRootAppliesASAndCPU(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires an isolated default-cap root process; do not sudo or add caps; do not run on the shared collector")
	}
	p := New()
	p.Log = testutil.Logger{Name: "bklite_script"}
	p.RunDir = t.TempDir()
	p.runTimeout = 59 * time.Second
	p.Interpreter = "/bin/sh"
	p.Script = limitsProbeScript()
	p.RunAs = ""
	require.NoError(t, p.Init())
	require.False(t, p.hasCredential)

	var acc testutil.Accumulator
	require.NoError(t, p.Gather(&acc))
	require.Empty(t, acc.Errors)
	h := health(t, &acc)
	require.Equal(t, int64(1), h["up"], "health=%v metrics=%v", h, acc.Metrics)
	require.Equal(t, "0", parseLimitField(t, &acc, "limit_uid"))
	require.Equal(t, "268435456", parseLimitField(t, &acc, "limit_as"))
	require.Equal(t, "59", parseLimitField(t, &acc, "limit_cpu"))
}
