package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.naturallyfunny.dev/postera"
)

func TestListQuery(t *testing.T) {
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC)

	type userKey struct{}
	type tenantKey struct{}

	tests := []struct {
		name        string
		registry    *Registry
		ctx         context.Context
		q           postera.TimeRange
		wantArgLen  int
		wantArgs    []any
		containsSQL []string
		absentSQL   []string
	}{
		{
			name:        "no bounds no mappings",
			registry:    &Registry{},
			ctx:         context.Background(),
			q:           postera.TimeRange{},
			wantArgLen:  0,
			containsSQL: []string{"ORDER BY p.trigger_at ASC"},
			absentSQL:   []string{"JOIN", "trigger_at >=", "trigger_at <", "WHERE"},
		},
		{
			name:        "from only no mappings",
			registry:    &Registry{},
			ctx:         context.Background(),
			q:           postera.TimeRange{From: t0},
			wantArgLen:  1,
			wantArgs:    []any{t0},
			containsSQL: []string{"p.trigger_at >= $1", "ORDER BY p.trigger_at ASC"},
			absentSQL:   []string{"trigger_at <", "JOIN"},
		},
		{
			name:        "to only no mappings",
			registry:    &Registry{},
			ctx:         context.Background(),
			q:           postera.TimeRange{To: t1},
			wantArgLen:  1,
			wantArgs:    []any{t1},
			containsSQL: []string{"p.trigger_at < $1", "ORDER BY p.trigger_at ASC"},
			absentSQL:   []string{"trigger_at >="},
		},
		{
			name:        "both bounds no mappings",
			registry:    &Registry{},
			ctx:         context.Background(),
			q:           postera.TimeRange{From: t0, To: t1},
			wantArgLen:  2,
			wantArgs:    []any{t0, t1},
			containsSQL: []string{"p.trigger_at >= $1", "p.trigger_at < $2", "ORDER BY p.trigger_at ASC"},
		},
		{
			name: "mapping with context value no bounds",
			registry: &Registry{
				columnMappings: []columnMapping{{ctxKey: userKey{}, colName: "user_id"}},
			},
			ctx:        context.WithValue(context.Background(), userKey{}, "alice"),
			q:          postera.TimeRange{},
			wantArgLen: 1,
			wantArgs:   []any{"alice"},
			containsSQL: []string{
				"INNER JOIN",
				`m."user_id" = $1`,
				"ORDER BY p.trigger_at ASC",
			},
		},
		{
			name: "mapping with no context value — JOIN present but no metadata WHERE",
			registry: &Registry{
				columnMappings: []columnMapping{{ctxKey: userKey{}, colName: "user_id"}},
			},
			ctx:         context.Background(),
			q:           postera.TimeRange{},
			wantArgLen:  0,
			containsSQL: []string{"INNER JOIN", "ORDER BY p.trigger_at ASC"},
			absentSQL:   []string{`"user_id" =`},
		},
		{
			name: "two mappings both present with bounds",
			registry: &Registry{
				columnMappings: []columnMapping{
					{ctxKey: userKey{}, colName: "user_id"},
					{ctxKey: tenantKey{}, colName: "tenant_id"},
				},
			},
			ctx: func() context.Context {
				ctx := context.WithValue(context.Background(), userKey{}, "alice")
				return context.WithValue(ctx, tenantKey{}, "org1")
			}(),
			q:          postera.TimeRange{From: t0},
			wantArgLen: 3,
			wantArgs:   []any{"alice", "org1", t0},
			containsSQL: []string{
				"INNER JOIN",
				`m."user_id" = $1`,
				`m."tenant_id" = $2`,
				"p.trigger_at >= $3",
			},
		},
		{
			name: "two mappings one absent — absent mapping skipped",
			registry: &Registry{
				columnMappings: []columnMapping{
					{ctxKey: userKey{}, colName: "user_id"},
					{ctxKey: tenantKey{}, colName: "tenant_id"},
				},
			},
			ctx:        context.WithValue(context.Background(), userKey{}, "alice"),
			q:          postera.TimeRange{},
			wantArgLen: 1,
			wantArgs:   []any{"alice"},
			containsSQL: []string{
				"INNER JOIN",
				`m."user_id" = $1`,
			},
			absentSQL: []string{`"tenant_id" =`},
		},
		{
			name: "canonical posterum table names are fixed",
			registry: &Registry{
				columnMappings: []columnMapping{
					{ctxKey: userKey{}, colName: "user_id"},
				},
			},
			ctx:        context.WithValue(context.Background(), userKey{}, "bob"),
			q:          postera.TimeRange{},
			wantArgLen: 1,
			containsSQL: []string{
				`"posterum" p`,
				`"posterum_metadata" m`,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sql, args := tc.registry.listQuery(tc.ctx, tc.q)

			if len(args) != tc.wantArgLen {
				t.Fatalf("args len: want %d, got %d (args=%v)", tc.wantArgLen, len(args), args)
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
