# P0 — HTTP server foundations

## What we built

A `gitcrackind` binary that starts an `net/http` server, answers `GET
/healthz` with `{"status":"ok"}`, and shuts down cleanly on `SIGINT`/
`SIGTERM` instead of dropping connections mid-flight. Every request gets a
random request ID (returned as `X-Request-ID` and included in a structured
JSON log line), every panic is caught and turned into a `500` instead of
killing the connection, and every timeout `http.Server` supports
(`ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`) is set
explicitly from config instead of left at Go's "wait forever" zero value.
Config comes from `GITCRACKIND_*` environment variables with documented
defaults, and fails the process at startup — not three requests in — if a
value doesn't parse. There is no database, no git logic, and no auth yet:
this phase is purely "can we run an HTTP server that won't fall over or
leak resources under a slow or hostile client."

## File-by-file

- **`go.mod`** — declares the module and a `go 1.23` minimum, chosen
  deliberately below this sandbox's actual installed toolchain so the
  module stays buildable by any recent public Go release and any Docker
  `golang` base image, rather than implicitly requiring whatever
  environment-specific toolchain happened to run `go mod init`.
- **`cmd/gitcrackind/main.go`** — the process entrypoint. Loads config,
  builds the router and server, starts listening in a goroutine, and
  blocks on either a listen error or a shutdown signal. Owns the *only*
  `os.Exit` calls and the *only* signal handling in the codebase. Without
  it there is no runnable program — `internal/config` and `internal/web`
  are libraries with no `main`.
- **`internal/config/config.go`** — turns `GITCRACKIND_*` env vars into a
  typed, validated `Config`. Every other file trusts `Config`'s values
  without re-validating them. Without it, timeouts would have to be
  hardcoded or parsed ad hoc in multiple places, and a malformed env var
  would surface as a confusing runtime symptom instead of a clear startup
  error.
- **`internal/web/server.go`** — `NewServer` builds a `*http.Server` with
  every timeout wired from `Config`, plus `BaseContext` tied to the
  process's root context. Without it, `main.go` would have to duplicate
  this configuration, and it would be easy to add a new timeout field to
  `Config` and forget to actually apply it.
- **`internal/web/router.go`** — builds the `http.ServeMux` route table,
  wraps ServeMux's built-in 404/405 responses in this project's JSON error
  shape via `dispatch`, and assembles the middleware chain in the order
  that keeps request IDs, logging, and panic recovery correct relative to
  each other. Without it there is no `/healthz` route and no consistent
  error shape.
- **`internal/web/middleware.go`** — `withRequestID`, `withLogging`,
  `withRecover`, and the `chain` helper that composes them. Without it,
  every route added in later phases would have to reimplement
  correlation IDs, access logging, and panic safety itself.
- **`internal/web/errors.go`** — `writeError`, the one function that turns
  a status + message + underlying cause into a response body and a log
  line. Without it, `dispatch` and `withRecover` would each invent their
  own error body shape, and a `500` could leak a raw Go error string to
  the client.
- **`Makefile`** — `build`, `run`, `test`, `vet`, `fmt`, `up`, `down`,
  `logs`, `clean`. Kept to what P0 actually has; no `migrate`/`psql`/`seed`
  targets yet because those operate on things (a database, migrations)
  that don't exist until P1.
- **`docker-compose.yml`** / **`Dockerfile`** — containerize the
  `gitcrackind` binary itself. Deliberately scoped to *only* the app: no
  Postgres service. The stack table in `plan.md` is explicit that Phase 1
  is what sets up Postgres in Docker; adding it here would mean P0 ships a
  service nothing in P0's code talks to.
- **`.gitignore`** — ignores `/bin/`, the `make build` output directory, so
  a compiled binary never gets committed by accident.

## The interesting lines

**1. `internal/config/config.go` — fail fast on a bad duration, not a bad default:**

```go
if d, err := time.ParseDuration(v); err != nil {
    return 0, fmt.Errorf("config: %s=%q is not a valid duration: %w", key, v, err)
}
```

The tempting shortcut is "if it doesn't parse, use the default." That
silently hides operator typos (`GITCRACKIND_READ_TIMEOUT=5s` typo'd as
`5esec`) behind a value nobody chose. Returning an error here means the
process refuses to start, and the reason is in the exit message, not
discovered later as "why is this timeout not what I set."

