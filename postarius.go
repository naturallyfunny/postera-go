package postera

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrInvalidInput is returned by Postarius when a caller-supplied value
// fails validation at the public API boundary (for example, a zero
// ExecuteAt or a negative day count). It lets callers distinguish a
// programming error in their own code from an infrastructure failure
// surfaced by a Registry or an Enqueuer using errors.Is.
var ErrInvalidInput = errors.New("postera: invalid input")

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

// namespaceKey is the unexported, zero-sized type used as the context key
// for the namespace value. Type identity alone — not a string field —
// guarantees that no key in any other package can collide with this one,
// and the empty struct carries no data and incurs no allocation.
type namespaceKey struct{}

// NamespaceKey is the context key under which postera stores the agent's
// namespace. It is exported so that external Registry and Enqueuer
// implementations can read from the same context — for example, to map the
// namespace onto a database column or an HTTP header — without depending on
// the helpers in this package.
var NamespaceKey = namespaceKey{}

// WithNamespace returns a derived context that carries namespace.
//
// An empty namespace is rejected: it is indistinguishable from "no
// namespace set" at the extraction site, so accepting it would silently
// defeat the partition guarantee that namespaces exist to provide. The
// caller passing "" is a programming error and WithNamespace panics so
// that the bug surfaces at its source rather than corrupting downstream
// reads.
func WithNamespace(ctx context.Context, namespace string) context.Context {
	if namespace == "" {
		panic("postera: WithNamespace called with empty namespace")
	}
	return context.WithValue(ctx, NamespaceKey, namespace)
}

// NamespaceFromContext returns the namespace stored in ctx and a boolean
// that reports whether one was present.
//
// postera is identity-agnostic and does not itself require a namespace.
// Registry and Enqueuer implementations that mandate one must enforce that
// requirement in their own layer; the comma-ok signature here exists so
// that single-tenant implementations can legitimately observe absence
// without it being modeled as an error.
func NamespaceFromContext(ctx context.Context) (string, bool) {
	namespace, ok := ctx.Value(NamespaceKey).(string)
	return namespace, ok
}

// Postarius is the orchestrator that keeps a Registry (persistence) and an
// Enqueuer (scheduler) in sync. A *Postarius is safe for concurrent use as
// long as the underlying Registry and Enqueuer are.
type Postarius struct {
	registry Registry
	enqueuer Enqueuer
}

// New returns a Postarius backed by registry and enqueuer.
func New(registry Registry, enqueuer Enqueuer) *Postarius {
	return &Postarius{registry: registry, enqueuer: enqueuer}
}

// Create assigns a fresh ID and CreatedAt to posterum, enqueues it, and
// persists it. Any ID or CreatedAt set by the caller is overwritten — the
// orchestrator is the sole authority for these fields.
//
// posterum.ExecuteAt must be non-zero; Create returns an error wrapping
// ErrInvalidInput otherwise.
//
// If persistence fails after a successful enqueue, Create attempts a
// best-effort rollback by calling Enqueuer.Cancel. The rollback runs with a
// context detached from the caller's cancellation so it can complete even if
// the caller has already given up.
func (p *Postarius) Create(ctx context.Context, posterum Posterum) (Posterum, error) {
	if posterum.ExecuteAt.IsZero() {
		return Posterum{}, fmt.Errorf("postera: create: ExecuteAt must be non-zero: %w", ErrInvalidInput)
	}

	posterum.ID = uuid.NewString()
	posterum.CreatedAt = now()

	if err := p.enqueuer.Enqueue(ctx, posterum); err != nil {
		return Posterum{}, fmt.Errorf("postera: enqueue: %w", err)
	}

	if err := p.registry.Save(ctx, posterum); err != nil {
		rollback := p.enqueuer.Cancel(context.WithoutCancel(ctx), posterum.ID)
		if rollback != nil {
			return Posterum{}, errors.Join(
				fmt.Errorf("postera: create: %w", err),
				fmt.Errorf("postera: rollback cancel: %w", rollback),
			)
		}
		return Posterum{}, fmt.Errorf("postera: create: %w", err)
	}

	return posterum, nil
}

