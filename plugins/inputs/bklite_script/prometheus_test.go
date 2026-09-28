package bklite_script

import (
	"strings"
	"testing"

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
	require.Error(t, checkRootPermission(0, "", false))
	require.Error(t, checkRootPermission(0, "root", false))
	require.Error(t, checkRootPermission(1000, "root", false))
	require.NoError(t, checkRootPermission(0, "nobody", false))
	require.NoError(t, checkRootPermission(0, "", true))
	require.NoError(t, checkRootPermission(0, "root", true))
	require.NoError(t, checkRootPermission(1000, "", false))
}

func TestSanitizeName(t *testing.T) {
	require.Equal(t, "disk_check", sanitizeName("disk_check"))
	require.Equal(t, "a_b", sanitizeName("a b"))
	require.Equal(t, "default", sanitizeName(""))
}
