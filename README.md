# Postera

```
   _ \    _ \    ___| __ __|  ____|   _ \      \    
  |   |  |   | \___ \    |    __|    |   |    _ \   
  ___/   |   |       |   |    |      __ <    ___ \  
 _|     \___/  _____/   _|   _____| _| \_\ _/    _\ 
                                                    
```

[![Go Reference](https://pkg.go.dev/badge/go.naturallyfunny.dev/postera.svg)](https://pkg.go.dev/go.naturallyfunny.dev/postera)
[![Go Report Card](https://goreportcard.com/badge/go.naturallyfunny.dev/postera)](https://goreportcard.com/report/go.naturallyfunny.dev/postera)
[![CI](https://github.com/naturallyfunny/postera-go/actions/workflows/test.yml/badge.svg)](https://github.com/naturallyfunny/postera-go/actions/workflows/test.yml)
[![Go 1.25](https://img.shields.io/badge/go-1.25-00ADD8.svg)](https://go.dev/doc/devel/release#go1.25)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache_2.0-blue.svg)](LICENSE)

**Postera lets an agent wake its future self.** A `Posterum` is a message an agent
addresses to a future moment; when that moment arrives, the trigger fires and the
agent "receives" its own message and acts on it — instead of only reacting when
called.

```go
// "At 09:00 tomorrow, remind myself to follow up." Then go idle.
posterum, err := postarius.Create(ctx, postera.CreateArgs{
    Message:   "Follow up with the user on the Q3 report",
    TriggerAt: "2026-06-15T09:00:00", // local wall-clock; timezone comes from context
})
```

Postera is a deliberately thin orchestration layer, not a framework. It coordinates
two swappable pieces — a `Store` (persistence) and a `Queue` (infrastructure
scheduling, e.g. Google Cloud Tasks) — and stays out of the way of everything else.
Its **core package depends only on the Go standard library**; the third-party
adapters live in subpackages you opt into (see [Design principles](#design-principles)).

In cognitive terms Postera is an agent's **prospective memory** — remembering to act
later. It is one of three sibling SDKs covering an agent's memory across time:

| SDK          | Memory type    | What                                  |
| ------------ | -------------- | ------------------------------------- |
| Chronica     | episodic       | what has happened (session history)   |
| Cognita      | semantic       | what is known (knowledge)             |
| **Postera**  | **prospective**| **what will happen (self-awakening)** |

---

## Contents

- [Why this shape](#why-this-shape)
- [Design principles](#design-principles)
- [The surface at a glance](#the-surface-at-a-glance)
- [Install](#install)
- [Quick start](#quick-start)
- [The identity model](#the-identity-model)
- [Timezones](#timezones)
- [Design decisions & trade-offs](#design-decisions--trade-offs)
- [Non-goals](#non-goals)
- [Roadmap](#roadmap)
- [Verification](#verification)
- [Compatibility](#compatibility)
- [Layout](#layout)
- [License](#license)

## Why this shape

An agent that only acts when invoked cannot *remember to do something later*. The
usual fixes are heavy: a cron worker, a durable-workflow engine, a bespoke scheduler
service. Each drags in a runtime you now own and operate.

Postera takes the smaller position. Two facts about "wake me later" drive the whole
design:

1. **The scheduling is already a solved, dumb-edge problem.** Cloud Tasks (or any
   equivalent) will make an HTTP call at a chosen time and retry it. Postera does not
   re-implement that — it *drives* it. The `Queue` is the dumb edge; **self-awakening**
   is the capability built on top.
2. **The message is self-addressed.** What makes a scheduled HTTP call a *prospective
   memory* is that it carries the agent's own identity and context forward — so when
   it fires, the agent knows it is hearing from its past self, and why.

So Postera is the thin coordination between *what to remember* (the `Store`) and *when
to be woken* (the `Queue`), plus the identity/timezone plumbing that makes the woken
call meaningful. Everything the SDK does follows from keeping that layer thin and
honest.

## Design principles

These rules hold across the whole codebase. Later [decisions](#design-decisions--trade-offs)
cite them by name.

- **Stdlib-only core.** The root `postera` package imports nothing outside the
  standard library. Third-party dependencies (`pgx`, the Cloud Tasks client) are
  quarantined in the `postgres/` and `cloudtasks/` adapter subpackages, behind the
  `Store` and `Queue` interfaces. Bring your own adapters and you pull zero
  third-party code from the core. *(Verified: `go list -deps .` on the root package
  lists no external modules.)*
- **Errors over panics.** Constructors and options return `error`; no production code
  path panics, including on nil dependencies. A library is a guest in someone else's
  process and must not take it down — see
  [Effective Go, "Defer, Panic, and Recover"](https://go.dev/doc/effective_go#panic).
- **Fail closed at the boundary, permissive in the middle.** Inputs are validated
  where they enter (timezone strings, datetimes, the schedule horizon, target URLs);
  identity scoping is permissive by design and is *not* a security boundary (see
  [identity model](#the-identity-model)).
- **Context carries request-scoped identity, not optional parameters.** Caller
  identity and timezone are read from `context.Context`, which is exactly the
  [sanctioned use](https://pkg.go.dev/context#pkg-overview): "request-scoped data that
  transits processes and API boundaries", not a grab-bag for optional args.
- **The SDK enables enforcement; it never enforces.** Authentication, authorization,
  and tenant isolation belong above Postera. It gives you the plumbing to make that
  easy, and is honest that it is only plumbing.

## The surface at a glance

`Postarius` is the whole agent-facing API — three methods, all reading identity and
timezone from context:

| You call         | It does                                                    | Identity from context |
| ---------------- | ---------------------------------------------------------- | --------------------- |
| `Create`         | stamps identity + timezone, enqueues the trigger, persists | **stamped** onto it   |
| `ListUpcoming`   | returns your posterums from now onward                     | **filters** the query |
| `Cancel(id)`     | removes a posterum by ID                                   | **scopes** the delete |

Behind it, two interfaces you implement or take off the shelf:

| Interface | Package                | Off-the-shelf adapter          | Responsibility                        |
| --------- | ---------------------- | ------------------------------ | ------------------------------------- |
| `Store`   | `postera` (root)       | `postera/postgres`             | durable persistence + queries         |
| `Queue`   | `postera` (root)       | `postera/cloudtasks`           | schedule / cancel the future trigger  |

## Install

```bash
go get go.naturallyfunny.dev/postera
```

Requires **Go 1.25+**. The root package has no third-party dependencies; the
`postgres` and `cloudtasks` adapters pull `jackc/pgx` and the Google Cloud Tasks
client respectively, only if you import them.

## Quick start

A complete wiring, from adapters to a scheduled self-message.

```go
package main

import (
	"context"
	"log"
	"time"

	gcptasks "cloud.google.com/go/cloudtasks/apiv2"
	"go.naturallyfunny.dev/postera"
	"go.naturallyfunny.dev/postera/cloudtasks"
	"go.naturallyfunny.dev/postera/postgres"
)

// Your own context key types — unexported, collision-free.
type (
	timezoneKey struct{}
	humanKey    struct{}
	agentKey    struct{}
	sessionKey  struct{}
)

func main() {
	ctx := context.Background()

	// 1. Store — persistence. WithAutoMigrate applies the embedded schema.
	//    (dbPool is a *pgxpool.Pool or any postgres.Querier you already have.)
	store, err := postgres.NewStore(ctx, dbPool,
		postgres.WithAutoMigrate(),
		postgres.WithRetry(3, 100*time.Millisecond), // opt-in; off by default
	)
	if err != nil {
		log.Fatal(err)
	}

	// 2. Queue — the future trigger. Cloud Tasks POSTs to your webhook at TriggerAt,
	//    carrying identity as headers so the woken call knows who it is.
	client, err := gcptasks.NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	queue, err := cloudtasks.NewQueue(client, "my-project", "us-central1", "my-queue",
		cloudtasks.WithTargetURL("https://my-service.example.com/awaken"),
		cloudtasks.WithHumanHeader("x-postera-human"),
		cloudtasks.WithAgentHeader("x-postera-agent"),
		cloudtasks.WithSessionHeader("x-postera-session"),
		cloudtasks.WithMetadataHeader("timezone", "x-postera-timezone"),
		cloudtasks.WithRetry(3, 200*time.Millisecond), // opt-in; off by default
	)
	if err != nil {
		log.Fatal(err)
	}

	// 3. Postarius — declare where identity and timezone live in context, once.
	postarius, err := postera.New(store, queue,
		postera.WithTimezoneFromContext(timezoneKey{}),
		postera.WithHumanFromContext(humanKey{}),
		postera.WithAgentFromContext(agentKey{}),
		postera.WithSessionFromContext(sessionKey{}),
		postera.WithMetadataEntryFromContext("timezone", timezoneKey{}),
	)
	if err != nil {
		log.Fatal(err)
	}

	// 4. Your middleware populates context once; every call scopes to it.
	ctx = context.WithValue(ctx, timezoneKey{}, "Asia/Jakarta")
	ctx = context.WithValue(ctx, humanKey{}, "user-123")
	ctx = context.WithValue(ctx, agentKey{}, "support-agent")
	ctx = context.WithValue(ctx, sessionKey{}, "session-456")

	// Schedule a future self-message.
	p, err := postarius.Create(ctx, postera.CreateArgs{
		Message:   "Follow up with the user on the Q3 report",
		TriggerAt: "2026-06-15T09:00:00", // local wall-clock, no timezone suffix
	})
	if err != nil {
		log.Fatal(err)
	}

	// List this caller's upcoming posterums (scoped by the identity in ctx).
	upcoming, err := postarius.ListUpcoming(ctx)
	_ = upcoming

	// Cancel it (also scoped: an out-of-scope ID reads as not found).
	if err := postarius.Cancel(ctx, p.ID); err != nil {
		log.Fatal(err)
	}
}
```

For a single-timezone deployment, swap `WithTimezoneFromContext` for
`WithDefaultTimezone(time.UTC)`. For a read-only front-end that only lists and
cancels, configure just `WithHumanFromContext` — the unset identity fields simply
impose no filter.

### ADK integration

The Agent Development Kit toolset lives in a separate module so the core stays
dependency-light. It takes `*Postarius` directly:

```go
import posteraadk "go.naturallyfunny.dev/adk"

tools, err := posteraadk.Tools(postarius) // hand `tools` to your ADK agent
```

## The identity model

This is the most important thing to understand before relying on Postera.

Identity comes from **context** — the keys you configured with the `With*FromContext`
options — and all three methods honor it **uniformly and permissively**:

- **`Create` stamps** the human/agent/session/metadata onto the posterum.
- **`ListUpcoming` filters** results by them.
- **`Cancel` scopes** to them: a posterum outside your scope is reported as
  `ErrNotFound` — *indistinguishable* from a genuinely missing ID, which prevents
  cross-scope enumeration.
- **Empty identity means no constraint.** A caller with nothing in context (system or
  admin tooling) sees and can cancel everything. This is intentional, not a hole.

**Identity here is filtering and propagation, not access control.** Every check is
bypassable by anyone who holds the `Store` (it owns the database connection). So:

> Authentication, authorization, and tenant isolation belong **above** this SDK — at
> your HTTP/gRPC layer. When you pass a model-supplied ID into `Cancel`, scope it to
> the authenticated identity there first. An unguessable ID is not the same as an
> unleakable one.

What Postera gives you is the *plumbing* to make that enforcement easy (populate
context once in middleware, and every operation is consistently scoped) — never a
guarantee it enforces on your behalf.

`WithLogger(*slog.Logger)` adds one diagnostic: a `Warn` when an identity key is
configured but its context value is empty — the likely sign that middleware forgot to
populate context, leaving an operation unscoped. It records the misconfiguration; it
does not prevent it. Silent without a logger.

## Timezones

An agent supplies `TriggerAt` as a **local wall-clock** string
(`"2006-01-02T15:04:05"`, no offset), and Postera resolves it against the timezone in
context. The point is that the local time the agent *writes* and the times you render
*back* to it share one frame — so you can truthfully tell the model "no timezone
conversion is needed." `LocalizesFromContext()` reports whether a given `Postarius` is
configured this way, so a caller can make that promise accurately.

Resolution order: timezone from context → `WithDefaultTimezone` → (for `Create`) an
error, because scheduling into an unknown zone is a mistake, not a default.

## Design decisions & trade-offs

Each decision is stated as *what we did*, *the plausible alternative*, and *why this
side of the trade-off*.

### Two interfaces (`Store` + `Queue`), split

**Choice.** Persistence and infrastructure scheduling are separate interfaces.
**Alternative.** One combined `Backend` interface.
**Why.** They are different failure domains (a DB write vs. a gRPC call) and different
swap axes — you might pair Postgres with Cloud Tasks in production and an in-memory
store with a fake queue in tests. Splitting keeps each adapter single-purpose and each
test focused.

### Identity from context, not per-call parameters

**Choice.** Read human/agent/session/timezone from `context.Context`.
**Alternative.** Per-call arguments, or a `WithEnforcement()`/`scopeByContext bool`
knob.
**Why.** Context is the [sanctioned carrier](https://pkg.go.dev/context#pkg-overview)
for request-scoped data crossing API boundaries, which is exactly what caller identity
is. Reading it uniformly gives one property worth protecting: **all three methods
agree on what "the caller" means**, populated once in middleware. A per-call param or
an enforcement toggle would be a *false affordance* — it looks like a security control
but is bypassable by any holder of the `Store` — and it would break that symmetry.
Both were considered and rejected.

### Identity is filtering, not access control

**Choice.** Permissive scoping; empty identity imposes no constraint; out-of-scope
`Cancel` returns `ErrNotFound`.
**Alternative.** Enforce ownership/authz inside the SDK.
**Why.** The check is bypassable regardless (whoever holds the `Store` owns the DB), so
enforcing here would be security theater. Being explicit that this is *plumbing for*
enforcement — not enforcement — is the honest design. See [identity model](#the-identity-model).

### Local datetime string + timezone from context

**Choice.** `TriggerAt` is a local wall-clock string; the zone comes from context.
**Alternative.** RFC 3339 with an explicit offset, or a Unix epoch.
**Why.** Agents and humans reason in local wall-clock ("9am tomorrow"), and models are
bad at offset arithmetic. Keeping one local frame end-to-end removes a whole class of
conversion bugs. **Trade-off:** the caller must put a correct IANA zone in context;
garbage-in/garbage-out is not policed (`LocalizesFromContext` reports the *config*, not
the *correctness*).

### Stdlib-only IDs (`crypto/rand`, prefixed, URL-safe)

**Choice.** 128 bits from `crypto/rand`, `base64.RawURLEncoding`, `pstr_` prefix.
**Alternative.** A UUID dependency, or a database sequence.
**Why.** No dependency, URL-safe (IDs become Cloud Tasks task-name path segments), and
the prefix makes them greppable and self-describing in logs. `rand.Read` never returns
an error as of Go 1.24, so ID generation needs no error path.

### Idempotent enqueue via deterministic task name

**Choice.** The Cloud Tasks task name is derived from `Posterum.ID`; an `AlreadyExists`
from `CreateTask` is treated as success.
**Alternative.** Random task names + dedup bookkeeping.
**Why.** A retried `Create` (or a retried enqueue) must not schedule the wake twice.
Deriving the name from the ID makes the enqueue naturally idempotent at the platform
level, with no extra state.

### Schedule-horizon guard → `ErrScheduleOutOfRange`

**Choice.** Reject a `TriggerAt` less than ~29s or more than ~29 days out, with a
sentinel error consumers can match via `errors.Is`.
**Alternative.** Pass every schedule straight to Cloud Tasks.
**Why.** Cloud Tasks rejects schedules beyond 30 days; the 29-day cap leaves margin for
the gap between the check and the call landing. The lower bound guards a race — a task
scheduled seconds out can fire *before* the upstream state that produced it is durably
written. The sentinel lets callers map a bad request to a 4xx instead of an
infrastructure 5xx.

### Opt-in retry, off by default, with a correct transient-error classifier

**Choice.** `WithRetry(maxAttempts, baseDelay)` on each adapter; without it, calls run
once. When on, only *transient* failures retry with exponential backoff — gRPC
`Unavailable`/`DeadlineExceeded`/`ResourceExhausted`/`Aborted` for Cloud Tasks;
`pgconn.SafeToRetry`, connection-class SQLSTATE (`08*`), serialization failure
(`40001`), and deadlock (`40P01`) for Postgres.
**Alternative.** Always retry with built-in defaults, or never retry at all.
**Why.** Retry *policy* (how many, how long, is this call idempotent) is an application
decision, and the GCP client already retries some failures at the gRPC layer — so
baking in defaults risks double-retrying and surprise latency. But *classifying* which
errors are safe to retry is easy to get wrong, so Postera ships the correct classifier
and lets you decide whether to use it. Off by default means no hidden latency.

### Enqueue-then-persist, with best-effort rollback

**Choice.** `Create` enqueues the trigger first, then saves; if the save fails, it
best-effort cancels the queued task. `Cancel` mirrors this (delete task, then remove
row, rollback re-enqueue on failure). When a rollback also fails, both errors are
surfaced via `errors.Join`.
**Alternative.** A distributed transaction across DB and queue (there isn't one), or
ignoring partial failure.
**Why.** Two systems with no shared transaction can always diverge; the honest move is
to pick an order, compensate on failure, and never hide a compensation that itself
failed. Ordering enqueue-first means a save failure leaves *nothing* dangling after the
rollback succeeds.

### Idempotent, re-run-every-boot migrations

**Choice.** Migrations are embedded SQL (`embed.FS`); with `WithAutoMigrate` every file
is re-executed on startup, and every file is written to be idempotent (guarded
`IF EXISTS` / column-existence checks).
**Alternative.** A version-tracking migration runner (or an external migration tool).
**Why.** Zero dependencies and dead-simple to reason about for a young schema. **Known
trade-off:** re-running all files each boot only stays correct as long as every file is
idempotent, which is a discipline the runner does not enforce. Replacing this with a
lightweight version-tracked runner is on the [roadmap](#roadmap).

## Non-goals

Boundaries are deliberate. Postera does **not**:

- **Enforce authorization or tenant isolation.** Identity is filtering, not access
  control — see [the identity model](#the-identity-model). This belongs above the SDK.
- **Run a worker or execute the wake.** Postera schedules the trigger; delivering it is
  the `Queue`'s dumb-edge job (an HTTP POST to your webhook). What your service *does*
  when woken is yours.
- **Ship metrics or tracing.** This is a young library, and our attention is on the
  core capability — feature depth and correctness — not on an observability surface we
  would then have to carry and version forever. Wiring in metrics/tracing prematurely
  buys permanent maintenance for little present value, so it is a conscious *not yet*.
  The one built-in signal is the optional `WithLogger` identity-scoping diagnostic;
  everything else composes cleanly from the outside (wrap the `Store`/`Queue`
  interfaces, or instrument your webhook). When observability earns its keep, it goes
  in behind those seams without touching the core.
- **Provide recurring / cron schedules.** A posterum is a single future moment. Recur
  by having the woken handler schedule the next one.

## Roadmap

- **Version-tracked migrations.** Replace the re-run-every-boot runner with a
  lightweight, dependency-free version-tracking runner so each migration runs exactly
  once and callers control *when* migrations apply.

## Verification

Everything above is backed by the test suite and the standard Go checks. Reproduce:

```bash
go build ./...
go vet ./...
gofmt -l .          # expected: no output
go test -race ./...
go test -cover ./...
```

Observed this session (Go 1.25, `-race` clean, `gofmt`/`vet` clean):

| Package                  | Coverage | Notes                                                        |
| ------------------------ | -------- | ----------------------------------------------------------- |
| `postera` (root)         | 81.0%    | orchestration, identity, timezone, rollback paths           |
| `postera/cloudtasks`     | 91.1%    | options, header mapping, horizon guard, retry classifier    |
| `postera/postgres`       | 51.3%    | pure logic covered (query building, retry classification, metadata); the DB-touching methods (`Save`/`Get`/`Remove`/`List`/`migrate`) require a live Postgres and are exercised via a fake `Querier`, not a server, so their driver round-trips are not counted |

The lower `postgres` number is honest and expected: unit tests deliberately do not
stand up a database. The query builder, retry-eligibility classifier, and metadata
marshalling — the parts where logic can be wrong — are covered directly.

## Compatibility

- **Go:** 1.25+ (`go.mod`).
- **Core dependencies:** none beyond the standard library. Confirm with
  `go list -deps go.naturallyfunny.dev/postera`.
- **Adapter dependencies:** `postgres` uses `jackc/pgx/v5`; `cloudtasks` uses the
  Google Cloud Tasks v2 client. You pay for these only if you import the adapter.
- **Stability:** v0.x — the API may change on minor bumps until v1. The `v1.x` tag
  range is retracted (published in error); `@latest` resolves to the v0.x line.

## Layout

```
.
├── postarius.go        # Postarius: Create / ListUpcoming / Cancel, options, identity + timezone
├── store.go            # Store interface, Query, ErrNotFound
├── queue.go            # Queue interface, ErrScheduleOutOfRange
├── cloudtasks/         # Queue adapter — Google Cloud Tasks (opt-in retry, header mapping, horizon guard)
└── postgres/           # Store adapter — pgx (opt-in retry, embedded idempotent migrations)
    └── migrations/     # embedded .sql, applied by WithAutoMigrate
```

## License

[Apache 2.0](LICENSE) © 2026 Ardian — see [`NOTICE`](NOTICE) for attribution.
