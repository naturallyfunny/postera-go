# Postera

```
   _ \    _ \    ___| __ __|  ____|   _ \      \    
  |   |  |   | \___ \    |    __|    |   |    _ \   
  ___/   |   |       |   |    |      __ <    ___ \  
 _|     \___/  _____/   _|   _____| _| \_\ _/    _\ 
                                                    
```

**Postera lets agents wake their future selves.** A `Posterum` is a message an agent sends forward to a future moment; when that moment arrives, Postera delivers it back and the agent wakes to act on it — instead of only reacting when called. Built on durable scheduling.

In cognitive terms, Postera is an agent's **prospective memory** — remembering to act later. It is one of three sibling SDKs covering an agent's memory across time:

| SDK | Memory type | What |
|---|---|---|
| Chronica | episodic | what has happened (session history) |
| Cognita | semantic | what is known (knowledge graph) |
| **Postera** | **prospective** | **what will happen (self-awakening)** |

## Background & Goals

AI agents are traditionally reactive — they act only when invoked. Postera lets an agent reach into its own future: schedule a self-addressed message for a later moment and be woken by it, carrying the human, agent, and session context it needs to know *why*. It is deliberately a thin, safe orchestration layer over a persistence store and an infrastructure scheduler — not a framework.

## Key Features

- **Self-Awakening for Agents**: An agent schedules a self-addressed message for a future moment and is woken by it when the time comes — carrying human, agent, session, and metadata context.
- **Atomic-ish Orchestration**: Coordinates Store (Persistence) and Enqueuer (Scheduler) with automatic rollback mechanisms to maintain data integrity.
- **Explicit Query Context**: Supports multi-tenant storage through first-class `Human`, `Agent`, `Session`, and `Metadata` fields owned by the caller.
- **Cloud Native**: Integrations available directly for GCP Cloud Tasks and PostgreSQL.
- **Agent-First Interface**: Postarius is the agent tool directly — `Create`, `ListUpcoming`, and `Cancel` with identity and timezone resolved from context. ADK integration is available as a separate module at `go.naturallyfunny.dev/adk`.

## Architecture

Postera operates through a central orchestrator called **Postarius**, which manages two primary interfaces:

- **Store**: Handles persistent storage of scheduled messages (e.g., using PostgreSQL).
- **Enqueuer**: Schedules infrastructure-level triggers (e.g., using GCP Cloud Tasks).

## Installation

Postera requires Go 1.25.0 or above.

```bash
go get go.naturallyfunny.dev/postera
```

## Usage Guide

### 1. Setup Postarius

Postarius requires a Store (for persistence) and an Enqueuer (for scheduling), plus options to declare how identity and timezone are propagated through context.

#### 1.1 Setup Store (Using PostgreSQL as example)

```go
import "go.naturallyfunny.dev/postera/postgres"

store, _ := postgres.NewStore(ctx, dbPool,
    postgres.WithAutoMigrate(),
)
```

#### 1.2 Setup Enqueuer (Using Cloud Tasks as example)

```go
import (
    gcptasks "cloud.google.com/go/cloudtasks/apiv2"
    "go.naturallyfunny.dev/postera/cloudtasks"
)

client, _ := gcptasks.NewClient(ctx)
defer client.Close()

enq, _ := cloudtasks.NewEnqueuer(client, "my-project", "us-central1", "my-queue",
    cloudtasks.WithTargetURL("https://my-service.example.com/webhook"),
    cloudtasks.WithHumanHeader("x-postera-human"),
    cloudtasks.WithAgentHeader("x-postera-agent"),
    cloudtasks.WithSessionHeader("x-postera-session"),
    cloudtasks.WithMetadataHeader("timezone", "x-postera-timezone"),
)
```

#### 1.3 Create Postarius

Define your own context key types, then pass them as options so Postarius knows where to read identity and timezone from context:

```go
type timezoneKey struct{}
type humanKey struct{}
type agentKey struct{}
type sessionKey struct{}
type metadataKey struct{}

postarius := postera.New(store, enq,
    postera.WithTimezoneFromContext(timezoneKey{}),
    postera.WithHumanFromContext(humanKey{}),
    postera.WithAgentFromContext(agentKey{}),
    postera.WithSessionFromContext(sessionKey{}),
    postera.WithMetadataFromContext(metadataKey{}),
)
```

For single-timezone deployments, use `WithDefaultTimezone` instead of `WithTimezoneFromContext`:

