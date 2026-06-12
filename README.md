# Postera

```
   _ \    _ \    ___| __ __|  ____|   _ \      \    
  |   |  |   | \___ \    |    __|    |   |    _ \   
  ___/   |   |       |   |    |      __ <    ___ \  
 _|     \___/  _____/   _|   _____| _| \_\ _/    _\ 
                                                    
```

Postera is a Go SDK for scheduled messages in the human x agent layer. A `Posterum` is a message an agent sends to its future self on behalf of, or in relation to, a human.

## Background & Goals

AI agents are traditionally reactive. To build a truly autonomous agent ecosystem, agents need the ability to plan and execute tasks beyond the current conversation session. Postera was built to address this challenge by providing safe and consistent orchestration between the persistence and infrastructure scheduling layers.

## Key Features

- **Agent-First Scheduled Messaging**: Lets agents schedule future self-addressed messages with human, agent, session, and metadata context.
- **Atomic-ish Orchestration**: Coordinates Store (Persistence) and Enqueuer (Scheduler) with automatic rollback mechanisms to maintain data integrity.
- **Explicit Query Context**: Supports multi-tenant storage through first-class `Human`, `Agent`, `Session`, and `Metadata` fields owned by the caller.
- **Cloud Native**: Integrations available directly for GCP Cloud Tasks and PostgreSQL.
- **Framework-Agnostic Toolset**: The `/agent` package exposes a framework-agnostic toolset. ADK integration is available as a separate module at `go.naturallyfunny.dev/adk`.

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

Postarius requires two main components: a Store (for persistence) and an Enqueuer (for scheduling).

#### 1.1 Setup Store (Using PostgreSQL as example)

```go
import (
    "go.naturallyfunny.dev/postera/postgres"
)

// Store configuration (PostgreSQL for example)
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

// Enqueuer configuration (GCP Cloud Tasks for example)
enq, _ := cloudtasks.NewEnqueuer(client, cfg,
    cloudtasks.WithHumanHeader("x-postera-human"),
    cloudtasks.WithAgentHeader("x-postera-agent"),
    cloudtasks.WithSessionHeader("x-postera-session"),
    cloudtasks.WithMetadataHeader("timezone", "x-postera-timezone"),
)
```

#### 1.3 Create the Postarius

```go
// Create Postarius orchestrator
postarius := postera.New(store, enq)
```

### 1.4 Create and List Posterums

```go
posterum, _ := postarius.Create(ctx, postera.Posterum{
    Human:     "user-123",
    Agent:     "support-agent",
    Session:   "session-456",
    Metadata:  map[string]string{"timezone": "Asia/Jakarta"},
    Message:   "Follow up with the user",
    TriggerAt: time.Now().Add(2 * time.Hour),
})

entries, _ := postarius.List(ctx, postera.Query{
    Human: "user-123",
    From:  time.Now(),
})
```

### 2. Setup The Agent Toolset

For agents serving public users with different timezones, configure the toolset to use timezone from context. This ensures the agent is timezone-agnostic and expects user timezone to be propagated through context.

```go
import (
    "go.naturallyfunny.dev/postera/agent"
)

// Define your own timezone key type
type timezoneKey struct{}
type humanKey struct{}
type agentKey struct{}
type sessionKey struct{}
type metadataKey struct{}

// Setup agent toolset with timezone from context
// (expects user timezone to be propagated through context, no need to input zone)
agentToolSet := agent.NewToolSet(postarius,
    agent.WithTimezoneFromContext(timezoneKey{}),
    agent.WithHumanFromContext(humanKey{}),
    agent.WithAgentFromContext(agentKey{}),
    agent.WithSessionFromContext(sessionKey{}),
    agent.WithMetadataFromContext(metadataKey{}),
)

// Example: propagating caller-owned context outside the agent-controlled schema
ctx = context.WithValue(ctx, timezoneKey{}, "Asia/Jakarta")
ctx = context.WithValue(ctx, humanKey{}, "user-123")
ctx = context.WithValue(ctx, agentKey{}, "support-agent")
ctx = context.WithValue(ctx, sessionKey{}, "session-456")
ctx = context.WithValue(ctx, metadataKey{}, map[string]string{"timezone": "Asia/Jakarta"})
```

If your agent doesn't need timezone from context (e.g., single timezone use case), you can use `WithDefaultTimezone` instead:

```go
agentToolSet := agent.NewToolSet(postarius,
    agent.WithDefaultTimezone(time.UTC),
)
```

### 3. Setup ADK Toolset

ADK integration is provided by a separate module. Install it first:

```bash
go get go.naturallyfunny.dev/adk
```

Then convert the agent toolset to ADK-compatible tools:

```go
import (
    posteraadk "go.naturallyfunny.dev/adk"
)

// Get tools ready for agent use
tools, _ := posteraadk.Tools(agentToolSet)
```

### 4. Assign the Tools to Your Agent (ADK Agent for Example)

Initialize your ADK agent with the toolset:

```go
import (
    "github.com/google/adk-go"
)

// Create ADK agent with the tools
myAgent := &adk.Agent{
    Tools: tools,
}
```

## Roadmap

- **Migration versioning**: The current `WithAutoMigrate()` runner re-executes all SQL files on every startup and relies on idempotent SQL. The plan is to replace this with a lightweight version-tracking runner (no external dependencies) so each migration runs exactly once and callers have full control over when migrations are applied.

## Project Structure

| Directory     | Description                                                                       |
| ------------- | --------------------------------------------------------------------------------- |
| `/`           | Core interfaces and Postarius orchestrator logic.                                 |
| `/postgres`   | Store implementation using PostgreSQL with automatic schema migration support.    |
| `/cloudtasks` | Enqueuer implementation using GCP Cloud Tasks.                                    |
| `/agent`      | Framework-agnostic toolset adapter.                                               |
