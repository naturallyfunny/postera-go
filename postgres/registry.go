// Package postgres provides a postera.Registry backed by PostgreSQL.
//
// A Registry stores Posterum entries in a single table (default: posterum)
// and partitions them by namespace, preventing one tenant from observing
// another's entries. Additional context-to-column mappings can be registered
// via WithColumnMapping to store and filter by supplementary identity metadata
// (user ID, session ID, etc.) alongside the core Posterum fields.
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
// lexicographic order. Every file must be idempotent (use IF NOT EXISTS)
// because the runner re-executes all files on every startup; there is no
// migration-state table.
//
// WithColumnMappingAutoMigrate applies add-only DDL for each registered
// column mapping. It issues ALTER TABLE ... ADD COLUMN IF NOT EXISTS ... TEXT,
// which is safe to run on every startup and never drops or renames columns.
//
// # Data migration boundary
//
// WithAutoMigrate only touches schema structure (table and index definitions).
// It never reads or rewrites row content. If you change the format of values
// stored in the namespace column — for example migrating tenant identifiers
// from "user:123" to "org:123" — you must apply the corresponding
// SQL UPDATE yourself, outside this library. postera has no visibility into
// namespace semantics and cannot perform content migrations on your behalf.
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

// validColumnNameRE restricts column names accepted by WithColumnMapping to
// safe SQL identifiers. The first character must be a letter or underscore;
// subsequent characters may also be digits.
var validColumnNameRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// coreColumns is the set of column names that belong to the Registry's core
// schema. WithColumnMapping panics when a caller attempts to map a context key
// to one of these names to prevent accidental shadowing of built-in columns.
var coreColumns = map[string]bool{
	"id":         true,
	"namespace":  true,
	"message":    true,
	"remind_at":  true,
	"created_at": true,
}

// columnMapping pairs a context key with the database column that stores its
// value and, when the value is present in context, filters query results.
type columnMapping struct {
	ctxKey  any
	colName string
}

// Registry persists Posterum entries in a PostgreSQL table, partitioned by
// namespace. A zero Registry is invalid; construct one with NewRegistry.
// Registry is safe for concurrent use.
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
// panics on an empty name: a blank table reference would corrupt every query,
// and surfacing the mistake at the call site is safer than a runtime error
// inside a data operation.
func WithTableName(name string) Option {
	if name == "" {
		panic("postgres: WithTableName called with empty name")
	}
	return func(r *Registry) {
		r.tableName = name
	}
}

// WithAutoMigrate instructs NewRegistry to create the table and index if they
// do not yet exist. The migration uses IF NOT EXISTS, making it idempotent
// and safe to call on every startup. Only DDL (structure) is touched; row
// content is never modified.
func WithAutoMigrate() Option {
	return func(r *Registry) {
		r.autoMigrate = true
	}
}

// WithColumnMapping registers a context-to-column mapping. On every Save, if
// the value at ctxKey in the request context is a string, it is stored in
// colName. On every Get and List, when a string value for ctxKey is present
// in context, results are additionally filtered to rows where colName equals
// that value. Context values that are absent or not of type string are skipped
// silently, consistent with the existing namespace behaviour.
//
// Multiple WithColumnMapping options compose: each is evaluated independently
// on every operation.
//
// colName must match [a-zA-Z_][a-zA-Z0-9_]* and must not be one of the
// reserved core column names (id, namespace, message, remind_at, created_at).
// WithColumnMapping panics on any violation so that misconfiguration surfaces
// at the configuration site rather than corrupting data silently at runtime.
//
// The column must exist in the database before NewRegistry is called. Supply
// WithColumnMappingAutoMigrate to let the Registry create it, or create it
// manually when schema ownership belongs to another tool.
func WithColumnMapping(ctxKey any, colName string) Option {
	if ctxKey == nil {
		panic("postgres: WithColumnMapping called with nil ctxKey")
	}
	if !validColumnNameRE.MatchString(colName) {
		panic(fmt.Sprintf("postgres: WithColumnMapping: %q is not a valid column name (must match [a-zA-Z_][a-zA-Z0-9_]*)", colName))
	}
	if coreColumns[colName] {
		panic(fmt.Sprintf("postgres: WithColumnMapping: %q is a reserved core column name", colName))
	}
	return func(r *Registry) {
		r.columnMappings = append(r.columnMappings, columnMapping{ctxKey: ctxKey, colName: colName})
	}
}

