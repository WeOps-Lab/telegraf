//go:build !windows

package bklite_script

import (
	"os"
	"os/user"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/plugins/inputs"
	"github.com/influxdata/telegraf/testutil"
)

func newTestPlugin(t *testing.T) *BkliteScript {
	t.Helper()
	p := New()
	p.Log = testutil.Logger{Name: "bklite_script"}
	p.RunDir = t.TempDir()
	p.runTimeout = 5 * time.Second
	if os.Geteuid() == 0 {
		p.RunAs = "nobody"
	}
	return p
}

func health(t *testing.T, acc *testutil.Accumulator) map[string]interface{} {
	t.Helper()
	for _, m := range acc.Metrics {
		if m.Measurement == "bklite_script" {
			return m.Fields
		}
	}
	t.Fatal("missing bklite_script health metrics")
	return nil
}

func metricTag(t *testing.T, acc *testutil.Accumulator, measurement, key string) string {
	t.Helper()
	for _, m := range acc.Metrics {
		if m.Measurement == measurement {
			return m.Tags[key]
		}
	}
	t.Fatalf("missing measurement %q", measurement)
	return ""
}

func TestTimeoutSetsExitCode(t *testing.T) {
	p := newTestPlugin(t)
	p.Interpreter = "/bin/sh"
	p.Script = "sleep 5"
	p.runTimeout = 200 * time.Millisecond
	require.NoError(t, p.Init())

	var acc testutil.Accumulator
	require.NoError(t, p.Gather(&acc))
	require.Empty(t, acc.Errors)

	h := health(t, &acc)
	require.Equal(t, int64(0), h["up"])
	require.Equal(t, int64(exitTimeout), h["exit_code"])
}

func TestLockPreventsOverlap(t *testing.T) {
	p := newTestPlugin(t)
	p.ScriptName = "locktest"
	marker := filepath.Join(t.TempDir(), "started")
	require.NoError(t, os.Chmod(filepath.Dir(marker), 0777))
	p.Interpreter = "/bin/sh"
	p.Script = "echo started > " + marker + "\nsleep 5\n"
	p.runTimeout = 8 * time.Second
	require.NoError(t, p.Init())

	var acc1, acc2 testutil.Accumulator
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		require.NoError(t, p.Gather(&acc1))
	}()

	require.Eventually(t, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}, 3*time.Second, 20*time.Millisecond)

	require.NoError(t, p.Gather(&acc2))
	wg.Wait()

	h2 := health(t, &acc2)
	require.Equal(t, int64(0), h2["up"])
	require.Equal(t, int64(exitLockBusy), h2["exit_code"])

	h1 := health(t, &acc1)
	require.Equal(t, int64(0), h1["exit_code"])
}

func TestRootRefuseLinux(t *testing.T) {
	orig := osGeteuid
	origChown := osChown
	osGeteuid = func() int { return 0 }
	osChown = func(string, int, int) error { return nil }
	t.Cleanup(func() {
		osGeteuid = orig
		osChown = origChown
	})

	p := New()
	p.Log = testutil.Logger{}
	p.RunDir = t.TempDir()
	p.Script = "true"
	p.runTimeout = time.Second
	p.RunAs = ""
	require.ErrorContains(t, p.Init(), "refusing to run as root")

	pRoot := New()
	pRoot.Log = testutil.Logger{}
	pRoot.RunDir = t.TempDir()
	pRoot.Script = "true"
	pRoot.runTimeout = time.Second
	pRoot.RunAs = "root"
	require.ErrorContains(t, pRoot.Init(), "refusing to run as root")

	p2 := New()
	p2.Log = testutil.Logger{}
	p2.RunDir = t.TempDir()
	p2.Script = "true"
	p2.runTimeout = time.Second
	p2.RunAs = "nobody"
	require.NoError(t, p2.Init())
}

func TestInitChownsExistingRunDirForRunAsUser(t *testing.T) {
	origGeteuid := osGeteuid
	origLookup := lookupUser
	origChown := osChown
	t.Cleanup(func() {
		osGeteuid = origGeteuid
		lookupUser = origLookup
		osChown = origChown
	})

	osGeteuid = func() int { return 0 }
	lookupUser = func(name string) (*user.User, error) {
		require.Equal(t, "nobody", name)
		return &user.User{Uid: "1234", Gid: "5678", Username: "nobody"}, nil
	}

	var chownPath string
	var chownUID, chownGID int
	osChown = func(name string, uid, gid int) error {
		chownPath = name
		chownUID = uid
		chownGID = gid
		return nil
	}

	// Simulate a leftover root:root 0700 run_dir from a previous start.
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0700))

	p := New()
	p.Log = testutil.Logger{}
	p.RunDir = dir
	p.Script = "true"
	p.runTimeout = time.Second
	p.RunAs = "nobody"
	require.NoError(t, p.Init())
	require.True(t, p.hasCredential)
	require.Equal(t, uint32(1234), p.uid)
	require.Equal(t, uint32(5678), p.gid)
	require.Equal(t, dir, chownPath)
	require.Equal(t, 1234, chownUID)
	require.Equal(t, 5678, chownGID)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
}

func TestInitSkipsRunDirChownWithoutCredential(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("euid 0 with empty run_as is refused at Init")
	}

	origChown := osChown
	t.Cleanup(func() { osChown = origChown })

	called := false
	osChown = func(string, int, int) error {
		called = true
		return nil
	}

	p := New()
	p.Log = testutil.Logger{}
	p.RunDir = t.TempDir()
	p.Script = "true"
	p.runTimeout = time.Second
	require.NoError(t, p.Init())
	require.False(t, p.hasCredential)
	require.False(t, called)
}

