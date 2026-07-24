package postgres

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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
	maxAttempts int
	baseDelay   time.Duration
}

type Option func(*Store) error

func WithAutoMigrate() Option {
	return func(s *Store) error {
		s.autoMigrate = true
		return nil
	}
}

// WithRetry retries transient failures (connection errors, serialization
// failures, deadlocks) up to maxAttempts times, doubling baseDelay between
// tries. Off by default.
func WithRetry(maxAttempts int, baseDelay time.Duration) Option {
	return func(s *Store) error {
		if maxAttempts < 1 {
			return fmt.Errorf("postgres: WithRetry: maxAttempts must be >= 1, got %d", maxAttempts)
		}
		s.maxAttempts = maxAttempts
		s.baseDelay = baseDelay
		return nil
	}
}

func NewStore(ctx context.Context, db Querier, opts ...Option) (*Store, error) {
	if db == nil {
		panic("postgres: NewStore called with nil Querier")
	}
	s := &Store{db: db}
	for _, opt := range opts {
		if err := opt(s); err != nil {
			return nil, err
		}
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
	err = s.do(ctx, func() error {
		_, e := s.db.Exec(ctx,
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
		return e
	})
	if err != nil {
		return fmt.Errorf("postgres: save %s: %w", p.ID, err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, id string) (postera.Posterum, error) {
	var (
		p         postera.Posterum
		human     pgtype.Text
		agent     pgtype.Text
		session   pgtype.Text
		metadata  []byte
		triggerAt time.Time
		createdAt time.Time
	)
	err := s.do(ctx, func() error {
		row := s.db.QueryRow(ctx,
			`SELECT id, message, human, agent, session, metadata, trigger_at, created_at FROM `+s.tableRef()+` WHERE id = $1`,
			id,
		)
		return row.Scan(&p.ID, &p.Message, &human, &agent, &session, &metadata, &triggerAt, &createdAt)
	})
	if err != nil {
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
	var tag pgconn.CommandTag
	err := s.do(ctx, func() error {
		var e error
		tag, e = s.db.Exec(ctx, `DELETE FROM `+s.tableRef()+` WHERE id = $1`, id)
		return e
	})
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

	var result []postera.Posterum
	err := s.do(ctx, func() error {
		result = nil
		rows, err := s.db.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()

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
				return err
			}
			p.Human = human.String
			p.Agent = agent.String
			p.Session = session.String
			if err := setMetadata(&p, metadata); err != nil {
				return fmt.Errorf("metadata: %w", err)
			}
			p.TriggerAt = triggerAt.UTC()
			p.CreatedAt = createdAt.UTC()
			result = append(result, p)
		}
		return rows.Err()
	})
	if err != nil {
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

// do runs op, retrying transient failures per the WithRetry config.
func (s *Store) do(ctx context.Context, op func() error) error {
	attempts := s.maxAttempts
	if attempts < 1 {
		attempts = 1
	}
	delay := s.baseDelay
	for attempt := 1; ; attempt++ {
		err := op()
		if err == nil {
			return nil
		}
		if attempt >= attempts || !retryable(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
		}
		delay *= 2
	}
}

func retryable(err error) bool {
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if pgconn.SafeToRetry(err) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case strings.HasPrefix(pgErr.Code, "08"), // connection exception
			pgErr.Code == "40001", // serialization_failure
			pgErr.Code == "40P01", // deadlock_detected
			pgErr.Code == "53300", // too_many_connections
			pgErr.Code == "57P01": // admin_shutdown
			return true
		}
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
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
