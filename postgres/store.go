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

type Store struct {
	db          Querier
	autoMigrate bool
}

type Option func(*Store)

func WithAutoMigrate() Option {
	return func(s *Store) {
		s.autoMigrate = true
	}
}

func NewStore(ctx context.Context, db Querier, opts ...Option) (*Store, error) {
	if db == nil {
		panic("postgres: NewStore called with nil Querier")
	}
	s := &Store{db: db}
	for _, opt := range opts {
		opt(s)
	}

	if s.autoMigrate {
		if err := s.migrate(ctx); err != nil {
			return nil, fmt.Errorf("postgres: auto-migrate: %w", err)
		}
	}

	if err := s.validateSchema(ctx); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Store) Save(ctx context.Context, p postera.Posterum) error {
	metadata, err := metadataToJSON(p.Metadata)
	if err != nil {
		return fmt.Errorf("postgres: save %s: metadata: %w", p.ID, err)
	}
	_, err = s.db.Exec(ctx,
		`INSERT INTO `+s.tableRef()+` (id, message, human, agent, session, metadata, trigger_at, created_at)
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

func (s *Store) Get(ctx context.Context, id string) (postera.Posterum, error) {
	row := s.db.QueryRow(ctx,
		`SELECT id, message, human, agent, session, metadata, trigger_at, created_at FROM `+s.tableRef()+` WHERE id = $1`,
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

func (s *Store) Remove(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM `+s.tableRef()+` WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: remove %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: remove %s: %w", id, postera.ErrNotFound)
	}
	return nil
}

func (s *Store) List(ctx context.Context, q postera.Query) ([]postera.Posterum, error) {
	sql, args := s.listQuery(q)

	rows, err := s.db.Query(ctx, sql, args...)
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

func (s *Store) listQuery(q postera.Query) (string, []any) {
	var args []any
	base := `SELECT id, message, human, agent, session, metadata, trigger_at, created_at FROM ` + s.tableRef()
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

func (s *Store) migrate(ctx context.Context) error {
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
		if _, err := s.db.Exec(ctx, string(content)); err != nil {
			return fmt.Errorf("postgres: execute %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (s *Store) validateSchema(ctx context.Context) error {
	rows, err := s.db.Query(ctx,
		`SELECT id, message, human, agent, session, metadata, trigger_at, created_at FROM `+s.tableRef()+` LIMIT 0`,
	)
	if err != nil {
		return fmt.Errorf("postgres: schema validation for table %q: %w", "postera", err)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: schema validation for table %q: %w", "postera", err)
	}

	return nil
}

func (s *Store) tableRef() string {
	return pgx.Identifier{"postera"}.Sanitize()
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

var _ postera.Store = (*Store)(nil)
