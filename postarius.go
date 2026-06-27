package postera

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
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

// Postarius is the agent-facing orchestrator for scheduled messages. It reads
// caller identity (human, agent, session) and timezone from context using the
// keys configured via the With*FromContext options, then coordinates the Store
// and Enqueuer.
//
// Identity fields are used for filtering and propagation only — they are not
// access control. Create stamps them onto the posterum, ListUpcoming filters by
// them, and Cancel scopes to them. Every such check is bypassable by any caller
// holding the Store, so authentication, authorization, and tenant isolation must
// be enforced above this SDK. See the README's "Ownership & Access Control".
type metadataEntry struct {
	metaKey string
	ctxKey  any
}

type Postarius struct {
	store           Store
	enqueuer        Enqueuer
	defaultTZ       *time.Location
	timezoneKey     any
	humanKey        any
	agentKey        any
	sessionKey      any
	metadataEntries []metadataEntry
	logger          *slog.Logger
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

// WithHumanFromContext configures the context key from which the caller's human
// identity is read. It applies uniformly across the surface: Create stamps it,
// ListUpcoming filters by it, and Cancel scopes to it. It is an identity field
// for filtering and propagation, not access control.
func WithHumanFromContext(key any) Option {
	if key == nil {
		panic("postera: WithHumanFromContext: key must not be nil")
	}
	return func(p *Postarius) {
		p.humanKey = key
	}
}

// WithAgentFromContext configures the context key for the caller's agent
// identity. Like WithHumanFromContext, it is stamped, filtered, and scoped
// uniformly across Create, ListUpcoming, and Cancel — for filtering and
// propagation, not access control.
func WithAgentFromContext(key any) Option {
	if key == nil {
		panic("postera: WithAgentFromContext: key must not be nil")
	}
	return func(p *Postarius) {
		p.agentKey = key
	}
}

// WithSessionFromContext configures the context key for the caller's session
// identity. Like WithHumanFromContext, it is stamped, filtered, and scoped
// uniformly across Create, ListUpcoming, and Cancel — for filtering and
// propagation, not access control.
func WithSessionFromContext(key any) Option {
	if key == nil {
		panic("postera: WithSessionFromContext: key must not be nil")
	}
	return func(p *Postarius) {
		p.sessionKey = key
	}
}

// WithMetadataEntryFromContext configures one metadata entry stamped onto the
// posterum at Create: metaKey is the key written to Posterum.Metadata, ctxKey
// is the context key whose string value is read at call time. Call once per
// entry; entries with an empty context value are omitted from Metadata.
func WithMetadataEntryFromContext(metaKey string, ctxKey any) Option {
	if metaKey == "" {
		panic("postera: WithMetadataEntryFromContext: metaKey must not be empty")
	}
	if ctxKey == nil {
		panic("postera: WithMetadataEntryFromContext: ctxKey must not be nil")
	}
	return func(p *Postarius) {
		p.metadataEntries = append(p.metadataEntries, metadataEntry{metaKey: metaKey, ctxKey: ctxKey})
	}
}

// WithLogger enables diagnostic logging through l. The only event emitted is a
// Warn when an identity key configured via a With*FromContext option resolves to
// an empty context value — a likely sign the caller forgot to populate context,
// leaving the operation unscoped for that field. Without WithLogger, Postarius is
// silent.
//
// This is a debugging aid, not a guard: it records the misconfiguration, it does
// not prevent the unscoped operation. To be intentionally unscoped without noise
// (e.g. system tooling), use a Postarius constructed without that identity key.
func WithLogger(l *slog.Logger) Option {
	if l == nil {
		panic("postera: WithLogger: logger must not be nil")
	}
	return func(p *Postarius) {
		p.logger = l
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

// Create schedules a new posterum. Message and TriggerAt come from args;
// TriggerAt is a local datetime (TimeLayout) resolved against the caller's
// timezone from context. Human, Agent, Session, and Metadata are read from
// context and stamped onto the posterum; ID and CreatedAt are assigned here.
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

// ListUpcoming returns the caller's scheduled posterums from now onward, filtered
// by the human, agent, and session identity read from context. An identity field
// absent from context imposes no filter — so a caller with no identity in context
// sees everything. Populate identity in context to scope the result to a caller.
func (p *Postarius) ListUpcoming(ctx context.Context) ([]Posterum, error) {
	human, err := p.identity(ctx, p.humanKey, "human", "ListUpcoming")
	if err != nil {
		return nil, err
	}
	agent, err := p.identity(ctx, p.agentKey, "agent", "ListUpcoming")
	if err != nil {
		return nil, err
	}
	session, err := p.identity(ctx, p.sessionKey, "session", "ListUpcoming")
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

// Cancel removes a scheduled posterum by ID. It is scoped to the caller's
// identity from context the same way ListUpcoming is: a posterum outside the
// caller's scope is reported as ErrNotFound — indistinguishable from a genuinely
// missing ID, which prevents cross-scope enumeration. A caller with no identity
// in context can cancel any posterum. This scoping is filtering, not access
// control — it is bypassable and must not be relied on as a security boundary.
func (p *Postarius) Cancel(ctx context.Context, id string) error {
	posterum, err := p.store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("postera: cancel: posterum not found: %w", err)
		}
		return fmt.Errorf("postera: cancel: %w", err)
	}

	// Scope the cancel to the caller's identity from context: a posterum outside
	// the caller's scope is reported as not found, mirroring ListUpcoming's filter
	// and preventing cross-scope enumeration. This is filtering, not access
	// control — it is bypassable and must not be relied on as a security boundary
	// (see README). Reuses the posterum already fetched above; no extra read.
	inScope, err := p.inContextScope(ctx, posterum)
	if err != nil {
		return fmt.Errorf("postera: cancel: %w", err)
	}
	if !inScope {
		return fmt.Errorf("postera: cancel: posterum not found: %w", ErrNotFound)
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

// LocalizesFromContext reports whether this Postarius resolves the timezone
// per-request from context (configured via WithTimezoneFromContext). When true,
// the local datetime an agent supplies as trigger_at and the times rendered
// back to it share one consistent local frame, so a caller can truthfully tell
// an agent that no timezone conversion is needed.
//
// It reflects configuration only. It does NOT guarantee the context actually
// carries a (correct) timezone at call time — that remains the caller's
// responsibility, the same way identity scoping is enforced above this SDK
// (see "Ownership & Access Control"). Garbage-in/garbage-out is not policed here.
func (p *Postarius) LocalizesFromContext() bool { return p.timezoneKey != nil }

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
	if posterum.Human, err = p.identity(ctx, p.humanKey, "human", "Create"); err != nil {
		return err
	}
	if posterum.Agent, err = p.identity(ctx, p.agentKey, "agent", "Create"); err != nil {
		return err
	}
	if posterum.Session, err = p.identity(ctx, p.sessionKey, "session", "Create"); err != nil {
		return err
	}
	var metadata map[string]string
	for _, e := range p.metadataEntries {
		v, err := stringFromContext(ctx, e.ctxKey, e.metaKey)
		if err != nil {
			return err
		}
		if v != "" {
			if metadata == nil {
				metadata = make(map[string]string)
			}
			metadata[e.metaKey] = v
		}
	}
	posterum.Metadata = metadata
	return nil
}

// identity reads a configured identity field from context. When the field's key
// is configured but the context value is empty, it emits a Warn through the
// logger (if one is set via WithLogger): the operation will run unscoped for that
// field, a common source of accidental cross-scope reads. A field whose key is
// not configured is silent — absence is intentional, not a misconfiguration.
func (p *Postarius) identity(ctx context.Context, key any, field, op string) (string, error) {
	v, err := stringFromContext(ctx, key, field)
	if err != nil {
		return "", err
	}
	if key != nil && v == "" && p.logger != nil {
		p.logger.WarnContext(ctx,
			"postera: identity key configured but context value is empty; operation will not be scoped by this field",
			slog.String("operation", op),
			slog.String("field", field),
		)
	}
	return v, nil
}

// inContextScope reports whether posterum falls within the caller's identity
// scope, read from context. Each identity field is checked only when its context
// key is configured and a non-empty value is present — an absent field imposes no
// constraint, so a caller with no identity in context (e.g. system tooling) can
// cancel any posterum. Comparison reuses the same context keys as Create and
// ListUpcoming, so all three methods agree on what "the caller" means.
func (p *Postarius) inContextScope(ctx context.Context, posterum Posterum) (bool, error) {
	human, err := p.identity(ctx, p.humanKey, "human", "Cancel")
	if err != nil {
		return false, err
	}
	if human != "" && posterum.Human != human {
		return false, nil
	}

	agent, err := p.identity(ctx, p.agentKey, "agent", "Cancel")
	if err != nil {
		return false, err
	}
	if agent != "" && posterum.Agent != agent {
		return false, nil
	}

	session, err := p.identity(ctx, p.sessionKey, "session", "Cancel")
	if err != nil {
		return false, err
	}
	if session != "" && posterum.Session != session {
		return false, nil
	}

	return true, nil
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