// WithColumnMappingAutoMigrate instructs NewRegistry to issue
// ALTER TABLE ... ADD COLUMN IF NOT EXISTS ... TEXT for every column mapping
// registered via WithColumnMapping. The statement is add-only — it never
// drops, renames, or modifies existing columns — making it safe to apply on
// every startup against a live schema.
//
// Omit this option and create the columns manually when schema ownership
// belongs to another migration tool.
func WithColumnMappingAutoMigrate() Option {
	return func(r *Registry) {
		r.columnMappingAutoMigrate = true
	}
}

// NewRegistry returns a Registry backed by db.
//
// Options are applied in order. Column mapping duplicates are then validated.
// If WithAutoMigrate was supplied, the embedded DDL files are executed next.
// If WithColumnMappingAutoMigrate was supplied, ADD COLUMN statements run after
// the main migration so they always land on an existing table. Finally, a
// zero-row SELECT validates that the table and all configured columns exist,
// so a structural mismatch surfaces at initialization rather than at the first
// data operation.
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

	if err := validateColumnMappings(r.columnMappings); err != nil {
		return nil, err
	}

	if r.autoMigrate {
		if err := r.migrate(ctx); err != nil {
			return nil, fmt.Errorf("postgres: auto-migrate: %w", err)
		}
	}

	if r.columnMappingAutoMigrate {
		if err := r.migrateColumnMappings(ctx); err != nil {
			return nil, fmt.Errorf("postgres: column mapping auto-migrate: %w", err)
		}
	}

	if err := r.validateSchema(ctx); err != nil {
		return nil, err
	}

	return r, nil
}

// validateColumnMappings returns an error when two mappings target the same
// column. Two context keys writing to the same column produce non-deterministic
// results depending on which key is present in context; detecting the collision
// at startup prevents silent data corruption.
func validateColumnMappings(mappings []columnMapping) error {
	seen := make(map[string]bool, len(mappings))
	for _, m := range mappings {
		if seen[m.colName] {
			return fmt.Errorf("postgres: duplicate column mapping for %q", m.colName)
		}
		seen[m.colName] = true
	}
	return nil
}

// migrateColumnMappings issues ADD COLUMN IF NOT EXISTS for each registered
// column mapping. Every column is TEXT; the statement never drops or renames
// existing columns.
func (r *Registry) migrateColumnMappings(ctx context.Context) error {
	for _, m := range r.columnMappings {
		stmt := `ALTER TABLE ` + r.tableRef() + ` ADD COLUMN IF NOT EXISTS ` + pgx.Identifier{m.colName}.Sanitize() + ` TEXT`
		if _, err := r.db.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("add column %q: %w", m.colName, err)
		}
	}
	return nil
}

// Save persists p. If an entry with the same ID already exists, Save
// overwrites message and remind_at while preserving the original namespace
// assignment — an ID produced by Postarius.Create belongs to exactly one
// namespace for its lifetime.
//
// For each column mapping whose context key resolves to a string value in ctx,
// that value is stored in the corresponding column and updated on conflict.
func (r *Registry) Save(ctx context.Context, p postera.Posterum) error {
	ns := namespaceFrom(ctx)
	args := []any{p.ID, ns, p.Message, p.RemindAt.UTC(), p.CreatedAt.UTC()}

	extraCols, extraPlaceholders, extraConflict := r.extraColumnsFromContext(ctx, &args)

	sql := `INSERT INTO ` + r.tableRef() + ` (id, namespace, message, remind_at, created_at` + extraCols + `)
		VALUES ($1, $2, $3, $4, $5` + extraPlaceholders + `)
		ON CONFLICT (id) DO UPDATE
			SET message   = EXCLUDED.message,
			    remind_at = EXCLUDED.remind_at` + extraConflict

	if _, err := r.db.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("postgres: save %s: %w", p.ID, err)
	}
	return nil
}

