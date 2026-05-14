// Package postgres provides a postera.Registry backed by PostgreSQL.
//
// A Registry stores Posterum entries in a primary table (default: posterum)
// and an optional detached metadata table (posterum_metadata). The metadata
// table carries identity columns — user ID, tenant ID, or any other
// dimension — that are mapped from context values at runtime via
// WithColumnMapping. This keeps the primary table schema stable and
// provider-agnostic while allowing arbitrary identity isolation strategies.
//
// All timestamps are stored and returned in UTC.
//
// Connectivity is expressed through the Querier interface, which is satisfied
// by *pgxpool.Pool, *pgx.Conn, and pgx.Tx from github.com/jackc/pgx/v5,
// allowing a Registry to participate in caller-managed transactions without
// any adapter.
//
// # Schema migrations
//
// WithAutoMigrate applies the DDL files embedded in postgres/migrations/ in
// lexicographic order. Every file must be idempotent (use IF NOT EXISTS) because
// the runner re-executes all files on every startup; there is no migration-state
// table.
//
// # Fail-fast schema validation
//
// After migration (or immediately if WithAutoMigrate is absent), NewRegistry
// issues a zero-row SELECT to verify the primary table has all required columns.
// When column mappings are configured it also validates the metadata table. A
// mismatch surfaces at initialization — before any data operation is attempted —
// so the process fails loudly rather than silently corrupting data.
//
// # Data migration boundary
//
// WithAutoMigrate only touches schema structure. It never reads or rewrites row
// content. Content migrations must be applied outside this library.
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

// Querier is the minimal interface required by Registry to communicate with
// PostgreSQL. *pgxpool.Pool, *pgx.Conn, and pgx.Tx all satisfy it, so
// callers can pass a transaction to make Registry operations atomic with
// surrounding business logic without any adapter.
type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// transactioner is satisfied by *pgxpool.Pool, *pgx.Conn, and pgx.Tx.
// Pool and Conn create a new transaction; Tx creates a savepoint. Either
// provides atomicity for multi-table writes.
type transactioner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// columnMapping pairs a context key with the metadata column that should
// receive its value on every Save, Get, Remove, and List.
type columnMapping struct {
	ctxKey  any
	colName string
}

// validColName enforces safe, predictable column identifiers. Leading digit
// and bare underscore starts are excluded because PostgreSQL accepts them but
// they indicate unintentional input.
var validColName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Registry persists Posterum entries in a PostgreSQL table. A zero Registry
// is invalid; construct one with NewRegistry. Registry is safe for concurrent
// use.
type Registry struct {
	db                       Querier
	tableName                string
	autoMigrate              bool
	columnMappings           []columnMapping
	columnMappingAutoMigrate bool
}

// Option configures a Registry at construction time.
type Option func(*Registry)

// WithTableName overrides the default table name ("posterum"). WithTableName
// panics on an empty name.
func WithTableName(name string) Option {
	if name == "" {
		panic("postgres: WithTableName called with empty name")
	}
	return func(r *Registry) {
		r.tableName = name
	}
}

// WithAutoMigrate instructs NewRegistry to apply all DDL migration files on
// startup. Migrations are idempotent; the runner re-executes every file on
// every startup. When the schema is already at the latest version, every
// statement is a no-op.
func WithAutoMigrate() Option {
	return func(r *Registry) {
		r.autoMigrate = true
	}
}

// WithColumnMapping registers a context-to-metadata-column mapping. On every
// Save, the value at ctxKey is read from the request context and written to
// colName in the metadata table. On Get, Remove, and List the same value is
// used as an additional WHERE filter so that callers only observe entries
// whose metadata matches the current context — providing identity isolation
// without coupling the primary schema to any particular tenancy model.
//
// colName must match ^[a-zA-Z_][a-zA-Z0-9_]*$; WithColumnMapping panics on
// nil ctxKey or an invalid name.
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

// WithColumnMappingAutoMigrate instructs NewRegistry to add any configured
// column mapping columns to the metadata table if they do not yet exist. Only
// ADD COLUMN IF NOT EXISTS is issued; columns are never dropped, renamed, or
// altered. The metadata table itself must already exist (created by
// WithAutoMigrate or manually).
func WithColumnMappingAutoMigrate() Option {
	return func(r *Registry) {
		r.columnMappingAutoMigrate = true
	}
}

