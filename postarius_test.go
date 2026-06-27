package postera

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"
	"time"
)

type humanKey struct{}
type agentKey struct{}
type sessionKey struct{}
type timezoneKey struct{}

type captureStore struct {
	saved   Posterum
	queried Query
	getID   string
	getErr  error
	get     Posterum
}

func (s *captureStore) Save(_ context.Context, p Posterum) error {
	s.saved = p
	return nil
}

func (s *captureStore) Get(_ context.Context, id string) (Posterum, error) {
	s.getID = id
	return s.get, s.getErr
}

func (s *captureStore) Remove(context.Context, string) error {
	return nil
}

func (s *captureStore) List(_ context.Context, q Query) ([]Posterum, error) {
	s.queried = q
	return nil, nil
}

type captureEnqueuer struct {
	enqueued   Posterum
	canceledID string
}

func (e *captureEnqueuer) Enqueue(_ context.Context, p Posterum) error {
	e.enqueued = p
	return nil
}

func (e *captureEnqueuer) Cancel(_ context.Context, id string) error {
	e.canceledID = id
	return nil
}

func newPostarius(opts ...Option) (*Postarius, *captureStore, *captureEnqueuer) {
	store := &captureStore{}
	enqueuer := &captureEnqueuer{}
	return New(store, enqueuer, opts...), store, enqueuer
}

