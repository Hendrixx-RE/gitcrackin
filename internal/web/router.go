// FILE: internal/web/router.go
// ROLE:  Build the http.Handler tree: route table + middleware chain.
// WHY:   This is the one place that answers "what happens to an incoming
//        request" end to end — which pattern matches, and what runs before
//        the handler gets it. Keeping routing and middleware wiring in one
//        function (NewRouter) means there's no second place to check when
//        debugging "why didn't my request get a request ID".
// TEACHES: http.ServeMux's Go 1.22+ pattern syntax ("GET /healthz") and
//        method-aware routing without a third-party router; that a mux is
//        itself just an http.Handler, so wrapping it in middleware is no
//        different from wrapping any other handler; the difference between
//        "no pattern matches this path" (404) and "a pattern matches the
//        path but not this method" (405), and why a naive catch-all "/"
//        registration collapses that distinction; ServeMux.Handler as the
//        supported way to inspect routing before it runs, since ServeMux
//        has no NotFoundHandler hook to override directly.
// READ AFTER: internal/web/middleware.go

package web

import (
	"log/slog"
	"net/http"
)

// NewRouter builds the complete request-handling pipeline: a ServeMux
// carrying the route table, dispatched through a thin wrapper that
// normalizes ServeMux's built-in 404/405 responses into this project's
// JSON error shape, wrapped in the standard middleware chain.
//
// Order matters, and it is easy to get backwards: withRequestID must be
// outermost. It works by attaching the ID to a *new* request context and
// passing that new *http.Request down to whatever comes next — it cannot
// reach back up and modify a request some outer middleware is already
// holding. If logging wrapped requestID instead of the other way around,
// withLogging would keep the original, ID-less *http.Request it was
// called with and log an empty request_id on every line, even though the
// response header (set on the shared http.ResponseWriter, which — unlike
// the request — really is mutated in place) would show a real ID. This
// project shipped that exact bug for one commit; the regression test is
// TestNewRouter_LogsTheSameRequestIDItSendsToTheClient.
//
// withRecover sits innermost, immediately around dispatch, so a panic in
// routing or a handler is caught before it can unwind through (and skip)
// the logging and request-ID layers.
func NewRouter(logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", healthzHandler)

	return chain(dispatch(mux, logger),
		withRequestID,
		withLogging(logger),
		withRecover(logger),
	)
}

// dispatch wraps mux so that requests ServeMux itself would answer with
// its built-in plain-text fallback instead go through writeError, keeping
// every error response — ours and ServeMux's — in the same JSON shape.
//
// A naive way to override the 404 body is to register a catch-all "/"
// pattern (this project's first attempt did exactly that). That's a trap:
// once "/" is registered, it matches *any* method too, so a POST to
// "/healthz" — which should be 405 Method Not Allowed — instead matches
// the catch-all and silently becomes a 404. ServeMux only synthesizes a
// 405 when it can prove no registered pattern matches the method+path at
// all; a wildcard fallback makes that proof impossible.
//
// mux.Handler(r) sidesteps this: it returns the handler ServeMux *would*
// run and the pattern that matched, without registering anything extra.
// When pattern is "", nothing in our route table matched and h is
// ServeMux's internal fallback — which still correctly distinguishes 404
// from 405 by status code. We run it against a throwaway ResponseWriter to
// learn that status, then re-emit it ourselves in JSON.
func dispatch(mux *http.ServeMux, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			h.ServeHTTP(w, r)
			return
		}

		rec := &discardResponseWriter{}
		h.ServeHTTP(rec, r)

		status := rec.status
		if status == 0 {
			status = http.StatusNotFound
		}
		message := "not found"
		if status == http.StatusMethodNotAllowed {
			message = "method not allowed"
		}
		writeError(w, r, logger, status, message, nil)
	})
}

// discardResponseWriter captures only the status code a handler chose,
// discarding whatever body it wrote. It exists solely so dispatch can ask
// "what status would ServeMux's fallback have used?" without letting that
// fallback's plain-text body reach the real client.
type discardResponseWriter struct {
	header http.Header
	status int
}

func (d *discardResponseWriter) Header() http.Header {
	if d.header == nil {
		d.header = make(http.Header)
	}
	return d.header
}

func (d *discardResponseWriter) Write(b []byte) (int, error) { return len(b), nil }

func (d *discardResponseWriter) WriteHeader(status int) { d.status = status }

// healthzHandler reports liveness: if the process can run this handler at
// all, it answers 200. It deliberately does not check dependencies (a
// database, in later phases) — that's what a separate /readyz will be for
// once there is a dependency to check. Conflating the two makes a
// container orchestrator kill and restart a perfectly healthy process just
// because its database had a blip.
func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