// extraColumnsFromContext iterates column mappings and, for each context key
// that resolves to a string, appends the value to args and returns the SQL
// fragments for the INSERT column list, VALUES list, and ON CONFLICT SET clause.
func (r *Registry) extraColumnsFromContext(ctx context.Context, args *[]any) (string, string, string) {
	var cols, placeholders, conflictSet string
	for _, m := range r.columnMappings {
		v, ok := ctx.Value(m.ctxKey).(string)
		if !ok {
			continue
		}
		*args = append(*args, v)
		idx := len(*args)
		quoted := pgx.Identifier{m.colName}.Sanitize()
		cols += ", " + quoted
		placeholders += fmt.Sprintf(", $%d", idx)
		conflictSet += fmt.Sprintf(", %s = EXCLUDED.%s", quoted, quoted)
	}
	return cols, placeholders, conflictSet
}

// Get returns the Posterum with the given id within the current namespace.
// If id is found in a different namespace, Get returns an error wrapping
// postera.ErrNotFound — the presence of the id in another namespace is
// never disclosed.
//
// For each column mapping whose context key resolves to a string value in ctx,
// an additional equality filter is applied. Absent or non-string context values
// are skipped.
func (r *Registry) Get(ctx context.Context, id string) (postera.Posterum, error) {
	ns := namespaceFrom(ctx)
	args := []any{id, ns}
	whereExtra := r.extraWhereFromContext(ctx, &args)

	row := r.db.QueryRow(ctx,
		`SELECT id, message, remind_at, created_at
		FROM `+r.tableRef()+`
		WHERE id = $1 AND namespace = $2`+whereExtra,
		args...,
	)

	var (
		p         postera.Posterum
		remindAt  time.Time
		createdAt time.Time
	)
	if err := row.Scan(&p.ID, &p.Message, &remindAt, &createdAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return postera.Posterum{}, fmt.Errorf("postgres: get %s: %w", id, postera.ErrNotFound)
		}
		return postera.Posterum{}, fmt.Errorf("postgres: get %s: %w", id, err)
	}
	p.RemindAt = remindAt.UTC()
	p.CreatedAt = createdAt.UTC()
	return p, nil
}

// Remove deletes the Posterum with the given id from the current namespace.
// If no matching entry exists — including when the id belongs to a different
// namespace — Remove returns an error wrapping postera.ErrNotFound.
func (r *Registry) Remove(ctx context.Context, id string) error {
	ns := namespaceFrom(ctx)
	tag, err := r.db.Exec(ctx,
		`DELETE FROM `+r.tableRef()+`
		WHERE id = $1 AND namespace = $2`,
		id, ns,
	)
	if err != nil {
		return fmt.Errorf("postgres: remove %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: remove %s: %w", id, postera.ErrNotFound)
	}
	return nil
}

// List returns Posterum entries for the current namespace within q's
// half-open interval [q.From, q.To), ordered by RemindAt ascending.
// A zero q.From omits the lower bound; a zero q.To omits the upper bound.
//
// For each column mapping whose context key resolves to a string value in ctx,
// an additional equality filter is applied.
func (r *Registry) List(ctx context.Context, q postera.TimeRange) ([]postera.Posterum, error) {
	ns := namespaceFrom(ctx)
	sql, args := r.listQuery(ctx, ns, q)

	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list: %w", err)
	}
	defer rows.Close()

	var result []postera.Posterum
	for rows.Next() {
		var (
			p         postera.Posterum
			remindAt  time.Time
			createdAt time.Time
		)
		if err := rows.Scan(&p.ID, &p.Message, &remindAt, &createdAt); err != nil {
			return nil, fmt.Errorf("postgres: list: %w", err)
		}
		p.RemindAt = remindAt.UTC()
		p.CreatedAt = createdAt.UTC()
		result = append(result, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list: %w", err)
	}
	return result, nil
}

