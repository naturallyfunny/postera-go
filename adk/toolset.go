// Package adk provides Google ADK function tools for *postera.Postarius and
// the ToolSet adapter that bridges human-readable agent inputs (ISO 8601
// datetimes, IANA timezone names) to the precise Go types Postarius expects.
//
// Identity propagation: UserID is extracted from the ADK tool context on every
// call and injected into the Go context under UserIDKey. Register UserIDKey with
// postgres.WithColumnMapping or cloudtasks.WithHeaderMapping to propagate it
// into the storage and scheduling layers without coupling them to ADK.
//
// Timezone resolution: callers may register a context key via
// WithTimezoneFromContext, and ToolSet reads the IANA timezone string from
// that key on every operation. This decouples ToolSet from any specific HTTP
// framework or middleware.
//
// Callers running in environments without system timezone data should import
// the bundled database:
//
//	import _ "time/tzdata"
package adk

import (
	"context"
	"errors"
	"fmt"
	"time"

	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"go.naturallyfunny.dev/postera"
)

// timeLayout is the expected datetime format for agent-supplied TriggerAt and
// list-bound fields. Agents must NOT embed a timezone suffix: the timezone is
// resolved separately through time.ParseInLocation, which anchors the result
// to the user's IANA locale. Embedding an offset would silently override the
// resolved timezone and reintroduce the Server Time Leak anti-pattern.
const timeLayout = "2006-01-02T15:04:05"

// dateLayout is the expected date-only format for agent-supplied Date fields.
const dateLayout = "2006-01-02"

// naiveTimeLayout formats a time.Time as naive ISO 8601 with no timezone suffix.
// Keeping it separate from timeLayout prevents accidental use with time.Parse,
// which would silently treat it as UTC.
const naiveTimeLayout = "2006-01-02T15:04:05"

// userIDKeyType is the unexported type for UserIDKey, preventing key
// collisions with any other package.
type userIDKeyType struct{}

// UserIDKey is the context key under which the ADK adapter stores the
// authenticated user's identity. Register it with postgres.WithColumnMapping
// or cloudtasks.WithHeaderMapping to propagate identity into storage and
// scheduling layers without coupling them to ADK directly.
var UserIDKey = userIDKeyType{}

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
		panic("adk: WithDefaultTimezone: loc must not be nil")
	}
	return func(ts *ToolSet) {
		ts.defaultTZ = loc
	}
}

// WithTimezoneFromContext registers the context key under which ToolSet looks
// for an IANA timezone string on every operation. The value is read
// dynamically per-request from the context passed to each method.
//
// Panics if key is nil.
func WithTimezoneFromContext(key any) Option {
	if key == nil {
		panic("adk: WithTimezoneFromContext: key must not be nil")
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
		panic("adk: NewToolSet: p must not be nil")
	}
	ts := &ToolSet{postarius: p}
	for _, o := range opts {
		o(ts)
	}
	return ts
}

// CreateArgs holds the agent-supplied arguments for scheduling a new Posterum.
type CreateArgs struct {
	// Message is the text content to be delivered at trigger time.
	Message string

	// TriggerAt is an ISO 8601 datetime string without a timezone suffix
	// (e.g., "2024-01-15T09:00:00"). The timezone is resolved via the
	// Timezone field, the context key registered via WithTimezoneFromContext,
	// or the default registered via WithDefaultTimezone.
	TriggerAt string

	// Timezone is an IANA timezone name (e.g., "Asia/Jakarta").
	// When empty, resolution falls through to the context timezone registered
	// via WithTimezoneFromContext, then to the default registered via
	// WithDefaultTimezone.
	Timezone string
}

// Create parses TriggerAt in the resolved location, then creates and enqueues
// a new Posterum via the underlying Postarius.
func (ts *ToolSet) Create(ctx context.Context, args CreateArgs) (postera.Posterum, error) {
	loc, err := ts.resolveLocation(ctx, args.Timezone)
	if err != nil {
		return postera.Posterum{}, err
	}

	triggerAt, err := parseLocalTime(args.TriggerAt, loc)
	if err != nil {
		return postera.Posterum{}, err
	}

	result, err := ts.postarius.Create(ctx, args.Message, triggerAt)
	if err != nil {
		return postera.Posterum{}, normalizeError(err)
	}
	return result, nil
}

// ListArgs holds the agent-supplied arguments for a time-range query.
// Either bound may be omitted; an absent bound is treated as open.
type ListArgs struct {
	// From is the inclusive lower bound as an ISO 8601 datetime string without
	// a timezone suffix. Empty means no lower bound.
	From string

	// To is the exclusive upper bound as an ISO 8601 datetime string without a
	// timezone suffix. Empty means no upper bound.
	To string

	// Timezone is an IANA timezone name used to parse any non-empty bound.
	Timezone string
}

