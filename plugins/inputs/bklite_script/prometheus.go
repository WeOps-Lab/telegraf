package bklite_script

import (
	"bytes"
	"fmt"
	"unicode"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/metric"
)

func ensurePrometheusTypes(data []byte) []byte {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return data
	}

	lines := bytes.Split(data, []byte("\n"))
	declared := make(map[string]struct{})
	for _, line := range lines {
		trim := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trim, []byte("# TYPE ")) {
			continue
		}
		fields := bytes.Fields(trim)
		if len(fields) >= 3 {
			declared[string(fields[2])] = struct{}{}
		}
	}

	var out bytes.Buffer
	injected := make(map[string]struct{})
	for i, line := range lines {
		trim := bytes.TrimSpace(line)
		if len(trim) == 0 {
			continue
		}
		if trim[0] != '#' {
			name := prometheusMetricName(trim)
			if name != "" {
				if _, ok := declared[name]; !ok {
					if _, done := injected[name]; !done {
						fmt.Fprintf(&out, "# TYPE %s untyped\n", name)
						injected[name] = struct{}{}
						declared[name] = struct{}{}
					}
				}
			}
		}
		out.Write(trim)
		if i != len(lines)-1 || len(trim) > 0 {
			out.WriteByte('\n')
		}
	}
	return out.Bytes()
}

// writeByStdoutName promotes leftover v2 "prometheus" measurements so each
// sample is stored under its stdout metric name (field key), not prometheus_*.
// Metric version 1 already uses the stdout name; this is a no-op for those.
func writeByStdoutName(metrics []telegraf.Metric) []telegraf.Metric {
	if len(metrics) == 0 {
		return metrics
	}
	out := make([]telegraf.Metric, 0, len(metrics))
	for _, m := range metrics {
		if m.Name() != "prometheus" {
			out = append(out, m)
			continue
		}
		tags := m.Tags()
		t := m.Time()
		vtype := m.Type()
		for _, f := range m.FieldList() {
			if f.Key == "" {
				continue
			}
			out = append(out, metric.New(f.Key, tags, map[string]interface{}{"value": f.Value}, t, vtype))
		}
	}
	return out
}

func prometheusMetricName(line []byte) string {
	i := 0
	for i < len(line) {
		r := rune(line[i])
		if i == 0 {
			if r != ':' && r != '_' && !unicode.IsLetter(r) {
				return ""
			}
		} else if r == '{' || unicode.IsSpace(r) {
			break
		} else if r != ':' && r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return ""
		}
		i++
	}
	if i == 0 {
		return ""
	}
	return string(line[:i])
}
