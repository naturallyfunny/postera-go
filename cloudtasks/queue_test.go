package cloudtasks_test

import (
	"context"
	"errors"
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

func TestEnqueueRejectsScheduleOutOfRange(t *testing.T) {
	tests := []struct {
		name string
		lead time.Duration
	}{
		{name: "in the past", lead: -time.Hour},
		{name: "at now", lead: 0},
		{name: "just under min lead", lead: 20 * time.Second},
		{name: "just beyond max lead", lead: 30 * 24 * time.Hour},
		{name: "far beyond max lead", lead: 365 * 24 * time.Hour},
	}

	// A configured target URL means the horizon guard is what rejects these;
	// the nil client is never reached because the guard returns first.
	enq, err := cloudtasks.NewQueue(nil, "proj", "us-central1", "q",
		cloudtasks.WithTargetURL("https://example.test/awaken"))
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := postera.Posterum{
				ID:        "task-1",
				Message:   "hello",
				TriggerAt: time.Now().Add(tc.lead),
			}
			err := enq.Enqueue(context.Background(), p)
			if !errors.Is(err, postera.ErrScheduleOutOfRange) {
				t.Fatalf("want ErrScheduleOutOfRange, got %v", err)
			}
			if !strings.Contains(err.Error(), "task-1") {
				t.Fatalf("error should mention posterum ID, got: %v", err)
			}
		})
	}
}

func TestEnqueueTargetURLCheckPrecedesHorizon(t *testing.T) {
	// Without a target URL, the config error is reported even for an in-range
	// trigger, so consumers see the actionable misconfiguration first.
	enq, err := cloudtasks.NewQueue(nil, "proj", "us-central1", "q")
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	p := postera.Posterum{
		ID:        "task-1",
		Message:   "hello",
		TriggerAt: time.Now().Add(24 * time.Hour),
	}
	err = enq.Enqueue(context.Background(), p)
	if errors.Is(err, postera.ErrScheduleOutOfRange) {
		t.Fatalf("want target URL error, got ErrScheduleOutOfRange: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "WithTargetURL") {
		t.Fatalf("error should mention WithTargetURL, got: %v", err)
	}
}
