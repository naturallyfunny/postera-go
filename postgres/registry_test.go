package postgres

import (
	"strings"
	"testing"
	"time"

	"go.naturallyfunny.dev/postera"
)

// TestListQuery verifies that listQuery always namespaces the query, that
// positional parameter numbers are assigned correctly, and that the half-open
// time bounds are emitted only when the corresponding Query field is non-zero.
func TestListQuery(t *testing.T) {
	r := &Registry{tableName: "posterum"}

	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC)

	tests := []struct {
		name        string
		q           postera.Query
		wantArgs    []any
		containsSQL []string
		absentSQL   []string
	}{
		{
			name:     "no bounds",
			q:        postera.Query{},
			wantArgs: []any{"tenant"},
			containsSQL: []string{
				"namespace = $1",
				"ORDER BY execute_at ASC",
			},
			absentSQL: []string{
				"execute_at >=",
				"execute_at <",
			},
		},
		{
			name:     "from only",
			q:        postera.Query{From: t0},
			wantArgs: []any{"tenant", t0},
			containsSQL: []string{
				"namespace = $1",
				"execute_at >= $2",
				"ORDER BY execute_at ASC",
			},
			absentSQL: []string{"execute_at <"},
		},
		{
			name:     "to only",
			q:        postera.Query{To: t1},
			wantArgs: []any{"tenant", t1},
			containsSQL: []string{
				"namespace = $1",
				"execute_at < $2",
				"ORDER BY execute_at ASC",
			},
			absentSQL: []string{"execute_at >="},
		},
		{
			name:     "both bounds",
			q:        postera.Query{From: t0, To: t1},
			wantArgs: []any{"tenant", t0, t1},
			containsSQL: []string{
				"namespace = $1",
				"execute_at >= $2",
				"execute_at < $3",
				"ORDER BY execute_at ASC",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sql, args := r.listQuery("tenant", tc.q)

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
