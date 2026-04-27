package postera

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by a Registry — and propagated by Postarius — when
// no Posterum exists for a given id.
var ErrNotFound = errors.New("postera: posterum not found")

// Query is a half-open time-range filter used by Registry.List.
//
// The lower bound is inclusive; the upper bound is exclusive. A zero From
// disables the lower bound; a zero To disables the upper bound.
type Query struct {
	From time.Time
	To   time.Time
}

// Registry persists Posterum entries.
//
// Implementations are responsible for any namespacing logic: a multi-tenant
// Registry should read the namespace from ctx (see WithNamespace and
// NamespaceKey) and isolate entries accordingly so that one namespace
// cannot observe another's entries.
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

	// List returns the entries matching q, ordered by ExecuteAt ascending.
	List(ctx context.Context, q Query) ([]Posterum, error)
}
