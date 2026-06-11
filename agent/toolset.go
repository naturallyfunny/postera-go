package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.naturallyfunny.dev/postera"
)

const TimeLayout = "2006-01-02T15:04:05"

type ToolSet struct {
	postarius   *postera.Postarius
	defaultTZ   *time.Location
	timezoneKey any
	humanKey    any
	agentKey    any
	sessionKey  any
	metadataKey any
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

func WithHumanFromContext(key any) ToolSetOption {
	if key == nil {
		panic("agent: WithHumanFromContext: key must not be nil")
	}
	return func(ts *ToolSet) {
		ts.humanKey = key
	}
}

func WithAgentFromContext(key any) ToolSetOption {
	if key == nil {
		panic("agent: WithAgentFromContext: key must not be nil")
	}
	return func(ts *ToolSet) {
		ts.agentKey = key
	}
}

func WithSessionFromContext(key any) ToolSetOption {
	if key == nil {
		panic("agent: WithSessionFromContext: key must not be nil")
	}
	return func(ts *ToolSet) {
		ts.sessionKey = key
	}
}

func WithMetadataFromContext(key any) ToolSetOption {
	if key == nil {
		panic("agent: WithMetadataFromContext: key must not be nil")
	}
	return func(ts *ToolSet) {
		ts.metadataKey = key
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

	posterum := postera.Posterum{
		Message:   args.Message,
		TriggerAt: triggerAt,
	}
	if err := ts.applyPosterumContext(ctx, &posterum); err != nil {
		return postera.Posterum{}, err
	}

	result, err := ts.postarius.Create(ctx, posterum)
	if err != nil {
		return postera.Posterum{}, normalizeError(err)
	}
	return result, nil
}

func (ts *ToolSet) List(ctx context.Context, args ListArgs) ([]postera.Posterum, error) {
	var q postera.Query

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
	if err := ts.applyQueryContext(ctx, &q); err != nil {
		return nil, err
	}

	results, err := ts.postarius.List(ctx, q)
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

func (ts *ToolSet) applyPosterumContext(ctx context.Context, p *postera.Posterum) error {
	var err error
	if p.Human, err = stringFromContext(ctx, ts.humanKey, "human"); err != nil {
		return err
	}
	if p.Agent, err = stringFromContext(ctx, ts.agentKey, "agent"); err != nil {
		return err
	}
	if p.Session, err = stringFromContext(ctx, ts.sessionKey, "session"); err != nil {
		return err
	}
	if p.Metadata, err = metadataFromContext(ctx, ts.metadataKey); err != nil {
		return err
	}
	return nil
}

func (ts *ToolSet) applyQueryContext(ctx context.Context, q *postera.Query) error {
	var err error
	if q.Human, err = stringFromContext(ctx, ts.humanKey, "human"); err != nil {
		return err
	}
	if q.Agent, err = stringFromContext(ctx, ts.agentKey, "agent"); err != nil {
		return err
	}
	if q.Session, err = stringFromContext(ctx, ts.sessionKey, "session"); err != nil {
		return err
	}
	return nil
}

func stringFromContext(ctx context.Context, key any, field string) (string, error) {
	if key == nil {
		return "", nil
	}
	v := ctx.Value(key)
	if v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("postera: agent: %s context value must be a string, got %T", field, v)
	}
	return s, nil
}

func metadataFromContext(ctx context.Context, key any) (map[string]string, error) {
	if key == nil {
		return nil, nil
	}
	v := ctx.Value(key)
	if v == nil {
		return nil, nil
	}
	metadata, ok := v.(map[string]string)
	if !ok {
		return nil, fmt.Errorf("postera: agent: metadata context value must be map[string]string, got %T", v)
	}
	copied := make(map[string]string, len(metadata))
	for k, v := range metadata {
		copied[k] = v
	}
	return copied, nil
}

func parseLocalTime(s string, loc *time.Location) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("postera: agent: datetime is required: provide a value in format %q (e.g., %q)", TimeLayout, "2024-01-15T09:00:00")
	}
	parsed, err := time.ParseInLocation(TimeLayout, s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("postera: agent: invalid datetime %q: expected format %q without a timezone suffix (e.g., %q)", s, TimeLayout, "2024-01-15T09:00:00")
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
