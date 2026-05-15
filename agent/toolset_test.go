package agent_test

import (
	"context"
	"testing"
	"time"

	"go.naturallyfunny.dev/postera"
	"go.naturallyfunny.dev/postera/agent"
)

type testKey struct{}

type contextCheckingRegistry struct {
	t         *testing.T
	key       any
	wantValue string
	wantTime  time.Time
}

func (r contextCheckingRegistry) Save(ctx context.Context, p postera.Posterum) error {
	r.t.Helper()
	if got, _ := ctx.Value(r.key).(string); got != r.wantValue {
		r.t.Fatalf("registry context value: want %q, got %q", r.wantValue, got)
	}
	if !p.TriggerAt.Equal(r.wantTime) {
		r.t.Fatalf("triggerAt: want %s, got %s", r.wantTime, p.TriggerAt)
	}
	return nil
}

func (r contextCheckingRegistry) Get(context.Context, string) (postera.Posterum, error) {
	r.t.Helper()
	r.t.Fatal("unexpected Get")
	return postera.Posterum{}, nil
}

func (r contextCheckingRegistry) Remove(context.Context, string) error {
	r.t.Helper()
	r.t.Fatal("unexpected Remove")
	return nil
}

func (r contextCheckingRegistry) List(context.Context, postera.TimeRange) ([]postera.Posterum, error) {
	r.t.Helper()
	r.t.Fatal("unexpected List")
	return nil, nil
}

type contextCheckingEnqueuer struct {
	t         *testing.T
	key       any
	wantValue string
}

func (e contextCheckingEnqueuer) Enqueue(ctx context.Context, _ postera.Posterum) error {
	e.t.Helper()
	if got, _ := ctx.Value(e.key).(string); got != e.wantValue {
		e.t.Fatalf("enqueuer context value: want %q, got %q", e.wantValue, got)
	}
	return nil
}

func (e contextCheckingEnqueuer) Cancel(context.Context, string) error {
	e.t.Helper()
	e.t.Fatal("unexpected Cancel")
	return nil
}

func TestToolSetCreatePreservesContextAndParsesLocalTime(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Fatal(err)
	}
	key := testKey{}
	wantTime := time.Date(2026, 5, 14, 9, 30, 0, 0, loc)
	postarius := postera.New(
		contextCheckingRegistry{t: t, key: key, wantValue: "user-123", wantTime: wantTime},
		contextCheckingEnqueuer{t: t, key: key, wantValue: "user-123"},
	)
	tools := agent.NewToolSet(postarius, agent.WithDefaultTimezone(loc))

	ctx := context.WithValue(context.Background(), key, "user-123")
	got, err := tools.Create(ctx, agent.CreateArgs{
		Message:   "follow up",
		TriggerAt: "2026-05-14T09:30:00",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Message != "follow up" {
		t.Fatalf("message: want %q, got %q", "follow up", got.Message)
	}
}