**2. `internal/web/server.go` — `BaseContext` ties every connection to the process lifecycle:**

```go
BaseContext: func(net.Listener) context.Context { return ctx },
```

Without this, every request's `context.Context` descends from
`context.Background()`, completely disconnected from `main.go`'s
shutdown signal. With it, a handler that checks `r.Context().Err()` can
observe "the process is shutting down" through the same `context.Context`
mechanism it already uses for cancellation — one cancellation signal,
not two.

**3. `internal/web/router.go` — why `withRequestID` has to be outermost (a bug this phase actually shipped and caught):**

```go
return chain(dispatch(mux, logger),
    withRequestID,
    withLogging(logger),
    withRecover(logger),
)
```

`withRequestID` attaches an ID by building a *new* `*http.Request` (via
`r.WithContext(ctx)`) and passing that new value to whatever runs next —
it cannot reach backward and mutate a `*http.Request` an outer middleware
is already holding. The first version of this file had `withLogging`
outermost and `withRequestID` innermost; `withLogging` kept its own,
ID-less copy of the request for the entire call, so the JSON log line's
`request_id` field was always `""` even though the `X-Request-ID`
response header (mutated in place on the shared `http.ResponseWriter`)
was correct. Running the live demo — `curl -v` plus reading the server's
log line side by side — is what surfaced this; it's now pinned down by
`TestNewRouter_LogsTheSameRequestIDItSendsToTheClient`.

**4. `internal/web/router.go` — `dispatch` distinguishes 404 from 405 without a catch-all route:**

```go
h, pattern := mux.Handler(r)
if pattern != "" {
    h.ServeHTTP(w, r)
    return
}
rec := &discardResponseWriter{}
h.ServeHTTP(rec, r)
status := rec.status
```

The obvious way to give 404s a JSON body is `mux.Handle("/",
notFoundHandler)`. That's a trap: a bare `"/"` pattern matches *every*
method too, so a `POST /healthz` — which should be `405 Method Not
Allowed` — instead matches the catch-all and silently becomes a `404`.
`mux.Handler(r)` returns the handler ServeMux would run without
registering anything, so we can run it against a throwaway
`ResponseWriter`, read the status it chose (ServeMux's own fallback
already distinguishes 404 from 405 correctly), and re-emit that status in
JSON.

**5. `internal/web/middleware.go` — capturing status when a handler never calls `WriteHeader`:**

```go
func (r *statusRecorder) Write(b []byte) (int, error) {
    if !r.wroteHeader {
        r.status = http.StatusOK
        r.wroteHeader = true
    }
    return r.ResponseWriter.Write(b)
}
```

`http.ResponseWriter` has no method to ask "what status did you send?"
after the fact — that's specifically why `withLogging` needs a wrapper
type at all. A handler is allowed to skip `WriteHeader` and just call
`Write`, which implicitly sends `200`; the recorder has to replicate that
exact stdlib rule or it would log a wrong status (`0`) for the common
case.

**6. `cmd/gitcrackind/main.go` — shutdown needs a context that isn't the one that triggered it:**

```go
case <-ctx.Done():
    shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
```

`ctx` is already cancelled at this point — that's what got us into this
branch. Passing an already-cancelled context to `server.Shutdown` would
make it return immediately, abandoning every in-flight request instead of
waiting for them. `shutdownCtx` is a fresh context, descended from
`context.Background()`, with its own bounded deadline — "stop accepting
new work" and "give up waiting after N seconds" are two different
signals, and conflating them defeats graceful shutdown entirely.

## Decisions & rejected alternatives

- **`net/http` stdlib routing over a third-party router (chi, gorilla).**
  Go 1.22's `http.ServeMux` gained method-aware pattern routing
  (`"GET /healthz"`), which covers what this project needs through several
  more phases. Per `plan.md`'s "No black boxes" rule, a router is not a
  place we're trying to learn a *library's* abstraction — we want to see
  the request dispatch itself.
- **JSON structured logging (`log/slog`) over a logging library.** `slog`
  shipped in the stdlib in Go 1.21 and does everything this phase needs
  (structured key-value fields, levels, a JSON handler). Pulling in a
  third-party logger would mean learning its API instead of the
  underlying concept (structured logs are just consistently-shaped
  key-value records).
