package postgres

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"go.naturallyfunny.dev/postera"
)

//go:embed migrations
var migrationFiles embed.FS

type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Registry struct {
	db          Querier
	autoMigrate bool
}

type Option func(*Registry)

func WithAutoMigrate() Option {
	return func(r *Registry) {
		r.autoMigrate = true
	}
}

func NewRegistry(ctx context.Context, db Querier, opts ...Option) (*Registry, error) {
	if db == nil {
		panic("postgres: NewRegistry called with nil Querier")
	}
	r := &Registry{db: db}
	for _, opt := range opts {
		opt(r)
	}

	if r.autoMigrate {
		if err := r.migrate(ctx); err != nil {
			return nil, fmt.Errorf("postgres: auto-migrate: %w", err)
		}
	}

	if err := r.validateSchema(ctx); err != nil {
		return nil, err
	}

	return r, nil
}

func (r *Registry) Save(ctx context.Context, p postera.Posterum) error {
	metadata, err := metadataToJSON(p.Metadata)
	if err != nil {
		return fmt.Errorf("postgres: save %s: metadata: %w", p.ID, err)
	}
	_, err = r.db.Exec(ctx,
		`INSERT INTO `+r.tableRef()+` (id, message, human, agent, session, metadata, trigger_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO UPDATE
			SET message    = EXCLUDED.message,
			    human      = EXCLUDED.human,
			    agent      = EXCLUDED.agent,
			    session    = EXCLUDED.session,
			    metadata   = EXCLUDED.metadata,
			    trigger_at = EXCLUDED.trigger_at`,
		p.ID,
		p.Message,
		p.Human,
		p.Agent,
		p.Session,
		metadata,
		p.TriggerAt.UTC(),
		p.CreatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("postgres: save %s: %w", p.ID, err)
	}
	return nil
}

func (r *Registry) Get(ctx context.Context, id string) (postera.Posterum, error) {
	row := r.db.QueryRow(ctx,
		`SELECT id, message, human, agent, session, metadata, trigger_at, created_at FROM `+r.tableRef()+` WHERE id = $1`,
		id,
	)

	var (
		p         postera.Posterum
		human     pgtype.Text
		agent     pgtype.Text
		session   pgtype.Text
		metadata  []byte
		triggerAt time.Time
		createdAt time.Time
	)
	if err := row.Scan(&p.ID, &p.Message, &human, &agent, &session, &metadata, &triggerAt, &createdAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return postera.Posterum{}, fmt.Errorf("postgres: get %s: %w", id, postera.ErrNotFound)
		}
		return postera.Posterum{}, fmt.Errorf("postgres: get %s: %w", id, err)
	}
	p.Human = human.String
	p.Agent = agent.String
	p.Session = session.String
	if err := setMetadata(&p, metadata); err != nil {
		return postera.Posterum{}, fmt.Errorf("postgres: get %s: metadata: %w", id, err)
	}
	p.TriggerAt = triggerAt.UTC()
	p.CreatedAt = createdAt.UTC()
	return p, nil
}

func (r *Registry) Remove(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM `+r.tableRef()+` WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: remove %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: remove %s: %w", id, postera.ErrNotFound)
	}
	return nil
}

func (r *Registry) List(ctx context.Context, q postera.Query) ([]postera.Posterum, error) {
	sql, args := r.listQuery(q)

	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list: %w", err)
	}
	defer rows.Close()

	var result []postera.Posterum
	for rows.Next() {
		var (
			p         postera.Posterum
			human     pgtype.Text
			agent     pgtype.Text
			session   pgtype.Text
			metadata  []byte
			triggerAt time.Time
			createdAt time.Time
		)
		if err := rows.Scan(&p.ID, &p.Message, &human, &agent, &session, &metadata, &triggerAt, &createdAt); err != nil {
			return nil, fmt.Errorf("postgres: list: %w", err)
		}
		p.Human = human.String
		p.Agent = agent.String
		p.Session = session.String
		if err := setMetadata(&p, metadata); err != nil {
			return nil, fmt.Errorf("postgres: list: metadata: %w", err)
		}
		p.TriggerAt = triggerAt.UTC()
		p.CreatedAt = createdAt.UTC()
		result = append(result, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list: %w", err)
	}
	return result, nil
}

func (r *Registry) listQuery(q postera.Query) (string, []any) {
	var args []any
	base := `SELECT id, message, human, agent, session, metadata, trigger_at, created_at FROM ` + r.tableRef()
	var conditions []string

	if q.Human != "" {
		args = append(args, q.Human)
		conditions = append(conditions, fmt.Sprintf("human = $%d", len(args)))
	}
	if q.Agent != "" {
		args = append(args, q.Agent)
		conditions = append(conditions, fmt.Sprintf("agent = $%d", len(args)))
	}
	if q.Session != "" {
		args = append(args, q.Session)
		conditions = append(conditions, fmt.Sprintf("session = $%d", len(args)))
	}
	if !q.From.IsZero() {
		args = append(args, q.From.UTC())
		conditions = append(conditions, fmt.Sprintf("trigger_at >= $%d", len(args)))
	}
	if !q.To.IsZero() {
		args = append(args, q.To.UTC())
		conditions = append(conditions, fmt.Sprintf("trigger_at < $%d", len(args)))
	}

	sql := base
	if len(conditions) > 0 {
		sql += " WHERE " + strings.Join(conditions, " AND ")
	}
	sql += " ORDER BY trigger_at ASC"
	return sql, args
}

func (r *Registry) migrate(ctx context.Context) error {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("postgres: read migrations: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		content, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("postgres: read %s: %w", entry.Name(), err)
		}
		if _, err := r.db.Exec(ctx, string(content)); err != nil {
			return fmt.Errorf("postgres: execute %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (r *Registry) validateSchema(ctx context.Context) error {
	rows, err := r.db.Query(ctx,
		`SELECT id, message, human, agent, session, metadata, trigger_at, created_at FROM `+r.tableRef()+` LIMIT 0`,
	)
	if err != nil {
		return fmt.Errorf("postgres: schema validation for table %q: %w", "posterum", err)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: schema validation for table %q: %w", "posterum", err)
	}

	return nil
}

func (r *Registry) tableRef() string {
	return pgx.Identifier{"posterum"}.Sanitize()
}

func metadataToJSON(metadata map[string]string) (any, error) {
	if metadata == nil {
		return nil, nil
	}
	return json.Marshal(metadata)
}

func setMetadata(p *postera.Posterum, raw []byte) error {
	if raw == nil {
		p.Metadata = nil
		return nil
	}
	var metadata map[string]string
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return err
	}
	p.Metadata = metadata
	return nil
}

var _ postera.Registry = (*Registry)(nil)
