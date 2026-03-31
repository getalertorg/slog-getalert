package sloggetalert

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

func TestSendCloudEvent(t *testing.T) {
	var mu sync.Mutex
	var received []cloudEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var ce cloudEvent
		if err := json.Unmarshal(body, &ce); err != nil {
			t.Errorf("invalid JSON: %v", err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		received = append(received, ce)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "status": "queued"})
	}))
	defer srv.Close()

	h := Option{
		Level:    slog.LevelWarn,
		Endpoint: srv.URL + "/v1/events",
		APIKey:   "test-key",
		Source:   "//test-service",
		Type:     "log.record",
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

	ce := received[0]
	if ce.SpecVersion != "1.0" {
		t.Errorf("specversion = %q, want 1.0", ce.SpecVersion)
	}
	if ce.Source != "//test-service" {
		t.Errorf("source = %q, want //test-service", ce.Source)
	}
	if ce.Type != "log.record" {
		t.Errorf("type = %q, want log.record", ce.Type)
	}
	if ce.Severity != "warning" {
		t.Errorf("severity = %q, want warning", ce.Severity)
	}
	if ce.Environment != "production" {
		t.Errorf("environment = %q, want production", ce.Environment)
	}
	if ce.DataContentType != "application/json" {
		t.Errorf("datacontenttype = %q, want application/json", ce.DataContentType)
	}
	if ce.ID == "" {
		t.Error("id should not be empty")
	}
	if title, ok := ce.Data["title"]; !ok || title != "something happened" {
		t.Errorf("data.title = %v, want 'something happened'", title)
	}
	if v, ok := ce.Data["key"]; !ok || v != "value" {
		t.Errorf("data.key = %v, want 'value'", v)
	}
}

func TestSeverityMapping(t *testing.T) {
	var mu sync.Mutex
	var received []cloudEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var ce cloudEvent
		json.Unmarshal(body, &ce)
		mu.Lock()
		received = append(received, ce)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	h := Option{
		Level:    slog.LevelWarn,
		Endpoint: srv.URL + "/v1/events",
		APIKey:   "test-key",
		Source:   "//test",
		Type:     "log.record",
	}.NewHandler()

	log := slog.New(h)
	log.Warn("warn msg")
	time.Sleep(100 * time.Millisecond)
	log.Error("error msg")
	time.Sleep(200 * time.Millisecond)
	h.Close()

	mu.Lock()
	defer mu.Unlock()

	if len(received) < 2 {
		t.Fatalf("expected 2 events, got %d", len(received))
	}
	if received[0].Severity != "warning" {
		t.Errorf("warn severity = %q, want warning", received[0].Severity)
	}
	if received[1].Severity != "error" {
		t.Errorf("error severity = %q, want error", received[1].Severity)
	}
}

func TestMultipleEvents(t *testing.T) {
	var mu sync.Mutex
	var count int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	h := Option{
		Level:    slog.LevelWarn,
		Endpoint: srv.URL + "/v1/events",
		APIKey:   "test-key",
		Source:   "//test",
		Type:     "log.record",
	}.NewHandler()

	log := slog.New(h)
	log.Warn("msg1")
	log.Warn("msg2")
	log.Warn("msg3")

	time.Sleep(300 * time.Millisecond)
	h.Close()

	mu.Lock()
	defer mu.Unlock()

	if count != 3 {
		t.Errorf("expected 3 requests (one per event), got %d", count)
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
		Level:      slog.LevelWarn,
		Endpoint:   srv.URL,
		APIKey:     "test-key",
		Source:     "//test",
		Type:       "log.record",
		MaxRetries: 3,
		RetryDelay: 10 * time.Millisecond,
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
	var received cloudEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		json.Unmarshal(body, &received)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	h := Option{
		Level:    slog.LevelWarn,
		Endpoint: srv.URL + "/v1/events",
		APIKey:   "test-key",
		Source:   "//test",
		Type:     "log.record",
	}.NewHandler()

	log := slog.New(h).With("app", "myapp")
	log.Warn("test message")

	time.Sleep(200 * time.Millisecond)
	h.Close()

	mu.Lock()
	defer mu.Unlock()

	if v, ok := received.Data["app"]; !ok || v != "myapp" {
		t.Errorf("expected data.app=myapp, got: %v", received.Data)
	}
}

func TestWithGroup(t *testing.T) {
	var mu sync.Mutex
	var received cloudEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		json.Unmarshal(body, &received)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	h := Option{
		Level:    slog.LevelWarn,
		Endpoint: srv.URL + "/v1/events",
		APIKey:   "test-key",
		Source:   "//test",
		Type:     "log.record",
	}.NewHandler()

	log := slog.New(h).WithGroup("request")
	log.Warn("failed", "method", "GET", "status", 500)

	time.Sleep(200 * time.Millisecond)
	h.Close()

	mu.Lock()
	defer mu.Unlock()

	if v, ok := received.Data["request.method"]; !ok || v != "GET" {
		t.Errorf("expected data[request.method]=GET, got: %v", received.Data)
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
		Level:    slog.LevelWarn,
		Endpoint: srv.URL + "/v1/events",
		APIKey:   "my-secret-key",
		Source:   "//test",
		Type:     "log.record",
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

func TestPostMethod(t *testing.T) {
	var mu sync.Mutex
	var method, path, contentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		method = r.Method
		path = r.URL.Path
		contentType = r.Header.Get("Content-Type")
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	h := Option{
		Level:    slog.LevelWarn,
		Endpoint: srv.URL + "/v1/events",
		APIKey:   "test-key",
		Source:   "//test",
		Type:     "log.record",
	}.NewHandler()

	log := slog.New(h)
	log.Warn("test")

	time.Sleep(200 * time.Millisecond)
	h.Close()

	mu.Lock()
	defer mu.Unlock()

	if method != http.MethodPost {
		t.Errorf("method = %q, want POST", method)
	}
	if path != "/v1/events" {
		t.Errorf("path = %q, want /v1/events", path)
	}
	if contentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", contentType)
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
		Level:    slog.LevelWarn,
		Endpoint: srv.URL + "/v1/events",
		APIKey:   "test-key",
		Source:   "//test",
		Type:     "log.record",
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
	var received []cloudEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var ce cloudEvent
		json.Unmarshal(body, &ce)
		mu.Lock()
		received = append(received, ce)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	h := Option{
		Level:    slog.LevelWarn,
		Endpoint: srv.URL + "/v1/events",
		APIKey:   "test-key",
		Source:   "//test",
		Type:     "log.record",
	}.NewHandler()

	log := slog.New(h)
	log.Error("flush me")

	h.Close()

	mu.Lock()
	defer mu.Unlock()

	if len(received) == 0 {
		t.Fatal("expected event to be flushed on Close")
	}
	if title, ok := received[0].Data["title"]; !ok || title != "flush me" {
		t.Errorf("expected data.title='flush me', got: %v", received[0].Data)
	}
}

func TestEnvironmentOption(t *testing.T) {
	var mu sync.Mutex
	var received cloudEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		json.Unmarshal(body, &received)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	h := Option{
		Level:       slog.LevelWarn,
		Endpoint:    srv.URL,
		APIKey:      "test-key",
		Source:      "//test",
		Type:        "log.record",
		Environment: "staging",
	}.NewHandler()

	log := slog.New(h)
	log.Warn("test")

	time.Sleep(200 * time.Millisecond)
	h.Close()

	mu.Lock()
	defer mu.Unlock()

	if received.Environment != "staging" {
		t.Errorf("environment = %q, want staging", received.Environment)
	}
}
