package bklite_script

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/metric"
	"github.com/influxdata/telegraf/plugins/parsers/prometheus"
	"github.com/influxdata/telegraf/plugins/serializers/influx"
	"github.com/influxdata/telegraf/testutil"
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

func stdoutParser(t *testing.T, version int) *BkliteScript {
	t.Helper()
	p := New()
	p.Log = testutil.Logger{Name: "bklite_script"}
	header := make(http.Header)
	header.Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	p.parser = &prometheus.Parser{
		Header:          header,
		IgnoreTimestamp: true,
		MetricVersion:   version,
		Log:             p.Log,
	}
	return p
}

func TestParseStdoutUsesStdoutMeasurementName(t *testing.T) {
	p := stdoutParser(t, 1)
	metrics, err, trunc := p.parseStdout([]byte("host_cpu_usage_percent 42\n"))
	require.NoError(t, err)
	require.False(t, trunc)
	require.Len(t, metrics, 1)
	require.Equal(t, "host_cpu_usage_percent", metrics[0].Name())
	v, ok := metrics[0].GetField("value")
	require.True(t, ok)
	require.Equal(t, 42.0, v)

	ser := &influx.Serializer{}
	require.NoError(t, ser.Init())
	line, err := ser.Serialize(metrics[0])
	require.NoError(t, err)
	text := string(line)
	require.True(t, strings.HasPrefix(text, "host_cpu_usage_percent"))
	require.NotContains(t, text, "prometheus,")
	require.NotContains(t, text, "prometheus_host_cpu_usage_percent")
	t.Logf("influx line: %s", strings.TrimSpace(text))
}

func TestParseStdoutGaugeType(t *testing.T) {
	p := stdoutParser(t, 1)
	metrics, err, trunc := p.parseStdout([]byte("# TYPE my_free_bytes gauge\nmy_free_bytes 123\n"))
	require.NoError(t, err)
	require.False(t, trunc)
	require.Len(t, metrics, 1)
	require.Equal(t, "my_free_bytes", metrics[0].Name())
	_, ok := metrics[0].GetField("gauge")
	require.True(t, ok)
}

func TestParseStdoutKeepsLabels(t *testing.T) {
	p := stdoutParser(t, 1)
	metrics, err, trunc := p.parseStdout([]byte(`host_cpu_usage_percent{core="0"} 3.5` + "\n"))
	require.NoError(t, err)
	require.False(t, trunc)
	require.Len(t, metrics, 1)
	require.Equal(t, "host_cpu_usage_percent", metrics[0].Name())
	require.Equal(t, "0", metrics[0].Tags()["core"])
}

func TestWriteByStdoutNamePromotesPrometheusMeasurement(t *testing.T) {
	m := metric.New("prometheus", map[string]string{"job": "s"}, map[string]interface{}{
		"host_cpu_usage_percent": 7.0,
	}, time.Unix(0, 0), telegraf.Untyped)
	out := writeByStdoutName([]telegraf.Metric{m})
	require.Len(t, out, 1)
	require.Equal(t, "host_cpu_usage_percent", out[0].Name())
	v, ok := out[0].GetField("value")
	require.True(t, ok)
	require.Equal(t, 7.0, v)
	require.Equal(t, "s", out[0].Tags()["job"])
}

func TestWriteByStdoutNameLeavesHealthMeasurement(t *testing.T) {
	m := metric.New("bklite_script", map[string]string{"script": "default"}, map[string]interface{}{
		"up": int64(1),
	}, time.Unix(0, 0), telegraf.Gauge)
	out := writeByStdoutName([]telegraf.Metric{m})
	require.Len(t, out, 1)
	require.Equal(t, "bklite_script", out[0].Name())
	_, ok := out[0].GetField("up")
	require.True(t, ok)
}

func TestParseStdoutV2StillUsesStdoutName(t *testing.T) {
	p := stdoutParser(t, 2)
	metrics, err, trunc := p.parseStdout([]byte("host_cpu_usage_percent 42\n"))
	require.NoError(t, err)
	require.False(t, trunc)
	require.Len(t, metrics, 1)
	require.Equal(t, "host_cpu_usage_percent", metrics[0].Name())
	require.NotEqual(t, "prometheus", metrics[0].Name())
}
