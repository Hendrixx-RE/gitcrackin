# gitcrackin — Build Plan


---

## Context

`gitcrackin` is currently an empty repo — one README, one commit. The goal is a working self-hosted git code-hosting site (clone/push over HTTPS and SSH, repo browsing, issues, pull requests with real merges, orgs/teams, webhooks).

**But the deliverable is not really the website.** The stated goal is *learning backend internals in depth and with clarity*. The website is the vehicle — it's chosen well, because a git host forces you through nearly every hard backend problem there is: streaming HTTP with backpressure, a binary wire protocol, subprocess management, authn/authz, relational modelling with real transactional constraints, filesystem consistency, async job delivery, and security surface (path traversal, SSRF, timing attacks, command injection).

Therefore this plan is structured as a **curriculum with a running artifact**, not a feature backlog. Every phase ships working code *and* a written explanation of that code. Frontend is deliberately minimal (server-rendered Go templates) and carries **no explanation obligation**.

---

## The Explanation Contract (the most important section)

This governs all backend work, whether I do it inline or delegate it to a worker/subagent. Every subagent prompt for backend work will embed this contract verbatim, and I will not mark a phase complete until its docs exist and I have verified they match the code that was actually written.

**1. Every backend Go file opens with a doc block:**

```go
// FILE: internal/git/pktline.go
// ROLE:  Encode/decode git's pkt-line framing — the length-prefixed packet
//        format every byte of the git wire protocol travels inside.
// WHY:   Git's smart protocol is not JSON and not newline-delimited. Without
//        this file we cannot read the client's "want"/"have" negotiation, so
//        clone and push are impossible.
// TEACHES: binary framing, length-prefixed protocols, io.Reader composition,
//        why you must never trust a length field from the network.
// READ AFTER: internal/git/exec.go
```

**2. Every phase produces `docs/NN-<topic>.md`**, written by whoever wrote the code, following this exact skeleton:

| Section | Contents |
|---|---|
| **What we built** | Plain-English summary of the phase's behaviour |
| **File-by-file** | *Every* file touched: what it is, why it exists, what breaks without it |
| **The interesting lines** | The 5–15 non-obvious lines, quoted with an explanation of the reasoning |
| **Decisions & rejected alternatives** | What else we could have done and the concrete reason we didn't |
| **The concept** | The transferable backend lesson, explained independent of this codebase |
| **Failure modes** | How this code breaks in production: races, leaks, unbounded growth, attacks |
| **Poke at it yourself** | Copy-pasteable commands to observe the behaviour live |

**3. Delegation rule.** Any worker assigned backend work receives: the contract above, the phase spec, the existing code conventions, and an explicit instruction that *the explanation doc is a required deliverable, not a nice-to-have — a phase with working code and no doc is an incomplete phase.* Workers report back with their doc, and I relay the substance to you rather than just saying "done".

**4. No black boxes.** If a library would hide a concept we're trying to learn, we write it ourselves. Concretely: raw SQL over an ORM, `net/http` over a web framework, hand-rolled pkt-line over a git library, hand-rolled migration runner over `golang-migrate`. We *do* use libraries where the concept isn't the lesson (argon2 hashing, the `x/crypto/ssh` transport primitives).

---

## Stack

| Layer | Choice | Why this teaches more |
|---|---|---|
| Language | **Go 1.27** (installed) | Explicit error handling, visible concurrency, no hidden allocation. What Gitea/Forgejo/GitLab-shell are built on. |
| HTTP | **`net/http` stdlib**, `http.ServeMux` pattern routing (Go 1.22+) | You see the `Server`, the `Handler` chain, the `ResponseWriter` — no framework between you and the socket. |
| DB | **Postgres 16 in Docker**, `pgx` via `database/sql`, **raw SQL** | Every query is visible. You learn pooling, prepared statements, transactions, isolation, indexes — not an ORM's DSL. |
| Migrations | Hand-rolled runner (~100 lines) over `.sql` files | Teaches schema versioning + advisory locks. |
| Git | **Real `git` binary** driven via `os/exec`; **protocol hand-rolled** | You implement pkt-line, `/info/refs`, `git-upload-pack`, `git-receive-pack` yourself. Git does the packfile math. |
| SSH | `golang.org/x/crypto/ssh`, in-process daemon (no OS users, no `authorized_keys`) | Teaches pubkey auth, SSH channels, `exec` requests, command allowlisting. |
| Frontend | `html/template`, server-rendered, near-zero JS | Intentionally boring. Not explained. |
| Tests | stdlib `testing`, `httptest`, table-driven, golden files, throwaway Postgres per run | Teaches test isolation and fixtures. |

**No Postgres client is installed locally** — Phase 1 sets up `docker compose` for Postgres and we use `docker compose exec` for `psql`.