// Get returns the Posterum with the given id.
func (p *Postarius) Get(ctx context.Context, id string) (Posterum, error) {
	posterum, err := p.registry.Get(ctx, id)
	if err != nil {
		return Posterum{}, fmt.Errorf("postera: get: %w", err)
	}
	return posterum, nil
}

// Remove cancels the schedule for id and then deletes it from the Registry.
//
// The full Posterum is fetched first so that, if the Registry deletion
// fails after a successful cancellation, the cancellation can be rolled
// back by re-enqueuing the original entry. Cancel runs before Remove so
// that a pending task cannot fire against an already-deleted Registry
// entry. Both the Registry deletion and the rollback enqueue run with a
// context detached from the caller's cancellation so that the two systems
// do not drift out of sync on a late cancellation.
func (p *Postarius) Remove(ctx context.Context, id string) error {
	posterum, err := p.registry.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("postera: remove: %w", err)
	}

	if err := p.enqueuer.Cancel(ctx, id); err != nil {
		return fmt.Errorf("postera: cancel: %w", err)
	}

	if err := p.registry.Remove(context.WithoutCancel(ctx), id); err != nil {
		rollback := p.enqueuer.Enqueue(context.WithoutCancel(ctx), posterum)
		if rollback != nil {
			return errors.Join(
				fmt.Errorf("postera: remove: %w", err),
				fmt.Errorf("postera: rollback enqueue: %w", rollback),
			)
		}
		return fmt.Errorf("postera: remove: %w", err)
	}

	return nil
}

// List returns the entries matching q, in the order produced by the Registry.
func (p *Postarius) List(ctx context.Context, q Query) ([]Posterum, error) {
	entries, err := p.registry.List(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("postera: list: %w", err)
	}
	return entries, nil
}

// ListIncoming returns the entries scheduled to execute at or after the
// present instant.
func (p *Postarius) ListIncoming(ctx context.Context) ([]Posterum, error) {
	return p.List(ctx, Query{From: now()})
}

// ListToday returns the entries scheduled within the current UTC calendar
// day, regardless of whether they are past or future.
func (p *Postarius) ListToday(ctx context.Context) ([]Posterum, error) {
	return p.ListByDate(ctx, now())
}

// ListIncomingToday returns the entries within the current UTC calendar
// day that have not yet executed.
func (p *Postarius) ListIncomingToday(ctx context.Context) ([]Posterum, error) {
	t := now()
	_, end := dayBounds(t)
	return p.List(ctx, Query{From: t, To: end})
}

// ListLastWeek returns the entries from the last seven days, ending at now.
// It is shorthand for ListLastNDays(ctx, 7).
func (p *Postarius) ListLastWeek(ctx context.Context) ([]Posterum, error) {
	return p.ListLastNDays(ctx, 7)
}

// ListLastNDays returns the entries from the last n days, ending at now. n
// must be non-negative; ListLastNDays returns an error wrapping
// ErrInvalidInput otherwise.
func (p *Postarius) ListLastNDays(ctx context.Context, n int) ([]Posterum, error) {
	if n < 0 {
		return nil, fmt.Errorf("postera: list last n days: n must be non-negative, got %d: %w", n, ErrInvalidInput)
	}
	t := now()
	return p.List(ctx, Query{From: t.AddDate(0, 0, -n), To: t})
}

// ListByDate returns the entries within the calendar day of date, computed
// in date's location.
func (p *Postarius) ListByDate(ctx context.Context, date time.Time) ([]Posterum, error) {
	from, to := dayBounds(date)
	return p.List(ctx, Query{From: from, To: to})
}

// dayBounds returns the [start, end) bounds of the calendar day containing
// t, in t's location.
func dayBounds(t time.Time) (time.Time, time.Time) {
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return start, start.AddDate(0, 0, 1)
}

// now returns the current instant in UTC. Postarius generates and queries
// timestamps exclusively in UTC so that registry implementations are not
// exposed to the host's local time zone — a difference that would silently
// shift query bounds relative to stored values on any non-UTC host.
func now() time.Time {
	return time.Now().UTC()
}
