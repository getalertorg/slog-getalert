# slog-getalert

Go [slog](https://pkg.go.dev/log/slog) handler that sends `WARN` and `ERROR` log records to [getalert.ru](https://getalert.ru) as push notifications via Max messenger.

Drop-in replacement for [slog-telegram](https://github.com/samber/slog-telegram) — same pattern, different delivery channel.

## Features

- **Async** — non-blocking `Handle()`, background worker sends via HTTP
- **Batching** — groups multiple log records into a single API request to reduce overhead
- **Dynamic tags** — automatically tags messages as `warn` or `error` based on log level
- **Retry with backoff** — retries on 5xx / timeout with exponential delay
- **Graceful shutdown** — `Close()` flushes pending messages before exit
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
			Level:      slog.LevelWarn,
			Endpoint:   "https://api.getalert.ru",
			APIKey:     os.Getenv("GETALERT_API_KEY"),
			Project:    "myproject",
			Topic:      "logs",
			DynamicTag: true,
		}.NewHandler()
		defer alertHandler.Close()

		logHandlers = append(logHandlers, alertHandler)
	}

	log := slog.New(slogmulti.Fanout(logHandlers...))

	log.Info("starts normally — not sent to getalert")
	log.Warn("something is off", "component", "auth")    // → getalert (?tag=warn)
	log.Error("connection lost", "err", "timeout")        // → getalert (?tag=error)
}
```

### Without slog-multi

```go
handler := sloggetalert.Option{
	Level:    slog.LevelWarn,
	Endpoint: "https://api.getalert.ru",
	APIKey:   "ga_...",
	Project:  "myapp",
	Topic:    "errors",
}.NewHandler()
defer handler.Close()

log := slog.New(handler)
log.Error("database unreachable")
```

## Configuration

| Option | Default | Description |
|--------|---------|-------------|
| `Level` | `slog.LevelWarn` | Minimum log level to send |
| `Endpoint` | — | API base URL (`https://api.getalert.ru`) |
| `APIKey` | — | Bearer token for authentication |
| `Project` | — | Project code |
| `Topic` | — | Topic code |
| `Tag` | `""` | Static tag value (e.g. `"critical"`) |
| `DynamicTag` | `false` | Auto-tag by level: `warn` or `error` |
| `BufferSize` | `256` | Channel buffer size. Logs are dropped when full |
| `BatchSize` | `10` | Max messages per HTTP request |
| `BatchInterval` | `2s` | Max wait before flushing a batch |
| `Timeout` | `5s` | HTTP request timeout |
| `MaxRetries` | `2` | Retry attempts on 5xx / timeout |
| `RetryDelay` | `500ms` | Base delay between retries (doubles each attempt) |
| `Source` | `false` | Include source file:line in the message |

## How it works

```
slog.Warn("msg") → Handle() → channel → worker goroutine → batch → HTTP PUT → getalert API → Max messenger
```

1. `Handle()` formats the log record and pushes it into a buffered channel (non-blocking)
2. A background goroutine collects messages into batches (by count or time interval)
3. Each batch is sent as a single HTTP request to `PUT /{project}/{topic}?tag=...`
4. On 5xx or timeout, retries with exponential backoff
5. `Close()` drains the channel and flushes remaining messages

## License

MIT
