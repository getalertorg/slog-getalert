package sloggetalert

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEnabled(t *testing.T) {
	h := Option{Level: slog.LevelWarn}.NewHandler()
	defer h.Close()

	if h.Enabled(nil, slog.LevelDebug) {
		t.Error("should not be enabled for debug")
	}
	if h.Enabled(nil, slog.LevelInfo) {
		t.Error("should not be enabled for info")
	}
	if !h.Enabled(nil, slog.LevelWarn) {
		t.Error("should be enabled for warn")
	}
	if !h.Enabled(nil, slog.LevelError) {
		t.Error("should be enabled for error")
	}
}

func TestSend(t *testing.T) {
	var mu sync.Mutex
	var received []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, string(body))
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	h := Option{
		Level:         slog.LevelWarn,
		Endpoint:      srv.URL,
		APIKey:        "test-key",
		Project:       "myproject",
		Topic:         "logs",
		BatchSize:     1,
		BatchInterval: 50 * time.Millisecond,
	}.NewHandler()

	log := slog.New(h)
	log.Warn("something happened", "key", "value")

	time.Sleep(200 * time.Millisecond)
	h.Close()

	mu.Lock()
	defer mu.Unlock()

	if len(received) == 0 {
		t.Fatal("expected at least 1 request")
	}
	if !strings.Contains(received[0], "something happened") {
		t.Errorf("expected message in body, got: %s", received[0])
	}
	if !strings.Contains(received[0], "key: value") {
		t.Errorf("expected attr in body, got: %s", received[0])
	}
}

func TestBatching(t *testing.T) {
	var mu sync.Mutex
	var bodies []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	h := Option{
		Level:         slog.LevelWarn,
		Endpoint:      srv.URL,
		APIKey:        "test-key",
		Project:       "proj",
		Topic:         "logs",
		BatchSize:     3,
		BatchInterval: 50 * time.Millisecond,
	}.NewHandler()

	log := slog.New(h)
	log.Warn("msg1")
	log.Warn("msg2")
	log.Warn("msg3")

	time.Sleep(200 * time.Millisecond)
	h.Close()

	mu.Lock()
	defer mu.Unlock()

	if len(bodies) != 1 {
		t.Fatalf("expected 1 batched request, got %d", len(bodies))
	}
	if !strings.Contains(bodies[0], "msg1") || !strings.Contains(bodies[0], "msg3") {
		t.Errorf("batch should contain all messages, got: %s", bodies[0])
	}
}

func TestDynamicTag(t *testing.T) {
	var mu sync.Mutex
	var tags []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tags = append(tags, r.URL.Query().Get("tag"))
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	h := Option{
		Level:         slog.LevelWarn,
		Endpoint:      srv.URL,
		APIKey:        "test-key",
		Project:       "proj",
		Topic:         "logs",
		DynamicTag:    true,
		BatchSize:     1,
		BatchInterval: 50 * time.Millisecond,
	}.NewHandler()

	log := slog.New(h)
	log.Warn("warning msg")

	time.Sleep(200 * time.Millisecond)

	log.Error("error msg")

	time.Sleep(200 * time.Millisecond)
	h.Close()

	mu.Lock()
	defer mu.Unlock()

	if len(tags) < 2 {
		t.Fatalf("expected 2 requests, got %d", len(tags))
	}
	if tags[0] != "warn" {
		t.Errorf("first tag should be warn, got: %s", tags[0])
	}
	if tags[1] != "error" {
		t.Errorf("second tag should be error, got: %s", tags[1])
	}
}

func TestRetryOn5xx(t *testing.T) {
	var mu sync.Mutex
	attempts := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()

		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	h := Option{
		Level:         slog.LevelWarn,
		Endpoint:      srv.URL,
		APIKey:        "test-key",
		Project:       "proj",
		Topic:         "logs",
		MaxRetries:    3,
		RetryDelay:    10 * time.Millisecond,
		BatchSize:     1,
		BatchInterval: 50 * time.Millisecond,
	}.NewHandler()

	log := slog.New(h)
	log.Error("retry me")

	time.Sleep(500 * time.Millisecond)
	h.Close()

	mu.Lock()
	defer mu.Unlock()

	if attempts < 2 {
		t.Errorf("expected at least 2 attempts, got %d", attempts)
	}
}

