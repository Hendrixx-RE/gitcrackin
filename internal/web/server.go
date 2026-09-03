// FILE: internal/web/server.go
// ROLE:  Construct a *http.Server configured with every timeout that keeps
//        one misbehaving connection from degrading the whole process.
// WHY:   http.Server's zero value has NO timeouts at all — ReadTimeout,
//        WriteTimeout, IdleTimeout, and ReadHeaderTimeout all default to
//        "wait forever". A server built with http.ListenAndServe(addr,
//        handler) and nothing else is trivially DoS-able by a client that
//        opens a connection and never finishes sending a request. This
//        file exists so that fact is impossible to forget: every timeout
//        is set explicitly, sourced from config, in one constructor.
// TEACHES: what each of http.Server's four timeout fields actually bounds
//        (header read vs. full read vs. write vs. idle-between-requests)
//        and which DoS pattern each one closes off; BaseContext as the
//        mechanism that ties every connection's request context to the
//        server's own lifecycle, so shutdown can be observed from inside
//        a handler via r.Context().Done().
// READ AFTER: internal/web/router.go

package web

import (
	"context"
	"log/slog"
	"net"
	"net/http"

	"github.com/Hendrixx-RE/gitcrackin/internal/config"
)

// NewServer builds an *http.Server ready to run. It does not start
// listening — that's ListenAndServe/Shutdown's job, called from
// cmd/gitcrackind/main.go where the graceful-shutdown signal handling
// lives, so this file stays pure configuration with no goroutines of its
// own to reason about.
//
// ctx is main.go's root context — the one signal.NotifyContext cancels on
// SIGINT/SIGTERM. Wiring it in as BaseContext means every connection's
// per-request context.Context descends from it, so a handler that checks
// r.Context().Err() can observe "the process is shutting down" even before
// Shutdown closes its listener.
func NewServer(ctx context.Context, cfg config.Config, handler http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:    cfg.Addr,
		Handler: handler,

		// The DoS control described in config.go's doc comment: bounds
		// slowloris-style header trickling.
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		// Bounds the full request read (headers + body).
		ReadTimeout: cfg.ReadTimeout,
		// Bounds how long a Write to the client may block.
		WriteTimeout: cfg.WriteTimeout,
		// Bounds how long a keep-alive connection may sit idle before we
		// reclaim its file descriptor.
		IdleTimeout: cfg.IdleTimeout,

		// http.Server logs its own internal errors (e.g. a failed
		// Accept) through this *log.Logger if we don't set one, straight
		// to os.Stderr with no structure. Routing it through slog keeps
		// every line of server output in the same structured format.
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),

		BaseContext: func(net.Listener) context.Context { return ctx },
	}
}
