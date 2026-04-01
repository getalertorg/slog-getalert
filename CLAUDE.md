# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

Go library (`sloggetalert` package) — an `slog.Handler` that sends log records to [getalert.ru](https://getalert.ru) as CloudEvents via HTTP POST. Async, non-blocking, with retry and graceful shutdown.

Module: `github.com/getalertorg/slog-getalert`  
Go version: 1.26.1

## Commands

```bash
go test ./...           # run all tests
go test -run TestName   # run a single test
go test -race ./...     # run tests with race detector
go vet ./...            # static analysis
```

No build step — this is a library, not a binary.

## Architecture

Single-package library with three source files:

- **handler.go** — all core logic: `Option` config struct, `Handler` (implements `slog.Handler`), background worker goroutine, HTTP sending with retry, CloudEvent construction, attribute collection. `Enabled()` always returns `true` so `Handle()` can inspect the per-record `"send"` attribute.
- **source.go** — `runtimeFrame()` helper for caller source location (`AddSource` option).
- **example_test.go** — runnable example in external test package (`sloggetalert_test`).

### Key design decisions

- `Enabled()` returns `true` unconditionally. The actual filtering (level threshold OR `"send"` attr) happens inside `Handle()`.
- The `"send"` attribute is a control flag stripped from CloudEvent data. It can be set per-record (`slog.Info("msg", "send", true)`) or via `With("send", true)` for all records from that logger.
- Events are pushed into a buffered channel (`ch`) and consumed by a single background goroutine (`worker`). Buffer overflow silently drops events to avoid blocking the caller.
- `Close()` closes the channel and waits for the worker to drain — idempotent via `sync.Once`.
- `WithAttrs`/`WithGroup` return new `Handler` instances sharing the same channel and done signal.

### Dependencies

- `github.com/google/uuid` — CloudEvent IDs
- `github.com/samber/slog-multi` — for `Fanout` in examples/tests (optional for users)