```go
postarius := postera.New(store, enq,
    postera.WithDefaultTimezone(time.UTC),
    postera.WithHumanFromContext(humanKey{}),
    // ...
)
```

#### 1.4 Use Postarius

Populate context from your middleware (HTTP, agent harness, etc.) and call Postarius directly:

```go
ctx = context.WithValue(ctx, timezoneKey{}, "Asia/Jakarta")
ctx = context.WithValue(ctx, humanKey{}, "user-123")
ctx = context.WithValue(ctx, agentKey{}, "support-agent")
ctx = context.WithValue(ctx, sessionKey{}, "session-456")

// Schedule a future self-message
posterum, _ := postarius.Create(ctx, postera.CreateArgs{
    Message:   "Follow up with the user on the Q3 report",
    TriggerAt: "2026-06-15T09:00:00",
})

// List all upcoming scheduled messages for the current session
upcoming, _ := postarius.ListUpcoming(ctx)

// Cancel a scheduled message
postarius.Cancel(ctx, posterum.ID)
```

`TriggerAt` is always provided as a local datetime string (`"2006-01-02T15:04:05"`) — no timezone suffix needed. Postera resolves the timezone from context, so the agent and the human always operate in the same local time without any conversion.

### 2. Setup ADK Toolset

ADK integration is provided by a separate module. Install it first:

```bash
go get go.naturallyfunny.dev/adk
```

Then pass Postarius directly to get ADK-compatible tools:

```go
import posteraadk "go.naturallyfunny.dev/adk"

tools, _ := posteraadk.Tools(postarius)
```

### 3. Assign the Tools to Your Agent (ADK Agent for Example)

```go
import "github.com/google/adk-go"

myAgent := &adk.Agent{
    Tools: tools,
}
```

## Ownership & Access Control

Postera does **not** enforce ownership or authorization. The `Human`, `Agent`, and `Session` fields are identity for **filtering and propagation only** — never access control:

- `Create` stamps them onto the posterum from context.
- `ListUpcoming` filters results by them.
- `Cancel` scopes to them — a posterum outside the caller's scope is reported as not found.

All three honor the same identity, read from context via the `With*FromContext` options. Configure those keys once and every operation filters, stamps, and scopes by the same caller consistently.

These checks are **bypassable** by any caller that holds the `Store` (it owns the database connection), so they are not a security boundary. Authentication, authorization, and tenant isolation belong **above** this SDK — at your HTTP/gRPC layer. In particular, when passing a model-supplied ID into `Cancel`, scope it to the authenticated identity first; an unguessable ID is not the same as an unleakable one.

What Postera gives you is the *plumbing* to make that enforcement easy on your side — not a guarantee it performs on your behalf.

## Known Limitations

These are known gaps to be aware of before using Postera in a production environment:

- **No retry on transient errors**: Calls to Cloud Tasks (`Enqueue`, `Cancel`) and the Store are not retried on transient failures (e.g., gRPC `Unavailable`, network timeouts). Callers are responsible for wrapping with their own retry/backoff logic.
- **Minimal observability**: There are no metrics or tracing hooks, and no operational logging of enqueue rates or latency — those require an external wrapper. The only built-in log is an optional diagnostic: pass `postera.WithLogger(slog.Logger)` and Postarius emits a `Warn` when an identity key is configured but its context value is empty (a likely sign context was not populated, leaving the operation unscoped). It is silent without `WithLogger`, and it diagnoses the misconfiguration rather than preventing it.
- **Remove rollback fires immediately on past schedules**: If `store.Remove` fails after `enqueuer.Cancel` succeeds, the rollback re-enqueues the original `Posterum`. If `TriggerAt` is already in the past, Cloud Tasks will dispatch the task immediately rather than restoring the original schedule.
- **Target URL is not format-validated**: `WithTargetURL` only rejects empty strings. A malformed URL will be accepted at construction time and rejected later by the Cloud Tasks API with a less informative error.

## Roadmap

- **Migration versioning**: The current `WithAutoMigrate()` runner re-executes all SQL files on every startup and relies on idempotent SQL. The plan is to replace this with a lightweight version-tracking runner (no external dependencies) so each migration runs exactly once and callers have full control over when migrations are applied.

## Project Structure

| Directory     | Description                                                                       |
| ------------- | --------------------------------------------------------------------------------- |
| `/`           | Core interfaces and Postarius orchestrator logic.                                 |
| `/postgres`   | Store implementation using PostgreSQL with automatic schema migration support.    |
| `/cloudtasks` | Enqueuer implementation using GCP Cloud Tasks.                                    |
