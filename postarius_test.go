package postera

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type captureRegistry struct {
	saved Posterum
}

func (r *captureRegistry) Save(_ context.Context, p Posterum) error {
	r.saved = p
	return nil
}

func (r *captureRegistry) Get(context.Context, string) (Posterum, error) {
	panic("unexpected Get")
}

func (r *captureRegistry) Remove(context.Context, string) error {
	panic("unexpected Remove")
}

func (r *captureRegistry) List(context.Context, Query) ([]Posterum, error) {
	panic("unexpected List")
}

type captureEnqueuer struct {
	enqueued Posterum
}

func (e *captureEnqueuer) Enqueue(_ context.Context, p Posterum) error {
	e.enqueued = p
	return nil
}

func (e *captureEnqueuer) Cancel(context.Context, string) error {
	panic("unexpected Cancel")
}

func TestPostariusCreateAcceptsPosterumAndOverwritesManagedFields(t *testing.T) {
	registry := &captureRegistry{}
	enqueuer := &captureEnqueuer{}
	postarius := New(registry, enqueuer)
	triggerAt := time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)
	callerCreatedAt := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	before := time.Now().UTC()

	got, err := postarius.Create(context.Background(), Posterum{
		ID:        "caller-id",
		Human:     "human-1",
		Agent:     "agent-1",
		Session:   "session-1",
		Metadata:  map[string]string{"timezone": "Asia/Jakarta"},
		Message:   "hello",
		TriggerAt: triggerAt,
		CreatedAt: callerCreatedAt,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	after := time.Now().UTC()

	if got.ID == "" || got.ID == "caller-id" {
		t.Fatalf("ID should be generated, got %q", got.ID)
	}
	if got.CreatedAt.Equal(callerCreatedAt) || got.CreatedAt.Before(before) || got.CreatedAt.After(after) {
		t.Fatalf("CreatedAt should be overwritten with current UTC time, got %s", got.CreatedAt)
	}
	if got.Human != "human-1" || got.Agent != "agent-1" || got.Session != "session-1" {
		t.Fatalf("identity fields not preserved: %#v", got)
	}
	if got.Metadata["timezone"] != "Asia/Jakarta" {
		t.Fatalf("metadata not preserved: %#v", got.Metadata)
	}
	if !reflect.DeepEqual(registry.saved, got) {
		t.Fatalf("saved posterum: want %#v, got %#v", got, registry.saved)
	}
	if !reflect.DeepEqual(enqueuer.enqueued, got) {
		t.Fatalf("enqueued posterum: want %#v, got %#v", got, enqueuer.enqueued)
	}
}

func TestPostariusCreateRejectsZeroTriggerAt(t *testing.T) {
	postarius := New(&captureRegistry{}, &captureEnqueuer{})

	_, err := postarius.Create(context.Background(), Posterum{Message: "hello"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Create error: want ErrInvalidInput, got %v", err)
	}
}
