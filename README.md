# slog-getalert

Go [slog](https://pkg.go.dev/log/slog) handler that sends log records to [getalert.ru](https://getalert.ru) in [CloudEvents](https://cloudevents.io/) format.

## Features

- **CloudEvents 1.0** — each log record is sent as a CloudEvent with `subject` and structured `data`
- **Async** — non-blocking `Handle()`, background worker sends via HTTP
- **`send` attribute** — force-send any record regardless of level via `"send", true`
- **Retry with backoff** — retries on 5xx / timeout with exponential delay
- **Graceful shutdown** — `Close()` flushes pending events before exit
- **slog-compatible** — implements `slog.Handler`, works with `slog-multi.Fanout`

## Install

```bash
go get github.com/getalertorg/slog-getalert
```

## Usage

```go
package main

import (
	"log/slog"
	"os"

	sloggetalert "github.com/getalertorg/slog-getalert"
	slogmulti "github.com/samber/slog-multi"
	"github.com/lmittmann/tint"
)

func main() {
	logHandlers := []slog.Handler{
		tint.NewHandler(os.Stdout, &tint.Options{Level: slog.LevelDebug}),
	}

	if os.Getenv("ENVIRONMENT") == "prod" {
		alertHandler := sloggetalert.Option{
			Level:    slog.LevelWarn,
			Endpoint: "https://api.getalert.ru/v1/events",
			APIKey:   os.Getenv("GETALERT_API_KEY"),
			Source:   "//my-service",
			AddEmoji: true,
		}.NewHandler()
		defer alertHandler.Close()

		logHandlers = append(logHandlers, alertHandler)
	}

	log := slog.New(slogmulti.Fanout(logHandlers...))

	log.Info("starts normally — not sent to getalert")
	log.Warn("something is off", "component", "auth")    // → ⚠️ something is off
	log.Error("connection lost", "err", "timeout")        // → 🔴 connection lost
}
```

### Without slog-multi

```go
handler := sloggetalert.Option{
	Level:    slog.LevelWarn,
	Endpoint: "https://api.getalert.ru/v1/events",
	APIKey:   "ga_...",
	Source:   "//my-app",
	AddEmoji: true,
}.NewHandler()
defer handler.Close()

log := slog.New(handler)
log.Error("database unreachable")
```

### Force-send with `send` attribute

By default only records at `Level` or above are sent. Add `"send", true` to force-send any record regardless of level:

```go
// per-record: send a single Info record
log.Info("user registered", "send", true, "user_id", 123)

// via With: every record from this logger is sent
alertLog := log.With("send", true)
alertLog.Info("important info")   // → sent
alertLog.Debug("debug details")   // → sent
```

The `send` attribute is stripped from the CloudEvent data — it is a control flag only.

## Configuration

| Option | Default | Description |
|--------|---------|-------------|
| `Level` | `slog.LevelWarn` | Minimum log level to send |
| `Endpoint` | — | Full URL for event ingestion (e.g. `https://api.getalert.ru/v1/events`) |
| `APIKey` | — | Bearer token for authentication |
| `Source` | — | CloudEvent source (e.g. `"//my-service"`) |
| `Type` | `"log"` | CloudEvent type |
| `Environment` | `"production"` | CloudEvent environment (`production`, `staging`, etc.) |
| `AddSource` | `false` | Include caller file:line in `data.source_location` |
| `AddEmoji` | `false` | Prepend severity emoji to subject (⚠️ warning, 🔴 error, ℹ️ info) |
| `BufferSize` | `256` | Channel buffer size. Events are dropped when full |
| `Timeout` | `5s` | HTTP request timeout |
| `MaxRetries` | `2` | Retry attempts on 5xx / timeout |
| `RetryDelay` | `500ms` | Base delay between retries (doubles each attempt) |

## CloudEvent mapping

Each slog record produces a CloudEvent:

```json
{
  "specversion": "1.0",
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "source": "//my-service",
  "type": "log",
  "subject": "⚠️ something is off",
  "time": "2025-01-15T10:30:00Z",
  "datacontenttype": "application/json",
  "severity": "warning",
  "environment": "production",
  "data": {
    "component": "auth"
  }
}
```

| slog | CloudEvent |
|------|------------|
| `record.Message` | `subject` (with emoji if `AddEmoji: true`) |
| `slog.LevelWarn` | `severity: "warning"` |
| `slog.LevelError` | `severity: "error"` |
| `slog.LevelInfo` / `Debug` | `severity: "info"` |
| Attributes | `data.*` fields |
| Groups | dot-prefixed keys (e.g. `request.method`) |

## How it works

```
slog.Warn("msg") → Handle() → channel → worker goroutine → POST JSON → getalert API
```

1. `Enabled()` always returns `true` so `Handle()` can inspect per-record `send` attribute
2. `Handle()` checks: level >= threshold **OR** `send: true` present — otherwise skips
3. Builds a CloudEvent (message → `subject`, attrs → `data`) and pushes it into a buffered channel (non-blocking)
4. A background goroutine reads events and sends each as `POST` with JSON body
5. On 5xx or timeout, retries with exponential backoff
6. `Close()` drains the channel and flushes remaining events

## License

MIT
