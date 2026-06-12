package postera

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type humanKey struct{}
type agentKey struct{}
type sessionKey struct{}
type metadataKey struct{}
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
		WithMetadataFromContext(metadataKey{}),
	)

	metadata := map[string]string{"timezone": "Asia/Jakarta"}
	ctx := context.WithValue(context.Background(), humanKey{}, "human-1")
	ctx = context.WithValue(ctx, agentKey{}, "agent-1")
	ctx = context.WithValue(ctx, sessionKey{}, "session-1")
	ctx = context.WithValue(ctx, metadataKey{}, metadata)

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
	if !reflect.DeepEqual(got.Metadata, metadata) {
		t.Fatalf("metadata: want %#v, got %#v", metadata, got.Metadata)
	}
	// verify metadata is copied, not shared
	metadata["timezone"] = "UTC"
	if got.Metadata["timezone"] != "Asia/Jakarta" {
		t.Fatal("metadata should be copied before save")
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

func TestCancelNotFoundError(t *testing.T) {
	store := &captureStore{getErr: ErrNotFound}
	p := New(store, &captureEnqueuer{})

	err := p.Cancel(context.Background(), "pstr_1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
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
