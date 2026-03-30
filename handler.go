package sloggetalert

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	defaultBufferSize    = 256
	defaultBatchSize     = 10
	defaultBatchInterval = 2 * time.Second
	defaultTimeout       = 5 * time.Second
	defaultMaxRetries    = 2
	defaultRetryDelay    = 500 * time.Millisecond
)

// Option configures a getalert slog handler.
type Option struct {
	// Level is the minimum log level to send. Defaults to slog.LevelWarn.
	Level slog.Leveler

	// Endpoint is the getalert API base URL (e.g. "https://api.getalert.ru").
	Endpoint string

	// APIKey is the Bearer token for authentication.
	APIKey string

	// Project is the project code in getalert.
	Project string

	// Topic is the topic code in getalert.
	Topic string

	// Tag is a static tag value. If DynamicTag is true, the log level is used instead.
	Tag string

	// DynamicTag sends ?tag=warn or ?tag=error based on the log level.
	// Takes precedence over Tag.
	DynamicTag bool

	// BufferSize is the channel buffer size. Defaults to 256.
	BufferSize int

	// BatchSize is the max number of messages per batch. Defaults to 10.
	BatchSize int

	// BatchInterval is the max time to wait before flushing a batch. Defaults to 2s.
	BatchInterval time.Duration

	// Timeout is the HTTP request timeout. Defaults to 5s.
	Timeout time.Duration

	// MaxRetries is the number of retry attempts on 5xx or timeout. Defaults to 2.
	MaxRetries int

	// RetryDelay is the base delay between retries (doubled each attempt). Defaults to 500ms.
	RetryDelay time.Duration

	// Source includes the caller source location in the message.
	Source bool
}

func (o Option) withDefaults() Option {
	if o.Level == nil {
		o.Level = slog.LevelWarn
	}
	if o.BufferSize <= 0 {
		o.BufferSize = defaultBufferSize
	}
	if o.BatchSize <= 0 {
		o.BatchSize = defaultBatchSize
	}
	if o.BatchInterval <= 0 {
		o.BatchInterval = defaultBatchInterval
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

// NewHandler creates a new getalert slog handler.
func (o Option) NewHandler() *Handler {
	o = o.withDefaults()

	h := &Handler{
		opt:  o,
		ch:   make(chan string, o.BufferSize),
		done: make(chan struct{}),
	}
	go h.worker()
	return h
}

// Handler is an slog.Handler that sends log records to getalert API.
type Handler struct {
	opt    Option
	attrs  []slog.Attr
	groups []string
	ch     chan string
	done   chan struct{}
	once   sync.Once
}

func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.opt.Level.Level()
}

func (h *Handler) Handle(_ context.Context, record slog.Record) error {
	msg := h.format(record)

	select {
	case h.ch <- msg:
	default:
		// buffer full — drop to avoid blocking the application
	}
	return nil
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{
		opt:    h.opt,
		attrs:  append(cloneAttrs(h.attrs), attrs...),
		groups: h.groups,
		ch:     h.ch,
		done:   h.done,
	}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &Handler{
		opt:    h.opt,
		attrs:  cloneAttrs(h.attrs),
		groups: append(cloneStrings(h.groups), name),
		ch:     h.ch,
		done:   h.done,
	}
}

// Close flushes pending messages and stops the background worker.
// It blocks until all buffered messages are sent or the context is cancelled.
func (h *Handler) Close() {
	h.once.Do(func() {
		close(h.ch)
		<-h.done
	})
}

func (h *Handler) format(record slog.Record) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("[%s] %s", record.Level, record.Message))

	if h.opt.Source {
		// slog doesn't expose source by default in Handle; we skip the runtime.Callers
		// approach for simplicity. Use record's PC if available.
		if record.PC != 0 {
			f := runtimeFrame(record.PC)
			if f.File != "" {
				b.WriteString(fmt.Sprintf("\n  source: %s:%d", f.File, f.Line))
			}
		}
	}

	prefix := groupPrefix(h.groups)

	for _, a := range h.attrs {
		writeAttr(&b, prefix, a)
	}

	record.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, prefix, a)
		return true
	})

	return b.String()
}

func (h *Handler) worker() {
	defer close(h.done)

	client := &http.Client{Timeout: h.opt.Timeout}
	batch := make([]string, 0, h.opt.BatchSize)
	timer := time.NewTimer(h.opt.BatchInterval)
	defer timer.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		body := strings.Join(batch, "\n\n---\n\n")
		tag := h.resolveTag(batch)
		h.send(client, body, tag)
		batch = batch[:0]
	}

	for {
		select {
		case msg, ok := <-h.ch:
			if !ok {
				flush()
				return
			}
			batch = append(batch, msg)
			if len(batch) >= h.opt.BatchSize {
				flush()
				timer.Reset(h.opt.BatchInterval)
			}
		case <-timer.C:
			flush()
			timer.Reset(h.opt.BatchInterval)
		}
	}
}

func (h *Handler) resolveTag(batch []string) string {
	if !h.opt.DynamicTag {
		return h.opt.Tag
	}
	for _, msg := range batch {
		if strings.HasPrefix(msg, "[ERROR]") {
			return "error"
		}
	}
	return "warn"
}

func (h *Handler) send(client *http.Client, body string, tag string) {
	url := fmt.Sprintf("%s/%s/%s", h.opt.Endpoint, h.opt.Project, h.opt.Topic)
	if tag != "" {
		url += "?tag=" + tag
	}

	var lastErr error
	for attempt := range h.opt.MaxRetries {
		if attempt > 0 {
			delay := h.opt.RetryDelay * time.Duration(1<<(attempt-1))
			time.Sleep(delay)
		}

		req, err := http.NewRequest(http.MethodPut, url, bytes.NewBufferString(body))
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+h.opt.APIKey)
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")

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

func groupPrefix(groups []string) string {
	if len(groups) == 0 {
		return ""
	}
	return strings.Join(groups, ".") + "."
}

func writeAttr(b *strings.Builder, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}

	if a.Value.Kind() == slog.KindGroup {
		groupAttrs := a.Value.Group()
		newPrefix := prefix
		if a.Key != "" {
			newPrefix = prefix + a.Key + "."
		}
		for _, ga := range groupAttrs {
			writeAttr(b, newPrefix, ga)
		}
		return
	}

	b.WriteString(fmt.Sprintf("\n  %s%s: %s", prefix, a.Key, a.Value.String()))
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
