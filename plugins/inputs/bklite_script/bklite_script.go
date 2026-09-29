//go:generate ../../../tools/readme_config_includer/generator
package bklite_script

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/plugins/inputs"
	"github.com/influxdata/telegraf/plugins/parsers/prometheus"
)

//go:embed sample.conf
var sampleConfig string

// BkliteScript runs a user script and collects Prometheus metrics from stdout.
type BkliteScript struct {
	Interval    config.Duration `toml:"interval"`
	ScriptName  string          `toml:"script_name"`
	Interpreter string          `toml:"interpreter"`
	Params      []string        `toml:"params"`
	Script      string          `toml:"script"`
	ScriptEnv   string          `toml:"script_env"`
	Environment []string        `toml:"environment"`
	RunDir      string          `toml:"run_dir"`
	RunAs       string          `toml:"run_as"`
	Log         telegraf.Logger `toml:"-"`

	// runTimeout is interval-1s after Init. Tests may set it before Init to
	// avoid waiting a full minute.
	runTimeout    time.Duration
	lockPath      string
	hasCredential bool
	uid           uint32
	gid           uint32
	parser        *prometheus.Parser
}

func New() *BkliteScript {
	return &BkliteScript{}
}

func (*BkliteScript) SampleConfig() string {
	return sampleConfig
}

func (b *BkliteScript) Init() error {
	if b.runTimeout == 0 {
		if err := validateInterval(time.Duration(b.Interval)); err != nil {
			return err
		}
		b.runTimeout = derivedTimeout(time.Duration(b.Interval))
	}

	if !b.usesScriptBody() {
		return errors.New("must set script or script_env")
	}

	if b.Interpreter == "" {
		interp, err := defaultInterpreter()
		if err != nil {
			return err
		}
		b.Interpreter = interp
	}

	if b.RunDir == "" {
		b.RunDir = filepath.Join(os.TempDir(), "bklite_script")
	}

	// Resolve run_as uid/gid before creating or chowning run_dir so a
	// root-owned 0700 directory from a previous start is given to the
	// dropped-privilege user (CreateTemp and the child both need access).
	if err := b.initUser(); err != nil {
		return err
	}
	if err := os.MkdirAll(b.RunDir, 0700); err != nil {
		return fmt.Errorf("creating run_dir %q: %w", b.RunDir, err)
	}
	if err := b.chownRunDir(); err != nil {
		return err
	}
	b.lockPath = filepath.Join(b.RunDir, b.instanceID()+".lock")

	header := make(http.Header)
	header.Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	// Metric version 1 writes each Prometheus sample under its stdout name
	// (e.g. host_cpu_usage_percent) instead of the v2 "prometheus" family
	// that serializes as prometheus_<stdout>. Health metrics stay on
	// measurement bklite_script and do not depend on name_prefix.
	b.parser = &prometheus.Parser{
		Header:          header,
		IgnoreTimestamp: true,
		MetricVersion:   1,
		Log:             b.Log,
	}
	return nil
}

func (b *BkliteScript) usesScriptBody() bool {
	return b.Script != "" || b.ScriptEnv != ""
}

func (b *BkliteScript) instanceID() string {
	if b.ScriptName != "" {
		return sanitizeName(b.ScriptName)
	}
	h := sha256.Sum256([]byte(strings.Join([]string{
		b.Script, b.ScriptEnv, b.Interpreter, strings.Join(b.Params, "\x00"),
	}, "\x00")))
	return hex.EncodeToString(h[:8])
}

func (b *BkliteScript) scriptTag() string {
	if b.ScriptName != "" {
		return b.ScriptName
	}
	return "default"
}

func (b *BkliteScript) Gather(acc telegraf.Accumulator) error {
	start := time.Now()
	health := healthResult{up: 1}

	acquired, err := b.withLock(func() {
		b.runLocked(acc, &health)
	})
	health.duration = time.Since(start)

	if err != nil {
		b.logErrorf("lock: %v", err)
		health.up = 0
		if health.exitCode == 0 {
			health.exitCode = 1
		}
	} else if !acquired {
		health.up = 0
		health.exitCode = exitLockBusy
	}

	b.emitHealth(acc, health)
	return nil
}

func (b *BkliteScript) runLocked(acc telegraf.Accumulator, health *healthResult) {
	argv, cleanup, err := b.prepareArgv()
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		b.logErrorf("prepare: %v", err)
		health.up = 0
		health.exitCode = exitNoStart
		return
	}

	env := b.childEnv()
	res := b.runCommand(argv, env)
	if res.stderr != "" {
		b.logErrorf("stderr: %s", res.stderr)
	}
	if res.truncated {
		health.truncated = 1
	}
	if res.err != nil && health.exitCode == 0 {
		health.exitCode = res.exitCode
		health.up = 0
	} else if res.exitCode != 0 && health.exitCode == 0 {
		health.exitCode = res.exitCode
		health.up = 0
	}

	parsed, perr, trunc := b.parseStdout(res.stdout)
	if perr != nil {
		b.logErrorf("parse: %v", perr)
		health.parseErrors = 1
		health.up = 0
	}
	if trunc {
		health.truncated = 1
	}

	series := parsed
	if len(series) > platformMaxSeries {
		series = series[:platformMaxSeries]
		health.truncated = 1
	}
	tag := b.scriptTag()
	for _, m := range series {
		m.AddTag("script", tag)
		acc.AddMetric(m)
	}
}

func (b *BkliteScript) parseStdout(stdout []byte) ([]telegraf.Metric, error, bool) {
	if len(stdout) == 0 {
		return nil, nil, false
	}
	data := ensurePrometheusTypes(stdout)
	metrics, err := b.parser.Parse(data)
	if err != nil {
		return nil, err, false
	}
	metrics = writeByStdoutName(metrics)
	if len(metrics) > platformMaxSeries {
		return metrics[:platformMaxSeries], nil, true
	}
	return metrics, nil, false
}

func (b *BkliteScript) childEnv() []string {
	env := os.Environ()
	if b.ScriptEnv != "" {
		prefix := b.ScriptEnv + "="
		filtered := make([]string, 0, len(env))
		for _, kv := range env {
			if strings.HasPrefix(kv, prefix) {
				continue
			}
			filtered = append(filtered, kv)
		}
		env = filtered
	}
	if len(b.Environment) > 0 {
		env = append(env, b.Environment...)
	}
	return env
}

func (b *BkliteScript) emitHealth(acc telegraf.Accumulator, h healthResult) {
	acc.AddGauge("bklite_script", map[string]interface{}{
		"up":               int64(h.up),
		"duration_seconds": h.duration.Seconds(),
		"exit_code":        int64(h.exitCode),
		"parse_errors":     int64(h.parseErrors),
		"truncated":        int64(h.truncated),
	}, map[string]string{"script": b.scriptTag()})
}

func (b *BkliteScript) logErrorf(format string, args ...interface{}) {
	if b.Log == nil {
		return
	}
	b.Log.Errorf(format, args...)
}

type healthResult struct {
	up          int
	exitCode    int
	parseErrors int
	truncated   int
	duration    time.Duration
}

func sanitizeName(s string) string {
	var builder strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "default"
	}
	return builder.String()
}

func init() {
	inputs.Add("bklite_script", func() telegraf.Input {
		return New()
	})
}
