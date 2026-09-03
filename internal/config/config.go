// FILE: internal/config/config.go
// ROLE:  Turn process environment variables into a typed, validated Config
//        struct that the rest of the program treats as immutable.
// WHY:   Every timeout in internal/web/server.go comes from here. If we let
//        handlers or the server construct their own ad-hoc defaults, the
//        values drift and nobody can answer "what's our read timeout?" by
//        looking in one place. Config also fails fast: a malformed env var
//        (e.g. GITCRACKIND_READ_TIMEOUT=banana) must stop the process at
//        startup, not surface as a mysterious runtime bug three requests in.
// TEACHES: 12-factor config (env vars, not files, for a single binary);
//        fail-fast validation vs. silently falling back to a default;
//        why every network-facing timeout needs an explicit, named value
//        instead of inheriting Go's zero-value (no timeout at all).
// READ AFTER: (none — this is the first file loaded)

package config

import (
	"fmt"
	"os"
	"time"
)

// Config holds every tunable the HTTP server needs. Zero values are never
// used at runtime — Load always returns either a fully-populated Config or
// an error, so callers never have to wonder whether a field was set.
type Config struct {
	// Addr is the "host:port" the HTTP server listens on.
	Addr string

	// ReadHeaderTimeout bounds how long the server will wait to receive a
	// client's request headers. This is the single most important DoS
	// control in this file: without it, a client can open a connection,
	// send one byte every 30 seconds, and hold a worker goroutine (and a
	// file descriptor) hostage forever ("slowloris").
	ReadHeaderTimeout time.Duration

	// ReadTimeout bounds the entire request read, headers and body. It
	// protects against a client that finishes headers quickly but then
	// trickles the body in forever.
	ReadTimeout time.Duration

	// WriteTimeout bounds how long we allow ourselves to write the
	// response. It protects against a client that stops reading the
	// response (e.g. a dead TCP peer) and leaves our goroutine blocked
	// on a Write forever.
	WriteTimeout time.Duration

	// IdleTimeout bounds how long a keep-alive connection may sit idle
	// between requests before we close it and reclaim the file
	// descriptor.
	IdleTimeout time.Duration

	// ShutdownTimeout bounds how long graceful shutdown waits for
	// in-flight requests to finish before main.go gives up and exits
	// anyway.
	ShutdownTimeout time.Duration
}

// defaults mirror what a small, single-tenant HTTP server should ship with
// out of the box. They are deliberately conservative: it is much easier to
// notice "requests are timing out, loosen this" in development than to
// notice "we have no timeout, we are exposed" in production.
const (
	defaultAddr              = ":8080"
	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 10 * time.Second
	defaultWriteTimeout      = 10 * time.Second
	defaultIdleTimeout       = 120 * time.Second
	defaultShutdownTimeout   = 10 * time.Second
)

// Load reads GITCRACKIND_* environment variables and returns a validated
// Config. Every variable is optional (a documented default applies), but if
// a variable *is* set, it must parse — an unparsable value is a config bug
// the operator needs to see immediately, not a value we silently ignore.
func Load() (Config, error) {
	cfg := Config{
		Addr:              getEnv("GITCRACKIND_ADDR", defaultAddr),
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		ReadTimeout:       defaultReadTimeout,
		WriteTimeout:      defaultWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
		ShutdownTimeout:   defaultShutdownTimeout,
	}

	var err error
	if cfg.ReadHeaderTimeout, err = getDuration("GITCRACKIND_READ_HEADER_TIMEOUT", defaultReadHeaderTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ReadTimeout, err = getDuration("GITCRACKIND_READ_TIMEOUT", defaultReadTimeout); err != nil {
		return Config{}, err
	}
	if cfg.WriteTimeout, err = getDuration("GITCRACKIND_WRITE_TIMEOUT", defaultWriteTimeout); err != nil {
		return Config{}, err
	}
	if cfg.IdleTimeout, err = getDuration("GITCRACKIND_IDLE_TIMEOUT", defaultIdleTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = getDuration("GITCRACKIND_SHUTDOWN_TIMEOUT", defaultShutdownTimeout); err != nil {
		return Config{}, err
	}

	if cfg.Addr == "" {
		return Config{}, fmt.Errorf("config: GITCRACKIND_ADDR must not be empty")
	}
	if cfg.ReadHeaderTimeout <= 0 {
		return Config{}, fmt.Errorf("config: GITCRACKIND_READ_HEADER_TIMEOUT must be positive, got %s", cfg.ReadHeaderTimeout)
	}

	return cfg, nil
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// getDuration parses a duration env var using time.ParseDuration (accepts
// forms like "5s", "250ms", "2m").
func getDuration(key string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s=%q is not a valid duration: %w", key, v, err)
	}
	return d, nil
}
