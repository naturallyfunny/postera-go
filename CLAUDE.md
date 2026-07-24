# CLAUDE.md

Guidance for working in this repository.

## What this is

`postera` (module `go.naturallyfunny.dev/postera`, Go 1.25) is an SDK for an agent's
**prospective memory** — its ability to **wake its future self**. An agent schedules a
`Posterum` (a self-addressed message for a future moment); `Postarius` coordinates a
`Store` (persistence) and a `Queue` (infrastructure scheduling, e.g. Cloud Tasks). When
the moment arrives the trigger fires, and the agent "receives" its own message and acts
on it — instead of only reacting when called.

The `Queue` is the dumb edge (it just makes the scheduled call); **self-awakening** is
the capability built on top. Postera is one of three sibling SDKs (see `bigpicture.md`):
**Chronica** = episodic memory (past), **Cognita** = semantic memory (planned),
**Postera** = prospective memory (future).

## Design principles (keep these true)

- **Stdlib-only core.** The root `postera` package imports only the standard library.
  Third-party deps (`pgx`, Cloud Tasks client) stay quarantined in the `postgres/` and
  `cloudtasks/` adapters, behind the `Store`/`Queue` interfaces. Do not add a
  third-party import to the root package.
- **Errors over panics.** Constructors and options return `error`; no production path
  panics, including on nil dependencies.
- **Identity is filtering, not access control.** See below — do not turn it into
  enforcement.

## Public surface

`Postarius`, configured with `With{Human,Agent,Session}FromContext`,
`WithMetadataEntryFromContext(metaKey, ctxKey)`, `WithTimezoneFromContext` /
`WithDefaultTimezone`, and optional `WithLogger`:

- `Create(ctx, CreateArgs{Message, TriggerAt})` — stamps identity + resolves timezone
  from context, enqueues the trigger, persists.
- `ListUpcoming(ctx)` — the caller's posterums from now onward, filtered by context
  identity.
- `Cancel(ctx, id)` — removes by ID, scoped to the caller's context identity.

ADK integration lives in a separate module, `go.naturallyfunny.dev/adk`, which takes
`*Postarius` directly.

## The identity model (most important to preserve)

Identity comes from **context** (configured keys) and operations honor it **uniformly
and permissively**:

- **Create stamps** it, **ListUpcoming filters** by it, **Cancel scopes** to it
  (out-of-scope → `ErrNotFound`, indistinguishable from missing → no cross-scope
  enumeration).
- **Empty identity = no constraint** (system/admin callers).
- Identity is **filtering/propagation, not access control** — every check is bypassable
  by anyone holding the `Store`. Authn, authz, and tenant isolation belong **above** this
  SDK. The SDK *enables* the consumer's enforcement (populate ctx once in middleware); it
  never *enforces*. See README "The identity model".
- `WithLogger(*slog.Logger)` emits a `Warn` only when an identity key is configured but
  the context value is empty (a likely unscoped-by-mistake op). Silent by default; a
  diagnostic, not a guard.

Do not re-introduce per-call identity params or a `WithEnforcement`-style option — both
break the symmetry that all three methods read identity from context, and both are false
affordances (the check is bypassable regardless).

## Adapter notes

- **cloudtasks**: `Enqueue` guards the schedule horizon (`~29s < lead < ~29d`) and
  returns `ErrScheduleOutOfRange`; task name is derived from `Posterum.ID` so enqueue is
  idempotent (`AlreadyExists` is treated as success). `WithTargetURL` validates
  scheme+host. `WithRetry(maxAttempts, baseDelay)` is opt-in and retries only transient
  gRPC codes.
- **postgres**: `WithAutoMigrate` runs embedded SQL migrations; every migration file is
  written to be idempotent because the runner re-executes all files on each startup.
  `WithRetry` is opt-in and retries only transient failures (`pgconn.SafeToRetry`,
  connection-class / serialization / deadlock SQLSTATEs). The table is `postera`.

## Commands

```bash
go test ./...        # all tests
go test -race ./...  # race detector
go vet ./...
go build ./...
gofmt -l .           # must be empty
```

## Versioning

v0 / unstable (`v0.x.0`). Breaking changes ride minor bumps in v0. The module path has
no version suffix (correct for v0/v1).

The `v1.x` range (v1.0.0–v1.16.0) was published by mistake and is retracted: `go.mod`
carries `retract [v1.0.0, v1.17.0]` (self-inclusive) in the highest tag, so `@latest`
resolves to the v0.x line. Keep that `retract` block, and do not publish any v1.x ≥
v1.18.0 without re-extending the range.
