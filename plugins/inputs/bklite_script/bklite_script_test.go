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

// runDirReachableByDroppedUser returns a RunDir whose ancestors are
// traversable by a non-root child. Do not use t.TempDir for this: Go
// 1.23 nests /tmp/TestNameXXX/001, both created 0700. Chmod of the 001
// leaf (tests-v2) leaves TestNameXXX at 0700, so a dropped user still
// cannot traverse (ops 17:57, tests-v2-1743, e.g.
// /tmp/TestRootDropsToExplicitNonRootUser…/001/run/script-…).
// Production run_dir is /tmp/bklite_script-*, a child of world-writable
// /tmp, so this is a fixture constraint, not a plugin chown or
// materialize defect. Do not chmod the shared temp root, do not chmod
// production ancestors, and do not loosen run_dir 0700 / script 0600
// to make the fixture pass.
func runDirReachableByDroppedUser(t *testing.T) string {
	t.Helper()
	tempRoot := traversableTempRoot(t)
	// Directory is created directly under the system temp root, then
	// chmod 0755. Only this test-owned directory is changed; cleanup
	// removes it. The plugin Init still MkdirAll/chown the "run" leaf.
	parent, err := os.MkdirTemp(tempRoot, "bklite-script-testrun-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	require.NoError(t, os.Chmod(parent, 0o755))
	requireDroppedUserCanTraverse(t, parent)
	return filepath.Join(parent, "run")
}

// dirReachableByDroppedUser returns a test-owned directory directly under
// a world-traversable temp root, mode 0777, so a dropped child can create
// files in it. chmod of a t.TempDir leaf is not enough: Go 1.23 leaves
// the TestName ancestor at 0700. Never chmod the shared temp root.
func dirReachableByDroppedUser(t *testing.T) string {
	t.Helper()
	tempRoot := traversableTempRoot(t)
	dir, err := os.MkdirTemp(tempRoot, "bklite-script-testio-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	require.NoError(t, os.Chmod(dir, 0o777))
	requireDroppedUserCanTraverse(t, dir)
	return dir
}

func traversableTempRoot(t *testing.T) string {
	t.Helper()
	candidates := []string{os.TempDir()}
	if os.TempDir() != "/tmp" {
		candidates = append(candidates, "/tmp")
	}
	for _, root := range candidates {
		if otherExecutable(t, root) {
			return root
		}
	}
	t.Fatalf("no world-traversable temp root among %v; refuse to chmod a shared temp directory; set TMPDIR to a world-traversable dir such as /tmp", candidates)
	return ""
}

func otherExecutable(t *testing.T, path string) bool {
	t.Helper()
	st, err := os.Stat(path)
	require.NoError(t, err)
	return st.Mode()&0o001 != 0
}

// requireDroppedUserCanTraverse fails the fixture immediately if any
// ancestor between dir and a world-traversable directory is still not
// other-executable. It never chmods those ancestors.
func requireDroppedUserCanTraverse(t *testing.T, dir string) {
	t.Helper()
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no world-traversable ancestor above %s", dir)
		}
		if otherExecutable(t, parent) {
			return
		}
		t.Fatalf("ancestor %s of fixture %s is not other-executable (mode %s); a dropped user cannot traverse; do not chmod this path from the test", parent, dir, mustMode(t, parent))
	}
}

func mustMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	require.NoError(t, err)
	return st.Mode()
}

func TestRunDirFixtureAncestorsAreWorldTraversable(t *testing.T) {
	runDir := runDirReachableByDroppedUser(t)
	parent := filepath.Dir(runDir)
	st, err := os.Stat(parent)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), st.Mode()&os.ModePerm)
	grand := filepath.Clean(filepath.Dir(parent))
	require.True(t, grand == filepath.Clean(os.TempDir()) || grand == "/tmp", "fixture parent %s is not directly under a temp root (grand=%s)", parent, grand)
	requireDroppedUserCanTraverse(t, parent)
	// Contrast with t.TempDir: the nested TestName/001 layout is what
	// tests-v2 left non-traversable at the TestName ancestor. This
	// assertion documents why this helper must not be switched back.
	nested := t.TempDir()
	if !otherExecutable(t, filepath.Dir(nested)) {
		t.Logf("confirmed t.TempDir ancestor %s is not other-executable (mode %s); using it as RunDir parent would reproduce tests-v2-1743", filepath.Dir(nested), mustMode(t, filepath.Dir(nested)))
	}
}

func newTestPlugin(t *testing.T) *BkliteScript {
	t.Helper()
	p := New()
	p.Log = testutil.Logger{Name: "bklite_script"}
	p.runTimeout = 5 * time.Second
	if os.Geteuid() == 0 {
		// Exercise the explicit non-root drop path when the suite runs as root.
		p.RunAs = "nobody"
		p.RunDir = runDirReachableByDroppedUser(t)
	} else {
		p.RunDir = t.TempDir()
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
	marker := filepath.Join(dirReachableByDroppedUser(t), "started")
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

func TestLinuxRunAsPolicyAtInit(t *testing.T) {
	orig := osGeteuid
	origChown := osChown
	osGeteuid = func() int { return 0 }
	osChown = func(string, int, int) error { return nil }
	t.Cleanup(func() {
		osGeteuid = orig
		osChown = origChown
	})

	// Empty or blank run_as inherits the process user, including root.
	for _, raw := range []string{"", "  ", "\t"} {
		p := New()
		p.Log = testutil.Logger{}
		p.RunDir = t.TempDir()
		p.Script = "true"
		p.runTimeout = time.Second
		p.RunAs = raw
		require.NoError(t, p.Init(), "run_as %q", raw)
		require.False(t, p.hasCredential, "run_as %q", raw)
		require.Equal(t, "", p.RunAs, "run_as %q should normalize to empty", raw)
	}

	pRoot := New()
	pRoot.Log = testutil.Logger{}
	pRoot.RunDir = t.TempDir()
	pRoot.Script = "true"
	pRoot.runTimeout = time.Second
	pRoot.RunAs = "root"
	require.ErrorContains(t, pRoot.Init(), "refusing to run as root")
	require.False(t, pRoot.hasCredential)

	p2 := New()
	p2.Log = testutil.Logger{}
	p2.RunDir = t.TempDir()
	p2.Script = "true"
	p2.runTimeout = time.Second
	p2.RunAs = "nobody"
	require.NoError(t, p2.Init())
	require.True(t, p2.hasCredential)
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

func TestInitLookupFailureDoesNotSetCredentials(t *testing.T) {
	origGeteuid := osGeteuid
	origLookup := lookupUser
	osGeteuid = func() int { return 0 }
	lookupUser = func(name string) (*user.User, error) {
		require.Equal(t, "missing-run-as-user", name)
		return nil, user.UnknownUserError(name)
	}
	t.Cleanup(func() {
		osGeteuid = origGeteuid
		lookupUser = origLookup
	})

	p := New()
	p.Log = testutil.Logger{}
	p.RunDir = t.TempDir()
	p.Script = "true"
	p.runTimeout = time.Second
	p.RunAs = "missing-run-as-user"
	err := p.Init()
	require.Error(t, err)
	require.ErrorContains(t, err, "looking up user")
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
	dir := dirReachableByDroppedUser(t)
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