func TestRootRefuseUIDZeroAliasAtInit(t *testing.T) {
	origLookup := lookupUser
	lookupUser = func(string) (*user.User, error) {
		return &user.User{Uid: "0", Gid: "0", Username: "toor"}, nil
	}
	t.Cleanup(func() { lookupUser = origLookup })

	p := New()
	p.Log = testutil.Logger{}
	p.RunDir = t.TempDir()
	p.Script = "true"
	p.runTimeout = time.Second
	p.RunAs = "toor"
	err := p.Init()
	require.Error(t, err)
	require.ErrorContains(t, err, "uid 0")
	require.False(t, p.hasCredential)
}

func TestHealthMetricsOnFailure(t *testing.T) {
	p := newTestPlugin(t)
	p.ScriptName = "failing"
	p.Interpreter = "/bin/sh"
	p.Script = "echo 'this is not prometheus'; exit 2"
	require.NoError(t, p.Init())

	var acc testutil.Accumulator
	require.NoError(t, p.Gather(&acc))
	require.Empty(t, acc.Errors)

	h := health(t, &acc)
	require.Equal(t, int64(0), h["up"])
	require.Equal(t, int64(2), h["exit_code"])
	require.Equal(t, int64(1), h["parse_errors"])
}

func TestSuccessfulPrometheusScript(t *testing.T) {
	p := newTestPlugin(t)
	p.ScriptName = "ok"
	p.Interpreter = "/bin/sh"
	p.Script = "echo 'demo_metric 42'"
	require.NoError(t, p.Init())

	var acc testutil.Accumulator
	require.NoError(t, p.Gather(&acc))
	require.Empty(t, acc.Errors)

	h := health(t, &acc)
	require.Equal(t, int64(1), h["up"])
	require.Equal(t, int64(0), h["exit_code"])
	require.Equal(t, int64(0), h["parse_errors"])
	require.True(t, acc.HasMeasurement("bklite_script"))
	require.True(t, acc.HasField("demo_metric", "value"))
	require.False(t, acc.HasMeasurement("prometheus"))
	require.False(t, acc.HasField("prometheus", "demo_metric"))
	require.Equal(t, "ok", metricTag(t, &acc, "demo_metric", "script"))
}

func TestScriptBodyNotInArgv(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0777))
	out := filepath.Join(dir, "cmdline")
	p := newTestPlugin(t)
	p.Interpreter = "/bin/sh"
	marker := "secret_script_body_should_not_appear_in_ps"
	p.Script = "tr '\\0' ' ' < /proc/self/cmdline > " + out + "\necho 'ok_metric 1'\n# " + marker + "\n"
	require.NoError(t, p.Init())

	var acc testutil.Accumulator
	require.NoError(t, p.Gather(&acc))
	require.Empty(t, acc.Errors)

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	require.NotContains(t, string(data), marker)
	require.Contains(t, string(data), "/bin/sh")
}

func TestScriptEnvNotPassedToChild(t *testing.T) {
	p := newTestPlugin(t)
	p.Interpreter = "/bin/sh"
	p.ScriptEnv = "BKLITE_SCRIPT_BODY"
	t.Setenv("BKLITE_SCRIPT_BODY", "echo 'fromenv_metric 1'")
	require.NoError(t, p.Init())

	var acc testutil.Accumulator
	require.NoError(t, p.Gather(&acc))
	require.Empty(t, acc.Errors)
	h := health(t, &acc)
	require.Equal(t, int64(1), h["up"])
	require.True(t, acc.HasField("fromenv_metric", "value"))
	require.False(t, acc.HasMeasurement("prometheus"))
}

func TestMaxSeriesTruncated(t *testing.T) {
	p := newTestPlugin(t)
	p.Interpreter = "/bin/sh"
	p.Script = "i=1; while [ $i -le 51 ]; do echo \"m$i 1\"; i=$((i+1)); done"
	require.NoError(t, p.Init())

	var acc testutil.Accumulator
	require.NoError(t, p.Gather(&acc))
	h := health(t, &acc)
	require.Equal(t, int64(1), h["truncated"])
}

func TestGatherNeverErrorsOnScriptFailure(t *testing.T) {
	p := newTestPlugin(t)
	p.Interpreter = "/bin/sh"
	p.Script = "exit 1"
	require.NoError(t, p.Init())

	var acc testutil.Accumulator
	require.NoError(t, acc.GatherError(p.Gather))
	h := health(t, &acc)
	require.Equal(t, int64(0), h["up"])
	require.NotEqual(t, int64(0), h["exit_code"])
}

func TestPluginRegistered(t *testing.T) {
	creator, ok := inputs.Inputs["bklite_script"]
	require.True(t, ok)
	in := creator()
	_, ok = in.(*BkliteScript)
	require.True(t, ok)
}

func TestInitRequiresWork(t *testing.T) {
	p := newTestPlugin(t)
	require.ErrorContains(t, p.Init(), "must set script or script_env")
}

func TestIntervalMinimum(t *testing.T) {
	p := New()
	p.Log = testutil.Logger{}
	p.RunDir = t.TempDir()
	p.Script = "echo 'x 1'"
	p.Interval = config.Duration(10 * time.Second)
	require.ErrorContains(t, p.Init(), "interval must be at least")
}

func TestTimeoutDerivedFromInterval(t *testing.T) {
	p := New()
	p.Log = testutil.Logger{}
	p.RunDir = t.TempDir()
	p.Script = "echo 'x 1'"
	p.Interval = config.Duration(60 * time.Second)
	if os.Geteuid() == 0 {
		p.RunAs = "nobody"
	}
	require.NoError(t, p.Init())
	require.Equal(t, 59*time.Second, p.runTimeout)
}
