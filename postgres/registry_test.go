package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.naturallyfunny.dev/postera"
)

// TestListQuery verifies that listQuery always namespaces the query, that
// positional parameter numbers are assigned correctly, and that the half-open
// time bounds are emitted only when the corresponding TimeRange field is non-zero.
func TestListQuery(t *testing.T) {
	r := &Registry{tableName: "posterum"}

	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC)

	tests := []struct {
		name        string
		q           postera.TimeRange
		wantArgs    []any
		containsSQL []string
		absentSQL   []string
	}{
		{
			name:     "no bounds",
			q:        postera.TimeRange{},
			wantArgs: []any{"tenant"},
			containsSQL: []string{
				"namespace = $1",
				"ORDER BY remind_at ASC",
			},
			absentSQL: []string{
				"remind_at >=",
				"remind_at <",
			},
		},
		{
			name:     "from only",
			q:        postera.TimeRange{From: t0},
			wantArgs: []any{"tenant", t0},
			containsSQL: []string{
				"namespace = $1",
				"remind_at >= $2",
				"ORDER BY remind_at ASC",
			},
			absentSQL: []string{"remind_at <"},
		},
		{
			name:     "to only",
			q:        postera.TimeRange{To: t1},
			wantArgs: []any{"tenant", t1},
			containsSQL: []string{
				"namespace = $1",
				"remind_at < $2",
				"ORDER BY remind_at ASC",
			},
			absentSQL: []string{"remind_at >="},
		},
		{
			name:     "both bounds",
			q:        postera.TimeRange{From: t0, To: t1},
			wantArgs: []any{"tenant", t0, t1},
			containsSQL: []string{
				"namespace = $1",
				"remind_at >= $2",
				"remind_at < $3",
				"ORDER BY remind_at ASC",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sql, args := r.listQuery(context.Background(), "tenant", tc.q)

			if len(args) != len(tc.wantArgs) {
				t.Fatalf("args len: want %d, got %d", len(tc.wantArgs), len(args))
			}
			for i, want := range tc.wantArgs {
				if args[i] != want {
					t.Errorf("args[%d]: want %v, got %v", i, want, args[i])
				}
			}
			for _, fragment := range tc.containsSQL {
				if !strings.Contains(sql, fragment) {
					t.Errorf("SQL missing %q:\n%s", fragment, sql)
				}
			}
			for _, fragment := range tc.absentSQL {
				if strings.Contains(sql, fragment) {
					t.Errorf("SQL must not contain %q:\n%s", fragment, sql)
				}
			}
		})
	}
}

// userIDKey is a private context key type for tests.
type userIDKey struct{}

// sessionKey is a private context key type for tests.
type sessionKey struct{}

