package cloudtasks

import (
	"context"
	"errors"
	"testing"
	"time"

	taskspb "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	gax "github.com/googleapis/gax-go/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.naturallyfunny.dev/postera"
)

func validPosterum() postera.Posterum {
	return postera.Posterum{
		ID:        "task-1",
		Message:   "hello",
		TriggerAt: time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC),
	}
}

// fakeTaskClient fails the first failCreate/failDelete calls, then succeeds.
type fakeTaskClient struct {
	createCalls int
	deleteCalls int
	failCreate  int
	failDelete  int
	createErr   error
	deleteErr   error
}

func (f *fakeTaskClient) CreateTask(context.Context, *taskspb.CreateTaskRequest, ...gax.CallOption) (*taskspb.Task, error) {
	f.createCalls++
	if f.createCalls <= f.failCreate {
		return nil, f.createErr
	}
	return &taskspb.Task{}, nil
}

func (f *fakeTaskClient) DeleteTask(context.Context, *taskspb.DeleteTaskRequest, ...gax.CallOption) error {
	f.deleteCalls++
	if f.deleteCalls <= f.failDelete {
		return f.deleteErr
	}
	return nil
}

func futurePosterum() postera.Posterum {
	return postera.Posterum{
		ID:        "task-1",
		Message:   "hello",
		TriggerAt: time.Now().Add(time.Hour),
	}
}

func TestEnqueueRetriesTransientErrors(t *testing.T) {
	fake := &fakeTaskClient{failCreate: 2, createErr: status.Error(codes.Unavailable, "boom")}
	q := &Queue{
		client:      fake,
		queuePath:   "projects/p/locations/l/queues/q",
		targetURL:   "https://example.test/awaken",
		maxAttempts: 5,
	}

	if err := q.Enqueue(context.Background(), futurePosterum()); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if fake.createCalls != 3 {
		t.Fatalf("want 3 CreateTask calls (2 transient + 1 success), got %d", fake.createCalls)
	}
}

func TestEnqueueNoRetryWithoutPolicy(t *testing.T) {
	fake := &fakeTaskClient{failCreate: 1, createErr: status.Error(codes.Unavailable, "boom")}
	q := &Queue{
		client:    fake,
		queuePath: "projects/p/locations/l/queues/q",
		targetURL: "https://example.test/awaken",
	}

	err := q.Enqueue(context.Background(), futurePosterum())
	if err == nil {
		t.Fatal("expected error without retry policy, got nil")
	}
	if fake.createCalls != 1 {
		t.Fatalf("want a single CreateTask call without WithRetry, got %d", fake.createCalls)
	}
}

func TestEnqueueDoesNotRetryPermanentErrors(t *testing.T) {
	fake := &fakeTaskClient{failCreate: 5, createErr: status.Error(codes.InvalidArgument, "bad")}
	q := &Queue{
		client:      fake,
		queuePath:   "projects/p/locations/l/queues/q",
		targetURL:   "https://example.test/awaken",
		maxAttempts: 5,
	}

	err := q.Enqueue(context.Background(), futurePosterum())
	if err == nil {
		t.Fatal("expected error for InvalidArgument, got nil")
	}
	if fake.createCalls != 1 {
		t.Fatalf("permanent error must not be retried, got %d calls", fake.createCalls)
	}
}

func TestCancelRetriesTransientErrors(t *testing.T) {
	fake := &fakeTaskClient{failDelete: 2, deleteErr: status.Error(codes.Unavailable, "boom")}
	q := &Queue{
		client:      fake,
		queuePath:   "projects/p/locations/l/queues/q",
		maxAttempts: 5,
	}

	if err := q.Cancel(context.Background(), "task-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if fake.deleteCalls != 3 {
		t.Fatalf("want 3 DeleteTask calls, got %d", fake.deleteCalls)
	}
}

