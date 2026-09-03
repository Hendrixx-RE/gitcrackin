package web

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHealthz_OK(t *testing.T) {
	router := NewRouter(testLogger())

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("status field = %q, want %q", body.Status, "ok")
	}
}

func TestHealthz_RejectsWrongMethod(t *testing.T) {
	router := NewRouter(testLogger())

	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	// ServeMux's method-aware "GET /healthz" pattern responds 405 to any
	// other method on the same path, not 404 — that's how a caller knows
	// the resource exists but the verb is wrong. This is exactly the
	// distinction a naive catch-all "/" registration would collapse; see
	// dispatch's doc comment in router.go.
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}

	var body errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Error != "method not allowed" {
		t.Errorf("error message = %q, want %q", body.Error, "method not allowed")
	}
}

func TestUnknownRoute_Returns404WithJSONBody(t *testing.T) {
	router := NewRouter(testLogger())

	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message")
	}
}

func TestRouter_SetsRequestIDHeader(t *testing.T) {
	router := NewRouter(testLogger())

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	id := rec.Header().Get("X-Request-ID")
	if id == "" {
		t.Fatal("expected X-Request-ID header to be set")
	}
	if len(id) != 16 { // 8 bytes, hex-encoded
		t.Errorf("X-Request-ID = %q, want 16 hex characters", id)
	}
}

func TestRouter_TwoRequestsGetDifferentRequestIDs(t *testing.T) {
	router := NewRouter(testLogger())

	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Header().Get("X-Request-ID")
	}

	id1, id2 := get(), get()
	if id1 == id2 {
		t.Errorf("expected distinct request IDs, got %q twice", id1)
	}
}

// TestNewRouter_LogsTheSameRequestIDItSendsToTheClient is the regression
// test for the middleware-ordering bug described in NewRouter's doc
// comment: withRequestID must be outermost, or the access log line ends
// up with an empty request_id even though the response's X-Request-ID
// header is correctly set.
func TestNewRouter_LogsTheSameRequestIDItSendsToTheClient(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	router := NewRouter(logger)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	headerID := rec.Header().Get("X-Request-ID")
	if headerID == "" {
		t.Fatal("expected X-Request-ID header to be set")
	}

	var logLine struct {
		RequestID string `json:"request_id"`
	}
	line := strings.TrimSpace(logBuf.String())
	if err := json.Unmarshal([]byte(line), &logLine); err != nil {
		t.Fatalf("decoding log line %q: %v", line, err)
	}
	if logLine.RequestID != headerID {
		t.Errorf("logged request_id = %q, want it to match the X-Request-ID header %q", logLine.RequestID, headerID)
	}
}