// listQuery builds the SQL statement and positional argument slice for List.
// It is separated from List to allow unit-testing query construction without
// a live database.
func (r *Registry) listQuery(ctx context.Context, namespace string, q postera.TimeRange) (string, []any) {
	args := []any{namespace}
	sql := `SELECT id, message, remind_at, created_at FROM ` + r.tableRef() + ` WHERE namespace = $1`
	sql += r.extraWhereFromContext(ctx, &args)
	if !q.From.IsZero() {
		args = append(args, q.From.UTC())
		sql += fmt.Sprintf(" AND remind_at >= $%d", len(args))
	}
	if !q.To.IsZero() {
		args = append(args, q.To.UTC())
		sql += fmt.Sprintf(" AND remind_at < $%d", len(args))
	}
	sql += " ORDER BY remind_at ASC"
	return sql, args
}

// extraWhereFromContext iterates column mappings and, for each context key
// that resolves to a string, appends the value to args and returns the
// corresponding AND clauses. Absent or non-string context values are skipped.
func (r *Registry) extraWhereFromContext(ctx context.Context, args *[]any) string {
	var where string
	for _, m := range r.columnMappings {
		v, ok := ctx.Value(m.ctxKey).(string)
		if !ok {
			continue
		}
		*args = append(*args, v)
		where += fmt.Sprintf(" AND %s = $%d", pgx.Identifier{m.colName}.Sanitize(), len(*args))
	}
	return where
}

// migrate executes every *.sql file in the embedded migrations directory in
// lexicographic order. Files are executed as-is after placeholder substitution;
// each file must be idempotent because there is no migration-state table and
// the runner re-executes all files on every startup.
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

// applyPlaceholders replaces migration placeholders in sql with the
// safely-quoted identifiers for this Registry's table and indexes. Using
// explicit placeholders rather than replacing the literal table name prevents
// accidental substitution inside comments or string literals that
// coincidentally contain the default name.
//
// Supported placeholders:
//   - {{table}}      — quoted table identifier (current schema)
//   - {{index}}      — quoted index identifier for (namespace, remind_at ASC)
//   - {{index_old}}  — quoted index identifier for (namespace, execute_at ASC),
//     used by rename migrations to drop the pre-rename index
func (r *Registry) applyPlaceholders(sql string) string {
	indexOld := pgx.Identifier{"idx_" + r.tableName + "_namespace_execute_at"}.Sanitize()
	indexNew := pgx.Identifier{"idx_" + r.tableName + "_namespace_remind_at"}.Sanitize()
	return strings.NewReplacer(
		"{{table}}",     r.tableRef(),
		"{{index}}",     indexNew,
		"{{index_old}}", indexOld,
	).Replace(sql)
}

// validateSchema issues a zero-row SELECT at startup to confirm the table
// and all required columns are present. PostgreSQL rejects the query at
// planning time when the table or any column is absent, giving a clear error
// before any data operation is attempted.
func (r *Registry) validateSchema(ctx context.Context) error {
	sel := "id, namespace, message, remind_at, created_at"
	for _, m := range r.columnMappings {
		sel += ", " + pgx.Identifier{m.colName}.Sanitize()
	}
	rows, err := r.db.Query(ctx,
		`SELECT `+sel+` FROM `+r.tableRef()+` LIMIT 0`,
	)
	if err != nil {
		return fmt.Errorf("postgres: schema validation for table %q: %w", r.tableName, err)
	}
	rows.Close()
	return rows.Err()
}

// tableRef returns the safely-quoted PostgreSQL identifier for the table,
// preventing SQL injection even when WithTableName receives an unusual value.
func (r *Registry) tableRef() string {
	return pgx.Identifier{r.tableName}.Sanitize()
}

// namespaceFrom extracts the namespace from ctx. An absent namespace yields
// an empty string, which acts as an implicit single-tenant namespace and is
// distinct from every named namespace stored with WithNamespace.
func namespaceFrom(ctx context.Context) string {
	ns, _ := postera.NamespaceFromContext(ctx)
	return ns
}

// Compile-time proof that *Registry satisfies postera.Registry.
var _ postera.Registry = (*Registry)(nil)
