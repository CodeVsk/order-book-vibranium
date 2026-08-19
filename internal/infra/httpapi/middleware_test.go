package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestCorrelationID_HeaderPresent(t *testing.T) {
	const testID = "test-request-12345"

	var capturedID string
	handler := CorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Request-Id", testID)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Header().Get("X-Request-Id") != testID {
		t.Fatalf("expected response header %q, got %q", testID, w.Header().Get("X-Request-Id"))
	}
	if capturedID != testID {
		t.Fatalf("expected captured ID %q, got %q", testID, capturedID)
	}
}

func TestCorrelationID_HeaderAbsent(t *testing.T) {
	var capturedID string
	handler := CorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	responseID := w.Header().Get("X-Request-Id")
	if _, err := uuid.Parse(responseID); err != nil {
		t.Fatalf("generated ID should be a valid UUID, got %q: %v", responseID, err)
	}
	if responseID != capturedID {
		t.Fatalf("expected response ID %q to match captured ID %q", responseID, capturedID)
	}
}

func TestCorrelationID_HeaderExceedsMaxLength(t *testing.T) {
	oversizedID := strings.Repeat("x", maxRequestIDLen+1)

	var capturedID string
	handler := CorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Request-Id", oversizedID)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	responseID := w.Header().Get("X-Request-Id")
	if responseID == oversizedID {
		t.Fatalf("oversized header should not be used; got %q", responseID)
	}
	if _, err := uuid.Parse(responseID); err != nil {
		t.Fatalf("should generate a valid UUID when inbound header is oversized, got %q: %v", responseID, err)
	}
	if responseID != capturedID {
		t.Fatalf("expected response ID %q to match captured ID %q", responseID, capturedID)
	}
}

func TestRequestLogging_ExplicitWriteHeader(t *testing.T) {
	observedCore, logs := observer.New(zapcore.InfoLevel)
	logger := zap.New(observedCore)

	handler := RequestLogging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("created"))
	}))

	req := httptest.NewRequest("POST", "/api/test", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "test-id-123"))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, w.Code)
	}
	if w.Body.String() != "created" {
		t.Fatalf("expected body %q, got %q", "created", w.Body.String())
	}

	logEntries := logs.All()
	if len(logEntries) != 1 {
		t.Fatalf("should log exactly one request, got %d", len(logEntries))
	}

	entry := logEntries[0]
	if entry.Message != "http_request" {
		t.Fatalf("expected message %q, got %q", "http_request", entry.Message)
	}
	if entry.Level != zapcore.InfoLevel {
		t.Fatalf("expected level %v, got %v", zapcore.InfoLevel, entry.Level)
	}

	fields := entry.ContextMap()
	if fields["method"] != "POST" {
		t.Fatalf("expected method %q, got %q", "POST", fields["method"])
	}
	if fields["path"] != "/api/test" {
		t.Fatalf("expected path %q, got %q", "/api/test", fields["path"])
	}
	if fields["status"] != int64(http.StatusCreated) {
		t.Fatalf("expected status %d, got %v", http.StatusCreated, fields["status"])
	}
	if fields["request_id"] != "test-id-123" {
		t.Fatalf("expected request_id %q, got %q", "test-id-123", fields["request_id"])
	}

	latency, ok := fields["latency"]
	if !ok {
		t.Fatalf("latency field should be logged")
	}
	_, ok = latency.(time.Duration)
	if !ok {
		t.Fatalf("latency should be stored as time.Duration, got %T", latency)
	}
}

func TestRequestLogging_ImplicitDefaultStatus(t *testing.T) {
	observedCore, logs := observer.New(zapcore.InfoLevel)
	logger := zap.New(observedCore)

	handler := RequestLogging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))

	req := httptest.NewRequest("GET", "/hello", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "test-id-456"))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}
	if w.Body.String() != "hello" {
		t.Fatalf("expected body %q, got %q", "hello", w.Body.String())
	}

	logEntries := logs.All()
	if len(logEntries) != 1 {
		t.Fatalf("should log exactly one request, got %d", len(logEntries))
	}

	entry := logEntries[0]
	fields := entry.ContextMap()
	if fields["method"] != "GET" {
		t.Fatalf("expected method %q, got %q", "GET", fields["method"])
	}
	if fields["path"] != "/hello" {
		t.Fatalf("expected path %q, got %q", "/hello", fields["path"])
	}
	if fields["status"] != int64(http.StatusOK) {
		t.Fatalf("expected status %d, got %v", http.StatusOK, fields["status"])
	}
	if fields["request_id"] != "test-id-456" {
		t.Fatalf("expected request_id %q, got %q", "test-id-456", fields["request_id"])
	}
}

func TestRequestIDFromContext_Missing(t *testing.T) {
	ctx := context.Background()
	id := RequestIDFromContext(ctx)
	if id != "" {
		t.Fatalf("expected empty string, got %q", id)
	}
}

func TestRequestIDFromContext_Present(t *testing.T) {
	ctx := context.WithValue(context.Background(), requestIDKey, "my-id-789")
	id := RequestIDFromContext(ctx)
	if id != "my-id-789" {
		t.Fatalf("expected %q, got %q", "my-id-789", id)
	}
}
