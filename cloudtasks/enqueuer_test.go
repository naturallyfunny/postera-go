package cloudtasks_test

import (
	"context"
	"strings"
	"testing"
	"time"

	taskspb "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"github.com/googleapis/gax-go/v2"

	"go.naturallyfunny.dev/postera"
	"go.naturallyfunny.dev/postera/cloudtasks"
)

type stubTasksClient struct {
	created   []*taskspb.CreateTaskRequest
	deleted   []*taskspb.DeleteTaskRequest
	createErr error
	deleteErr error
}

func (s *stubTasksClient) CreateTask(_ context.Context, req *taskspb.CreateTaskRequest, _ ...gax.CallOption) (*taskspb.Task, error) {
	s.created = append(s.created, req)
	return &taskspb.Task{Name: req.Task.Name}, s.createErr
}

func (s *stubTasksClient) DeleteTask(_ context.Context, req *taskspb.DeleteTaskRequest, _ ...gax.CallOption) error {
	s.deleted = append(s.deleted, req)
	return s.deleteErr
}

func baseConfig() cloudtasks.Config {
	return cloudtasks.Config{ProjectID: "proj", LocationID: "us-central1", QueueID: "q"}
}

func validPosterum() postera.Posterum {
	return postera.Posterum{
		ID:        "task-1",
		Message:   "hello",
		TriggerAt: time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC),
	}
}

func newTestEnqueuer(t *testing.T, client *stubTasksClient, opts ...cloudtasks.Option) *cloudtasks.Enqueuer {
	t.Helper()
	cfg := baseConfig()
	cfg.TargetURL = "https://example.com/callback"
	enq, err := cloudtasks.NewEnqueuer(client, cfg, opts...)
	if err != nil {
		t.Fatalf("NewEnqueuer: %v", err)
	}
	return enq
}

func createdHeaders(t *testing.T, client *stubTasksClient) map[string]string {
	t.Helper()
	if len(client.created) != 1 {
		t.Fatalf("created requests: want 1, got %d", len(client.created))
	}
	return client.created[0].Task.GetHttpRequest().Headers
}

func TestNewEnqueuerRequiresQueueFields(t *testing.T) {
	_, err := cloudtasks.NewEnqueuer(&stubTasksClient{}, cloudtasks.Config{TargetURL: "https://example.com/cb"})
	if err == nil {
		t.Fatal("expected error for missing queue fields, got nil")
	}
	if !strings.Contains(err.Error(), "ProjectID") {
		t.Fatalf("error should mention ProjectID, got: %v", err)
	}
}

func TestNewEnqueuerAcceptsEmptyTargetURL(t *testing.T) {
	_, err := cloudtasks.NewEnqueuer(&stubTasksClient{}, baseConfig())
	if err != nil {
		t.Fatalf("NewEnqueuer with empty TargetURL: %v", err)
	}
}

func TestEnqueueReturnsErrorWhenTargetURLEmpty(t *testing.T) {
	enq, err := cloudtasks.NewEnqueuer(&stubTasksClient{}, baseConfig())
	if err != nil {
		t.Fatalf("NewEnqueuer: %v", err)
	}

	err = enq.Enqueue(context.Background(), validPosterum())
	if err == nil {
		t.Fatal("expected error from Enqueue with empty TargetURL, got nil")
	}
	if !strings.Contains(err.Error(), "TargetURL") {
		t.Fatalf("expected TargetURL error, got: %v", err)
	}
}

func TestCancelWorksWithoutTargetURL(t *testing.T) {
	client := &stubTasksClient{}
	enq, err := cloudtasks.NewEnqueuer(client, baseConfig())
	if err != nil {
		t.Fatalf("NewEnqueuer: %v", err)
	}

	if err := enq.Cancel(context.Background(), "task-id-123"); err != nil {
		t.Fatalf("Cancel with no TargetURL: %v", err)
	}
	if len(client.deleted) != 1 {
		t.Fatalf("deleted requests: want 1, got %d", len(client.deleted))
	}
}

func TestEnqueueSucceedsWithTargetURL(t *testing.T) {
	client := &stubTasksClient{}
	enq := newTestEnqueuer(t, client)

	if err := enq.Enqueue(context.Background(), validPosterum()); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if got := client.created[0].Task.GetHttpRequest().Url; got != "https://example.com/callback" {
		t.Fatalf("target URL: want %q, got %q", "https://example.com/callback", got)
	}
}

