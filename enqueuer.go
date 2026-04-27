package postera

import "context"

// Enqueuer schedules a Posterum to fire at p.ExecuteAt.
//
// Implementations are free to translate a Posterum into whatever transport
// they target — for example, an HTTP task in GCP Cloud Tasks or an event in
// AWS EventBridge. The id passed to Cancel matches the ID of a previously
// enqueued Posterum, so the orchestrator can keep the Registry and the
// Enqueuer in sync.
//
// Implementations must be safe for concurrent use.
type Enqueuer interface {
	// Enqueue schedules p to fire at p.ExecuteAt.
	Enqueue(ctx context.Context, p Posterum) error

	// Cancel removes the previously scheduled entry for id. Cancel is
	// best-effort: if the entry has already fired or never existed,
	// implementations may return nil.
	Cancel(ctx context.Context, id string) error
}
