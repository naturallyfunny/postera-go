// Package agent provides an SDK-agnostic adapter layer between AI agent
// frameworks and *postera.Postarius. Its sole responsibility is translating
// human-readable string inputs (ISO 8601 datetime, IANA timezone name) into
// the precise Go types that Postarius expects, then delegating every
// operation unchanged.
//
// Namespace isolation is identity-agnostic: callers inject the namespace once
// into the context via postera.WithNamespace; the ToolSet passes that context
// through to Postarius, which threads it to the Registry and Enqueuer. The
// ToolSet itself never reads or interprets namespace values.
//
// Timezone resolution is infrastructure-agnostic: callers may register a
// context key via WithTimezoneFromContext, and the ToolSet reads the IANA
// timezone string from that key on every operation. This decouples the
// ToolSet from any specific HTTP framework or middleware. The ToolSet itself
// never interprets timezone semantics beyond IANA name resolution.
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
// fields. Agents must NOT embed a timezone suffix: the timezone is resolved
// separately through time.ParseInLocation, which anchors the result to the
// user's IANA locale. Embedding an offset in the string would silently
// override the resolved timezone and reintroduce the Server Time Leak
// anti-pattern.
const timeLayout = "2006-01-02T15:04:05"

// dateLayout is the expected date-only format for agent-supplied LocalDate
// fields.
const dateLayout = "2006-01-02"

// ToolSet is an SDK-agnostic adapter that bridges an AI agent to
// *postera.Postarius. It converts human-readable time strings into precise
// time.Time values before forwarding calls to the underlying orchestrator.
//
// A *ToolSet is safe for concurrent use.
type ToolSet struct {
	postarius   *postera.Postarius
	defaultTZ   *time.Location
	timezoneKey any
}

// Option configures a ToolSet at construction time.
type Option func(*ToolSet)

// WithDefaultTimezone registers loc as the fallback IANA location used when
// a call omits the Timezone field and no timezone is found in the context.
// Without this option, a missing timezone produces a validation error that
// the agent can self-correct.
//
// Panics if loc is nil.
func WithDefaultTimezone(loc *time.Location) Option {
	if loc == nil {
		panic("agent: WithDefaultTimezone: loc must not be nil")
	}
	return func(ts *ToolSet) {
		ts.defaultTZ = loc
	}
}

// WithTimezoneFromContext registers the context key under which the ToolSet
// looks for an IANA timezone string on every operation. The value is read
// dynamically per-request from the context passed to each method — it is
// never read at construction time. This makes the option safe for concurrent
// use across requests that carry different timezone values.
//
// The key is any comparable value; the corresponding context value must be a
// non-empty string containing a valid IANA timezone name (e.g.
// "Asia/Jakarta"). When the key is registered but the context carries no
// value for it, resolution falls through to WithDefaultTimezone. When the
// context carries a value but it is not a valid IANA name, resolveLocation
// returns an error.
//
// The caller (not this package) is responsible for supplying the key; this
// keeps agent independent of any specific middleware package.
//
// Panics if key is nil.
func WithTimezoneFromContext(key any) Option {
	if key == nil {
		panic("agent: WithTimezoneFromContext: key must not be nil")
	}
	return func(ts *ToolSet) {
		ts.timezoneKey = key
	}
}

// NewToolSet returns a ToolSet backed by p.
//
// Panics if p is nil.
func NewToolSet(p *postera.Postarius, opts ...Option) *ToolSet {
	if p == nil {
		panic("agent: NewToolSet: p must not be nil")
	}
	ts := &ToolSet{postarius: p}
	for _, o := range opts {
		o(ts)
	}
	return ts
}

// CreateArgs holds the agent-supplied arguments for scheduling a new Posterum.
type CreateArgs struct {
	// Message is the text content to be delivered at reminder time.
	Message string

	// LocalTime is an ISO 8601 datetime string in the user's local time,
	// without a timezone suffix (e.g., "2024-01-15T09:00:00").
	// The timezone is conveyed separately via the Timezone field or resolved
	// from the context via WithTimezoneFromContext.
	LocalTime string

	// Timezone is an IANA timezone name (e.g., "Asia/Jakarta").
	// When empty, resolution falls through to the context timezone registered
	// via WithTimezoneFromContext, then to the default timezone registered via
	// WithDefaultTimezone.
	Timezone string
}