func TestWithAttrs(t *testing.T) {
	var mu sync.Mutex
	var received string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = string(body)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	h := Option{
		Level:         slog.LevelWarn,
		Endpoint:      srv.URL,
		APIKey:        "test-key",
		Project:       "proj",
		Topic:         "logs",
		BatchSize:     1,
		BatchInterval: 50 * time.Millisecond,
	}.NewHandler()

	log := slog.New(h).With("app", "myapp")
	log.Warn("test message")

	time.Sleep(200 * time.Millisecond)
	h.Close()

	mu.Lock()
	defer mu.Unlock()

	if !strings.Contains(received, "app: myapp") {
		t.Errorf("expected with-attr in body, got: %s", received)
	}
}

func TestWithGroup(t *testing.T) {
	var mu sync.Mutex
	var received string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = string(body)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	h := Option{
		Level:         slog.LevelWarn,
		Endpoint:      srv.URL,
		APIKey:        "test-key",
		Project:       "proj",
		Topic:         "logs",
		BatchSize:     1,
		BatchInterval: 50 * time.Millisecond,
	}.NewHandler()

	log := slog.New(h).WithGroup("request")
	log.Warn("failed", "method", "GET", "status", 500)

	time.Sleep(200 * time.Millisecond)
	h.Close()

	mu.Lock()
	defer mu.Unlock()

	if !strings.Contains(received, "request.method: GET") {
		t.Errorf("expected grouped attr, got: %s", received)
	}
}

func TestAuthHeader(t *testing.T) {
	var mu sync.Mutex
	var authHeader string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authHeader = r.Header.Get("Authorization")
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	h := Option{
		Level:         slog.LevelWarn,
		Endpoint:      srv.URL,
		APIKey:        "my-secret-key",
		Project:       "proj",
		Topic:         "logs",
		BatchSize:     1,
		BatchInterval: 50 * time.Millisecond,
	}.NewHandler()

	log := slog.New(h)
	log.Warn("test")

	time.Sleep(200 * time.Millisecond)
	h.Close()

	mu.Lock()
	defer mu.Unlock()

	if authHeader != "Bearer my-secret-key" {
		t.Errorf("expected Bearer auth header, got: %s", authHeader)
	}
}

func TestInfoNotSent(t *testing.T) {
	requests := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	h := Option{
		Level:         slog.LevelWarn,
		Endpoint:      srv.URL,
		APIKey:        "test-key",
		Project:       "proj",
		Topic:         "logs",
		BatchSize:     1,
		BatchInterval: 50 * time.Millisecond,
	}.NewHandler()

	log := slog.New(h)
	log.Info("this should not be sent")
	log.Debug("this too")

	time.Sleep(200 * time.Millisecond)
	h.Close()

	if requests != 0 {
		t.Errorf("expected 0 requests for info/debug, got %d", requests)
	}
}

func TestFlushOnClose(t *testing.T) {
	var mu sync.Mutex
	var received []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, string(body))
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	h := Option{
		Level:         slog.LevelWarn,
		Endpoint:      srv.URL,
		APIKey:        "test-key",
		Project:       "proj",
		Topic:         "logs",
		BatchSize:     100,
		BatchInterval: 10 * time.Second,
	}.NewHandler()

	log := slog.New(h)
	log.Error("flush me")

	h.Close()

	mu.Lock()
	defer mu.Unlock()

	if len(received) == 0 {
		t.Fatal("expected message to be flushed on Close")
	}
	if !strings.Contains(received[0], "flush me") {
		t.Errorf("expected flushed message, got: %s", received[0])
	}
}
