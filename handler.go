package sloggetalert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	defaultBufferSize = 256
	defaultTimeout    = 5 * time.Second
	defaultMaxRetries = 2
	defaultRetryDelay = 500 * time.Millisecond
)

// Option configures a getalert slog handler.
type Option struct {
	// Level is the minimum log level to send. Defaults to slog.LevelWarn.
	Level slog.Leveler

	// Endpoint is the getalert API base URL (e.g. "https://api.getalert.ru").
	Endpoint string

	// APIKey is the Bearer token for authentication.
	APIKey string

	// Source is the CloudEvent source (e.g. "//my-service").
	Source string

	// Type is the CloudEvent type. Defaults to "log".
	Type string

	// Environment is the CloudEvent environment. Defaults to "production".
	Environment string

	// BufferSize is the channel buffer size. Defaults to 256.
	BufferSize int

	// Timeout is the HTTP request timeout. Defaults to 5s.
	Timeout time.Duration

	// MaxRetries is the number of retry attempts on 5xx or timeout. Defaults to 2.
	MaxRetries int

	// RetryDelay is the base delay between retries (doubled each attempt). Defaults to 500ms.
	RetryDelay time.Duration

	// AddSource includes the caller source location in the event data.
	AddSource bool

	// AddEmoji prepends a severity emoji to the subject (⚠️ warning, 🔴 error, ℹ️ info).
	AddEmoji bool
}

func (o Option) withDefaults() Option {
	if o.Level == nil {
		o.Level = slog.LevelWarn
	}
	if o.Type == "" {
		o.Type = "log"
	}
	if o.Environment == "" {
		o.Environment = "production"
	}
	if o.BufferSize <= 0 {
		o.BufferSize = defaultBufferSize
	}
	if o.Timeout <= 0 {
		o.Timeout = defaultTimeout
	}
	if o.MaxRetries < 0 {
		o.MaxRetries = 0
	}
	if o.MaxRetries == 0 {
		o.MaxRetries = defaultMaxRetries
	}
	if o.RetryDelay <= 0 {
		o.RetryDelay = defaultRetryDelay
	}
	return o
}

type cloudEvent struct {
	SpecVersion     string         `json:"specversion"`
	ID              string         `json:"id"`
	Source          string         `json:"source"`
	Type            string         `json:"type"`
	Subject         string         `json:"subject"`
	Time            time.Time      `json:"time"`
	DataContentType string         `json:"datacontenttype"`
	Severity        string         `json:"severity"`
	Environment     string         `json:"environment"`
	Data            map[string]any `json:"data"`
}

// NewHandler creates a new getalert slog handler that sends CloudEvents.
func (o Option) NewHandler() *Handler {
	o = o.withDefaults()

	h := &Handler{
		opt:  o,
		ch:   make(chan cloudEvent, o.BufferSize),
		done: make(chan struct{}),
	}
	go h.worker()
	return h
}

// Handler is an slog.Handler that sends log records as CloudEvents to the getalert API.
//
// A record is sent when its level meets the configured threshold OR when
// it (or the logger) carries the attribute "send" set to true.
// The "send" attribute is stripped from the CloudEvent data.
type Handler struct {
	opt     Option
	attrs   []slog.Attr
	groups  []string
	hasSend bool // true when With("send", true) was called on this handler
	ch      chan cloudEvent
	done    chan struct{}
	once    sync.Once
}

const sendAttrKey = "send"

func (h *Handler) Enabled(_ context.Context, _ slog.Level) bool {
	// Always return true so Handle() can inspect per-record "send" attribute.
	return true
}

