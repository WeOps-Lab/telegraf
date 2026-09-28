# BKLite Script Input Plugin

The `bklite_script` plugin runs a user-provided script on each collection
interval and parses **Prometheus text exposition** from stdout. It is the
BlueKing Lite script-monitoring input: a hardened alternative to `inputs.exec`
for running operator scripts.

`inputs.exec` is unchanged. Use `[[inputs.exec]]` when you need arbitrary
data formats or globbed command lists without BKLite's security controls.

## Global configuration options <!-- @/docs/includes/plugin_config.md -->

In addition to the plugin-specific configuration settings, plugins support
additional global and plugin configuration settings. These settings are used to
modify metrics, tags, and field or create aliases and configure ordering, etc.
See the [CONFIGURATION.md][CONFIGURATION.md] for more details.

[CONFIGURATION.md]: ../../../docs/CONFIGURATION.md#plugins

## Configuration

```toml @sample.conf
# BKLite script monitoring: run a user script on an interval and collect
# Prometheus text metrics from stdout. Failures are reported via health
# metrics; Gather itself does not fail the Telegraf pipeline.
[[inputs.bklite_script]]
  ## Optional name used as the "script" tag on health and parsed metrics.
  # script_name = ""

  ## Interpreter used to execute a materialized script file (argv[0]).
  ## The script body is written to a private temp file under run_dir; argv
  ## contains only the interpreter, optional args, and that path (no script
  ## text, so `ps` cannot leak secrets). Default on Unix: "/bin/sh".
  ## On Windows this is required when using script / script_env.
  # interpreter = "/bin/sh"

  ## Extra arguments inserted between the interpreter and the script path.
  ##   example: interpreter_args = ["-u"]
  # interpreter_args = []

  ## Inline script body. Prefer this or script_env over embedding the script
  ## in a command string.
  # script = """
  # echo 'my_metric 1'
  # """

  ## Environment variable whose value is the script body. The variable is
  ## stripped from the child environment so the body is not copied into the
  ## child argv or (unnecessarily) the child environ.
  # script_env = "BKLITE_SCRIPT_BODY"

  ## Existing script file to execute (path only). Used when the body is not
  ## provided via script / script_env.
  # script_file = ""

  ## Exec-style command fallback (single string or array). The command is
  ## still subject to timeout, lock, privilege drop, and resource limits, but
  ## the command string is visible in `ps`. Prefer script / script_env for
  ## secrets. Glob expansion of the first token matches inputs.exec.
  # command = ""
  # commands = []

  ## Child environment ("KEY=value"). Put secrets here, not in argv.
  # environment = []

  ## Private directory for temp scripts and the instance lock file.
  ## Created with mode 0700 if missing. Default: ${TMPDIR}/bklite_script
  # run_dir = ""

  ## Linux: user to run the child as. When Telegraf is root this is required
  ## unless allow_root = true. Switching user requires Telegraf itself to be
  ## root. Windows: not supported; run the Telegraf service under the desired
  ## account.
  # user = ""

  ## Linux: allow the child to run as root. Default false.
  # allow_root = false

  ## Timeout. On expiry Linux kills the process group (SIGTERM then SIGKILL);
  ## Windows kills the process best-effort (grandchildren may survive).
  # timeout = "5s"

  ## Resource limits (best-effort, Linux only).
  ## memory_limit: cgroup v2 memory.max when writable, plus RLIMIT_AS
  ## (address-space) via prlimit. 0 = unlimited.
  ## cpu_limit_seconds: RLIMIT_CPU CPU-time seconds via prlimit. 0 = unlimited.
  ## Windows: ignored.
  # memory_limit = "0"
  # cpu_limit_seconds = 0

  ## Max Prometheus series kept from stdout (health metrics are extra).
  # max_series = 50

  ## Truncate stdout after this many bytes before parsing.
  # max_output_bytes = "64KiB"
```

### How the script is launched

When `script`, `script_env`, or `script_file` is set, Telegraf writes the body
(if any) to a file under `run_dir` with mode `0600` and executes:

```text
<interpreter> [interpreter_args...] <script_path>
```

The script text is never placed in process argv. Secrets should be passed with
`environment` (or the Telegraf process environment), not baked into a
`bash -c '...'` command.

`command` / `commands` remain available for existing files already on disk
(same idea as `inputs.exec`) but **are visible in `ps`**. Prefer `script` /
`script_env` when the body or secrets must not leak via argv.

### Privilege drop (Linux)

If Telegraf is running as root, the plugin **refuses to start** unless you set
`user` to a non-root account or set `allow_root = true`. The child is launched
with `setuid`/`setgid` via `SysProcAttr.Credential`.

Windows cannot drop privileges inside the plugin; run the Telegraf service
under the desired account.

### Timeouts and process groups

Linux: the child is placed in its own process group. On timeout the group is
sent SIGTERM, then SIGKILL (same pattern as `inputs.exec`).

Windows: `Process.Kill()` on the child (best-effort; grandchildren may remain).

### Resource limits (Linux, best-effort)

| Knob | Mechanism |
|------|-----------|
| `memory_limit` | cgroup v2 `memory.max` when `/sys/fs/cgroup` is writable; always also `prlimit` `RLIMIT_AS` |
| `cpu_limit_seconds` | `prlimit` `RLIMIT_CPU` (CPU seconds, not quota/bandwidth) |

Applying limits after `Start()` is racy for a process that immediately
allocates; treat this as a backstop, not a hard sandbox. If cgroup v2 is not
delegated to Telegraf, only `setrlimit`/`prlimit` is used.

Windows: limits are ignored.

### Concurrency

Each plugin instance takes an exclusive flock (Windows: `LockFileEx`) on a
file under `run_dir`. If the previous interval is still running, this gather
is skipped (`exit_code = 125`) and health metrics are still emitted.

### Output handling

Stdout is treated as Prometheus text. Missing `# TYPE` lines are inserted as
`untyped`. Output larger than `max_output_bytes` is truncated; more than
`max_series` series are dropped. `truncated=1` is set in either case.

On parse or script failure **Gather still returns nil** so the Telegraf
pipeline keeps running. Inspect health metrics.

## Metrics

Health measurement `bklite_script` (Prometheus names `bklite_script_*`):

- `up` — 1 if the script exited 0 and parsed without error, else 0
- `duration_seconds` — wall time of this gather
- `exit_code` — process exit code, or `124` (timeout), `125` (lock busy),
  `127` (failed to start)
- `parse_errors` — 1 if Prometheus parsing failed, else 0
- `truncated` — 1 if stdout or series count was capped

Tag: `script` = `script_name` (or `default`).

Plus whatever series the script prints, tagged with `script` as well.

## Example

```toml
[[inputs.bklite_script]]
  script_name = "disk_check"
  timeout = "5s"
  user = "telegraf"
  script = '''
echo '# TYPE my_free_bytes gauge'
echo 'my_free_bytes 123'
  '''
```

Example health output:

```text
bklite_script,script=disk_check up=1i,duration_seconds=0.02,exit_code=0i,parse_errors=0i,truncated=0i
```
