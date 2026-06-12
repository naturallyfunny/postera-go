package cloudtasks

import (
	"testing"
	"time"

	"go.naturallyfunny.dev/postera"
)

func validPosterum() postera.Posterum {
	return postera.Posterum{
		ID:        "task-1",
		Message:   "hello",
		TriggerAt: time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC),
	}
}

func enqueuerWithHeaders(t *testing.T, opts ...Option) *Enqueuer {
	t.Helper()
	e := &Enqueuer{}
	for _, opt := range opts {
		if err := opt(e); err != nil {
			t.Fatalf("option error: %v", err)
		}
	}
	return e
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
			e := enqueuerWithHeaders(t, tc.option)
			p := validPosterum()
			tc.modify(&p)

			headers := e.headersFromPosterum(p)
			if got := headers[tc.headerName]; got != tc.want {
				t.Fatalf("header %s: want %q, got %q", tc.headerName, tc.want, got)
			}
		})
	}
}

func TestHeadersFromPosterumOmitsEmptyIdentityFields(t *testing.T) {
	e := enqueuerWithHeaders(t,
		WithHumanHeader("x-human"),
		WithAgentHeader("x-agent"),
		WithSessionHeader("x-session"),
	)

	headers := e.headersFromPosterum(validPosterum())
	if headers != nil {
		t.Fatalf("want nil headers for empty identity fields, got %v", headers)
	}
}

func TestHeadersFromPosterumMetadata(t *testing.T) {
	e := enqueuerWithHeaders(t,
		WithMetadataHeader("timezone", "x-timezone"),
		WithMetadataHeader("trace", "x-trace"),
		WithMetadataHeader("locale", "x-locale"),
	)
	p := validPosterum()
	p.Metadata = map[string]string{
		"timezone": "Asia/Jakarta",
		"trace":    "",
	}

	headers := e.headersFromPosterum(p)
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
	e := enqueuerWithHeaders(t,
		WithFixedHeader("Content-Type", "application/json"),
		WithHumanHeader("x-human"),
	)
	p := validPosterum()
	p.Human = "user-1"

	headers := e.headersFromPosterum(p)
	if got := headers["Content-Type"]; got != "application/json" {
		t.Fatalf("Content-Type: want %q, got %q", "application/json", got)
	}
	if got := headers["x-human"]; got != "user-1" {
		t.Fatalf("x-human: want %q, got %q", "user-1", got)
	}
}

func TestHeadersFromPosterumFixedHeaderAlwaysPresent(t *testing.T) {
	e := enqueuerWithHeaders(t,
		WithFixedHeader("Content-Type", "application/json"),
	)

	headers := e.headersFromPosterum(validPosterum())
	if got := headers["Content-Type"]; got != "application/json" {
		t.Fatalf("fixed header should always be present, got %q", got)
	}
}

func TestHeadersFromPosterumNilWhenNoMappings(t *testing.T) {
	e := &Enqueuer{}
	if headers := e.headersFromPosterum(validPosterum()); headers != nil {
		t.Fatalf("want nil with no mappings, got %v", headers)
	}
}
