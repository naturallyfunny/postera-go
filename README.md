```text
░▒▓███████▓▒░ ░▒▓██████▓▒░ ░▒▓███████▓▒░▒▓████████▓▒░▒▓████████▓▒░▒▓███████▓▒░ ░▒▓██████▓▒░
░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░         ░▒▓█▓▒░   ░▒▓█▓▒░      ░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░
░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░         ░▒▓█▓▒░   ░▒▓█▓▒░      ░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░
░▒▓███████▓▒░░▒▓█▓▒░░▒▓█▓▒░░▒▓██████▓▒░   ░▒▓█▓▒░   ░▒▓██████▓▒░ ░▒▓███████▓▒░░▒▓████████▓▒░
░▒▓█▓▒░      ░▒▓█▓▒░░▒▓█▓▒░      ░▒▓█▓▒░  ░▒▓█▓▒░   ░▒▓█▓▒░      ░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░
░▒▓█▓▒░      ░▒▓█▓▒░░▒▓█▓▒░      ░▒▓█▓▒░  ░▒▓█▓▒░   ░▒▓█▓▒░      ░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░
░▒▓█▓▒░       ░▒▓██████▓▒░░▒▓███████▓▒░   ░▒▓█▓▒░   ░▒▓████████▓▒░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░


```

# Postera

An orchestrator for scheduled tasks (prospective memory) that ensures synchronization between a persistence layer (Registry) and a scheduler (Enqueuer).

## Key Features

- **Atomic-like Sync**: Robust synchronization between your Database (e.g., PostgreSQL) and Task Queue (e.g., Google Cloud Tasks) featuring a best-effort rollback mechanism.
- **Identity-Agnostic**: Supports multi-tenancy natively via context namespaces, ensuring secure data partition and isolation.
- **Centralized Time Handling**: All timestamps are automatically converted to and processed in UTC to prevent timezone drift issues.
- **Expressive Queries**: Built-in instant queries for easy schedule extraction (e.g., Today, Last Week, Incoming Today).

## Installation

```bash
go get go.naturallyfunny.dev/postera
```

## Core Concepts

- **Postarius**: The main orchestrator component bridging the `Registry` and the `Enqueuer`.
- **Posterum**: The scheduled memory/task entity containing the payload (`Body`) and execution schedule (`ExecuteAt`).
- **Registry**: The interface for permanent storage/persistence.
- **Enqueuer**: The interface for the queue/scheduler processor.

## Usage

### 1. Initialization

```go
package main

import (
	"go.naturallyfunny.dev/postera"
	// Import your preferred registry and enqueuer adapters
	// "go.naturallyfunny.dev/postera/postgres"
	// "go.naturallyfunny.dev/postera/cloudtasks"
)

func main() {
	// 1. Setup Registry and Enqueuer
	// reg := postgres.NewRegistry(dbPool)
	// enq := cloudtasks.NewEnqueuer(client)

	// 2. Initialize Postarius
	// p := postera.New(reg, enq)
}
```

### 2. Scheduling a Task

```go
ctx := context.Background()

// (Optional) Attach a namespace for multi-tenancy
ctx = postera.WithNamespace(ctx, "tenant-1")

// Schedule a task 24 hours from now
task := postera.Posterum{
	Body:      []byte(`{"message": "hello"}`),
	ExecuteAt: time.Now().Add(24 * time.Hour),
}

saved, err := p.Create(ctx, task)
if err != nil {
	// Handle error, e.g.: errors.Is(err, postera.ErrInvalidInput)
}
```

### 3. Managing Schedules

```go
// Retrieve a schedule by its ID
item, err := p.Get(ctx, saved.ID)

// Remove a schedule (cancels from the queue and deletes from the registry)
err = p.Remove(ctx, saved.ID)
```

### 4. Queries & Filters

```go
// List schedules that are running or will run today
incoming, err := p.ListIncomingToday(ctx)

// List all schedules from the last 7 days
lastWeek, err := p.ListLastWeek(ctx)

// List all schedules (including past ones) for today
today, err := p.ListToday(ctx)
```
