package cloudtasks_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.naturallyfunny.dev/postera"
	"go.naturallyfunny.dev/postera/cloudtasks"
)

func TestNewQueueRequiresQueueFields(t *testing.T) {
	tests := []struct {
		name             string
		project          string
		location         string
		queue            string
		wantErrSubstring string
	}{
		{
			name:             "all empty",
			wantErrSubstring: "project",
		},
		{
			name:             "missing location and queue",
			project:          "proj",
			wantErrSubstring: "location",
		},
		{
			name:             "missing queue",
			project:          "proj",
			location:         "us-central1",
			wantErrSubstring: "queue",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cloudtasks.NewQueue(nil, tc.project, tc.location, tc.queue)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErrSubstring) {
				t.Fatalf("error should mention %q, got: %v", tc.wantErrSubstring, err)
			}
		})
	}
}

func TestNewQueueAcceptsNoOptions(t *testing.T) {
	_, err := cloudtasks.NewQueue(nil, "proj", "us-central1", "q")
	if err != nil {
		t.Fatalf("NewQueue with no options: %v", err)
	}
}

func TestEnqueueReturnsErrorWhenNoTargetURL(t *testing.T) {
	enq, err := cloudtasks.NewQueue(nil, "proj", "us-central1", "q")
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}

	p := postera.Posterum{
		ID:        "task-1",
		Message:   "hello",
		TriggerAt: time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC),
	}
	err = enq.Enqueue(context.Background(), p)
	if err == nil {
		t.Fatal("expected error from Enqueue with no target URL, got nil")
	}
	if !strings.Contains(err.Error(), "WithTargetURL") {
		t.Fatalf("error should mention WithTargetURL, got: %v", err)
	}
}

func TestNewQueueRejectsEmptyHeaderOption(t *testing.T) {
	tests := []struct {
		name   string
		option cloudtasks.Option
		want   string
	}{
		{
			name:   "human header",
			option: cloudtasks.WithHumanHeader(""),
			want:   "WithHumanHeader",
		},
		{
			name:   "agent header",
			option: cloudtasks.WithAgentHeader(""),
			want:   "WithAgentHeader",
		},
		{
			name:   "session header",
			option: cloudtasks.WithSessionHeader(""),
			want:   "WithSessionHeader",
		},
		{
			name:   "metadata empty key",
			option: cloudtasks.WithMetadataHeader("", "x-meta"),
			want:   "WithMetadataHeader",
		},
		{
			name:   "metadata empty header",
			option: cloudtasks.WithMetadataHeader("key", ""),
			want:   "WithMetadataHeader",
		},
		{
			name:   "fixed empty name",
			option: cloudtasks.WithFixedHeader("", "value"),
			want:   "WithFixedHeader",
		},
		{
			name:   "fixed empty value",
			option: cloudtasks.WithFixedHeader("name", ""),
			want:   "WithFixedHeader",
		},
		{
			name:   "target url empty",
			option: cloudtasks.WithTargetURL(""),
			want:   "WithTargetURL",
		},
		{
			name:   "service account email empty",
			option: cloudtasks.WithServiceAccountEmail(""),
			want:   "WithServiceAccountEmail",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cloudtasks.NewQueue(nil, "proj", "us-central1", "q", tc.option)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error should mention %q, got: %v", tc.want, err)
			}
		})
	}
}
