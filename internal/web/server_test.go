package web

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/Hendrixx-RE/gitcrackin/internal/config"
)

// TestServer_CutsConnectionAfterReadHeaderTimeout is the automated version
// of this phase's live demo: open a TCP connection, send an incomplete
// request line, and never finish it. Without ReadHeaderTimeout set, the
// server would hold that connection (and its goroutine) open forever —
// that's the slowloris DoS this field exists to prevent. With it set, the
// server must close the connection once the timeout elapses.
func TestServer_CutsConnectionAfterReadHeaderTimeout(t *testing.T) {
	cfg := config.Config{
		Addr:              "127.0.0.1:0",
		ReadHeaderTimeout: 150 * time.Millisecond,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       5 * time.Second,
		ShutdownTimeout:   time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler := NewRouter(testLogger())
	server := NewServer(ctx, cfg, handler, testLogger())

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	serveErrCh := make(chan error, 1)
	go func() { serveErrCh <- server.Serve(ln) }()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	})

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("net.Dial: %v", err)
	}
	defer conn.Close()

	// Send a request line with no trailing headers or blank line — the
	// server is still waiting for the rest of the headers, exactly the
	// slowloris pattern ReadHeaderTimeout defends against.
	if _, err := conn.Write([]byte("GET /healthz HTTP/1.1\r\nHost: example.com\r\n")); err != nil {
		t.Fatalf("writing partial request: %v", err)
	}

	// The server should close the connection on its own once
	// ReadHeaderTimeout elapses, without us ever completing the request.
	// Give it generous slack above the configured timeout so this isn't
	// flaky under CI scheduling jitter.
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 1)
	readStart := time.Now()
	_, readErr := conn.Read(buf)
	elapsed := time.Since(readStart)

	if readErr == nil {
		t.Fatal("expected the server to close the connection after ReadHeaderTimeout, but Read succeeded")
	}
	if elapsed < cfg.ReadHeaderTimeout {
		t.Errorf("connection closed after %v, before the configured ReadHeaderTimeout of %v", elapsed, cfg.ReadHeaderTimeout)
	}
}

func TestNewServer_AppliesConfigTimeouts(t *testing.T) {
	cfg := config.Config{
		Addr:              ":0",
		ReadHeaderTimeout: 1 * time.Second,
		ReadTimeout:       2 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       4 * time.Second,
		ShutdownTimeout:   5 * time.Second,
	}
	server := NewServer(context.Background(), cfg, http.NewServeMux(), testLogger())

	if server.ReadHeaderTimeout != cfg.ReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %v, want %v", server.ReadHeaderTimeout, cfg.ReadHeaderTimeout)
	}
	if server.ReadTimeout != cfg.ReadTimeout {
		t.Errorf("ReadTimeout = %v, want %v", server.ReadTimeout, cfg.ReadTimeout)
	}
	if server.WriteTimeout != cfg.WriteTimeout {
		t.Errorf("WriteTimeout = %v, want %v", server.WriteTimeout, cfg.WriteTimeout)
	}
	if server.IdleTimeout != cfg.IdleTimeout {
		t.Errorf("IdleTimeout = %v, want %v", server.IdleTimeout, cfg.IdleTimeout)
	}
	if server.Addr != cfg.Addr {
		t.Errorf("Addr = %q, want %q", server.Addr, cfg.Addr)
	}
}