---

## Directory layout

```
gitcrackin/
├── cmd/
│   ├── gitcrackind/main.go      # server entrypoint: wire deps, start HTTP+SSH, graceful shutdown
│   └── gcadmin/main.go          # CLI: create-user, reset-password, run-migrations, requeue-job
├── internal/
│   ├── config/config.go         # env → typed Config, fail fast on missing secrets
│   ├── database/
│   │   ├── db.go                # pool construction + tuning, Tx helper
│   │   ├── migrate.go           # migration runner w/ advisory lock
│   │   └── migrations/*.sql     # 0001_users.sql, 0002_repos.sql, ...
│   ├── store/                   # data access, one file per aggregate, raw SQL
│   │   ├── users.go  repos.go  sshkeys.go  tokens.go  sessions.go
│   │   ├── orgs.go   issues.go pulls.go    comments.go
│   │   ├── webhooks.go  jobs.go
│   │   └── errors.go            # ErrNotFound / ErrConflict — DB errors → domain errors
│   ├── git/                     # ★ the heart of the project
│   │   ├── exec.go              # safe subprocess: env scrub, ctx timeout, stderr capture
│   │   ├── repo.go              # bare repo lifecycle, path derivation, traversal defense
│   │   ├── pktline.go           # ★ pkt-line reader/writer
│   │   ├── advertise.go         # ★ ref advertisement for /info/refs
│   │   ├── service.go           # ★ upload-pack / receive-pack streaming
│   │   ├── hooks.go             # install pre/post-receive shims that call back to us
│   │   ├── objects.go           # cat-file --batch long-lived reader
│   │   ├── refs.go  log.go  diff.go  blame.go
│   │   └── merge.go             # merge-base, merge-tree, conflict detection
│   ├── auth/
│   │   ├── password.go          # argon2id hash/verify
│   │   ├── session.go           # opaque tokens, hashed at rest
│   │   ├── token.go             # personal access tokens (git push credentials)
│   │   ├── middleware.go        # cookie + Basic auth extraction → context
│   │   ├── csrf.go              # double-submit token
│   │   └── authz.go             # ★ single permission-resolution function
│   ├── web/
│   │   ├── server.go  router.go  middleware.go  render.go  errors.go
│   │   └── handlers/            # gitsmarthttp.go, repo.go, browse.go, issues.go,
│   │                            # pulls.go, orgs.go, settings.go, auth.go
│   ├── sshd/server.go  auth.go  session.go
│   ├── jobs/queue.go  worker.go # SKIP LOCKED queue + worker pool
│   ├── webhook/deliver.go  sign.go
│   └── observe/log.go  metrics.go
├── web/templates/  web/static/   # frontend — not explained
├── docs/                        # ★ the learning output, one file per phase
├── docker-compose.yml           # postgres (+ later: the app itself)
├── Makefile                     # run, test, migrate, psql, seed, lint
└── plan.md                      # this file
```

---

## Data model (built up across phases, not all at once)

`users` · `sessions` · `access_tokens` · `ssh_keys` · `organizations` · `org_members` · `teams` · `team_members` · `team_repos` · `repositories` · `collaborators` · `issues` · `issue_comments` · `pull_requests` · `pr_reviews` · `review_comments` · `webhooks` · `webhook_deliveries` · `jobs` · `audit_log`

Deliberate teaching choices baked into the schema:
- **`repositories` has both an `id` and an `owner_id + name` unique index** — teaches surrogate vs natural keys.
- **Per-repo `issues.number`** allocated via `UPDATE ... RETURNING` on a counter column, *not* `MAX(number)+1` — teaches read-modify-write races and how to actually fix them.
- **Soft-delete on repos** with a partial unique index — teaches partial indexes and why `UNIQUE(owner_id, name)` alone breaks name reuse.
- **`jobs` polled with `FOR UPDATE SKIP LOCKED`** — teaches row locking and safe multi-worker queues.

---

## Phases

Each phase = working code + `docs/NN-*.md` + tests + a demo you run yourself.

### P0 — HTTP server foundations
**Build:** `cmd/gitcrackind/main.go`, `internal/config`, `internal/web/{server,router,middleware,errors}.go`, `/healthz`, `Makefile`, `docker-compose.yml`.
**Teaches:** what `http.Server` actually does per connection; read/write/idle timeouts and the DoS each one prevents; middleware as `func(http.Handler) http.Handler`; `context.Context` cancellation propagation; graceful shutdown and why `SIGTERM` handling matters; structured logging with a request ID.
**Demo:** `curl -v`, then hold a connection open past `ReadHeaderTimeout` and watch it get cut.