- **Request ID as 8 random bytes over a UUID library.** A correlation ID
  only needs to be locally unique enough that two log lines aren't
  confused — it is never used as a security token or a database key. 64
  bits from `crypto/rand` is simpler than pulling in a UUID dependency and
  teaches the same "random bytes, hex-encoded" idea a full UUID would.
- **JSON error bodies over `http.Error`'s plain text.** `http.Error`
  (and ServeMux's built-in 404/405 handlers) write plain text. This
  project standardizes on `{"error": "..."}` because every future JSON
  API endpoint (P4 onward serves git's binary protocol, but the web UI's
  JSON endpoints will want this) should have one predictable error shape,
  not "whatever the code path that failed happened to write."
- **`docker-compose.yml` scoped to the app only, not Postgres.** `plan.md`
  lists `docker-compose.yml` under P0's build list, but the Stack table is
  explicit that "Phase 1 sets up docker compose for Postgres." Adding a
  Postgres service now, before any code in the repo talks to a database,
  would be scope creep into P1 disguised as "the file already existed."
  P1 adds a second service to this same file when it's actually needed.
- **`/healthz` with no dependency checks, no separate `/readyz` yet.**
  Liveness ("is the process able to run at all") and readiness ("can it
  currently serve traffic") are different questions with different
  failure responses (liveness failing means "restart me"; readiness
  failing means "stop sending me traffic, don't restart me"). Conflating
  them means an orchestrator restarts a perfectly healthy process because
  a dependency it doesn't even have yet had a blip. There's no dependency
  to check until P1, so `/readyz` is deferred rather than built against
  nothing.

## The concept

**`http.Server`'s zero-value timeouts are not a safe default — they are no
limit at all.** `http.ListenAndServe(addr, handler)`, the version shown in
almost every "hello world" HTTP tutorial, builds a server where
`ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, and `IdleTimeout` are
all zero, meaning "wait forever." Each one closes off a distinct, real
denial-of-service pattern:

- No `ReadHeaderTimeout` → a client that opens a connection and sends one
  header byte every 30 seconds ("slowloris") holds a goroutine and a file
  descriptor hostage indefinitely. A server with a fixed-size worker pool
  or file descriptor limit degrades to zero availability with only a
  handful of such connections.
- No `ReadTimeout` → a client that finishes headers instantly but
  trickles the *body* in forever has the same effect, just later in the
  request lifecycle.
- No `WriteTimeout` → a client that stops reading the response (a dead
  peer, a deliberately misbehaving client) leaves the server blocked on a
  `Write` call that will never complete.
- No `IdleTimeout` → keep-alive connections that finish a request and then
  just... sit there, never close, accumulate file descriptors forever.

This generalizes past HTTP: **any server that accepts a byte stream from
an untrusted client needs an explicit bound on how long it will wait at
every stage of that stream** — the initial handshake, the headers, the
body, the response. "No timeout" is not a neutral default; it is an
unbounded resource commitment to whoever connects, trusted or not. The
fix is never subtle or clever — it is just naming every stage and setting
a number for it, which is exactly what `internal/config/config.go` and
`internal/web/server.go` do.

The second transferable idea is **graceful shutdown as two separate
concerns**: stop accepting *new* work, and give existing work a bounded
amount of time to finish. `server.Shutdown(ctx)` handles the first
automatically (closes the listener, refuses new connections); the second
is why `shutdownCtx` needs its own timeout separate from the signal that
triggered shutdown. A process that just exits on `SIGTERM` corrupts
whatever was mid-flight; a process that waits forever for in-flight work
never actually exits when something is stuck. Bounded graceful shutdown is
the compromise, and it shows up in every server that owns state longer
than a single request (a DB transaction, a streaming file write — this
project's later phases lean on this same pattern for in-progress git
pushes).

## Failure modes

- **Slowloris without a header timeout.** If `ReadHeaderTimeout` were
  accidentally set to `0` (config's validation currently only rejects
  *negative or zero* values for this one field precisely because of this
  risk — see `Load`'s validation), the server would again be vulnerable to
  the exact attack this phase exists to close off. `TestServer_
  CutsConnectionAfterReadHeaderTimeout` and the live demo both exist to
  make this regression loud, not silent.
- **A leaked goroutine per slow client if `WriteTimeout` were unset.** A
  client that opens a connection and never reads the response would block
  a `Write` forever; each such client is one goroutine that never returns,
  growing without bound under sustained slow/malicious traffic.
- **Silent config drift.** If a future contributor adds a new timeout
  field to `Config` but forgets to wire it into `NewServer`, `go vet`
  and the tests won't catch it — `TestNewServer_AppliesConfigTimeouts`
  only checks the fields that exist today. This is a real gap: it's an
  argument for extending that test whenever `Config` grows a field meant
  to reach `http.Server`.
- **Request ID collisions under extremely high volume.** 64 bits of
  random ID has a real (if astronomically small at this project's scale)
  birthday-bound collision probability. It is fine for log correlation —
  a false match just means two unrelated log lines briefly look related —
  but it must never be treated as a uniqueness guarantee for anything
  security- or data-integrity-sensitive (a future phase's idempotency
  keys, for instance, need a different, larger source of uniqueness).
- **Panic recovery only covers the handler goroutine `net/http` already
  isolates.** `withRecover` stops one request's panic from producing a
  broken connection instead of a `500`, but it cannot save the process
  from a panic in a goroutine a handler spawns and doesn't itself recover
  in — that goroutine's panic still crashes the whole process. This
  matters once later phases spawn goroutines for streaming git protocol
  data.
- **`ErrorLog` routes `http.Server`'s internal errors through `slog`, but
  `slog`'s own I/O can still block.** If the log destination (currently
  `os.Stdout`) were ever swapped for something that can block or fail (a
  slow log-shipping pipe), a burst of errors could back up request
  handling through the shared logger. Not a concern with `os.Stdout`
  today, but a reason `internal/observe` gets its own phase (P11) instead
  of being bolted on ad hoc.

## Poke at it yourself

Build and run the server directly (no Docker required — `docker-
compose.yml` containerizes the same binary for later phases that need a
multi-service stack, but P0 has nothing else to compose):

```bash
make build
./bin/gitcrackind
# {"time":"...","level":"INFO","msg":"listening","addr":":8080"}
```

In a second terminal, hit the health check and watch the response headers
and JSON body:

```bash
curl -v http://localhost:8080/healthz
```

Confirm method-aware routing gives a `405`, not a `404`, for the wrong
verb on a real route — and a `404` for a route that doesn't exist at all:

```bash
curl -i -X POST http://localhost:8080/healthz   # 405, {"error":"method not allowed"}
curl -i http://localhost:8080/nope               # 404, {"error":"not found"}
```

Watch the two responses' `X-Request-ID` headers land in the server's log
output (first terminal) as the same value in the `request_id` field —
this is the exact thing `TestNewRouter_LogsTheSameRequestIDItSendsToTheClient`
guards against regressing:

```bash
curl -s -D - http://localhost:8080/healthz -o /dev/null | grep -i x-request-id
```

Now the main event: hold a connection open with an incomplete request and
watch `ReadHeaderTimeout` cut it, with no application log line at all
(the cut happens inside `net/http` before your handler chain ever sees the
request):

```bash
exec 3<>/dev/tcp/localhost/8080
printf 'GET /healthz HTTP/1.1\r\nHost: localhost\r\n' >&3   # no trailing \r\n — headers incomplete
time head -c 100 <&3   # blocks, then returns empty after ~5s (default ReadHeaderTimeout)
```

Try a shorter timeout to see the same behavior faster:

```bash
GITCRACKIND_READ_HEADER_TIMEOUT=1s ./bin/gitcrackind &
exec 3<>/dev/tcp/localhost/8080
printf 'GET /healthz HTTP/1.1\r\n' >&3
time head -c 100 <&3   # now cuts off after ~1s instead of 5s
```

Send `SIGTERM` and watch graceful shutdown in the log — "shutdown signal
received" followed by "shutdown complete", not a bare killed process:

```bash
kill -TERM $(pgrep -f bin/gitcrackind)
```

Trigger a panic-recovery path (there's no route that panics on purpose
yet, so exercise it via the test instead — it's the fastest way to see the
exact JSON body a `500` produces):

```bash
go test ./internal/web/ -run TestWithRecover_TurnsPanicInto500 -v
```

Run everything this phase's verification gates require:

```bash
make test
make vet
gofmt -l .   # should print nothing
```
