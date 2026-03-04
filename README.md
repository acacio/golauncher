# golauncher

A minimal process launcher designed to run as PID 1 inside a container. It starts multiple child processes from a single config file, multiplexes their output with clear labels, forwards OS signals, and shuts everything down cleanly when any child exits or a signal is received.

## Features

- Starts multiple processes from a single textproto config file
- Prefixes every stdout/stderr line with `[name]` for easy identification
- Forwards `SIGINT` and `SIGTERM` to all children
- Shuts down all processes when any one of them exits
- Waits for every child to finish before exiting (clean drain)
- If one child process exits, terminate all other to ensure all processes stop together.
- Single static binary — no CGO, no external dependencies

## Build

```sh
# Development build
go build -o golauncher .

# Static binary for Linux containers (cross-compile from any OS)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags="-s -w" -o golauncher .
```

## Usage

```
golauncher [config]
```

The optional argument is the path to a config file. If omitted, golauncher reads `golauncher.cfg` from the current directory. It exits with code 1 if the config cannot be read.

## Configuration

Config files use a minimal [text proto](https://protobuf.dev/reference/protobuf/textformat-spec/) syntax. Each `process {}` block defines one child process to launch.

### Fields

| Field     | Required | Repeatable | Description |
|-----------|----------|------------|-------------|
| `name`    | yes      | no         | Label used in log output. Choose a short, unique name. |
| `command` | yes      | no         | Executable to run. May be a bare name (resolved via `PATH`) or an absolute path. |
| `args`    | no       | yes        | Command-line arguments, one value per `args` line, in order. |
| `env`     | no       | yes        | Additional environment variables as `KEY=VALUE`. These are **appended** to the environment inherited from golauncher; they do not replace it. |

### Syntax rules

- Each string value must be double-quoted.
- Supported escape sequences inside quoted values: `\"` and `\\`.
- Lines beginning with `#` are comments and are ignored.
- Blank lines are ignored.
- Field order within a block does not matter.

### Example config

```textproto
# myapp.textproto

process {
  name: "api"
  command: "/usr/bin/python3"
  args: "-m"
  args: "http.server"
  args: "8080"
  env: "PYTHONDONTWRITEBYTECODE=1"
}

process {
  name: "worker"
  command: "/bin/sh"
  args: "-c"
  args: "while true; do echo processing; sleep 10; done"
}

process {
  name: "metrics"
  command: "node"
  args: "/app/metrics.js"
  env: "PORT=9090"
  env: "LOG_LEVEL=warn"
}
```

## Log output

golauncher writes its own lifecycle messages (process start, exit, shutdown) to **stdout** with a timestamp prefix. Child process output is also written to stdout/stderr with a `[name]` prefix per line:

```
22:01:05 [api] started pid=42
22:01:05 [worker] started pid=43
22:01:05 [metrics] started pid=44
[api] Serving HTTP on 0.0.0.0 port 8080 (http://0.0.0.0:8080/) ...
[worker] processing
[metrics] listening on :9090
[worker] processing
```

Each child's stdout and stderr are both prefixed with `[name]` and written to golauncher's stdout and stderr respectively.

## Shutdown behaviour

Two events trigger a shutdown:

1. **A signal is received** (`SIGINT` or `SIGTERM`) — golauncher forwards `SIGTERM` to every child, then waits for all of them to exit.
2. **Any child exits** — regardless of whether it exited successfully or with an error, golauncher sends `SIGTERM` to all remaining children and waits for them.

In both cases golauncher waits for every goroutine to finish before it exits itself, ensuring no orphaned processes.

**Signal example (Ctrl-C):**

```
^C
22:01:20 received interrupt, shutting down
22:01:20 [api] exited: signal: terminated
22:01:20 [worker] exited: signal: terminated
22:01:20 [metrics] exited: signal: terminated
22:01:20 shutdown complete
```

**Child exit example:**

```
22:05:00 [worker] exited ok
22:05:00 process exited, shutting down all
22:05:00 [api] exited: signal: terminated
22:05:00 [metrics] exited: signal: terminated
22:05:00 shutdown complete
```

## Container usage

### Dockerfile (scratch / distroless)

```dockerfile
FROM scratch
COPY golauncher     /golauncher
COPY golauncher.cfg /golauncher.cfg
ENTRYPOINT ["/golauncher"]
# or with an explicit config path:
# ENTRYPOINT ["/golauncher", "/golauncher.cfg"]
```

### docker run

```sh
docker run --rm myimage
# stop cleanly with:
docker stop <container>   # sends SIGTERM to PID 1
```

### docker-compose

```yaml
services:
  app:
    image: myimage
    stop_signal: SIGTERM
    stop_grace_period: 10s
```

### Why run as PID 1?

In a container, the process started by `ENTRYPOINT` becomes PID 1. The kernel and container runtimes send signals (e.g. `SIGTERM` on `docker stop`) directly to PID 1. By acting as PID 1, golauncher receives these signals and can forward them to its children, guaranteeing a clean shutdown regardless of what those children do.

## Testing

```sh
# Run all tests
go test -v ./...

# Run with race detector (recommended)
go test -v -race ./...

# Coverage report
go test -coverprofile=cover.out ./... && go tool cover -func=cover.out
```

Expected output: all tests pass, **≥83% statement coverage** of `main.go`. The untestable 0% is `main()` itself, which is a thin OS-entry wrapper around `launch()`.

## Error cases

| Situation | Behaviour |
|-----------|-----------|
| Config file not found | Logs error, exits 1 |
| Config has no `process` blocks | Logs error, exits 1 |
| A process fails to start | Already-started processes receive SIGTERM and are waited on; `launch` returns an error and the launcher exits 1 |
| A process exits with non-zero status | Logged as `[name] exited: exit status N`; triggers shutdown of all others |
| SIGTERM sent to already-exited child | Logged as `[name] sigterm: os: process already finished`; harmless |
