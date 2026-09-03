// FILE: internal/web/errors.go
// ROLE:  One place to turn "something went wrong" into an HTTP response.
// WHY:   Without a shared helper, every handler invents its own error body
//        shape (plain text here, JSON there, a stack trace leaked to the
//        client somewhere else). That inconsistency is itself a small
//        security bug: a handler that forgets to set Content-Type, or that
//        writes an internal error message straight to the response body,
//        leaks implementation detail to whoever is poking at the API.
// TEACHES: centralising error responses so handlers stay declarative
//        ("this failed, here's why") instead of each hand-rolling
//        http.Error calls; why a 500 body should never contain the actual
//        Go error string (that goes to the log, not the wire).
// READ AFTER: internal/web/middleware.go

package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// errorResponse is the wire shape for every error this server returns.
// Deliberately small: a machine-readable status text and nothing else.
// The real diagnostic detail (stack trace, wrapped error, request ID)
// goes to the structured log via the *slog.Logger passed to writeError,
// keyed by the same request ID the client sees in X-Request-ID — that's
// the join key for "client saw a 500, what actually happened."
type errorResponse struct {
	Error string `json:"error"`
}

// writeError writes a JSON error body and logs the underlying cause
// (which may be nil for expected 4xx responses). status drives both the
// HTTP status line and the log level: 5xx logs at Error, everything else
// at Warn, so an operator's error-level alerts fire only on server bugs,
// not on routine client mistakes.
func writeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, publicMessage string, cause error) {
	level := slog.LevelWarn
	if status >= 500 {
		level = slog.LevelError
	}
	logger.LogAttrs(r.Context(), level, "request error",
		slog.Int("status", status),
		slog.String("public_message", publicMessage),
		slog.Any("cause", cause),
	)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// Encoding errors here would mean the connection is already broken
	// (client gone, write past a deadline) — nothing left to do but drop
	// it; there's no second response we could send instead.
	_ = json.NewEncoder(w).Encode(errorResponse{Error: publicMessage})
}