// List returns Posterum entries matching the half-open window [From, To).
func (ts *ToolSet) List(ctx context.Context, args ListArgs) ([]postera.Posterum, error) {
	var q postera.TimeRange

	if args.From != "" || args.To != "" {
		loc, err := ts.resolveLocation(ctx, args.Timezone)
		if err != nil {
			return nil, err
		}

		if args.From != "" {
			from, err := parseLocalTime(args.From, loc)
			if err != nil {
				return nil, err
			}
			q.From = from
		}

		if args.To != "" {
			to, err := parseLocalTime(args.To, loc)
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
	// Date is an ISO 8601 date string (e.g., "2024-01-15").
	Date string

	// Timezone is an IANA timezone name (e.g., "Asia/Jakarta").
	Timezone string
}

// ListByDate returns all Posterum entries scheduled on the calendar day of
// Date, with day boundaries computed in the resolved location.
func (ts *ToolSet) ListByDate(ctx context.Context, args ListByDateArgs) ([]postera.Posterum, error) {
	loc, err := ts.resolveLocation(ctx, args.Timezone)
	if err != nil {
		return nil, err
	}

	date, err := parseLocalDate(args.Date, loc)
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
func (ts *ToolSet) ListIncoming(ctx context.Context) ([]postera.Posterum, error) {
	results, err := ts.postarius.ListIncoming(ctx)
	if err != nil {
		return nil, normalizeError(err)
	}
	return results, nil
}

// ListToday returns all Posterum entries scheduled within the current
// calendar day in the user's local timezone.
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
// Use this for view rendering where unavailability should fall back silently.
// For operations that parse user-supplied time strings use resolveLocation,
// which returns an error instead.
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

func (ts *ToolSet) resolveLocation(ctx context.Context, tz string) (*time.Location, error) {
	if tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return nil, fmt.Errorf("adk: unknown timezone %q: must be a valid IANA timezone name (e.g., %q)", tz, "Asia/Jakarta")
		}
		return loc, nil
	}
	if ts.timezoneKey != nil {
		if v, ok := ctx.Value(ts.timezoneKey).(string); ok && v != "" {
			loc, err := time.LoadLocation(v)
			if err != nil {
				return nil, fmt.Errorf("adk: timezone from context %q is not a valid IANA timezone name (e.g., %q)", v, "Asia/Jakarta")
			}
			return loc, nil
		}
	}
	if ts.defaultTZ != nil {
		return ts.defaultTZ, nil
	}
	return nil, fmt.Errorf("adk: timezone is required: provide a valid IANA timezone name (e.g., %q)", "Asia/Jakarta")
}

// posterumView is the agent-facing representation of a Posterum. Time fields
// are naive ISO 8601 strings (no timezone suffix) localised to the user's
// timezone: the LLM reads "22:00:00" and reasons about it naturally without
// being exposed to UTC offsets.
type posterumView struct {
	ID        string `json:"id"`
	Message   string `json:"message"`
	TriggerAt string `json:"trigger_at"`
	CreatedAt string `json:"created_at"`
}

type createArgs struct {
	Message   string `json:"message"`
	TriggerAt string `json:"trigger_at"`
}

type listArgs struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type listByDateArgs struct {
	Date string `json:"date"`
}

type listIncomingArgs struct{}

type listTodayArgs struct{}

type listOutput struct {
	Entries []posterumView `json:"entries"`
}

// Tools builds all five ADK function tools from ts and returns them as a
// slice. Returns an error if any tool fails to register.
func Tools(ts *ToolSet) ([]adktool.Tool, error) {
	create, err := functiontool.New(
		functiontool.Config{
			Name:        "create_posterum",
			Description: "Schedule a future memory trigger for yourself at a specific date and time. Provide the datetime in the user's local time as an ISO 8601 string without a timezone suffix (e.g. 2026-05-07T22:00:00). All times are automatically handled in the user's local timezone.",
		},
		func(toolCtx adktool.Context, in createArgs) (posterumView, error) {
			ctx, err := contextWithUserID(toolCtx)
			if err != nil {
				return posterumView{}, err
			}
			p, err := ts.Create(ctx, CreateArgs{
				Message:   in.Message,
				TriggerAt: in.TriggerAt,
			})
			if err != nil {
				return posterumView{}, err
			}
			return toPosterumView(p, ts.LocationFromContext(ctx)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	list, err := functiontool.New(
		functiontool.Config{
			Name:        "list_posterum",
			Description: "List scheduled memory triggers within an optional time window. Leave from or to empty to leave that side unbounded. Provide datetime bounds as ISO 8601 strings without a timezone suffix (e.g. 2024-01-15T09:00:00).",
		},
		func(toolCtx adktool.Context, in listArgs) (listOutput, error) {
			ctx, err := contextWithUserID(toolCtx)
			if err != nil {
				return listOutput{}, err
			}
			entries, err := ts.List(ctx, ListArgs{
				From: in.From,
				To:   in.To,
			})
			if err != nil {
				return listOutput{}, err
			}
			return listOutput{Entries: toPosterumViews(entries, ts.LocationFromContext(ctx))}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	listByDate, err := functiontool.New(
		functiontool.Config{
			Name:        "list_posterum_by_date",
			Description: "List all memory triggers scheduled on a specific calendar day. Day boundaries are computed in the user's local timezone. Provide the date as an ISO 8601 string (e.g. 2024-01-15).",
		},
		func(toolCtx adktool.Context, in listByDateArgs) (listOutput, error) {
			ctx, err := contextWithUserID(toolCtx)
			if err != nil {
				return listOutput{}, err
			}
			entries, err := ts.ListByDate(ctx, ListByDateArgs{
				Date: in.Date,
			})
			if err != nil {
				return listOutput{}, err
			}
			return listOutput{Entries: toPosterumViews(entries, ts.LocationFromContext(ctx))}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	listIncoming, err := functiontool.New(
		functiontool.Config{
			Name:        "list_incoming_posterum",
			Description: "List all memory triggers that are scheduled to execute at or after the current instant. Use this to show the user what future triggers are pending.",
		},
		func(toolCtx adktool.Context, _ listIncomingArgs) (listOutput, error) {
			ctx, err := contextWithUserID(toolCtx)
			if err != nil {
				return listOutput{}, err
			}
			entries, err := ts.ListIncoming(ctx)
			if err != nil {
				return listOutput{}, err
			}
			return listOutput{Entries: toPosterumViews(entries, ts.LocationFromContext(ctx))}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	listToday, err := functiontool.New(
		functiontool.Config{
			Name:        "list_today_posterum",
			Description: "List all memory triggers scheduled within the current calendar day in the user's local timezone, both past and future. Use this when the user asks what is on today's schedule.",
		},
		func(toolCtx adktool.Context, _ listTodayArgs) (listOutput, error) {
			ctx, err := contextWithUserID(toolCtx)
			if err != nil {
				return listOutput{}, err
			}
			entries, err := ts.ListToday(ctx)
			if err != nil {
				return listOutput{}, err
			}
			return listOutput{Entries: toPosterumViews(entries, ts.LocationFromContext(ctx))}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	return []adktool.Tool{create, list, listByDate, listIncoming, listToday}, nil
}

// contextWithUserID extracts UserID from toolCtx and returns a context.Context
// carrying it under UserIDKey. toolCtx is used as parent so deadline,
// cancellation, and upstream middleware values (e.g. timezone) propagate.
func contextWithUserID(toolCtx adktool.Context) (context.Context, error) {
	userID := toolCtx.UserID()
	if userID == "" {
		return nil, errors.New("adk: unauthenticated: UserID is empty; ensure the agent is configured with a valid user session")
	}
	return context.WithValue(toolCtx, UserIDKey, userID), nil
}

func toPosterumView(p postera.Posterum, loc *time.Location) posterumView {
	return posterumView{
		ID:        p.ID,
		Message:   p.Message,
		TriggerAt: p.TriggerAt.In(loc).Format(naiveTimeLayout),
		CreatedAt: p.CreatedAt.In(loc).Format(naiveTimeLayout),
	}
}

func toPosterumViews(entries []postera.Posterum, loc *time.Location) []posterumView {
	views := make([]posterumView, len(entries))
	for i, e := range entries {
		views[i] = toPosterumView(e, loc)
	}
	return views
}

func parseLocalTime(s string, loc *time.Location) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("adk: datetime is required: provide a value in format %q (e.g., %q)", timeLayout, "2024-01-15T09:00:00")
	}
	parsed, err := time.ParseInLocation(timeLayout, s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("adk: invalid datetime %q: expected format %q without a timezone suffix (e.g., %q)", s, timeLayout, "2024-01-15T09:00:00")
	}
	return parsed, nil
}

func parseLocalDate(s string, loc *time.Location) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("adk: date is required: provide a value in format %q (e.g., %q)", dateLayout, "2024-01-15")
	}
	parsed, err := time.ParseInLocation(dateLayout, s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("adk: invalid date %q: expected format %q (e.g., %q)", s, dateLayout, "2024-01-15")
	}
	return parsed, nil
}

func normalizeError(err error) error {
	switch {
	case errors.Is(err, postera.ErrInvalidInput):
		return fmt.Errorf("adk: invalid input — verify that trigger_at is a valid non-zero datetime and all required fields are provided: %w", err)
	case errors.Is(err, postera.ErrNotFound):
		return fmt.Errorf("adk: posterum not found — the entry does not exist or is inaccessible: %w", err)
	default:
		return err
	}
}