### P1 — Postgres, migrations, the store layer
**Build:** `internal/database/{db,migrate}.go`, `migrations/0001_init.sql`, `internal/store/{users,errors}.go`.
**Teaches:** connection pooling (`MaxOpenConns`/`MaxIdleConns`/`ConnMaxLifetime` — and what a leaked connection looks like); prepared statements and why parameterised queries are *structurally* immune to injection; `pgx` vs `lib/pq`; transactions and the `defer tx.Rollback()` idiom; `EXPLAIN ANALYZE`; B-tree indexes; unique-violation error codes → domain errors.
**Demo:** run a query with and without an index and read the plans; deliberately leak a connection and watch the pool starve.

### P2 — Authentication
**Build:** `internal/auth/{password,session,token,middleware,csrf}.go`, signup/login/logout handlers, `0002_auth.sql`.
**Teaches:** argon2id vs bcrypt vs SHA-256, memory-hard KDFs, and why salt is per-user; timing attacks and `subtle.ConstantTimeCompare`; opaque session tokens hashed at rest vs JWTs (and why we chose sessions — revocation); cookie flags `HttpOnly`/`Secure`/`SameSite` and what each blocks; CSRF via double-submit; user enumeration via response-time and error-message differences.
**Demo:** `psql` to see hashes, tamper with a session cookie, measure login timing across valid/invalid users.

### P3 — Repositories on disk
**Build:** `internal/git/{exec,repo}.go`, `internal/store/repos.go`, create-repo handler, `0003_repos.sql`.
**Teaches:** bare vs non-bare repos and the actual `.git` directory layout; `os/exec` done safely (never `sh -c`, env scrubbing, `CommandContext` timeouts, capturing stderr, killing process groups); **path traversal** — why `filepath.Join(root, userInput)` is a vulnerability and how to validate; fanned-out storage paths; the ordering problem of "row in DB" vs "dir on disk" and reconciliation.
**Demo:** `tree` a fresh bare repo; try to create a repo named `../../etc` and watch it get rejected.

### P4 — ★ Smart HTTP: clone and push
The centerpiece. Budget the most time here.
**Build:** `internal/git/{pktline,advertise,service,hooks}.go`, `internal/web/handlers/gitsmarthttp.go`.
Routes: `GET /{owner}/{repo}.git/info/refs?service=…`, `POST …/git-upload-pack`, `POST …/git-receive-pack`.
**Teaches:** pkt-line framing (4-byte hex length, `0000` flush, `0001` delim) and why you must bound a network-supplied length; the v0/v2 capability advertisement; the want/have negotiation; **streaming both directions with `io.Copy` and `Flusher`** — why buffering the whole packfile in memory is a self-inflicted OOM; chunked transfer encoding; `Content-Encoding: gzip` on push bodies; HTTP Basic auth carrying a PAT; `pre-receive`/`post-receive` hooks as the enforcement point for branch protection and the trigger point for events; non-fast-forward rejection.
**Demo:** `GIT_TRACE_PACKET=1 git clone http://…` — you will read your own protocol implementation's bytes in the terminal. Then push, and watch your hook fire.

### P5 — Read path: browsing repos
**Build:** `internal/git/{objects,refs,log,diff}.go`, browse handlers + templates.
**Teaches:** git's four object types and content-addressing; `cat-file --batch` as a **long-lived subprocess** you write to and read from (process pooling — vastly faster than fork-per-file, and teaches you deadlock-by-pipe-buffer); plumbing vs porcelain commands and why porcelain output is unsafe to parse; `-z` NUL-delimited output; MIME sniffing and `Content-Disposition` for raw blobs; ETag/`If-None-Match` caching; pagination that doesn't `OFFSET`.
**Demo:** `git cat-file --batch` by hand in a terminal, then watch the server do the same thing.

### P6 — SSH transport
**Build:** `internal/sshd/{server,auth,session}.go`, `internal/store/sshkeys.go`, key-management UI.
**Teaches:** the SSH connection → channel → request layering; public key auth (we identify the *user* by key fingerprint, then authorize the repo separately); why an in-process daemon beats OS users + `authorized_keys` `command=` forced commands; parsing and strictly allowlisting the `git-upload-pack '/owner/repo.git'` exec request; host keys and TOFU; running the same `internal/git` service layer over a different transport — the payoff of having kept protocol separate from HTTP.
**Demo:** `ssh -vvv`, then `git clone ssh://git@localhost:2222/you/repo.git`.

### P7 — Issues, comments, permissions
**Build:** `internal/store/{issues,comments}.go`, `internal/auth/authz.go`, handlers, `0004_issues.sql`.
**Teaches:** per-repo sequence allocation without races; the N+1 query problem, demonstrated then fixed with a join/`ANY($1)`; optimistic concurrency with a `version` column; cursor pagination; a **single** `Can(user, action, resource)` resolution function and why scattering permission checks across handlers is how real breaches happen; markdown rendering and stored XSS.
**Demo:** two concurrent issue creations racing for the same number; toggle logging to see N+1 in the query log.

