// Package agent provides an SDK-agnostic adapter layer between AI agent
// frameworks and *postera.Postarius. Its sole responsibility is translating
// human-readable string inputs (ISO 8601 datetime, IANA timezone name) into
// the precise Go types that Postarius expects, then delegating every
// operation unchanged.
//
// Namespace isolation is identity-agnostic: callers inject the namespace once
// into the context via postera.WithNamespace; the Tool passes that context
// through to Postarius, which threads it to the Registry and Enqueuer. The
// Tool itself never reads or interprets namespace values.
//
// Callers running in environments without system timezone data should import
// the bundled database:
//
//	import _ "time/tzdata"
package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.naturallyfunny.dev/postera"
)

// timeLayout is the expected datetime format for agent-supplied LocalTime
// fields. Agents must NOT embed a timezone suffix: the timezone is a separate
// field resolved through time.ParseInLocation, which anchors the result to
// the user's IANA locale. Embedding an offset in the string would silently
// override the explicit Timezone field and reintroduce the Server Time Leak
// anti-pattern.
const timeLayout = "2006-01-02T15:04:05"

// dateLayout is the expected date-only format for agent-supplied LocalDate
// fields.
const dateLayout = "2006-01-02"

// Tool is an SDK-agnostic adapter that bridges an AI agent to
// *postera.Postarius. It converts human-readable time strings into precise
// time.Time values before forwarding calls to the underlying orchestrator.
//
// A *Tool is safe for concurrent use.
type Tool struct {
	postarius *postera.Postarius
	defaultTZ *time.Location
}

// Option configures a Tool at construction time.
type Option func(*Tool)

// WithDefaultTimezone registers loc as the fallback IANA location used when
// an agent call omits the Timezone field. Without this option, a missing
// Timezone produces a validation error that the agent can self-correct.
//
// Panics if loc is nil.
func WithDefaultTimezone(loc *time.Location) Option {
	if loc == nil {
		panic("agent: WithDefaultTimezone: loc must not be nil")
	}
	return func(t *Tool) {
		t.defaultTZ = loc
	}
}

// New returns a Tool backed by p.
//
// Panics if p is nil.
func New(p *postera.Postarius, opts ...Option) *Tool {
	if p == nil {
		panic("agent: New: p must not be nil")
	}
	t := &Tool{postarius: p}
	for _, o := range opts {
		o(t)
	}
	return t
}

// CreateArgs holds the agent-supplied arguments for scheduling a new Posterum.
type CreateArgs struct {
	// Body is the payload to be delivered at execution time.
	Body []byte

	// LocalTime is an ISO 8601 datetime string in the user's local time,
	// without a timezone suffix (e.g., "2024-01-15T09:00:00").
	// The timezone is conveyed separately via the Timezone field.
	LocalTime string

	// Timezone is an IANA timezone name (e.g., "Asia/Jakarta").
	// Falls back to the Tool's default timezone when empty.
	Timezone string
}

// Create parses LocalTime in the location identified by Timezone, then
// creates and enqueues a new Posterum via the underlying Postarius.
//
// ctx must carry a namespace via postera.WithNamespace when the backing
// Registry enforces multi-tenant isolation.
func (t *Tool) Create(ctx context.Context, args CreateArgs) (postera.Posterum, error) {
	loc, err := t.resolveLocation(args.Timezone)
	if err != nil {
		return postera.Posterum{}, err
	}

	executeAt, err := parseLocalTime(args.LocalTime, loc)
	if err != nil {
		return postera.Posterum{}, err
	}

	result, err := t.postarius.Create(ctx, postera.Posterum{
		Body:      args.Body,
		ExecuteAt: executeAt,
	})
	if err != nil {
		return postera.Posterum{}, normalizeError(err)
	}
	return result, nil
}

// ListArgs holds the agent-supplied arguments for a time-range query.
// Either bound may be omitted; an absent bound is treated as open.
type ListArgs struct {
	// FromLocalTime is the inclusive lower bound as an ISO 8601 datetime string
	// without a timezone suffix (e.g., "2024-01-15T09:00:00").
	// Empty means no lower bound.
	FromLocalTime string

	// ToLocalTime is the exclusive upper bound as an ISO 8601 datetime string
	// without a timezone suffix. Empty means no upper bound.
	ToLocalTime string

	// Timezone is an IANA timezone name used to parse any non-empty bound.
	// Falls back to the Tool's default timezone when empty.
	Timezone string
}