func TestEnqueueSetsIdentityHeaders(t *testing.T) {
	tests := []struct {
		name       string
		option     cloudtasks.Option
		posterum   postera.Posterum
		headerName string
		want       string
	}{
		{
			name:       "human",
			option:     cloudtasks.WithHumanHeader("x-human"),
			posterum:   postera.Posterum{Human: "human-1"},
			headerName: "x-human",
			want:       "human-1",
		},
		{
			name:       "agent",
			option:     cloudtasks.WithAgentHeader("x-agent"),
			posterum:   postera.Posterum{Agent: "agent-1"},
			headerName: "x-agent",
			want:       "agent-1",
		},
		{
			name:       "session",
			option:     cloudtasks.WithSessionHeader("x-session"),
			posterum:   postera.Posterum{Session: "session-1"},
			headerName: "x-session",
			want:       "session-1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &stubTasksClient{}
			enq := newTestEnqueuer(t, client, tc.option)
			p := validPosterum()
			p.Human = tc.posterum.Human
			p.Agent = tc.posterum.Agent
			p.Session = tc.posterum.Session

			if err := enq.Enqueue(context.Background(), p); err != nil {
				t.Fatalf("Enqueue: %v", err)
			}
			if got := createdHeaders(t, client)[tc.headerName]; got != tc.want {
				t.Fatalf("header %s: want %q, got %q", tc.headerName, tc.want, got)
			}
		})
	}
}

func TestEnqueueOmitsEmptyIdentityHeaders(t *testing.T) {
	client := &stubTasksClient{}
	enq := newTestEnqueuer(t, client,
		cloudtasks.WithHumanHeader("x-human"),
		cloudtasks.WithAgentHeader("x-agent"),
		cloudtasks.WithSessionHeader("x-session"),
	)

	if err := enq.Enqueue(context.Background(), validPosterum()); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if headers := createdHeaders(t, client); headers != nil {
		t.Fatalf("headers: want nil, got %v", headers)
	}
}

func TestEnqueueSetsMetadataHeaders(t *testing.T) {
	client := &stubTasksClient{}
	enq := newTestEnqueuer(t, client,
		cloudtasks.WithMetadataHeader("timezone", "x-timezone"),
		cloudtasks.WithMetadataHeader("trace", "x-trace"),
		cloudtasks.WithMetadataHeader("locale", "x-locale"),
	)
	p := validPosterum()
	p.Metadata = map[string]string{
		"timezone": "Asia/Jakarta",
		"trace":    "",
	}

	if err := enq.Enqueue(context.Background(), p); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	headers := createdHeaders(t, client)
	if got := headers["x-timezone"]; got != "Asia/Jakarta" {
		t.Fatalf("metadata header: want %q, got %q", "Asia/Jakarta", got)
	}
	if _, ok := headers["x-trace"]; ok {
		t.Fatalf("empty metadata value should be omitted: %v", headers)
	}
	if _, ok := headers["x-locale"]; ok {
		t.Fatalf("absent metadata key should be omitted: %v", headers)
	}
}

func TestHeaderOptionsPanicOnEmptyArguments(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
		want string
	}{
		{
			name: "human header",
			fn:   func() { cloudtasks.WithHumanHeader("") },
			want: "cloudtasks: WithHumanHeader called with empty headerName",
		},
		{
			name: "agent header",
			fn:   func() { cloudtasks.WithAgentHeader("") },
			want: "cloudtasks: WithAgentHeader called with empty headerName",
		},
		{
			name: "session header",
			fn:   func() { cloudtasks.WithSessionHeader("") },
			want: "cloudtasks: WithSessionHeader called with empty headerName",
		},
		{
			name: "metadata key",
			fn:   func() { cloudtasks.WithMetadataHeader("", "x-meta") },
			want: "cloudtasks: WithMetadataHeader called with empty key",
		},
		{
			name: "metadata header",
			fn:   func() { cloudtasks.WithMetadataHeader("meta", "") },
			want: "cloudtasks: WithMetadataHeader called with empty headerName",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				got := recover()
				if got != tc.want {
					t.Fatalf("panic: want %q, got %v", tc.want, got)
				}
			}()
			tc.fn()
		})
	}
}
