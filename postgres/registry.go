package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"go.naturallyfunny.dev/postera"
)

//go:embed migrations
var migrationFiles embed.FS

type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type transactioner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

type columnMapping struct {
	ctxKey  any
	colName string
}

var validColName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

type Registry struct {
	db                       Querier
	autoMigrate              bool
	columnMappings           []columnMapping
	columnMappingAutoMigrate bool
}

type Option func(*Registry)

func WithAutoMigrate() Option {
	return func(r *Registry) {
		r.autoMigrate = true
	}
}

func WithColumnMapping(ctxKey any, colName string) Option {
	if ctxKey == nil {
		panic("postgres: WithColumnMapping: ctxKey must not be nil")
	}
	if !validColName.MatchString(colName) {
		panic(fmt.Sprintf("postgres: WithColumnMapping: invalid column name %q: must match ^[a-zA-Z_][a-zA-Z0-9_]*$", colName))
	}
	return func(r *Registry) {
		r.columnMappings = append(r.columnMappings, columnMapping{ctxKey: ctxKey, colName: colName})
	}
}

func WithColumnMappingAutoMigrate() Option {
	return func(r *Registry) {
		r.columnMappingAutoMigrate = true
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

	if r.columnMappingAutoMigrate && len(r.columnMappings) > 0 {
		if err := r.ensureMetadataColumns(ctx); err != nil {
			return nil, fmt.Errorf("postgres: column mapping auto-migrate: %w", err)
		}
	}

	if err := r.validateSchema(ctx); err != nil {
		return nil, err
	}

	return r, nil
}

func (r *Registry) Save(ctx context.Context, p postera.Posterum) error {
	if len(r.columnMappings) == 0 {
		return r.execSave(ctx, r.db, p)
	}
	return r.saveWithMetadata(ctx, p)
}

func (r *Registry) execSave(ctx context.Context, q Querier, p postera.Posterum) error {
	_, err := q.Exec(ctx,
		`INSERT INTO `+r.tableRef()+` (id, message, trigger_at, created_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE
			SET message    = EXCLUDED.message,
			    trigger_at = EXCLUDED.trigger_at`,
		p.ID,
		p.Message,
		p.TriggerAt.UTC(),
		p.CreatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("postgres: save %s: %w", p.ID, err)
	}
	return nil
}

func (r *Registry) saveWithMetadata(ctx context.Context, p postera.Posterum) error {
	if btx, ok := r.db.(transactioner); ok {
		tx, err := btx.Begin(ctx)
		if err != nil {
			return fmt.Errorf("postgres: save %s: begin tx: %w", p.ID, err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a harmless no-op
		if err := r.execSave(ctx, tx, p); err != nil {
			return err
		}
		if err := r.execSaveMetadata(ctx, tx, p.ID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	// db is already a Tx or some other non-transactioner querier; execute sequentially.
	if err := r.execSave(ctx, r.db, p); err != nil {
		return err
	}
	return r.execSaveMetadata(ctx, r.db, p.ID)
}

func (r *Registry) execSaveMetadata(ctx context.Context, q Querier, posterumID string) error {
	pidCol := pgx.Identifier{"posterum_id"}.Sanitize()
	cols := []string{pidCol}
	vals := []any{posterumID}
	updateClauses := []string{}

	for _, m := range r.columnMappings {
		v, _ := ctx.Value(m.ctxKey).(string)
		colQ := pgx.Identifier{m.colName}.Sanitize()
		cols = append(cols, colQ)
		vals = append(vals, v)
		updateClauses = append(updateClauses, fmt.Sprintf("%s = EXCLUDED.%s", colQ, colQ))
	}

	placeholders := make([]string, len(vals))
	for i := range vals {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}

	conflictAction := "DO NOTHING"
	if len(updateClauses) > 0 {
		conflictAction = "DO UPDATE SET " + strings.Join(updateClauses, ", ")
	}

	sql := fmt.Sprintf(
		`INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) %s`,
		r.metadataTableRef(),
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
		pidCol,
		conflictAction,
	)
	if _, err := q.Exec(ctx, sql, vals...); err != nil {
		return fmt.Errorf("postgres: save metadata %s: %w", posterumID, err)
	}
	return nil
}

func (r *Registry) Get(ctx context.Context, id string) (postera.Posterum, error) {
	sql, args := r.getQuery(ctx, id)
	row := r.db.QueryRow(ctx, sql, args...)

	var (
		p         postera.Posterum
		triggerAt time.Time
		createdAt time.Time
	)
	if err := row.Scan(&p.ID, &p.Message, &triggerAt, &createdAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return postera.Posterum{}, fmt.Errorf("postgres: get %s: %w", id, postera.ErrNotFound)
		}
		return postera.Posterum{}, fmt.Errorf("postgres: get %s: %w", id, err)
	}
	p.TriggerAt = triggerAt.UTC()
	p.CreatedAt = createdAt.UTC()
	return p, nil
}

func (r *Registry) getQuery(ctx context.Context, id string) (string, []any) {
	args := []any{id}
	base := `SELECT p.id, p.message, p.trigger_at, p.created_at FROM ` + r.tableRef() + ` p`
	conditions := []string{"p.id = $1"}

	if len(r.columnMappings) > 0 {
		base += ` INNER JOIN ` + r.metadataTableRef() + ` m ON p.id = m.posterum_id`
		for _, m := range r.columnMappings {
			if v, ok := ctx.Value(m.ctxKey).(string); ok && v != "" {
				args = append(args, v)
				conditions = append(conditions,
					fmt.Sprintf(`m.%s = $%d`, pgx.Identifier{m.colName}.Sanitize(), len(args)))
			}
		}
	}

	return base + " WHERE " + strings.Join(conditions, " AND "), args
}

func (r *Registry) Remove(ctx context.Context, id string) error {
	sql, args := r.removeQuery(ctx, id)
	tag, err := r.db.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("postgres: remove %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: remove %s: %w", id, postera.ErrNotFound)
	}
	return nil
}

func (r *Registry) removeQuery(ctx context.Context, id string) (string, []any) {
	args := []any{id}

	if len(r.columnMappings) == 0 {
		return `DELETE FROM ` + r.tableRef() + ` WHERE id = $1`, args
	}

	conditions := []string{"p.id = m.posterum_id", "p.id = $1"}
	for _, m := range r.columnMappings {
		if v, ok := ctx.Value(m.ctxKey).(string); ok && v != "" {
			args = append(args, v)
			conditions = append(conditions,
				fmt.Sprintf(`m.%s = $%d`, pgx.Identifier{m.colName}.Sanitize(), len(args)))
		}
	}

	sql := `DELETE FROM ` + r.tableRef() + ` p USING ` + r.metadataTableRef() + ` m WHERE ` +
		strings.Join(conditions, " AND ")
	return sql, args
}

func (r *Registry) List(ctx context.Context, q postera.TimeRange) ([]postera.Posterum, error) {
	sql, args := r.listQuery(ctx, q)

	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list: %w", err)
	}
	defer rows.Close()

	var result []postera.Posterum
	for rows.Next() {
		var (
			p         postera.Posterum
			triggerAt time.Time
			createdAt time.Time
		)
		if err := rows.Scan(&p.ID, &p.Message, &triggerAt, &createdAt); err != nil {
			return nil, fmt.Errorf("postgres: list: %w", err)
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

func (r *Registry) listQuery(ctx context.Context, q postera.TimeRange) (string, []any) {
	var args []any
	base := `SELECT p.id, p.message, p.trigger_at, p.created_at FROM ` + r.tableRef() + ` p`
	var conditions []string

	if len(r.columnMappings) > 0 {
		base += ` INNER JOIN ` + r.metadataTableRef() + ` m ON p.id = m.posterum_id`
		for _, m := range r.columnMappings {
			if v, ok := ctx.Value(m.ctxKey).(string); ok && v != "" {
				args = append(args, v)
				conditions = append(conditions,
					fmt.Sprintf(`m.%s = $%d`, pgx.Identifier{m.colName}.Sanitize(), len(args)))
			}
		}
	}

	if !q.From.IsZero() {
		args = append(args, q.From.UTC())
		conditions = append(conditions, fmt.Sprintf("p.trigger_at >= $%d", len(args)))
	}
	if !q.To.IsZero() {
		args = append(args, q.To.UTC())
		conditions = append(conditions, fmt.Sprintf("p.trigger_at < $%d", len(args)))
	}

	sql := base
	if len(conditions) > 0 {
		sql += " WHERE " + strings.Join(conditions, " AND ")
	}
	sql += " ORDER BY p.trigger_at ASC"
	return sql, args
}

func (r *Registry) ensureMetadataColumns(ctx context.Context) error {
	for _, m := range r.columnMappings {
		sql := fmt.Sprintf(
			`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s TEXT`,
			r.metadataTableRef(),
			pgx.Identifier{m.colName}.Sanitize(),
		)
		if _, err := r.db.Exec(ctx, sql); err != nil {
			return fmt.Errorf("postgres: ensure metadata column %q: %w", m.colName, err)
		}
	}
	return nil
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
		`SELECT id, message, trigger_at, created_at FROM `+r.tableRef()+` LIMIT 0`,
	)
	if err != nil {
		return fmt.Errorf("postgres: schema validation for table %q: %w", "posterum", err)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: schema validation for table %q: %w", "posterum", err)
	}

	if len(r.columnMappings) == 0 {
		return nil
	}

	cols := []string{pgx.Identifier{"posterum_id"}.Sanitize()}
	for _, m := range r.columnMappings {
		cols = append(cols, pgx.Identifier{m.colName}.Sanitize())
	}
	metaSQL := `SELECT ` + strings.Join(cols, ", ") + ` FROM ` + r.metadataTableRef() + ` LIMIT 0`
	rows, err = r.db.Query(ctx, metaSQL)
	if err != nil {
		return fmt.Errorf("postgres: schema validation for metadata table %q: %w", "posterum_metadata", err)
	}
	rows.Close()
	return rows.Err()
}

func (r *Registry) tableRef() string {
	return pgx.Identifier{"posterum"}.Sanitize()
}

func (r *Registry) metadataTableRef() string {
	return pgx.Identifier{"posterum_metadata"}.Sanitize()
}

var _ postera.Registry = (*Registry)(nil)
