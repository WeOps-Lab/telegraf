//go:build unix

package bklite_script

import (
	"os"
	"os/user"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/influxdata/telegraf/testutil"
)

func childIdentityScript() string {
	// Read /proc/self/status with shell builtins only. Command substitution
	// of `id` would fork, and this box's plugin cgroup/nproc limits made
	// those forks fail with "Cannot fork" during earlier runs.
	return "uid=\ngid=\n" +
		"while read -r key val _; do\n" +
		"  case \"$key\" in\n" +
		"    Uid:) uid=$val ;;\n" +
		"    Gid:) gid=$val ;;\n" +
		"  esac\n" +
		"done < /proc/self/status\n" +
		"echo \"bklite_child_uid $uid\"\n" +
		"echo \"bklite_child_gid $gid\"\n"
}

func metricValue(t *testing.T, acc *testutil.Accumulator, measurement string) int64 {
	t.Helper()
	for _, m := range acc.Metrics {
		if m.Measurement != measurement {
			continue
		}
		raw, ok := m.Fields["value"]
		require.Truef(t, ok, "missing value field on %s: %#v", measurement, m.Fields)
		switch v := raw.(type) {
		case int64:
			return v
		case float64:
			return int64(v)
		default:
			t.Fatalf("unexpected %s value type %T (%v)", measurement, raw, raw)
		}
	}
	t.Fatalf("missing measurement %q", measurement)
	return 0
}

func TestEmptyRunAsInheritsNonRootProcessUser(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("this case proves a non-root process does not escalate; root inherit is covered by TestEmptyRunAsOnRootProcessRunsAsRoot")
	}

	p := New()
	p.Log = testutil.Logger{Name: "bklite_script"}
	p.RunDir = t.TempDir()
	p.runTimeout = 5 * time.Second
	p.Interpreter = "/bin/sh"
	p.Script = childIdentityScript()
	p.RunAs = ""
	require.NoError(t, p.Init())
	require.False(t, p.hasCredential)

	var acc testutil.Accumulator
	require.NoError(t, p.Gather(&acc))
	require.Empty(t, acc.Errors)
	h := health(t, &acc)
	require.Equal(t, int64(1), h["up"], "health=%v errors=%v", h, acc.Errors)
	require.Equal(t, int64(0), h["exit_code"])
	uid := metricValue(t, &acc, "bklite_child_uid")
	gid := metricValue(t, &acc, "bklite_child_gid")
	require.Equal(t, int64(os.Geteuid()), uid)
	require.Equal(t, int64(os.Getegid()), gid)
	require.NotEqual(t, int64(0), uid, "non-root process must not start a uid 0 child")
}

func TestEmptyRunAsOnRootProcessRunsAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires an isolated root process to prove empty run_as inherits uid 0; do not run on the shared collector")
	}

	p := New()
	p.Log = testutil.Logger{Name: "bklite_script"}
	p.RunDir = t.TempDir()
	p.runTimeout = 5 * time.Second
	p.Interpreter = "/bin/sh"
	p.Script = childIdentityScript()
	p.RunAs = ""
	require.NoError(t, p.Init())
	require.False(t, p.hasCredential)

	var acc testutil.Accumulator
	require.NoError(t, p.Gather(&acc))
	require.Empty(t, acc.Errors)
	h := health(t, &acc)
	require.Equal(t, int64(1), h["up"], "health=%v", h)
	require.Equal(t, int64(0), metricValue(t, &acc, "bklite_child_uid"))
}

func TestRootDropsToExplicitNonRootUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires an isolated root process to prove setuid/setgid drop; do not run on the shared collector")
	}

	nobody, err := user.Lookup("nobody")
	require.NoError(t, err)
	wantUID, err := strconv.ParseInt(nobody.Uid, 10, 64)
	require.NoError(t, err)
	wantGID, err := strconv.ParseInt(nobody.Gid, 10, 64)
	require.NoError(t, err)
	require.NotEqual(t, int64(0), wantUID)

	p := New()
	p.Log = testutil.Logger{Name: "bklite_script"}
	p.RunDir = runDirReachableByDroppedUser(t)
	p.runTimeout = 5 * time.Second
	p.Interpreter = "/bin/sh"
	p.Script = childIdentityScript()
	p.RunAs = "nobody"
	require.NoError(t, p.Init())
	require.True(t, p.hasCredential)
	require.Equal(t, uint32(wantUID), p.uid)
	require.Equal(t, uint32(wantGID), p.gid)

	var acc testutil.Accumulator
	require.NoError(t, p.Gather(&acc))
	require.Empty(t, acc.Errors)
	h := health(t, &acc)
	require.Equal(t, int64(1), h["up"], "health=%v", h)
	require.Equal(t, wantUID, metricValue(t, &acc, "bklite_child_uid"))
	require.Equal(t, wantGID, metricValue(t, &acc, "bklite_child_gid"))
}

func TestEmptyRunAsScriptFailureStillReportsHealth(t *testing.T) {
	p := New()
	p.Log = testutil.Logger{Name: "bklite_script"}
	p.RunDir = t.TempDir()
	p.runTimeout = 5 * time.Second
	p.Interpreter = "/bin/sh"
	p.Script = "exit 2"
	p.RunAs = ""
	require.NoError(t, p.Init())
	require.False(t, p.hasCredential)

	var acc testutil.Accumulator
	require.NoError(t, p.Gather(&acc))
	require.Empty(t, acc.Errors)
	h := health(t, &acc)
	require.Equal(t, int64(0), h["up"])
	require.Equal(t, int64(2), h["exit_code"])
}
