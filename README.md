# easyworker

The **thin Go client** for the easystream (native-a11y) daemon. It owns no
a11y, capture or input logic of its own — the daemon (the platform's
first-class language: Swift / C# / Kotlin / Rust) owns all of it, and this
worker forwards.

## Two channels, one daemon

The daemon exposes **two protocols**, and the worker uses each for what it is
good at:

| Channel | Port | Carries | Why |
|---------|------|---------|-----|
| **est://** (TCP, binary) | 48079 | **H.264 video** + **input** (move/button/scroll/key/text) | a continuous, low-latency stream; video runs on its own thread so it is never blocked by control; input shares the connection so it is ordered with the video it acts on |
| **HTTP** (text/plain) | 48080 | **a11y queries** (`/apps`, `/tree`, `/find`, `/action`) + **screenshot** | discrete, one-shot requests that can run **concurrently** (est's single control thread serializes them) |

The split matters: putting input on HTTP would break drag/scroll (a gesture is
a *sequence* of primitives, not one request) and would desync input from the
video it acts on. Putting a11y queries on est would serialize them. So:

```
worker ──est://──▶ video + input        (continuous, ordered, low-latency)
       └─http───▶ /apps /tree /find    (discrete, concurrent)
```

The est protocol is specified in `abc-agent/worker-native/EST.md`.

## What it does

- **WorkerService Connect API** (`worker.v1`) — jobs, files, the control panel.
- **Jobs** run through the builtin shell interpreter, one job at a time, with
  live streamed output, detach and stdin.
- **GUI automation** (`ComputerService`) forwards to the daemon over the two
  channels above — `internal/computer/native.go`.
- **A built-in control panel** (`webui/`, Svelte 5 + Tailwind 4) served from
  the worker's own port, same-origin with the RPC.

## Layout

```
cmd/easyworker/       the binary (RPC + panel + job runner)
internal/a11y/          the a11y interfaces + the est/HTTP client (native/)
internal/computer/      the ComputerService driver (forwards to the daemon)
internal/jobsvc/        the job registry (memory + SQLite)
internal/shellh/        the builtin shell interpreter
internal/stream/        /est/ws — the video WebSocket for the panel
internal/webui/         the embedded control panel (built from webui/)
webui/                  the panel source (Svelte 5 + Tailwind 4)
proto/, gen/            the Connect RPC definitions and generated code
```

## Build

```
go build ./...
go test ./...
```