// List returns Posterum entries matching the half-open window [From, To).
// Either or both bounds may be omitted to leave that side unbounded.
//
// ctx must carry a namespace via postera.WithNamespace when the backing
// Registry enforces multi-tenant isolation.
func (t *Tool) List(ctx context.Context, args ListArgs) ([]postera.Posterum, error) {
	var q postera.Query

	if args.FromLocalTime != "" || args.ToLocalTime != "" {
		loc, err := t.resolveLocation(args.Timezone)
		if err != nil {
			return nil, err
		}

		if args.FromLocalTime != "" {
			from, err := parseLocalTime(args.FromLocalTime, loc)
			if err != nil {
				return nil, err
			}
			q.From = from
		}

		if args.ToLocalTime != "" {
			to, err := parseLocalTime(args.ToLocalTime, loc)
			if err != nil {
				return nil, err
			}
			q.To = to
		}
	}

	results, err := t.postarius.List(ctx, q)
	if err != nil {
		return nil, normalizeError(err)
	}
	return results, nil
}

// ListByDateArgs holds the agent-supplied arguments for a date-scoped query.
type ListByDateArgs struct {
	// LocalDate is an ISO 8601 date string (e.g., "2024-01-15").
	LocalDate string

	// Timezone is an IANA timezone name (e.g., "Asia/Jakarta").
	// Falls back to the Tool's default timezone when empty.
	Timezone string
}

// ListByDate returns all Posterum entries scheduled on the calendar day of
// LocalDate, with day boundaries computed in the location identified by
// Timezone. This ensures that "today" or "tomorrow" resolves to the user's
// local calendar day rather than the server's UTC day.
//
// ctx must carry a namespace via postera.WithNamespace when the backing
// Registry enforces multi-tenant isolation.
func (t *Tool) ListByDate(ctx context.Context, args ListByDateArgs) ([]postera.Posterum, error) {
	loc, err := t.resolveLocation(args.Timezone)
	if err != nil {
		return nil, err
	}

	date, err := parseLocalDate(args.LocalDate, loc)
	if err != nil {
		return nil, err
	}

	results, err := t.postarius.ListByDate(ctx, date)
	if err != nil {
		return nil, normalizeError(err)
	}
	return results, nil
}

// resolveLocation loads the *time.Location for the given IANA name. When tz
// is empty and a default timezone was registered, the default is returned.
// An empty tz with no default is a validation error returned to the agent.
func (t *Tool) resolveLocation(tz string) (*time.Location, error) {
	if tz == "" {
		if t.defaultTZ != nil {
			return t.defaultTZ, nil
		}
		return nil, fmt.Errorf("agent: timezone is required: provide a valid IANA timezone name (e.g., %q)", "Asia/Jakarta")
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("agent: unknown timezone %q: must be a valid IANA timezone name (e.g., %q)", tz, "Asia/Jakarta")
	}
	return loc, nil
}

// parseLocalTime parses s as an ISO 8601 datetime without a timezone suffix,
// anchoring the result to loc via time.ParseInLocation. This is the only
// correct way to convert a user-supplied local time: time.Parse would
// silently interpret s as UTC and produce a shifted moment.
func parseLocalTime(s string, loc *time.Location) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("agent: local_time is required: provide a datetime string in format %q (e.g., %q)", timeLayout, "2024-01-15T09:00:00")
	}
	parsed, err := time.ParseInLocation(timeLayout, s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("agent: invalid local_time %q: expected format %q without a timezone suffix (e.g., %q)", s, timeLayout, "2024-01-15T09:00:00")
	}
	return parsed, nil
}

// parseLocalDate parses s as an ISO 8601 date, returning midnight of that
// date in loc. Anchoring to loc ensures that day-boundary queries match the
// user's calendar day rather than the server's.
func parseLocalDate(s string, loc *time.Location) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("agent: local_date is required: provide a date string in format %q (e.g., %q)", dateLayout, "2024-01-15")
	}
	parsed, err := time.ParseInLocation(dateLayout, s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("agent: invalid local_date %q: expected format %q (e.g., %q)", s, dateLayout, "2024-01-15")
	}
	return parsed, nil
}

// normalizeError rewrites postera domain errors into agent-readable messages
// so the agent can self-correct without understanding internal error codes.
// The original error is preserved in the chain so callers can use errors.Is.
func normalizeError(err error) error {
	switch {
	case errors.Is(err, postera.ErrInvalidInput):
		return fmt.Errorf("agent: invalid input — verify that local_time is a valid non-zero datetime and all required fields are provided: %w", err)
	case errors.Is(err, postera.ErrNotFound):
		return fmt.Errorf("agent: posterum not found — the entry does not exist or is inaccessible in the current namespace: %w", err)
	default:
		return err
	}
}
