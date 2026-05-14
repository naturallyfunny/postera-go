```text
██████╗  ██████╗ ███████╗████████╗███████╗██████╗  █████╗
██╔══██╗██╔═══██╗██╔════╝╚══██╔══╝██╔════╝██╔══██╗██╔══██╗
██████╔╝██║   ██║███████╗   ██║   █████╗  ██████╔╝███████║
██╔═══╝ ██║   ██║╚════██║   ██║   ██╔══╝  ██╔══██╗██╔══██║
██║     ╚██████╔╝███████║   ██║   ███████╗██║  ██║██║  ██║
╚═╝      ╚═════╝ ╚══════╝   ╚═╝   ╚══════╝╚═╝  ╚═╝╚═╝  ╚═╝
```

A Go library providing "Prospective Memory" capabilities for AI Agents. It enables agents to schedule future actions—self-reminders or user-assigned tasks—with guaranteed consistency between storage and execution triggers.

## Key Features

- **Prospective Memory**: Empowers agents to "remember to act" at specific future times.
- **Atomic-ish Orchestration**: Synchronizes Registry (Persistence) and Enqueuer (Scheduler) with automatic best-effort rollbacks.
- **Identity Agnostic**: Secure multi-tenancy via caller-owned context keys and metadata mappings.
- **Cloud Ready**: Out-of-the-box implementations for GCP Cloud Tasks and PostgreSQL.
- **ADK Integrated**: Native support for the Google Agent Development Kit.

## Architecture

Postera coordinates through a central orchestrator called Postarius, managing two primary interfaces:

- **Registry**: Handles durable persistence of memory entries (e.g., PostgreSQL).
- **Enqueuer**: Schedules infrastructure-level triggers (e.g., GCP Cloud Tasks).

## Installation

```bash
go get go.naturallyfunny.dev/postera
```

## Quick Start

### 1. Initialize Postarius

```go
type userIDKey struct{}

// Setup providers
reg, _ := postgres.NewRegistry(ctx, dbPool,
    postgres.WithAutoMigrate(),
    postgres.WithColumnMapping(userIDKey{}, "user_id"),
    postgres.WithColumnMappingAutoMigrate(),
)
enq, _ := cloudtasks.NewEnqueuer(ctx, projectID, locationID, queueID, targetURL, saEmail,
    cloudtasks.WithHeaderMapping(userIDKey{}, "x-user-id"),
)

// Create the orchestrator
postarius := postera.New(reg, enq)
```

### 2. Schedule a Memory (Posterum)

```go
// Inject identity into context
ctx = context.WithValue(ctx, userIDKey{}, "user-id-123")

// Create the reminder
p, err := postarius.Create(ctx, "Follow up with client about the proposal", time.Now().Add(48*time.Hour))
```

### 3. ADK Integration (Agent Tools)

Expose capabilities directly to LLMs using the adk package:

```go
agentToolSet := postera.NewAgentToolSet(postarius,
    postera.WithAgentDefaultTimezone(time.UTC),
)
tools, _ := adk.Tools(agentToolSet,
    adk.WithUserIDContextKey(userIDKey{}),
)

// Register tools with your ADK agent
```

## Project Structure

- `/` : Core interfaces and the Postarius orchestrator.
- `/postgres` : PostgreSQL Registry implementation using the fixed `posterum` and `posterum_metadata` tables.
- `/cloudtasks` : GCP Cloud Tasks Enqueuer implementation.
- `/agent_toolset.go` : Framework-agnostic adapter for AI agents.
- `/adk` : Specific integration for Google Agent Development Kit.
