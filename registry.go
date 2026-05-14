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
// RemindAt. The lower bound is inclusive; the upper bound is exclusive. A
// zero From disables the lower bound; a zero To disables the upper bound.
type TimeRange struct {
	From time.Time
	To   time.Time
}

// Registry persists Posterum entries.
//
// Implementations are responsible for any multi-tenancy or isolation logic.
// Callers inject identity — namespace, user, session, or any other discriminant
// — into the context before calling; implementations read those values and scope
// their queries accordingly. The postera root package provides WithNamespace and
// NamespaceKey as optional helpers for the common case; implementations are free
// to define and read their own context keys instead.
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

	// List returns the entries matching q, ordered by RemindAt ascending.
	List(ctx context.Context, q TimeRange) ([]Posterum, error)
}
