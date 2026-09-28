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

const (
	defaultMaxSeries      = 50
	defaultMaxOutputBytes = 64 * 1024
	defaultTimeout        = 5 * time.Second

	// GNU timeout-style codes so operators can tell timeout/skip from the script.
	exitTimeout  = 124
	exitLockBusy = 125
	exitNoStart  = 127
)

// BkliteScript runs a user script and collects Prometheus metrics from stdout.
type BkliteScript struct {
	ScriptName      string          `toml:"script_name"`
	Interpreter     string          `toml:"interpreter"`
	InterpreterArgs []string        `toml:"interpreter_args"`
	Script          string          `toml:"script"`
	ScriptEnv       string          `toml:"script_env"`
	ScriptFile      string          `toml:"script_file"`
	Command         string          `toml:"command"`
	Commands        []string        `toml:"commands"`
	Environment     []string        `toml:"environment"`
	RunDir          string          `toml:"run_dir"`
	User            string          `toml:"user"`
	AllowRoot       bool            `toml:"allow_root"`
	Timeout         config.Duration `toml:"timeout"`
	MemoryLimit     config.Size     `toml:"memory_limit"`
	CPULimitSeconds int             `toml:"cpu_limit_seconds"`
	MaxSeries       int             `toml:"max_series"`
	MaxOutputBytes  config.Size     `toml:"max_output_bytes"`
	Log             telegraf.Logger `toml:"-"`

	lockPath      string
	hasCredential bool
	uid           uint32
	gid           uint32
	parser        *prometheus.Parser
}

func New() *BkliteScript {
	return &BkliteScript{
		Timeout:        config.Duration(defaultTimeout),
		MaxSeries:      defaultMaxSeries,
		MaxOutputBytes: config.Size(defaultMaxOutputBytes),
	}
}

func (*BkliteScript) SampleConfig() string {
	return sampleConfig
}

func (b *BkliteScript) Init() error {
	if b.Timeout <= 0 {
		b.Timeout = config.Duration(defaultTimeout)
	}
	if b.MaxSeries <= 0 {
		b.MaxSeries = defaultMaxSeries
	}
	if b.MaxOutputBytes <= 0 {
		b.MaxOutputBytes = config.Size(defaultMaxOutputBytes)
	}

	if !b.hasWork() {
		return errors.New("must set script, script_env, script_file, or command(s)")
	}

	if b.usesScriptBody() && b.Interpreter == "" {
		interp, err := defaultInterpreter()
		if err != nil {
			return err
		}
		b.Interpreter = interp
	}

	if b.RunDir == "" {
		b.RunDir = filepath.Join(os.TempDir(), "bklite_script")
	}
	if err := os.MkdirAll(b.RunDir, 0700); err != nil {
		return fmt.Errorf("creating run_dir %q: %w", b.RunDir, err)
	}
	b.lockPath = filepath.Join(b.RunDir, b.instanceID()+".lock")

	if err := b.initUser(); err != nil {
		return err
	}

	header := make(http.Header)
	header.Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	b.parser = &prometheus.Parser{
		Header:          header,
		IgnoreTimestamp: true,
		MetricVersion:   2,
		Log:             b.Log,
	}
	return nil
}

func (b *BkliteScript) hasWork() bool {
	return b.usesScriptBody() || b.ScriptFile != "" || b.Command != "" || len(b.Commands) > 0
}

func (b *BkliteScript) usesScriptBody() bool {
	return b.Script != "" || b.ScriptEnv != ""
}

func (b *BkliteScript) instanceID() string {
	if b.ScriptName != "" {
		return sanitizeName(b.ScriptName)
	}
	h := sha256.Sum256([]byte(strings.Join([]string{
		b.Script, b.ScriptEnv, b.ScriptFile, b.Command, strings.Join(b.Commands, "\x00"),
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
	commands, cleanup, err := b.prepareCommands()
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
	var series []telegraf.Metric
	for _, argv := range commands {
		res := b.runCommand(argv, env, time.Duration(b.Timeout), int64(b.MemoryLimit), b.CPULimitSeconds, int(b.MaxOutputBytes))
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
		series = append(series, parsed...)
	}

	if len(series) > b.MaxSeries {
		series = series[:b.MaxSeries]
		health.truncated = 1
	}
	tag := b.scriptTag()
	for _, m := range series {
		m.AddTag("script", tag)
		acc.AddMetric(m)
	}
}

func (b *BkliteScript) parseStdout(stdout []byte) ([]telegraf.Metric, error, bool) {
	truncated := false
	if len(stdout) == 0 {
		return nil, nil, false
	}
	data := ensurePrometheusTypes(stdout)
	metrics, err := b.parser.Parse(data)
	if err != nil {
		return nil, err, truncated
	}
	if len(metrics) > b.MaxSeries {
		return metrics[:b.MaxSeries], nil, true
	}
	return metrics, nil, truncated
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
