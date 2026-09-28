# BKLite Script Input Plugin

The `bklite_script` plugin runs a user-provided script on each collection
interval and parses **Prometheus text exposition** from stdout. It is the
BlueKing Lite script-monitoring input.

`inputs.exec` is unchanged.

BKLite child configs should only set the user-facing fields below. Sandbox
limits, timeout, and root policy are **fixed in the plugin** (not toggles).

## Global configuration options <!-- @/docs/includes/plugin_config.md -->

In addition to the plugin-specific configuration settings, plugins support
additional global and plugin configuration settings. These settings are used to
modify metrics, tags, and field or create aliases and configure ordering, etc.
See the [CONFIGURATION.md][CONFIGURATION.md] for more details.

[CONFIGURATION.md]: ../../../docs/CONFIGURATION.md#plugins

## Configuration

```toml @sample.conf
# BKLite script monitoring: run a user script on each collection interval
# and collect Prometheus text metrics from stdout. Gather never fails the
# Telegraf pipeline; failures are reported via bklite_script health metrics.
#
# Platform-enforced (not configurable in this plugin):
#   - timeout = interval - 1s (set interval >= 60s)
#   - Linux: refuse to run as root; drop to run_as when Telegraf is root
#   - Linux: CPU / memory / process limits (cgroup v2 and/or setrlimit)
#   - per-instance file lock (no overlapping runs)
#   - script body is never placed in process argv
[[inputs.bklite_script]]
  ## Collection interval. Must be at least 60s. Child timeout is interval-1s.
  interval = "60s"

  ## Optional name used as the "script" tag on health and parsed metrics.
  # script_name = ""

  ## Interpreter used to execute the materialized script file (argv[0]).
  ## The script body is written to a private temp file; argv contains only
  ## the interpreter, params, and that path. Default on Unix: "/bin/sh".
  ## On Windows this is required when using script / script_env.
  # interpreter = "/bin/sh"

  ## Extra arguments between the interpreter and the script path.
  ##   example: params = ["-u"]
  # params = []

  ## Inline script body (preferred). Never appears in `ps` argv.
  script = """
  echo 'my_metric 1'
  """

  ## Alternative: environment variable whose value is the script body.
  ## The variable is stripped from the child environment.
  # script_env = "BKLITE_SCRIPT_BODY"

  ## command / commands / script_file are not supported (exec argv bypass).
  ## Provide the script body via script or script_env only.

  ## Secrets for the child as "KEY=value". Do not put secrets in argv.
  # environment = ["TOKEN=secret"]

  ## Linux: non-root account to run the script as. Required when Telegraf
  ## is running as root. Names that resolve to uid 0 (including "root")
  ## are refused at startup. Empty run_as never silently runs as root.
  ## Windows: ignored; run the Telegraf service under the desired account.
  # run_as = "telegraf"
```

### User-facing fields

| Field | Notes |
|-------|--------|
| `interval` | Required, **≥ 60s**. Telegraf collection interval for this input. |
| `script` / `script_env` | Script body. Written to a private file; **not** in argv. |
| `interpreter` / `params` | How the file is executed. |
| `environment` | Secrets (`KEY=value`) for the child. |
| `run_as` | Linux non-root user. Windows: not supported (service account). |

### Platform-enforced behavior (fixed)

- **Timeout** = `interval - 1s`. There is no user `timeout` knob.
- **Linux root**: Init fails if the child would run as root: empty `run_as`
  while Telegraf is root, `run_as = "root"`, or any account whose uid is 0.
  There is no `allow_root` flag. When Telegraf is root, set `run_as` to a
  non-root account; the child is launched with `setuid`/`setgid`.
- **Linux resource limits** (always applied, no disable flag):
  - memory: 256MiB (`cgroup v2 memory.max` when writable, plus `RLIMIT_AS`)
  - CPU time: `RLIMIT_CPU` equal to the derived timeout (seconds)
  - processes: 64 via `cgroup v2 pids.max` when writable; if cgroup setup
    fails, `RLIMIT_NPROC=256` (per-UID ceiling) is applied so a fork bomb
    cannot unbounded-spawn. Windows has no CPU/memory/nproc quotas.
- **Lock**: exclusive per-instance file lock; overlapping gathers are skipped
  (`exit_code = 125`).
- **Output caps**: 50 Prometheus series and 64KiB stdout.

Windows: no user switch and no cgroup/setrlimit; timeout kill is
`Process.Kill()` (best-effort). Run the Telegraf service under the desired
account.

### How the script is launched

```text
<interpreter> [params...] <private_script_path>
```

Secrets belong in `environment`, not in a `bash -c '...'` command string.

## Metrics

Health measurement `bklite_script` (Prometheus names `bklite_script_*`):

- `up` — 1 if the script exited 0 and parsed without error, else 0
- `duration_seconds` — wall time of this gather
- `exit_code` — process exit code, or `124` (timeout), `125` (lock busy),
  `127` (failed to start)
- `parse_errors` — 1 if Prometheus parsing failed, else 0
- `truncated` — 1 if stdout or series count was capped

Tag: `script` = `script_name` (or `default`).

## Example

```toml
[[inputs.bklite_script]]
  interval = "60s"
  script_name = "disk_check"
  run_as = "telegraf"
  interpreter = "/bin/sh"
  environment = ["TOKEN=secret"]
  script = '''
echo '# TYPE my_free_bytes gauge'
echo 'my_free_bytes 123'
  '''
```
