package postera

import "time"

// Posterum is a single prospective memory: a scheduled future recall that
// the agent will receive when ExecuteAt arrives.
//
// ID and CreatedAt are populated by Postarius.Create; values supplied by
// the caller are overwritten so that the orchestrator can guarantee a
// single authoritative ID across the Registry and the Enqueuer.
type Posterum struct {
	ID        string
	Body      []byte
	ExecuteAt time.Time
	CreatedAt time.Time
}