func TestCancelRetryExhaustionSurfacesError(t *testing.T) {
	sentinel := status.Error(codes.Unavailable, "boom")
	fake := &fakeTaskClient{failDelete: 10, deleteErr: sentinel}
	q := &Queue{
		client:      fake,
		queuePath:   "projects/p/locations/l/queues/q",
		maxAttempts: 3,
	}

	err := q.Cancel(context.Background(), "task-1")
	if err == nil || !errors.Is(err, sentinel) {
		t.Fatalf("want surfaced Unavailable error, got %v", err)
	}
	if fake.deleteCalls != 3 {
		t.Fatalf("want 3 DeleteTask attempts before giving up, got %d", fake.deleteCalls)
	}
}

func queueWithHeaders(t *testing.T, opts ...Option) *Queue {
	t.Helper()
	q := &Queue{}
	for _, opt := range opts {
		if err := opt(q); err != nil {
			t.Fatalf("option error: %v", err)
		}
	}
	return q
}

func TestHeadersFromPosterumIdentityFields(t *testing.T) {
	tests := []struct {
		name       string
		option     Option
		modify     func(*postera.Posterum)
		headerName string
		want       string
	}{
		{
			name:       "human",
			option:     WithHumanHeader("x-human"),
			modify:     func(p *postera.Posterum) { p.Human = "human-1" },
			headerName: "x-human",
			want:       "human-1",
		},
		{
			name:       "agent",
			option:     WithAgentHeader("x-agent"),
			modify:     func(p *postera.Posterum) { p.Agent = "agent-1" },
			headerName: "x-agent",
			want:       "agent-1",
		},
		{
			name:       "session",
			option:     WithSessionHeader("x-session"),
			modify:     func(p *postera.Posterum) { p.Session = "session-1" },
			headerName: "x-session",
			want:       "session-1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := queueWithHeaders(t, tc.option)
			p := validPosterum()
			tc.modify(&p)

			headers := q.headersFromPosterum(p)
			if got := headers[tc.headerName]; got != tc.want {
				t.Fatalf("header %s: want %q, got %q", tc.headerName, tc.want, got)
			}
		})
	}
}

func TestHeadersFromPosterumOmitsEmptyIdentityFields(t *testing.T) {
	q := queueWithHeaders(t,
		WithHumanHeader("x-human"),
		WithAgentHeader("x-agent"),
		WithSessionHeader("x-session"),
	)

	headers := q.headersFromPosterum(validPosterum())
	if headers != nil {
		t.Fatalf("want nil headers for empty identity fields, got %v", headers)
	}
}

func TestHeadersFromPosterumMetadata(t *testing.T) {
	q := queueWithHeaders(t,
		WithMetadataHeader("timezone", "x-timezone"),
		WithMetadataHeader("trace", "x-trace"),
		WithMetadataHeader("locale", "x-locale"),
	)
	p := validPosterum()
	p.Metadata = map[string]string{
		"timezone": "Asia/Jakarta",
		"trace":    "",
	}

	headers := q.headersFromPosterum(p)
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

func TestHeadersFromPosterumFixedHeader(t *testing.T) {
	q := queueWithHeaders(t,
		WithFixedHeader("Content-Type", "application/json"),
		WithHumanHeader("x-human"),
	)
	p := validPosterum()
	p.Human = "user-1"

	headers := q.headersFromPosterum(p)
	if got := headers["Content-Type"]; got != "application/json" {
		t.Fatalf("Content-Type: want %q, got %q", "application/json", got)
	}
	if got := headers["x-human"]; got != "user-1" {
		t.Fatalf("x-human: want %q, got %q", "user-1", got)
	}
}

func TestHeadersFromPosterumFixedHeaderAlwaysPresent(t *testing.T) {
	q := queueWithHeaders(t,
		WithFixedHeader("Content-Type", "application/json"),
	)

	headers := q.headersFromPosterum(validPosterum())
	if got := headers["Content-Type"]; got != "application/json" {
		t.Fatalf("fixed header should always be present, got %q", got)
	}
}

func TestHeadersFromPosterumNilWhenNoMappings(t *testing.T) {
	q := &Queue{}
	if headers := q.headersFromPosterum(validPosterum()); headers != nil {
		t.Fatalf("want nil with no mappings, got %v", headers)
	}
}