// NewRegistry returns a Registry backed by db.
//
// Options are applied first. If WithAutoMigrate was included, the DDL
// migrations are executed. If WithColumnMappingAutoMigrate was included, any
// missing metadata columns are added. Finally, a zero-row SELECT validates
// the primary table (and the metadata table when column mappings are
// configured), so a structural mismatch surfaces at initialization rather
// than at the first data operation.
//
// NewRegistry panics if db is nil.
func NewRegistry(ctx context.Context, db Querier, opts ...Option) (*Registry, error) {
	if db == nil {
		panic("postgres: NewRegistry called with nil Querier")
	}
	r := &Registry{
		db:        db,
		tableName: "posterum",
	}
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

// Save persists p. If an entry with the same ID already exists, Save
// overwrites message and trigger_at. When column mappings are configured,
// the corresponding metadata row is written atomically in the same
// transaction.
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

// Get returns the Posterum with the given id. When column mappings are
// configured and the current context carries values for them, Get also
// filters by those metadata columns — a row in a different identity partition
// is treated as absent.
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

// Remove deletes the Posterum with the given id. When column mappings are
// configured and the current context carries values for them, Remove also
// requires those metadata columns to match — an entry in a different identity
// partition is treated as absent.
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

	// PostgreSQL DELETE … USING for a JOIN-style filter across two tables.
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

// List returns Posterum entries within q's half-open interval [q.From, q.To),
// ordered by trigger_at ascending. A zero q.From omits the lower bound; a
// zero q.To omits the upper bound. When column mappings are configured and the
// current context carries values for them, List also filters by those metadata
// columns.
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

// listQuery builds the SQL statement and positional argument slice for List.
// Separated from List to allow unit-testing query construction without a live
// database.
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

// ensureMetadataColumns issues ADD COLUMN IF NOT EXISTS for each configured
// column mapping that is absent from the metadata table. Only additive
// changes are made; no column is ever dropped or altered.
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

// migrate executes every *.sql file in the embedded migrations directory in
// lexicographic order. Files are executed after placeholder substitution; each
// file must be idempotent because there is no migration-state table and the
// runner re-executes all files on every startup.
func (r *Registry) migrate(ctx context.Context) error {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		content, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		if _, err := r.db.Exec(ctx, r.applyPlaceholders(string(content))); err != nil {
			return fmt.Errorf("execute %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// applyPlaceholders replaces migration placeholders with the safely-quoted
// identifiers for this Registry's table and indexes.
//
// Supported placeholders:
//   - {{table}}          — quoted primary table identifier
//   - {{metadata_table}} — quoted metadata table identifier ({table}_metadata)
//   - {{index}}          — quoted index on (trigger_at ASC) — current canonical index
//   - {{index_v2}}       — quoted index on (namespace, remind_at ASC) — migration 0002 era
//   - {{index_old}}      — quoted index on (namespace, execute_at ASC) — pre-0002 era
func (r *Registry) applyPlaceholders(sql string) string {
	idxOld := pgx.Identifier{"idx_" + r.tableName + "_namespace_execute_at"}.Sanitize()
	idxV2 := pgx.Identifier{"idx_" + r.tableName + "_namespace_remind_at"}.Sanitize()
	idxCurrent := pgx.Identifier{"idx_" + r.tableName + "_trigger_at"}.Sanitize()
	metaTable := pgx.Identifier{r.tableName + "_metadata"}.Sanitize()
	return strings.NewReplacer(
		"{{table}}",          r.tableRef(),
		"{{metadata_table}}", metaTable,
		"{{index}}",          idxCurrent,
		"{{index_v2}}",       idxV2,
		"{{index_old}}",      idxOld,
	).Replace(sql)
}

// validateSchema issues zero-row SELECTs at startup to confirm that all
// required columns are present. PostgreSQL rejects queries at planning time
// when a table or column is absent, giving a clear error before any data
// operation is attempted.
func (r *Registry) validateSchema(ctx context.Context) error {
	rows, err := r.db.Query(ctx,
		`SELECT id, message, trigger_at, created_at FROM `+r.tableRef()+` LIMIT 0`,
	)
	if err != nil {
		return fmt.Errorf("postgres: schema validation for table %q: %w", r.tableName, err)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: schema validation for table %q: %w", r.tableName, err)
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
		return fmt.Errorf("postgres: schema validation for metadata table %q: %w", r.tableName+"_metadata", err)
	}
	rows.Close()
	return rows.Err()
}

// tableRef returns the safely-quoted PostgreSQL identifier for the primary
// table, preventing SQL injection even when WithTableName receives an unusual
// value.
func (r *Registry) tableRef() string {
	return pgx.Identifier{r.tableName}.Sanitize()
}

// metadataTableRef returns the safely-quoted PostgreSQL identifier for the
// detached metadata table ({tableName}_metadata).
func (r *Registry) metadataTableRef() string {
	return pgx.Identifier{r.tableName + "_metadata"}.Sanitize()
}

// Compile-time proof that *Registry satisfies postera.Registry.
var _ postera.Registry = (*Registry)(nil)
