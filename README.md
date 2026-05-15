# Postera

```
██████╗  ██████╗ ███████╗████████╗███████╗██████╗  █████╗
██╔══██╗██╔═══██╗██╔════╝╚══██╔══╝██╔════╝██╔══██╗██╔══██╗
█████╔╝██║   ██║███████╗   ██║   █████╗  ██████╔╝███████║
██╔═══╝ ██║   ██║╚════██║   ██║   ██╔══╝  ██╔══██╗██╔══██║
██║     ╚██████╔╝███████║   ██║   ███████╗██║  ██║██║  ██║
╚═╝      ╚═════╝ ╚══════╝   ╚═╝   ╚══════╝╚═╝  ╚═╝╚═╝  ╚═╝
```

Postera is a Go SDK that provides "Prospective Memory" capabilities for AI Agents. The SDK enables agents to "remember to act" at specific times in the future, whether for self-reminders or user-assigned tasks, with guaranteed consistency between data persistence and execution triggers.

## Background & Goals

AI agents are traditionally reactive. To build a truly autonomous agent ecosystem, agents need the ability to plan and execute tasks beyond the current conversation session. Postera was built to address this challenge by providing safe and consistent orchestration between the persistence and infrastructure scheduling layers.

## Key Features

- **Prospective Memory**: Empowers agents to schedule future actions with complete context.
- **Atomic-ish Orchestration**: Coordinates Registry (Persistence) and Enqueuer (Scheduler) with automatic rollback mechanisms to maintain data integrity.
- **Metadata Agnostic**: Supports multi-tenancy through metadata mapping and context keys owned by the caller.
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

### 1. Setup Postarius

Postarius requires two main components: a Registry (for persistence) and an Enqueuer (for scheduling).

#### 1.1 Setup Registry (Using PostgreSQL as example)

```go
import (
    "go.naturallyfunny.dev/postera/postgres"
)

// Context key definitions for your addition metadata if you need any (can be multiple)
type myMetadataKey1 struct{}
type myMetadataKey2 struct{}

// Registry configuration (PostgreSQL for example)
reg, _ := postgres.NewRegistry(ctx, dbPool,
    postgres.WithAutoMigrate(),
    postgres.WithColumnMapping(myMetadataKey1{}, "my_metadata_1"),
    postgres.WithColumnMapping(myMetadataKey2{}, "my_metadata_2"),
    postgres.WithColumnMappingAutoMigrate(),
)
```

#### 1.2 Setup Enqueuer (Using Cloud Tasks as example)

```go
import (
    "go.naturallyfunny.dev/postera/cloudtasks"
)

// Enqueuer configuration (GCP Cloud Tasks for example)
enq, _ := cloudtasks.NewEnqueuer(ctx, cfg,
    cloudtasks.WithHeaderMapping(myMetadataKey1{}, "my-metadata-1"),
    cloudtasks.WithHeaderMapping(myMetadataKey2{}, "my-metadata-2"),
)
```

#### 1.3 Create the Postarius

```go
// Create Postarius orchestrator
postarius := postera.New(reg, enq)
```

### 2. Setup The Agent Toolset

For agents serving public users with different timezones, configure the toolset to use timezone from context. This ensures the agent is timezone-agnostic and expects user timezone to be propagated through context.

```go
import (
    "go.naturallyfunny.dev/postera/agent"
)

// Define your own timezone key type
type timezoneKey struct{}

// Setup agent toolset with timezone from context
// (expects user timezone to be propagated through context, no need to input zone)
agentToolSet := agent.NewToolSet(postarius,
    agent.WithTimezoneFromContext(timezoneKey{}),
)

// Example: propagating user timezone through context
ctx = context.WithValue(ctx, timezoneKey{}, "Asia/Jakarta")
```

If your agent doesn't need timezone from context (e.g., single timezone use case), you can use `WithDefaultTimezone` instead:

```go
agentToolSet := agent.NewToolSet(postarius,
    agent.WithDefaultTimezone(time.UTC),
)
```

### 3. Setup ADK Toolset

Convert the agent toolset to ADK-compatible tools:

```go
import (
    "go.naturallyfunny.dev/postera/adk"
)

// Get tools ready for agent use
tools, _ := adk.Tools(agentToolSet)
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

## Project Structure

| Directory     | Description                                                                       |
| ------------- | --------------------------------------------------------------------------------- |
| `/`           | Core interfaces and Postarius orchestrator logic.                                 |
| `/postgres`   | Registry implementation using PostgreSQL with automatic schema migration support. |
| `/cloudtasks` | Enqueuer implementation using GCP Cloud Tasks.                                    |
| `/agent`      | Framework-agnostic toolset adapter.                                               |
| `/adk`        | Integration specific to Google Agent Development Kit.                             |
