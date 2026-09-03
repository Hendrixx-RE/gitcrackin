// FILE: cmd/gitcrackind/main.go
// ROLE:  Process entrypoint — load config, wire the HTTP server, run it,
//        and shut it down cleanly on SIGINT/SIGTERM.
// WHY:   Somewhere has to own the process lifecycle: when to start
//        accepting connections, and — just as important — how to stop.
//        A server that dies via os.Exit or an unhandled signal drops every
//        in-flight request and can corrupt anything mid-write (later
//        phases: a half-written pack file, a half-applied migration). This
//        file is deliberately thin: config and http.Server construction
//        live in internal/config and internal/web so they're testable in
//        isolation; main.go only sequences them.
// TEACHES: graceful shutdown — the difference between a process that dies
//        instantly and one that stops accepting new work, finishes what's
//        in flight, then exits; signal.NotifyContext as the idiomatic way
//        to turn OS signals into a cancellable context.Context; why
//        Shutdown needs its own bounded context distinct from the one
//        that triggered it (otherwise "stop everything" also cancels the
//        thing responsible for stopping everything gracefully).
// READ AFTER: internal/web/server.go

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/Hendrixx-RE/gitcrackin/internal/config"
	"github.com/Hendrixx-RE/gitcrackin/internal/web"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", slog.Any("error", err))
		os.Exit(1)
	}

	// ctx is cancelled the moment we receive SIGINT or SIGTERM. stop()
	// restores the default signal behavior afterward so a second Ctrl-C
	// (or a stuck shutdown) still kills the process immediately instead
	// of being swallowed forever.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	handler := web.NewRouter(logger)
	server := web.NewServer(ctx, cfg, handler, logger)

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", slog.String("addr", cfg.Addr))
		// ListenAndServe always returns a non-nil error; ErrServerClosed
		// is the expected one when Shutdown is what stopped it, so it's
		// not a real failure and shouldn't be reported as one.
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			logger.Error("server failed", slog.Any("error", err))
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")

		// Shutdown needs a context that is NOT ctx: ctx is already
		// cancelled (that's what got us here), and passing a cancelled
		// context to Shutdown would make it return immediately without
		// waiting for in-flight requests at all. shutdownCtx instead
		// gets its own fresh deadline, bounded by config so a stuck
		// request can't hang the process forever on exit.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", slog.Any("error", err))
			os.Exit(1)
		}
		logger.Info("shutdown complete")
	}
}