// Create parses LocalTime in the resolved location, then creates and enqueues
// a new Posterum via the underlying Postarius.
//
// ctx must carry a namespace via postera.WithNamespace when the backing
// Registry enforces multi-tenant isolation.
func (ts *ToolSet) Create(ctx context.Context, args CreateArgs) (postera.Posterum, error) {
	loc, err := ts.resolveLocation(ctx, args.Timezone)
	if err != nil {
		return postera.Posterum{}, err
	}

	remindAt, err := parseLocalTime(args.LocalTime, loc)
	if err != nil {
		return postera.Posterum{}, err
	}

	result, err := ts.postarius.Create(ctx, postera.Posterum{
		Message:  args.Message,
		RemindAt: remindAt,
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
	// When empty, resolution falls through to the context timezone registered
	// via WithTimezoneFromContext, then to the default timezone registered via
	// WithDefaultTimezone.
	Timezone string
}

// List returns Posterum entries matching the half-open window [From, To).
// Either or both bounds may be omitted to leave that side unbounded.
//
// ctx must carry a namespace via postera.WithNamespace when the backing
// Registry enforces multi-tenant isolation.
func (ts *ToolSet) List(ctx context.Context, args ListArgs) ([]postera.Posterum, error) {
	var q postera.Query

	if args.FromLocalTime != "" || args.ToLocalTime != "" {
		loc, err := ts.resolveLocation(ctx, args.Timezone)
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

	results, err := ts.postarius.List(ctx, q)
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
	// When empty, resolution falls through to the context timezone registered
	// via WithTimezoneFromContext, then to the default timezone registered via
	// WithDefaultTimezone.
	Timezone string
}

// ListByDate returns all Posterum entries scheduled on the calendar day of
// LocalDate, with day boundaries computed in the resolved location. This
// ensures that "today" or "tomorrow" resolves to the user's local calendar
// day rather than the server's UTC day.
//
// ctx must carry a namespace via postera.WithNamespace when the backing
// Registry enforces multi-tenant isolation.
func (ts *ToolSet) ListByDate(ctx context.Context, args ListByDateArgs) ([]postera.Posterum, error) {
	loc, err := ts.resolveLocation(ctx, args.Timezone)
	if err != nil {
		return nil, err
	}

	date, err := parseLocalDate(args.LocalDate, loc)
	if err != nil {
		return nil, err
	}

	results, err := ts.postarius.ListByDate(ctx, date)
	if err != nil {
		return nil, normalizeError(err)
	}
	return results, nil
}

// ListIncoming returns all Posterum entries scheduled to execute at or after
// the present instant.
//
// ctx must carry a namespace via postera.WithNamespace when the backing
// Registry enforces multi-tenant isolation.
func (ts *ToolSet) ListIncoming(ctx context.Context) ([]postera.Posterum, error) {
	results, err := ts.postarius.ListIncoming(ctx)
	if err != nil {
		return nil, normalizeError(err)
	}
	return results, nil
}

// ListToday returns all Posterum entries scheduled within the current
// calendar day in the user's local timezone. Day boundaries are computed
// using the location resolved from the context (via WithTimezoneFromContext),
// the registered default timezone (via WithDefaultTimezone), or UTC when
// neither is available.
//
// ctx must carry a namespace via postera.WithNamespace when the backing
// Registry enforces multi-tenant isolation.
func (ts *ToolSet) ListToday(ctx context.Context) ([]postera.Posterum, error) {
	loc := ts.LocationFromContext(ctx)
	results, err := ts.postarius.ListByDate(ctx, time.Now().In(loc))
	if err != nil {
		return nil, normalizeError(err)
	}
	return results, nil
}

// LocationFromContext resolves the *time.Location for the current request.
// It checks, in order: the IANA timezone string at the registered context key
// (WithTimezoneFromContext), the registered default timezone
// (WithDefaultTimezone), and finally time.UTC. It never returns nil.
//
// This method is intended for view-rendering code that always needs a
// location and treats unavailability as a graceful fallback rather than an
// error. For operations that require a valid timezone to parse user-supplied
// time strings, use the internal resolveLocation which returns an error when
// no timezone can be determined.
func (ts *ToolSet) LocationFromContext(ctx context.Context) *time.Location {
	if ts.timezoneKey != nil {
		if v, ok := ctx.Value(ts.timezoneKey).(string); ok && v != "" {
			if loc, err := time.LoadLocation(v); err == nil {
				return loc
			}
		}
	}
	if ts.defaultTZ != nil {
		return ts.defaultTZ
	}
	return time.UTC
}

// resolveLocation loads the *time.Location for the current operation.
//
// Priority:
//  1. tz argument, if non-empty
//  2. IANA string at ts.timezoneKey in ctx, if registered and present
//  3. ts.defaultTZ, if registered
//  4. error
//
// Unlike LocationFromContext, this method returns an error when no timezone
// can be determined and when a context value is present but invalid — both
// conditions indicate a programming error that the caller should surface
// rather than silently absorbing.
func (ts *ToolSet) resolveLocation(ctx context.Context, tz string) (*time.Location, error) {
	if tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return nil, fmt.Errorf("agent: unknown timezone %q: must be a valid IANA timezone name (e.g., %q)", tz, "Asia/Jakarta")
		}
		return loc, nil
	}
	if ts.timezoneKey != nil {
		if v, ok := ctx.Value(ts.timezoneKey).(string); ok && v != "" {
			loc, err := time.LoadLocation(v)
			if err != nil {
				return nil, fmt.Errorf("agent: timezone from context %q is not a valid IANA timezone name (e.g., %q)", v, "Asia/Jakarta")
			}
			return loc, nil
		}
	}
	if ts.defaultTZ != nil {
		return ts.defaultTZ, nil
	}
	return nil, fmt.Errorf("agent: timezone is required: provide a valid IANA timezone name (e.g., %q)", "Asia/Jakarta")
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
