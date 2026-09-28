//go:build !windows

package bklite_script

import (
	"os"
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
	if os.Geteuid() == 0 {
		p.AllowRoot = true
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

func TestTimeoutSetsExitCode(t *testing.T) {
	p := newTestPlugin(t)
	p.Command = "sleep 5"
	p.Timeout = config.Duration(200 * time.Millisecond)
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
	p.Interpreter = "/bin/sh"
	p.Script = "echo started > " + marker + "\nsleep 5\n"
	p.Timeout = config.Duration(8 * time.Second)
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
	osGeteuid = func() int { return 0 }
	t.Cleanup(func() { osGeteuid = orig })

	p := New()
	p.Log = testutil.Logger{}
	p.RunDir = t.TempDir()
	p.Command = "true"
	p.AllowRoot = false
	require.ErrorContains(t, p.Init(), "refusing to run as root")

	p2 := New()
	p2.Log = testutil.Logger{}
	p2.RunDir = t.TempDir()
	p2.Command = "true"
	p2.AllowRoot = true
	require.NoError(t, p2.Init())
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
	require.True(t, acc.HasField("prometheus", "demo_metric"))
}

func TestScriptBodyNotInArgv(t *testing.T) {
	dir := t.TempDir()
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
	require.True(t, acc.HasField("prometheus", "fromenv_metric"))
}

func TestMaxSeriesTruncated(t *testing.T) {
	p := newTestPlugin(t)
	p.MaxSeries = 3
	p.Interpreter = "/bin/sh"
	p.Script = "echo 'a 1'; echo 'b 1'; echo 'c 1'; echo 'd 1'; echo 'e 1'"
	require.NoError(t, p.Init())

	var acc testutil.Accumulator
	require.NoError(t, p.Gather(&acc))
	h := health(t, &acc)
	require.Equal(t, int64(1), h["truncated"])
}

func TestGatherNeverErrorsOnCommandFailure(t *testing.T) {
	p := newTestPlugin(t)
	p.Command = "/bin/false"
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
	require.Error(t, p.Init())
}
