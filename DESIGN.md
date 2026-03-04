
# Requirements

- Start multiple processes within the container by single parent process.
- Ensure proper signal handling, forwarding signals like SIGINT and SIGTERM to child processes.
- Forward output (stdout, stderr) to the main process, ensuring unified logging.
- If one child process exits, terminate all other to ensure all processes stop together.
- Wait for all child processes to exit before shutting down the parent, ensuring a clean and predictable shutdown process.
- Specify the list of commands in a textproto config file

# Design Options

## Config Format

**Chosen: minimal textproto subset** — hand-rolled parser, zero external dependencies.

Alternatives considered:

| Option | Pros | Cons |
|--------|------|------|
| Full `google.golang.org/protobuf` | Standards-compliant | Adds ~1 MB dependency, complex schema |
| YAML/TOML | Familiar | External library, CGO risk on some hosts |
| JSON | stdlib | Verbose, no comments |
| Minimal textproto (chosen) | No deps, readable, comment support | Only supports needed fields |

Supported fields per `process {}` block:

```
process {
  name:    "label"      # prefix used in log output
  command: "/path/bin"  # executable path (required)
  args:    "arg1"       # repeated; one value per line
  env:     "KEY=VALUE"  # repeated; appended to inherited environment
}
```

## Output Multiplexing

**Chosen: `lineWriter` — prefix-stamping `io.Writer`.**

Each process gets a `lineWriter` wrapping `os.Stdout`/`os.Stderr`. The writer buffers bytes until a newline, then emits `[name] <line>`. This keeps stdout and stderr interleaved but clearly labelled.

Alternative: separate log files per process — rejected to keep the container log stream unified.

## Signal Handling

**Chosen: selective forward.**

The parent listens for `SIGINT` and `SIGTERM`. On receipt, it sends `SIGTERM` to every child via `cmd.Process.Signal`. Children are expected to handle `SIGTERM` cleanly.

`Setpgid` is intentionally **not** set so the kernel forwards terminal signals (Ctrl-C) naturally in local runs, while Docker/Kubernetes signal PID 1 directly.

## Shutdown Trigger

Two triggers cause shutdown:

1. **Signal** — operator or orchestrator requests stop.
2. **First child exit** — any process terminating (success or failure) causes the launcher to stop all remaining processes. This mirrors the behaviour of supervisor tools and prevents zombie/orphan states.

## Implementation

- **Single file** (`main.go`), no packages, no CGO.
- **No external dependencies** — `go.mod` declares only the Go version.
- **Static-friendly build**: `CGO_ENABLED=0 go build -ldflags="-s -w"` produces a fully static Linux binary suitable for scratch/distroless containers.

# Changes

## 2026-02-28 — Initial implementation

- Created `main.go`: single-file launcher with `parseConfig`, `lineWriter`, signal forwarding, and `sync.WaitGroup`-based shutdown.
- Created `go.mod` (module `golauncher`, Go 1.21, zero external deps).
- Created `example.textproto` with two sample processes.

## 2026-02-28 — Test suite (83.9% coverage)

Two minimal refactors enabled high test coverage without changing behaviour:

1. **`lineWriter.dst` widened to `io.Writer`** — previously `*os.File`; changing to the interface lets tests inject a `*bytes.Buffer` while production callers (`os.Stdout`, `os.Stderr`) are unchanged.

2. **`launch(cfg *config, stop <-chan os.Signal) error` extracted from `main()`** — the entire process-management body moved into a testable function. `main()` becomes a thin wrapper that parses args/config, registers signals, and calls `launch`. Error handling on partial start (processes started before a failing one) is done inside `launch` with SIGTERM+Wait cleanup rather than `log.Fatalf`.

Design tradeoffs:
- Keeping `main()` at 0% coverage is acceptable; it contains only `log.Fatalf`/`os.Exit` glue that cannot be tested without subprocess tricks.
- Real OS processes (`echo`, `sh -c sleep 100`) are used in integration tests rather than mocks, keeping the test honest about signal propagation.
- `signal.Notify` is not called inside `launch`; the caller owns the channel. This keeps `launch` free of global state and testable with a plain `chan os.Signal`.
