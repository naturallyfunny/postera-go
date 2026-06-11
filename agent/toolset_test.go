package agent_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.naturallyfunny.dev/postera"
	"go.naturallyfunny.dev/postera/agent"
)

type testKey struct{}

type contextCheckingStore struct {
	t         *testing.T
	key       any
	wantValue string
	wantTime  time.Time
}

func (s contextCheckingStore) Save(ctx context.Context, p postera.Posterum) error {
	s.t.Helper()
	if got, _ := ctx.Value(s.key).(string); got != s.wantValue {
		s.t.Fatalf("store context value: want %q, got %q", s.wantValue, got)
	}
	if !p.TriggerAt.Equal(s.wantTime) {
		s.t.Fatalf("triggerAt: want %s, got %s", s.wantTime, p.TriggerAt)
	}
	return nil
}

func (s contextCheckingStore) Get(context.Context, string) (postera.Posterum, error) {
	s.t.Helper()
	s.t.Fatal("unexpected Get")
	return postera.Posterum{}, nil
}

func (s contextCheckingStore) Remove(context.Context, string) error {
	s.t.Helper()
	s.t.Fatal("unexpected Remove")
	return nil
}

func (s contextCheckingStore) List(context.Context, postera.Query) ([]postera.Posterum, error) {
	s.t.Helper()
	s.t.Fatal("unexpected List")
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
		contextCheckingStore{t: t, key: key, wantValue: "user-123", wantTime: wantTime},
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

type humanKey struct{}
type agentKey struct{}
type sessionKey struct{}
type metadataKey struct{}

type captureStore struct {
	saved PosterumCapture
	query postera.Query
}

type PosterumCapture struct {
	p postera.Posterum
}

func (s *captureStore) Save(_ context.Context, p postera.Posterum) error {
	s.saved = PosterumCapture{p: p}
	return nil
}

func (s *captureStore) Get(context.Context, string) (postera.Posterum, error) {
	panic("unexpected Get")
}

func (s *captureStore) Remove(context.Context, string) error {
	panic("unexpected Remove")
}

func (s *captureStore) List(_ context.Context, q postera.Query) ([]postera.Posterum, error) {
	s.query = q
	return []postera.Posterum{{ID: "pstr_1"}}, nil
}

type noopEnqueuer struct {
	enqueued postera.Posterum
}

func (e *noopEnqueuer) Enqueue(_ context.Context, p postera.Posterum) error {
	e.enqueued = p
	return nil
}

func (e *noopEnqueuer) Cancel(context.Context, string) error {
	panic("unexpected Cancel")
}

func TestToolSetCreateAppliesContextIdentityAndMetadata(t *testing.T) {
	store := &captureStore{}
	enqueuer := &noopEnqueuer{}
	postarius := postera.New(store, enqueuer)
	tools := agent.NewToolSet(postarius,
		agent.WithDefaultTimezone(time.UTC),
		agent.WithHumanFromContext(humanKey{}),
		agent.WithAgentFromContext(agentKey{}),
		agent.WithSessionFromContext(sessionKey{}),
		agent.WithMetadataFromContext(metadataKey{}),
	)
	metadata := map[string]string{"timezone": "Asia/Jakarta", "trace": "abc"}
	ctx := context.WithValue(context.Background(), humanKey{}, "human-1")
	ctx = context.WithValue(ctx, agentKey{}, "agent-1")
	ctx = context.WithValue(ctx, sessionKey{}, "session-1")
	ctx = context.WithValue(ctx, metadataKey{}, metadata)

	got, err := tools.Create(ctx, agent.CreateArgs{
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
	metadata["timezone"] = "UTC"
	if got.Metadata["timezone"] != "Asia/Jakarta" {
		t.Fatalf("metadata should be copied before Create, got %#v", got.Metadata)
	}
	if !reflect.DeepEqual(store.saved.p, got) {
		t.Fatalf("saved posterum: want %#v, got %#v", got, store.saved.p)
	}
	if !reflect.DeepEqual(enqueuer.enqueued, got) {
		t.Fatalf("enqueued posterum: want %#v, got %#v", got, enqueuer.enqueued)
	}
}

func TestToolSetListAppliesContextIdentityFilters(t *testing.T) {
	store := &captureStore{}
	postarius := postera.New(store, &noopEnqueuer{})
	tools := agent.NewToolSet(postarius,
		agent.WithDefaultTimezone(time.UTC),
		agent.WithHumanFromContext(humanKey{}),
		agent.WithAgentFromContext(agentKey{}),
		agent.WithSessionFromContext(sessionKey{}),
		agent.WithMetadataFromContext(metadataKey{}),
	)
	ctx := context.WithValue(context.Background(), humanKey{}, "human-1")
	ctx = context.WithValue(ctx, agentKey{}, "agent-1")
	ctx = context.WithValue(ctx, sessionKey{}, "session-1")
	ctx = context.WithValue(ctx, metadataKey{}, map[string]string{"ignored": "for-list"})

	_, err := tools.List(ctx, agent.ListArgs{
		From: "2026-06-11T09:00:00",
		To:   "2026-06-11T10:00:00",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if store.query.Human != "human-1" || store.query.Agent != "agent-1" || store.query.Session != "session-1" {
		t.Fatalf("query identity fields: %#v", store.query)
	}
	if !store.query.From.Equal(time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("from: %s", store.query.From)
	}
	if !store.query.To.Equal(time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("to: %s", store.query.To)
	}
}

func TestToolSetContextWrongTypeErrors(t *testing.T) {
	tests := []struct {
		name string
		opt  agent.ToolSetOption
		key  any
		val  any
		want string
	}{
		{name: "human", opt: agent.WithHumanFromContext(humanKey{}), key: humanKey{}, val: 123, want: "human context value must be a string"},
		{name: "agent", opt: agent.WithAgentFromContext(agentKey{}), key: agentKey{}, val: 123, want: "agent context value must be a string"},
		{name: "session", opt: agent.WithSessionFromContext(sessionKey{}), key: sessionKey{}, val: 123, want: "session context value must be a string"},
		{name: "metadata", opt: agent.WithMetadataFromContext(metadataKey{}), key: metadataKey{}, val: map[string]any{"x": "y"}, want: "metadata context value must be map[string]string"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			postarius := postera.New(&captureStore{}, &noopEnqueuer{})
			tools := agent.NewToolSet(postarius, agent.WithDefaultTimezone(time.UTC), tc.opt)
			ctx := context.WithValue(context.Background(), tc.key, tc.val)

			_, err := tools.Create(ctx, agent.CreateArgs{
				Message:   "follow up",
				TriggerAt: "2026-06-11T09:30:00",
			})
			if err == nil {
				t.Fatal("Create error: want non-nil, got nil")
			}
			if !strings.Contains(err.Error(), "postera: agent:") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Create error: want postera agent type error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestToolSetContextMissingValuesAllowed(t *testing.T) {
	store := &captureStore{}
	postarius := postera.New(store, &noopEnqueuer{})
	tools := agent.NewToolSet(postarius,
		agent.WithDefaultTimezone(time.UTC),
		agent.WithHumanFromContext(humanKey{}),
		agent.WithAgentFromContext(agentKey{}),
		agent.WithSessionFromContext(sessionKey{}),
		agent.WithMetadataFromContext(metadataKey{}),
	)

	got, err := tools.Create(context.Background(), agent.CreateArgs{
		Message:   "follow up",
		TriggerAt: "2026-06-11T09:30:00",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Human != "" || got.Agent != "" || got.Session != "" || got.Metadata != nil {
		t.Fatalf("context fields should be empty when missing: %#v", got)
	}
}

func TestToolSetArgsExposeOnlyAgentControlledFields(t *testing.T) {
	if got := exportedFieldNames(reflect.TypeOf(agent.CreateArgs{})); !reflect.DeepEqual(got, []string{"Message", "TriggerAt"}) {
		t.Fatalf("CreateArgs fields: %#v", got)
	}
	if got := exportedFieldNames(reflect.TypeOf(agent.ListArgs{})); !reflect.DeepEqual(got, []string{"From", "To"}) {
		t.Fatalf("ListArgs fields: %#v", got)
	}
}

func TestToolSetContextOptionsPanicOnNilKey(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
	}{
		{name: "human", fn: func() { agent.WithHumanFromContext(nil) }},
		{name: "agent", fn: func() { agent.WithAgentFromContext(nil) }},
		{name: "session", fn: func() { agent.WithSessionFromContext(nil) }},
		{name: "metadata", fn: func() { agent.WithMetadataFromContext(nil) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic")
				}
			}()
			tc.fn()
		})
	}
}

func exportedFieldNames(typ reflect.Type) []string {
	names := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath == "" {
			names = append(names, field.Name)
		}
	}
	return names
}
