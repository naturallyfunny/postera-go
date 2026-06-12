package postera

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

var ErrInvalidInput = errors.New("invalid input")

const (
	TimeLayout = "2006-01-02T15:04:05"
	idPrefix   = "pstr_"
)

func generateID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("postera: crypto/rand failed: " + err.Error())
	}
	return idPrefix + base64.RawURLEncoding.EncodeToString(b[:])
}

type Posterum struct {
	ID        string
	Human     string
	Agent     string
	Session   string
	Metadata  map[string]string
	Message   string
	TriggerAt time.Time
	CreatedAt time.Time
}

type CreateArgs struct {
	Message   string
	TriggerAt string
}

type Postarius struct {
	store       Store
	enqueuer    Enqueuer
	defaultTZ   *time.Location
	timezoneKey any
	humanKey    any
	agentKey    any
	sessionKey  any
	metadataKey any
}

type Option func(*Postarius)

func WithDefaultTimezone(loc *time.Location) Option {
	if loc == nil {
		panic("postera: WithDefaultTimezone: loc must not be nil")
	}
	return func(p *Postarius) {
		p.defaultTZ = loc
	}
}

func WithTimezoneFromContext(key any) Option {
	if key == nil {
		panic("postera: WithTimezoneFromContext: key must not be nil")
	}
	return func(p *Postarius) {
		p.timezoneKey = key
	}
}

func WithHumanFromContext(key any) Option {
	if key == nil {
		panic("postera: WithHumanFromContext: key must not be nil")
	}
	return func(p *Postarius) {
		p.humanKey = key
	}
}

func WithAgentFromContext(key any) Option {
	if key == nil {
		panic("postera: WithAgentFromContext: key must not be nil")
	}
	return func(p *Postarius) {
		p.agentKey = key
	}
}

func WithSessionFromContext(key any) Option {
	if key == nil {
		panic("postera: WithSessionFromContext: key must not be nil")
	}
	return func(p *Postarius) {
		p.sessionKey = key
	}
}

func WithMetadataFromContext(key any) Option {
	if key == nil {
		panic("postera: WithMetadataFromContext: key must not be nil")
	}
	return func(p *Postarius) {
		p.metadataKey = key
	}
}

func New(store Store, enqueuer Enqueuer, opts ...Option) *Postarius {
	if store == nil {
		panic("postera: New: store must not be nil")
	}
	if enqueuer == nil {
		panic("postera: New: enqueuer must not be nil")
	}
	p := &Postarius{store: store, enqueuer: enqueuer}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *Postarius) Create(ctx context.Context, args CreateArgs) (Posterum, error) {
	loc, err := p.resolveLocation(ctx)
	if err != nil {
		return Posterum{}, err
	}

	triggerAt, err := parseLocalTime(args.TriggerAt, loc)
	if err != nil {
		return Posterum{}, err
	}

	posterum := Posterum{
		Message:   args.Message,
		TriggerAt: triggerAt,
	}
	if err := p.applyPosterumContext(ctx, &posterum); err != nil {
		return Posterum{}, err
	}

	posterum.ID = generateID()
	posterum.CreatedAt = time.Now().UTC()

	if err := p.enqueuer.Enqueue(ctx, posterum); err != nil {
		return Posterum{}, fmt.Errorf("postera: enqueue: %w", err)
	}

	if err := p.store.Save(ctx, posterum); err != nil {
		// rollback: best-effort cancel; if the cancel also fails, both errors are surfaced to the caller
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

func (p *Postarius) ListUpcoming(ctx context.Context) ([]Posterum, error) {
	human, err := stringFromContext(ctx, p.humanKey, "human")
	if err != nil {
		return nil, err
	}
	agent, err := stringFromContext(ctx, p.agentKey, "agent")
	if err != nil {
		return nil, err
	}
	session, err := stringFromContext(ctx, p.sessionKey, "session")
	if err != nil {
		return nil, err
	}

	q := Query{
		Human:   human,
		Agent:   agent,
		Session: session,
		From:    time.Now().UTC(),
	}

	results, err := p.store.List(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("postera: list upcoming: %w", err)
	}
	return results, nil
}

func (p *Postarius) Cancel(ctx context.Context, id string) error {
	posterum, err := p.store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("postera: cancel: posterum not found: %w", err)
		}
		return fmt.Errorf("postera: cancel: %w", err)
	}

	if err := p.enqueuer.Cancel(ctx, id); err != nil {
		return fmt.Errorf("postera: cancel: %w", err)
	}

	if err := p.store.Remove(context.WithoutCancel(ctx), id); err != nil {
		// rollback: best-effort re-enqueue; if posterum.TriggerAt is in the past,
		// Cloud Tasks will dispatch the task immediately rather than restoring the original schedule.
		rollback := p.enqueuer.Enqueue(context.WithoutCancel(ctx), posterum)
		if rollback != nil {
			return errors.Join(
				fmt.Errorf("postera: cancel: %w", err),
				fmt.Errorf("postera: rollback enqueue: %w", rollback),
			)
		}
		return fmt.Errorf("postera: cancel: %w", err)
	}

	return nil
}

func (p *Postarius) LocationFromContext(ctx context.Context) *time.Location {
	if p.timezoneKey != nil {
		if v, ok := ctx.Value(p.timezoneKey).(string); ok && v != "" {
			if loc, err := time.LoadLocation(v); err == nil {
				return loc
			}
		}
	}
	if p.defaultTZ != nil {
		return p.defaultTZ
	}
	return time.UTC
}

func (p *Postarius) resolveLocation(ctx context.Context) (*time.Location, error) {
	if p.timezoneKey != nil {
		if v, ok := ctx.Value(p.timezoneKey).(string); ok && v != "" {
			loc, err := time.LoadLocation(v)
			if err != nil {
				return nil, fmt.Errorf("postera: timezone from context %q is not a valid IANA timezone name (e.g., %q)", v, "Asia/Jakarta")
			}
			return loc, nil
		}
	}
	if p.defaultTZ != nil {
		return p.defaultTZ, nil
	}
	return nil, fmt.Errorf("postera: timezone is required: provide a valid IANA timezone name (e.g., %q)", "Asia/Jakarta")
}

func (p *Postarius) applyPosterumContext(ctx context.Context, posterum *Posterum) error {
	var err error
	if posterum.Human, err = stringFromContext(ctx, p.humanKey, "human"); err != nil {
		return err
	}
	if posterum.Agent, err = stringFromContext(ctx, p.agentKey, "agent"); err != nil {
		return err
	}
	if posterum.Session, err = stringFromContext(ctx, p.sessionKey, "session"); err != nil {
		return err
	}
	if posterum.Metadata, err = metadataFromContext(ctx, p.metadataKey); err != nil {
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
		return "", fmt.Errorf("postera: %s context value must be a string, got %T", field, v)
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
		return nil, fmt.Errorf("postera: metadata context value must be map[string]string, got %T", v)
	}
	copied := make(map[string]string, len(metadata))
	for k, v := range metadata {
		copied[k] = v
	}
	return copied, nil
}

func parseLocalTime(s string, loc *time.Location) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("postera: datetime is required: provide a value in format %q (e.g., %q)", TimeLayout, "2024-01-15T09:00:00")
	}
	parsed, err := time.ParseInLocation(TimeLayout, s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("postera: invalid datetime %q: expected format %q without a timezone suffix (e.g., %q)", s, TimeLayout, "2024-01-15T09:00:00")
	}
	return parsed, nil
}
