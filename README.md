# Postera

```
██████╗  ██████╗ ███████╗████████╗███████╗██████╗  █████╗
██╔══██╗██╔═══██╗██╔════╝╚══██╔══╝██╔════╝██╔══██╗██╔══██╗
█████╔╝██║   ██║███████╗   ██║   █████╗  ██████╔╝███████║
██╔═══╝ ██║   ██║╚════██║   ██║   ██╔══╝  ██╔══██╗██╔══██║
██║     ╚██████╔╝███████║   ██║   ███████╗██║  ██║██║  ██║
╚═╝      ╚═════╝ ╚══════╝   ╚═╝   ╚══════╝╚═╝  ╚═╝╚═╝  ╚═╝
```

Postera is a Go SDK that provides "Prospective Memory" capabilities for AI Agents. The SDK enables agents to "remember to act" at specific times in the future—whether for self-reminders or user-assigned tasks—with guaranteed consistency between data persistence and execution triggers.

## Background & Goals

AI agents are traditionally reactive. To build a truly autonomous agent ecosystem, agents need the ability to plan and execute tasks beyond the current conversation session. Postera was built to address this challenge by providing safe and consistent orchestration between the persistence and infrastructure scheduling layers.

## Key Features

- **Prospective Memory**: Empowers agents to schedule future actions with complete context.
- **Atomic-ish Orchestration**: Coordinates Registry (Persistence) and Enqueuer (Scheduler) with automatic rollback mechanisms to maintain data integrity.
- **Identity Agnostic**: Supports multi-tenancy through metadata mapping and context keys owned by the caller.
- **Cloud Native**: Integrations available directly for GCP Cloud Tasks and PostgreSQL.
- **ADK Integrated**: Native support for Google Agent Development Kit (ADK) to facilitate tool exposure to LLMs.

## Architecture

Postera operates through a central orchestrator called **Postarius**, which manages two primary interfaces:

- **Registry**: Handles persistent storage of memory entries (e.g., using PostgreSQL).
- **Enqueuer**: Schedules infrastructure-level triggers (e.g., using GCP Cloud Tasks).

## Installation

Postera requires Go 1.25.0 or above.

```bash
go get go.naturallyfunny.dev/postera
```

## Usage Guide

### 1. Initialize Postarius

You can configure providers with idiomatic options such as automatic column mapping for user identity:

```go
// Context key definition for identity
type userIDKey struct{}

// Registry configuration (PostgreSQL)
reg, _ := postgres.NewRegistry(ctx, dbPool,
    postgres.WithAutoMigrate(),
    postgres.WithColumnMapping(userIDKey{}, "user_id"),
    postgres.WithColumnMappingAutoMigrate(),
)

// Enqueuer configuration (GCP Cloud Tasks)
enq, _ := cloudtasks.NewEnqueuer(ctx, cfg,
    cloudtasks.WithHeaderMapping(userIDKey{}, "x-user-id"),
)

// Create Postarius orchestrator
postarius := postera.New(reg, enq)
```

### 2. Scheduling Memory (Posterum)

The system ensures identity context is carried throughout the scheduling process:

```go
// Insert identity into context
ctx = context.WithValue(ctx, userIDKey{}, "user-abc-123")

// Schedule reminder for 48 hours from now
p, err := postarius.Create(ctx, "Review project proposal draft", time.Now().Add(48*time.Hour))
```

### 3. ADK Integration (Agent Tools)

Postera provides an adapter to expose scheduling functions directly to AI agents through ADK:

```go
agentToolSet := agent.NewToolSet(postarius,
    agent.WithDefaultTimezone(time.UTC),
)

// Get tools ready for agent use
tools, _ := adk.Tools(agentToolSet)
```

## Project Structure

| Directory | Description |
|-----------|-------------|
| `/` | Core interfaces and Postarius orchestrator logic. |
| `/postgres` | Registry implementation using PostgreSQL with automatic schema migration support. |
| `/cloudtasks` | Enqueuer implementation using GCP Cloud Tasks. |
| `/agent` | Framework-agnostic toolset adapter. |
| `/adk` | Integration specific to Google Agent Development Kit. |

## Development Principles

Postera is developed following Go community best practices:

- **Interface Consistency**: Uses `context.Context` for cancellation and value propagation across API boundaries.
- **Error Management**: Errors are normalized to provide precise information to AI agents regarding input failures or missing data.
- **Scalability**: Designed for serverless environments like Cloud Run with minimal dependencies and non-blocking processing.