func (h *Handler) Handle(_ context.Context, record slog.Record) error {
	send := h.hasSend || record.Level >= h.opt.Level.Level()
	if !send {
		record.Attrs(func(a slog.Attr) bool {
			if a.Key == sendAttrKey && a.Value.Kind() == slog.KindBool && a.Value.Bool() {
				send = true
				return false
			}
			return true
		})
	}
	if !send {
		return nil
	}

	ce := h.buildEvent(record)

	select {
	case h.ch <- ce:
	default:
		// buffer full — drop to avoid blocking the application
	}
	return nil
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	hasSend := h.hasSend
	for _, a := range attrs {
		if a.Key == sendAttrKey && a.Value.Kind() == slog.KindBool {
			hasSend = a.Value.Bool()
		}
	}
	return &Handler{
		opt:     h.opt,
		attrs:   append(cloneAttrs(h.attrs), attrs...),
		groups:  h.groups,
		hasSend: hasSend,
		ch:      h.ch,
		done:    h.done,
	}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &Handler{
		opt:     h.opt,
		attrs:   cloneAttrs(h.attrs),
		groups:  append(cloneStrings(h.groups), name),
		hasSend: h.hasSend,
		ch:      h.ch,
		done:    h.done,
	}
}

// Close flushes pending events and stops the background worker.
func (h *Handler) Close() {
	h.once.Do(func() {
		close(h.ch)
		<-h.done
	})
}

func (h *Handler) buildEvent(record slog.Record) cloudEvent {
	data := make(map[string]any)

	if h.opt.AddSource && record.PC != 0 {
		f := runtimeFrame(record.PC)
		if f.File != "" {
			data["source_location"] = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
	}

	prefix := groupPrefix(h.groups)

	for _, a := range h.attrs {
		collectAttr(data, prefix, a)
	}

	record.Attrs(func(a slog.Attr) bool {
		collectAttr(data, prefix, a)
		return true
	})

	severity := mapSeverity(record.Level)
	subject := record.Message
	if h.opt.AddEmoji {
		subject = severityEmoji(severity) + " " + subject
	}

	return cloudEvent{
		SpecVersion:     "1.0",
		ID:              uuid.New().String(),
		Source:          h.opt.Source,
		Type:            h.opt.Type,
		Subject:         subject,
		Time:            record.Time,
		DataContentType: "application/json",
		Severity:        severity,
		Environment:     h.opt.Environment,
		Data:            data,
	}
}

func (h *Handler) worker() {
	defer close(h.done)

	client := &http.Client{Timeout: h.opt.Timeout}

	for ce := range h.ch {
		h.send(client, ce)
	}
}

func (h *Handler) send(client *http.Client, ce cloudEvent) {
	body, err := json.Marshal(ce)
	if err != nil {
		return
	}

	url := h.opt.Endpoint

	var lastErr error
	for attempt := range h.opt.MaxRetries {
		if attempt > 0 {
			delay := h.opt.RetryDelay * time.Duration(1<<(attempt-1))
			time.Sleep(delay)
		}

		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+h.opt.APIKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("getalert: status %d", resp.StatusCode)
			continue
		}
		return
	}
	_ = lastErr
}

func mapSeverity(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "error"
	case level >= slog.LevelWarn:
		return "warning"
	default:
		return "info"
	}
}

func severityEmoji(severity string) string {
	switch severity {
	case "error":
		return "🔴"
	case "warning":
		return "⚠️"
	default:
		return "ℹ️"
	}
}

func collectAttr(data map[string]any, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}

	if prefix == "" && a.Key == sendAttrKey {
		return
	}

	if a.Value.Kind() == slog.KindGroup {
		groupAttrs := a.Value.Group()
		newPrefix := prefix
		if a.Key != "" {
			newPrefix = prefix + a.Key + "."
		}
		for _, ga := range groupAttrs {
			collectAttr(data, newPrefix, ga)
		}
		return
	}

	key := prefix + a.Key
	data[key] = resolveValue(a.Value)
}

func resolveValue(v slog.Value) any {
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		return v.Float64()
	case slog.KindBool:
		return v.Bool()
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339Nano)
	default:
		return v.String()
	}
}

func groupPrefix(groups []string) string {
	if len(groups) == 0 {
		return ""
	}
	return strings.Join(groups, ".") + "."
}

func cloneAttrs(attrs []slog.Attr) []slog.Attr {
	if attrs == nil {
		return nil
	}
	c := make([]slog.Attr, len(attrs))
	copy(c, attrs)
	return c
}

func cloneStrings(s []string) []string {
	if s == nil {
		return nil
	}
	c := make([]string, len(s))
	copy(c, s)
	return c
}
