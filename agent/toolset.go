package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.naturallyfunny.dev/postera"
)

type ToolSet struct {
	postarius   *postera.Postarius
	defaultTZ   *time.Location
	timezoneKey any
}

type ToolSetOption func(*ToolSet)

func WithDefaultTimezone(loc *time.Location) ToolSetOption {
	if loc == nil {
		panic("agent: WithDefaultTimezone: loc must not be nil")
	}
	return func(ts *ToolSet) {
		ts.defaultTZ = loc
	}
}

func WithTimezoneFromContext(key any) ToolSetOption {
	if key == nil {
		panic("agent: WithTimezoneFromContext: key must not be nil")
	}
	return func(ts *ToolSet) {
		ts.timezoneKey = key
	}
}

func NewToolSet(p *postera.Postarius, opts ...ToolSetOption) *ToolSet {
	if p == nil {
		panic("agent: NewToolSet: p must not be nil")
	}
	ts := &ToolSet{postarius: p}
	for _, opt := range opts {
		opt(ts)
	}
	return ts
}

type CreateArgs struct {
	Message   string
	TriggerAt string
}

type ListArgs struct {
	From string
	To   string
}

func (ts *ToolSet) Create(ctx context.Context, args CreateArgs) (postera.Posterum, error) {
	loc, err := ts.resolveLocation(ctx)
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

func (ts *ToolSet) List(ctx context.Context, args ListArgs) ([]postera.Posterum, error) {
	var q postera.TimeRange

	if args.From != "" || args.To != "" {
		loc, err := ts.resolveLocation(ctx)
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

func (ts *ToolSet) ListIncoming(ctx context.Context) ([]postera.Posterum, error) {
	results, err := ts.postarius.ListIncoming(ctx)
	if err != nil {
		return nil, normalizeError(err)
	}
	return results, nil
}

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

func (ts *ToolSet) resolveLocation(ctx context.Context) (*time.Location, error) {
	if ts.timezoneKey != nil {
		if v, ok := ctx.Value(ts.timezoneKey).(string); ok && v != "" {
			loc, err := time.LoadLocation(v)
			if err != nil {
				return nil, fmt.Errorf("postera: agent: timezone from context %q is not a valid IANA timezone name (e.g., %q)", v, "Asia/Jakarta")
			}
			return loc, nil
		}
	}
	if ts.defaultTZ != nil {
		return ts.defaultTZ, nil
	}
	return nil, fmt.Errorf("postera: agent: timezone is required: provide a valid IANA timezone name (e.g., %q)", "Asia/Jakarta")
}

func parseLocalTime(s string, loc *time.Location) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("postera: agent: datetime is required: provide a value in format %q (e.g., %q)", "2006-01-02T15:04:05", "2024-01-15T09:00:00")
	}
	parsed, err := time.ParseInLocation("2006-01-02T15:04:05", s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("postera: agent: invalid datetime %q: expected format %q without a timezone suffix (e.g., %q)", s, "2006-01-02T15:04:05", "2024-01-15T09:00:00")
	}
	return parsed, nil
}

func normalizeError(err error) error {
	switch {
	case errors.Is(err, postera.ErrInvalidInput):
		return fmt.Errorf("postera: agent: invalid input: verify that trigger_at is a valid non-zero datetime and all required fields are provided: %w", err)
	case errors.Is(err, postera.ErrNotFound):
		return fmt.Errorf("postera: agent: posterum not found: the entry does not exist or is inaccessible: %w", err)
	default:
		return err
	}
}