### P8 — ★ Pull requests and the merge engine
**Build:** `internal/git/merge.go`, `internal/store/pulls.go`, PR handlers, `0005_pulls.sql`.
**Teaches:** `merge-base` and three-dot vs two-dot diffs; **three-way merge** and where conflicts actually come from; `git merge-tree` for *conflict prediction without a working tree* (this is how GitHub greys out the merge button); fast-forward vs merge commit vs squash vs rebase, each implemented; writing a commit with plumbing (`hash-object`/`mktree`/`commit-tree`/`update-ref`); `update-ref` compare-and-swap as concurrency control on a ref; hidden refs (`refs/pull/N/head`) and why PR branches survive a deleted source branch; the "PR is stale" recomputation problem.
**Demo:** open a PR, push a conflicting commit to the base, watch mergeability flip; merge all four ways and inspect the resulting graph with `git log --graph`.

### P9 — Orgs, teams, and the authz matrix
**Build:** `internal/store/orgs.go`, extend `authz.go`, `0006_orgs.sql`.
**Teaches:** RBAC modelling in SQL; permission resolution across user → team → org → repo with a recursive CTE; per-request authz caching; visibility rules (private repos must 404, not 403 — teaches information leakage); scoped access tokens; audit logging.

### P10 — Async: job queue and webhooks
**Build:** `internal/jobs/{queue,worker}.go`, `internal/webhook/{deliver,sign}.go`, `0007_jobs.sql`.
**Teaches:** a Postgres queue with `FOR UPDATE SKIP LOCKED` (and why naive `SELECT ... UPDATE` double-delivers); a worker pool with bounded concurrency and graceful drain; at-least-once delivery → idempotency keys; exponential backoff with jitter and dead-lettering; HMAC-SHA256 payload signing and constant-time verification; **SSRF defense** — a user-supplied webhook URL pointed at `169.254.169.254` or `localhost` is the classic cloud-metadata breach, so we resolve-then-validate the IP and pin the dial.
**Demo:** kill a worker mid-job and watch it get redelivered; point a webhook at the metadata IP and watch it get refused.

### P11 — Observability and hardening
**Build:** `internal/observe/*`, rate limiting, `pprof`, resource limits.
**Teaches:** structured logging with request correlation, and log injection; RED metrics; `pprof` for heap and goroutine leaks (a leaked goroutine per clone is a very real failure here); token-bucket rate limiting; request body size caps; per-repo concurrent-clone limits; timeouts at every layer; a load test against the clone endpoint with the profiler attached.

### P12 — Frontend pass
Bring the templates up to usable. **No explanation obligation.**

---

## Verification

Per-phase gates — a phase is done when all four pass:

1. `make test` — `go test ./...` green, including a fresh Postgres per run.
2. **The live demo** listed in the phase runs and behaves as described.
3. `docs/NN-*.md` exists, follows the contract skeleton, and its file-by-file section covers every file the phase touched.
4. `go vet ./...` clean.

End-to-end acceptance for the finished project:

```bash
make up && make migrate && make seed
git clone http://localhost:8080/alice/demo.git      # P4
cd demo && echo hi >> f && git commit -am x && git push
git clone ssh://git@localhost:2222/alice/demo.git   # P6
# open a PR in the UI from a feature branch, verify mergeability,
# merge it four ways on four branches, confirm graph shape       # P8
# register a webhook at a local listener, push, confirm HMAC signature # P10
GIT_TRACE_PACKET=1 git clone http://localhost:8080/alice/demo.git   # read your own protocol
```

Recurring learning instruments, used throughout: `GIT_TRACE_PACKET=1`, `GIT_TRACE=1`, `GIT_CURL_VERBOSE=1`, `git cat-file --batch`, `git count-objects -v`, `curl --http1.1 -v`, `docker compose exec db psql`, `go tool pprof`.

---

## Working agreement

- **Sequential phases, one at a time.** After each phase I stop, walk you through the doc, and you ask questions before we move on. This is a course, not a sprint.
- **Delegation** to workers is available for parallelizable phases; every such worker carries the Explanation Contract and I verify its doc against the diff before accepting it. Default is inline unless you ask for parallelism.
- **Scope guard:** no CI runners, no container registry, no git-LFS, no federation, no HA. Single-node, single-binary, local Postgres.
- **Adjustable:** if a phase turns out too shallow or too deep for you, say so and we re-cut the remaining phases.

## Open question for later (not blocking)

P4 will implement protocol **v0** first because it's simpler to read on the wire, then add **v2** (`Git-Protocol: version=2`) as a follow-on once v0 works — v2's `ls-refs`/`fetch` command model is a good second lesson, not a good first one.
