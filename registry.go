package postera

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by a Registry — and propagated by Postarius — when
// no Posterum exists for a given id.
var ErrNotFound = errors.New("postera: posterum not found")

// TimeRange is a half-open interval used to filter Posterum entries by
// TriggerAt. The lower bound is inclusive; the upper bound is exclusive. A
// zero From disables the lower bound; a zero To disables the upper bound.
type TimeRange struct {
	From time.Time
	To   time.Time
}

// Registry persists Posterum entries.
//
// Implementations are identity-agnostic: any isolation logic (multi-tenancy,
// per-user partitioning) is the responsibility of the specific implementation
// and is expressed through implementation-level configuration rather than
// through this interface.
//
// Implementations must be safe for concurrent use.
type Registry interface {
	// Save persists p. If an entry with the same ID already exists,
	// implementations overwrite it.
	Save(ctx context.Context, p Posterum) error

	// Get returns the Posterum with the given id. If no such entry exists,
	// Get returns an error that wraps ErrNotFound.
	Get(ctx context.Context, id string) (Posterum, error)

	// Remove deletes the entry with the given id. If no such entry exists,
	// Remove returns an error that wraps ErrNotFound.
	Remove(ctx context.Context, id string) error

	// List returns the entries matching q, ordered by TriggerAt ascending.
	List(ctx context.Context, q TimeRange) ([]Posterum, error)
}
