package postgres

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"go.naturallyfunny.dev/postera"
)

func TestListQuery(t *testing.T) {
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
			name:        "no filter",
			q:           postera.Query{},
			containsSQL: []string{"ORDER BY trigger_at ASC"},
			absentSQL:   []string{"JOIN", "WHERE", "trigger_at >=", "trigger_at <", "human =", "agent =", "session ="},
		},
		{
			name:        "human",
			q:           postera.Query{Human: "human-1"},
			wantArgs:    []any{"human-1"},
			containsSQL: []string{"human = $1", "ORDER BY trigger_at ASC"},
			absentSQL:   []string{"JOIN", "agent =", "session ="},
		},
		{
			name:        "agent",
			q:           postera.Query{Agent: "agent-1"},
			wantArgs:    []any{"agent-1"},
			containsSQL: []string{"agent = $1", "ORDER BY trigger_at ASC"},
			absentSQL:   []string{"JOIN", "human =", "session ="},
		},
		{
			name:        "session",
			q:           postera.Query{Session: "session-1"},
			wantArgs:    []any{"session-1"},
			containsSQL: []string{"session = $1", "ORDER BY trigger_at ASC"},
			absentSQL:   []string{"JOIN", "human =", "agent ="},
		},
		{
			name:        "from only",
			q:           postera.Query{From: t0},
			wantArgs:    []any{t0},
			containsSQL: []string{"trigger_at >= $1", "ORDER BY trigger_at ASC"},
			absentSQL:   []string{"trigger_at <", "JOIN"},
		},
		{
			name:        "to only",
			q:           postera.Query{To: t1},
			wantArgs:    []any{t1},
			containsSQL: []string{"trigger_at < $1", "ORDER BY trigger_at ASC"},
			absentSQL:   []string{"trigger_at >=", "JOIN"},
		},
		{
			name: "all filters",
			q: postera.Query{
				Human:   "human-1",
				Agent:   "agent-1",
				Session: "session-1",
				From:    t0,
				To:      t1,
			},
			wantArgs: []any{"human-1", "agent-1", "session-1", t0, t1},
			containsSQL: []string{
				"human = $1",
				"agent = $2",
				"session = $3",
				"trigger_at >= $4",
				"trigger_at < $5",
				"ORDER BY trigger_at ASC",
			},
			absentSQL: []string{"JOIN", "posterum_metadata"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sql, args := (&Registry{}).listQuery(tc.q)

			if !reflect.DeepEqual(args, tc.wantArgs) {
				t.Fatalf("args: want %#v, got %#v", tc.wantArgs, args)
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

func TestMetadataToJSON(t *testing.T) {
	got, err := metadataToJSON(nil)
	if err != nil {
		t.Fatalf("metadataToJSON nil: %v", err)
	}
	if got != nil {
		t.Fatalf("nil metadata: want nil, got %#v", got)
	}

	got, err = metadataToJSON(map[string]string{})
	if err != nil {
		t.Fatalf("metadataToJSON empty: %v", err)
	}
	if string(got.([]byte)) != "{}" {
		t.Fatalf("empty metadata: want {}, got %s", got)
	}

	got, err = metadataToJSON(map[string]string{"timezone": "Asia/Jakarta"})
	if err != nil {
		t.Fatalf("metadataToJSON value: %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(got.([]byte), &decoded); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if decoded["timezone"] != "Asia/Jakarta" {
		t.Fatalf("metadata round trip: %#v", decoded)
	}
}

func TestSetMetadata(t *testing.T) {
	var p postera.Posterum
	if err := setMetadata(&p, nil); err != nil {
		t.Fatalf("setMetadata nil: %v", err)
	}
	if p.Metadata != nil {
		t.Fatalf("nil raw metadata: want nil map, got %#v", p.Metadata)
	}

	if err := setMetadata(&p, []byte(`{}`)); err != nil {
		t.Fatalf("setMetadata empty: %v", err)
	}
	if p.Metadata == nil || len(p.Metadata) != 0 {
		t.Fatalf("empty raw metadata: want empty non-nil map, got %#v", p.Metadata)
	}
}

type saveCapturingDB struct {
	sql  string
	args []any
}

func (db *saveCapturingDB) Exec(_ context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	db.sql = sql
	db.args = arguments
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (db *saveCapturingDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("unexpected Query")
}

func (db *saveCapturingDB) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("unexpected QueryRow")
}

func TestSaveIncludesIdentityAndMetadataColumns(t *testing.T) {
	db := &saveCapturingDB{}
	r := &Registry{db: db}
	triggerAt := time.Date(2026, 6, 11, 9, 0, 0, 0, time.FixedZone("WIB", 7*60*60))
	createdAt := time.Date(2026, 6, 10, 9, 0, 0, 0, time.FixedZone("WIB", 7*60*60))

	err := r.Save(context.Background(), postera.Posterum{
		ID:        "pstr_1",
		Human:     "human-1",
		Agent:     "agent-1",
		Session:   "session-1",
		Metadata:  map[string]string{"timezone": "Asia/Jakarta"},
		Message:   "hello",
		TriggerAt: triggerAt,
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	for _, fragment := range []string{"human", "agent", "session", "metadata", "trigger_at"} {
		if !strings.Contains(db.sql, fragment) {
			t.Fatalf("Save SQL missing %q:\n%s", fragment, db.sql)
		}
	}
	wantArgs := []any{"pstr_1", "hello", "human-1", "agent-1", "session-1"}
	if !reflect.DeepEqual(db.args[:5], wantArgs) {
		t.Fatalf("Save args prefix: want %#v, got %#v", wantArgs, db.args[:5])
	}
	if db.args[5] == nil {
		t.Fatal("metadata arg should be JSON bytes, got nil")
	}
	if got := db.args[6].(time.Time); !got.Equal(triggerAt.UTC()) || got.Location() != time.UTC {
		t.Fatalf("triggerAt arg: want UTC %s, got %s", triggerAt.UTC(), got)
	}
	if got := db.args[7].(time.Time); !got.Equal(createdAt.UTC()) || got.Location() != time.UTC {
		t.Fatalf("createdAt arg: want UTC %s, got %s", createdAt.UTC(), got)
	}
}

type getDB struct {
	row pgx.Row
}

func (db getDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("unexpected Exec")
}

func (db getDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("unexpected Query")
}

func (db getDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return db.row
}

type posterumRow struct {
	p        postera.Posterum
	metadata []byte
}

func (r posterumRow) Scan(dest ...any) error {
	*(dest[0].(*string)) = r.p.ID
	*(dest[1].(*string)) = r.p.Message
	*(dest[2].(*pgtype.Text)) = pgtype.Text{String: r.p.Human, Valid: r.p.Human != ""}
	*(dest[3].(*pgtype.Text)) = pgtype.Text{String: r.p.Agent, Valid: r.p.Agent != ""}
	*(dest[4].(*pgtype.Text)) = pgtype.Text{String: r.p.Session, Valid: r.p.Session != ""}
	*(dest[5].(*[]byte)) = r.metadata
	*(dest[6].(*time.Time)) = r.p.TriggerAt
	*(dest[7].(*time.Time)) = r.p.CreatedAt
	return nil
}

func TestGetRoundTripsIdentityAndMetadata(t *testing.T) {
	triggerAt := time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)
	createdAt := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)
	r := &Registry{db: getDB{row: posterumRow{
		p: postera.Posterum{
			ID:        "pstr_1",
			Human:     "human-1",
			Agent:     "agent-1",
			Session:   "session-1",
			Message:   "hello",
			TriggerAt: triggerAt,
			CreatedAt: createdAt,
		},
		metadata: []byte(`{"timezone":"Asia/Jakarta"}`),
	}}}

	got, err := r.Get(context.Background(), "pstr_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Human != "human-1" || got.Agent != "agent-1" || got.Session != "session-1" {
		t.Fatalf("identity fields: %#v", got)
	}
	if got.Metadata["timezone"] != "Asia/Jakarta" {
		t.Fatalf("metadata: %#v", got.Metadata)
	}
}

func TestGetMetadataNilAndEmptyMap(t *testing.T) {
	for _, tc := range []struct {
		name      string
		raw       []byte
		wantNil   bool
		wantEmpty bool
	}{
		{name: "nil", raw: nil, wantNil: true},
		{name: "empty", raw: []byte(`{}`), wantEmpty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Registry{db: getDB{row: posterumRow{
				p: postera.Posterum{
					ID:        "pstr_1",
					Message:   "hello",
					TriggerAt: time.Now(),
					CreatedAt: time.Now(),
				},
				metadata: tc.raw,
			}}}

			got, err := r.Get(context.Background(), "pstr_1")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if tc.wantNil && got.Metadata != nil {
				t.Fatalf("metadata: want nil, got %#v", got.Metadata)
			}
			if tc.wantEmpty && (got.Metadata == nil || len(got.Metadata) != 0) {
				t.Fatalf("metadata: want empty non-nil map, got %#v", got.Metadata)
			}
		})
	}
}