func TestCreateParsesLocalTimeWithDefaultTimezone(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	p, store, enqueuer := newPostarius(WithDefaultTimezone(loc))

	got, err := p.Create(context.Background(), CreateArgs{
		Message:   "follow up",
		TriggerAt: "2026-06-11T09:30:00",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	want := time.Date(2026, 6, 11, 9, 30, 0, 0, loc)
	if !got.TriggerAt.Equal(want) {
		t.Fatalf("TriggerAt: want %s, got %s", want, got.TriggerAt)
	}
	if got.ID == "" {
		t.Fatal("ID should be generated")
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("CreatedAt should be set")
	}
	if !reflect.DeepEqual(store.saved, got) {
		t.Fatalf("saved posterum mismatch")
	}
	if !reflect.DeepEqual(enqueuer.enqueued, got) {
		t.Fatalf("enqueued posterum mismatch")
	}
}

func TestCreateParsesLocalTimeWithTimezoneFromContext(t *testing.T) {
	p, _, _ := newPostarius(WithTimezoneFromContext(timezoneKey{}))

	loc, _ := time.LoadLocation("Asia/Jakarta")
	ctx := context.WithValue(context.Background(), timezoneKey{}, "Asia/Jakarta")

	got, err := p.Create(ctx, CreateArgs{
		Message:   "follow up",
		TriggerAt: "2026-06-11T09:30:00",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	want := time.Date(2026, 6, 11, 9, 30, 0, 0, loc)
	if !got.TriggerAt.Equal(want) {
		t.Fatalf("TriggerAt: want %s, got %s", want, got.TriggerAt)
	}
}

func TestCreateAppliesIdentityFromContext(t *testing.T) {
	p, store, _ := newPostarius(
		WithDefaultTimezone(time.UTC),
		WithHumanFromContext(humanKey{}),
		WithAgentFromContext(agentKey{}),
		WithSessionFromContext(sessionKey{}),
		WithMetadataEntryFromContext("timezone", timezoneKey{}),
	)

	ctx := context.WithValue(context.Background(), humanKey{}, "human-1")
	ctx = context.WithValue(ctx, agentKey{}, "agent-1")
	ctx = context.WithValue(ctx, sessionKey{}, "session-1")
	ctx = context.WithValue(ctx, timezoneKey{}, "Asia/Jakarta")

	got, err := p.Create(ctx, CreateArgs{
		Message:   "follow up",
		TriggerAt: "2026-06-11T09:30:00",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got.Human != "human-1" || got.Agent != "agent-1" || got.Session != "session-1" {
		t.Fatalf("identity fields: %#v", got)
	}
	want := map[string]string{"timezone": "Asia/Jakarta"}
	if !reflect.DeepEqual(got.Metadata, want) {
		t.Fatalf("metadata: want %#v, got %#v", want, got.Metadata)
	}
	if !reflect.DeepEqual(store.saved, got) {
		t.Fatalf("saved posterum mismatch")
	}
}

func TestCreateMissingIdentityAllowed(t *testing.T) {
	p, _, _ := newPostarius(
		WithDefaultTimezone(time.UTC),
		WithHumanFromContext(humanKey{}),
	)

	got, err := p.Create(context.Background(), CreateArgs{
		Message:   "follow up",
		TriggerAt: "2026-06-11T09:30:00",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Human != "" {
		t.Fatalf("Human should be empty when not in context, got %q", got.Human)
	}
}

func TestCreateErrorsWhenNoTimezone(t *testing.T) {
	p, _, _ := newPostarius()

	_, err := p.Create(context.Background(), CreateArgs{
		Message:   "follow up",
		TriggerAt: "2026-06-11T09:30:00",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreateErrorsOnInvalidDatetime(t *testing.T) {
	p, _, _ := newPostarius(WithDefaultTimezone(time.UTC))

	_, err := p.Create(context.Background(), CreateArgs{
		Message:   "follow up",
		TriggerAt: "not-a-date",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreateErrorsOnWrongContextType(t *testing.T) {
	p, _, _ := newPostarius(
		WithDefaultTimezone(time.UTC),
		WithHumanFromContext(humanKey{}),
	)

	ctx := context.WithValue(context.Background(), humanKey{}, 123)
	_, err := p.Create(ctx, CreateArgs{
		Message:   "follow up",
		TriggerAt: "2026-06-11T09:30:00",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreateErrorsOnInvalidTimezoneInContext(t *testing.T) {
	p, _, _ := newPostarius(WithTimezoneFromContext(timezoneKey{}))

	ctx := context.WithValue(context.Background(), timezoneKey{}, "Not/ATimezone")
	_, err := p.Create(ctx, CreateArgs{
		Message:   "follow up",
		TriggerAt: "2026-06-11T09:30:00",
	})
	if err == nil {
		t.Fatal("expected error for invalid IANA timezone")
	}
}

func TestListUpcomingBuildsQueryFromContext(t *testing.T) {
	p, store, _ := newPostarius(
		WithHumanFromContext(humanKey{}),
		WithAgentFromContext(agentKey{}),
		WithSessionFromContext(sessionKey{}),
	)

	ctx := context.WithValue(context.Background(), humanKey{}, "human-1")
	ctx = context.WithValue(ctx, agentKey{}, "agent-1")
	ctx = context.WithValue(ctx, sessionKey{}, "session-1")

	before := time.Now().UTC()
	_, err := p.ListUpcoming(ctx)
	after := time.Now().UTC()

	if err != nil {
		t.Fatalf("ListUpcoming: %v", err)
	}
	if store.queried.Human != "human-1" || store.queried.Agent != "agent-1" || store.queried.Session != "session-1" {
		t.Fatalf("query identity fields: %#v", store.queried)
	}
	if store.queried.From.Before(before) || store.queried.From.After(after) {
		t.Fatalf("From should be approximately now, got %s", store.queried.From)
	}
	if !store.queried.To.IsZero() {
		t.Fatalf("To should be zero (no upper bound), got %s", store.queried.To)
	}
}

func TestCancelCallsEnqueueCancelAndStoreRemove(t *testing.T) {
	posterum := Posterum{ID: "pstr_1", Message: "hello", TriggerAt: time.Now().Add(time.Hour)}
	store := &captureStore{get: posterum}
	enqueuer := &captureEnqueuer{}
	p := New(store, enqueuer)

	err := p.Cancel(context.Background(), "pstr_1")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if enqueuer.canceledID != "pstr_1" {
		t.Fatalf("enqueuer.Cancel not called with correct ID: %q", enqueuer.canceledID)
	}
}

func TestCancelScopedToContextIdentity(t *testing.T) {
	posterum := Posterum{ID: "pstr_1", Human: "human-1", Agent: "agent-1", TriggerAt: time.Now().Add(time.Hour)}

	t.Run("matching identity cancels", func(t *testing.T) {
		store := &captureStore{get: posterum}
		enq := &captureEnqueuer{}
		p := New(store, enq, WithHumanFromContext(humanKey{}), WithAgentFromContext(agentKey{}))

		ctx := context.WithValue(context.Background(), humanKey{}, "human-1")
		ctx = context.WithValue(ctx, agentKey{}, "agent-1")
		if err := p.Cancel(ctx, "pstr_1"); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		if enq.canceledID != "pstr_1" {
			t.Fatal("expected enqueuer.Cancel to be called for in-scope posterum")
		}
	})

	t.Run("mismatched identity returns not found and does not cancel", func(t *testing.T) {
		store := &captureStore{get: posterum}
		enq := &captureEnqueuer{}
		p := New(store, enq, WithHumanFromContext(humanKey{}))

		ctx := context.WithValue(context.Background(), humanKey{}, "human-2")
		err := p.Cancel(ctx, "pstr_1")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("want ErrNotFound for out-of-scope posterum, got %v", err)
		}
		if enq.canceledID != "" {
			t.Fatal("enqueuer.Cancel must not be called for out-of-scope posterum")
		}
	})

	t.Run("no identity in context cancels any", func(t *testing.T) {
		store := &captureStore{get: posterum}
		enq := &captureEnqueuer{}
		p := New(store, enq, WithHumanFromContext(humanKey{}))

		if err := p.Cancel(context.Background(), "pstr_1"); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		if enq.canceledID != "pstr_1" {
			t.Fatal("expected cancel to proceed when no identity scope is present")
		}
	})
}

func TestCancelNotFoundError(t *testing.T) {
	store := &captureStore{getErr: ErrNotFound}
	p := New(store, &captureEnqueuer{})

	err := p.Cancel(context.Background(), "pstr_1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

type recordingHandler struct {
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func attrValue(r slog.Record, key string) (string, bool) {
	var out string
	var found bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			out, found = a.Value.String(), true
			return false
		}
		return true
	})
	return out, found
}

func TestIdentityWarnsWhenKeyConfiguredButContextEmpty(t *testing.T) {
	h := &recordingHandler{}
	store := &captureStore{}
	p := New(store, &captureEnqueuer{},
		WithHumanFromContext(humanKey{}),
		WithLogger(slog.New(h)),
	)

	if _, err := p.ListUpcoming(context.Background()); err != nil {
		t.Fatalf("ListUpcoming: %v", err)
	}

	if len(h.records) != 1 {
		t.Fatalf("want 1 warning, got %d", len(h.records))
	}
	rec := h.records[0]
	if rec.Level != slog.LevelWarn {
		t.Fatalf("want Warn level, got %s", rec.Level)
	}
	if field, ok := attrValue(rec, "field"); !ok || field != "human" {
		t.Fatalf("want field=human, got %q (present=%v)", field, ok)
	}
	if op, ok := attrValue(rec, "operation"); !ok || op != "ListUpcoming" {
		t.Fatalf("want operation=ListUpcoming, got %q (present=%v)", op, ok)
	}
}

func TestIdentitySilentWhenKeyNotConfigured(t *testing.T) {
	h := &recordingHandler{}
	p := New(&captureStore{}, &captureEnqueuer{}, WithLogger(slog.New(h)))

	if _, err := p.ListUpcoming(context.Background()); err != nil {
		t.Fatalf("ListUpcoming: %v", err)
	}
	if len(h.records) != 0 {
		t.Fatalf("expected no warnings when no identity key is configured, got %d", len(h.records))
	}
}

func TestIdentitySilentWhenContextValuePresent(t *testing.T) {
	h := &recordingHandler{}
	p := New(&captureStore{}, &captureEnqueuer{},
		WithHumanFromContext(humanKey{}),
		WithLogger(slog.New(h)),
	)

	ctx := context.WithValue(context.Background(), humanKey{}, "human-1")
	if _, err := p.ListUpcoming(ctx); err != nil {
		t.Fatalf("ListUpcoming: %v", err)
	}
	if len(h.records) != 0 {
		t.Fatalf("expected no warnings when identity is present, got %d", len(h.records))
	}
}

func TestLocalizesFromContext(t *testing.T) {
	t.Run("WithTimezoneFromContext returns true", func(t *testing.T) {
		p, _, _ := newPostarius(WithTimezoneFromContext(timezoneKey{}))
		if !p.LocalizesFromContext() {
			t.Fatal("want true for WithTimezoneFromContext")
		}
	})

	t.Run("no timezone option returns false", func(t *testing.T) {
		p, _, _ := newPostarius()
		if p.LocalizesFromContext() {
			t.Fatal("want false when no timezone option is set")
		}
	})

	t.Run("WithDefaultTimezone returns false", func(t *testing.T) {
		p, _, _ := newPostarius(WithDefaultTimezone(time.UTC))
		if p.LocalizesFromContext() {
			t.Fatal("want false for WithDefaultTimezone — a default zone is not per-request context localization")
		}
	})
}

func TestLocationFromContextFallsBackToDefaultThenUTC(t *testing.T) {
	jakarta, _ := time.LoadLocation("Asia/Jakarta")

	t.Run("from context", func(t *testing.T) {
		p, _, _ := newPostarius(WithTimezoneFromContext(timezoneKey{}))
		ctx := context.WithValue(context.Background(), timezoneKey{}, "Asia/Jakarta")
		if got := p.LocationFromContext(ctx); got.String() != "Asia/Jakarta" {
			t.Fatalf("want Asia/Jakarta, got %s", got)
		}
	})

	t.Run("from default", func(t *testing.T) {
		p, _, _ := newPostarius(WithDefaultTimezone(jakarta))
		if got := p.LocationFromContext(context.Background()); got.String() != "Asia/Jakarta" {
			t.Fatalf("want Asia/Jakarta, got %s", got)
		}
	})

	t.Run("falls back to UTC", func(t *testing.T) {
		p, _, _ := newPostarius()
		if got := p.LocationFromContext(context.Background()); got != time.UTC {
			t.Fatalf("want UTC, got %s", got)
		}
	})
}
