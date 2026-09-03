// FILE: internal/web/middleware.go
// ROLE:  Cross-cutting request behaviour — request IDs, access logging, and
//        panic recovery — implemented as the standard
//        func(http.Handler) http.Handler wrapper shape.
// WHY:   Every request needs a correlation ID, a log line, and a guarantee
//        that a bug in one handler can't take down the whole process. If we
//        wrote that logic into each handler we'd repeat it forever and
//        inevitably forget it somewhere. A middleware chain lets every
//        route get all three for free, and lets us reorder or swap them in
//        one place.
// TEACHES: middleware as function composition — http.Handler wrapping
//        http.Handler, nothing more; context.Context as the mechanism for
//        passing per-request values (the request ID) down through layers
//        that don't know about each other; capturing the status code by
//        wrapping http.ResponseWriter, since the stdlib gives you no other
//        way to observe what a handler wrote; recover() as the last line of
//        defense against a goroutine crash from a single request.
// READ AFTER: internal/web/errors.go

package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Middleware wraps a Handler to add behaviour before and/or after it runs.
// This is the entire "framework": a function that takes a Handler and
// returns a Handler. Nothing about it is specific to this project — it's
// the same shape every Go middleware library builds on top of.
type Middleware func(http.Handler) http.Handler

// chain applies middlewares to h in order, so that the first middleware in
// the list is the outermost — it sees the request first and the response
// last. chain(h, A, B, C) behaves as A(B(C(h))).
func chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

type contextKey int

const requestIDKey contextKey = iota

// requestIDFromContext returns the request ID stashed by withRequestID, or
// "" if none is present (e.g. code called outside a request, such as a
// test that builds its own context).
func requestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// withRequestID assigns each incoming request a short random ID, stores it
// in the request's context so downstream handlers and logging can read it
// without threading it through every function signature, and echoes it
// back as a response header so a client (or curl -v) can correlate a
// response with a server log line.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// newRequestID returns 8 random bytes as hex. That's 64 bits of entropy —
// not meant to be unguessable (it's not a security token), just unique
// enough that two requests logged a second apart are never confused.
func newRequestID() string {
	var b [8]byte
	// crypto/rand.Read on the stdlib's default reader does not fail in
	// practice on any platform this project targets; if the OS entropy
	// source is broken, the process has bigger problems than an unset
	// request ID, so we fall back to the all-zero ID rather than crash a
	// request over a logging convenience.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// statusRecorder wraps http.ResponseWriter to capture the status code a
// handler sent. The stdlib gives no way to ask a ResponseWriter "what
// status did you write?" after the fact, so logging middleware — which
// runs *after* the handler — has to intercept the call.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	// A handler that calls Write without calling WriteHeader first gets
	// an implicit 200, per net/http's documented behavior — mirror that
	// here so the logged status always matches what the client saw.
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	return r.ResponseWriter.Write(b)
}

// withLogging logs one structured line per request: method, path, status,
// duration, and the request ID that ties it back to withRequestID. It must
// run *inside* withRequestID (see NewRouter's doc comment) so the request
// it times and logs is the one already carrying an ID in its context, not
// the ID-less request that arrived before withRequestID touched it.
func withLogging(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			logger.LogAttrs(r.Context(), slog.LevelInfo, "request",
				slog.String("request_id", requestIDFromContext(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Duration("duration", time.Since(start)),
			)
		})
	}
}

// withRecover converts a panic anywhere in the handler chain into a 500
// response instead of a crashed connection (net/http's own recovery
// already stops one panicking request from killing the whole process —
// every request runs in its own goroutine — but without this, the client
// gets a bare connection reset and we get no log line explaining why).
func withRecover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					err, ok := rec.(error)
					if !ok {
						err = fmt.Errorf("panic: %v", rec)
					}
					writeError(w, r, logger, http.StatusInternalServerError, "internal server error", err)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