// TestListQueryWithColumnMappings verifies that registered column mappings add
// AND clauses when their context values are present, shift positional parameter
// numbers for time bounds correctly, and are skipped silently when absent.
func TestListQueryWithColumnMappings(t *testing.T) {
	type ctxValue struct {
		key any
		val string
	}

	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	r := &Registry{
		tableName: "posterum",
		columnMappings: []columnMapping{
			{ctxKey: userIDKey{}, colName: "user_id"},
			{ctxKey: sessionKey{}, colName: "session_id"},
		},
	}

	tests := []struct {
		name        string
		ctxValues   []ctxValue
		q           postera.TimeRange
		wantArgLen  int
		containsSQL []string
		absentSQL   []string
	}{
		{
			name:       "no column values in context",
			ctxValues:  nil,
			q:          postera.TimeRange{},
			wantArgLen: 1,
			containsSQL: []string{
				"namespace = $1",
				"ORDER BY remind_at ASC",
			},
			absentSQL: []string{"user_id", "session_id"},
		},
		{
			name:      "user_id present",
			ctxValues: []ctxValue{{userIDKey{}, "u-42"}},
			q:         postera.TimeRange{},
			wantArgLen: 2,
			containsSQL: []string{
				"namespace = $1",
				`"user_id" = $2`,
				"ORDER BY remind_at ASC",
			},
			absentSQL: []string{"session_id"},
		},
		{
			name: "both column values present",
			ctxValues: []ctxValue{
				{userIDKey{}, "u-42"},
				{sessionKey{}, "s-99"},
			},
			q:          postera.TimeRange{},
			wantArgLen: 3,
			containsSQL: []string{
				"namespace = $1",
				`"user_id" = $2`,
				`"session_id" = $3`,
				"ORDER BY remind_at ASC",
			},
		},
		{
			name:      "column values plus time bounds shift parameters",
			ctxValues: []ctxValue{{userIDKey{}, "u-42"}},
			q:         postera.TimeRange{From: t0},
			wantArgLen: 3,
			containsSQL: []string{
				"namespace = $1",
				`"user_id" = $2`,
				"remind_at >= $3",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			for _, cv := range tc.ctxValues {
				ctx = context.WithValue(ctx, cv.key, cv.val)
			}

			sql, args := r.listQuery(ctx, "tenant", tc.q)

			if len(args) != tc.wantArgLen {
				t.Fatalf("args len: want %d, got %d", tc.wantArgLen, len(args))
			}
			for _, fragment := range tc.containsSQL {
				if !strings.Contains(sql, fragment) {
					t.Errorf("SQL missing %q:\n%s", fragment, sql)
				}
			}
			for _, fragment := range tc.absentSQL {
				if strings.Contains(sql, fragment) {
					t.Errorf("SQL must not contain %q:\n%s", fragment, sql)
				}
			}
		})
	}
}

// TestValidateColumnMappings verifies duplicate detection.
func TestValidateColumnMappings(t *testing.T) {
	t.Run("no duplicates", func(t *testing.T) {
		mappings := []columnMapping{
			{ctxKey: userIDKey{}, colName: "user_id"},
			{ctxKey: sessionKey{}, colName: "session_id"},
		}
		if err := validateColumnMappings(mappings); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("duplicate column name", func(t *testing.T) {
		mappings := []columnMapping{
			{ctxKey: userIDKey{}, colName: "user_id"},
			{ctxKey: sessionKey{}, colName: "user_id"},
		}
		if err := validateColumnMappings(mappings); err == nil {
			t.Error("expected error for duplicate column name, got nil")
		}
	})

	t.Run("empty mappings", func(t *testing.T) {
		if err := validateColumnMappings(nil); err != nil {
			t.Errorf("unexpected error for nil mappings: %v", err)
		}
	})
}

// TestWithColumnMappingPanics verifies that WithColumnMapping rejects invalid
// inputs at the configuration site rather than at runtime.
func TestWithColumnMappingPanics(t *testing.T) {
	tests := []struct {
		name    string
		ctxKey  any
		colName string
	}{
		{"nil ctxKey", nil, "user_id"},
		{"empty column name", userIDKey{}, ""},
		{"digit-leading column name", userIDKey{}, "1user"},
		{"hyphen in column name", userIDKey{}, "user-id"},
		{"reserved: id", userIDKey{}, "id"},
		{"reserved: namespace", userIDKey{}, "namespace"},
		{"reserved: message", userIDKey{}, "message"},
		{"reserved: remind_at", userIDKey{}, "remind_at"},
		{"reserved: created_at", userIDKey{}, "created_at"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for ctxKey=%v colName=%q, but did not panic", tc.ctxKey, tc.colName)
				}
			}()
			WithColumnMapping(tc.ctxKey, tc.colName)
		})
	}
}

// TestWithColumnMappingValid verifies that WithColumnMapping accepts legal
// column names without panicking.
func TestWithColumnMappingValid(t *testing.T) {
	names := []string{
		"user_id",
		"session_id",
		"_private",
		"CamelCase",
		"col123",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("unexpected panic for column name %q: %v", name, r)
				}
			}()
			WithColumnMapping(userIDKey{}, name)
		})
	}
}
