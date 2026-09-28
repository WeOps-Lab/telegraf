package bklite_script

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEnsurePrometheusTypes(t *testing.T) {
	in := []byte("foo 1\nbar{le=\"1\"} 2\n")
	out := ensurePrometheusTypes(in)
	text := string(out)
	require.Contains(t, text, "# TYPE foo untyped")
	require.Contains(t, text, "# TYPE bar untyped")
	require.Contains(t, text, "foo 1")
	require.Equal(t, 1, strings.Count(text, "# TYPE foo"))
}

func TestEnsurePrometheusTypesKeepsExisting(t *testing.T) {
	in := []byte("# TYPE foo counter\nfoo 1\n")
	out := string(ensurePrometheusTypes(in))
	require.Contains(t, out, "# TYPE foo counter")
	require.NotContains(t, out, "# TYPE foo untyped")
}

func TestPrometheusMetricName(t *testing.T) {
	require.Equal(t, "cpu_usage", prometheusMetricName([]byte("cpu_usage 1")))
	require.Equal(t, "cpu_usage", prometheusMetricName([]byte(`cpu_usage{host="a"} 1`)))
	require.Equal(t, "", prometheusMetricName([]byte("123bad 1")))
}

func TestCheckRootPermission(t *testing.T) {
	require.Error(t, checkRootPermission(0, ""))
	require.Error(t, checkRootPermission(0, "root"))
	require.Error(t, checkRootPermission(1000, "root"))
	require.NoError(t, checkRootPermission(0, "nobody"))
	require.NoError(t, checkRootPermission(1000, ""))
	require.NoError(t, checkRootPermission(1000, "telegraf"))
	require.ErrorContains(t, refuseIfRootUID(0, "toor"), "uid 0")
	require.NoError(t, refuseIfRootUID(65534, "nobody"))
}

func TestDerivedTimeout(t *testing.T) {
	require.Equal(t, 59*time.Second, derivedTimeout(60*time.Second))
	require.Equal(t, 119*time.Second, derivedTimeout(120*time.Second))
	require.Error(t, validateInterval(10*time.Second))
	require.NoError(t, validateInterval(60*time.Second))
}

func TestSanitizeName(t *testing.T) {
	require.Equal(t, "disk_check", sanitizeName("disk_check"))
	require.Equal(t, "a_b", sanitizeName("a b"))
	require.Equal(t, "default", sanitizeName(""))
}
