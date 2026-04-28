// Package postgres provides a postera.Registry backed by PostgreSQL.
//
// A Registry stores Posterum entries in a single table (default: posterum)
// and partitions them by namespace, preventing one tenant from observing
// another's entries. All timestamps are stored and returned in UTC.
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

// Registry persists Posterum entries in a PostgreSQL table, partitioned by
// namespace. A zero Registry is invalid; construct one with NewRegistry.
// Registry is safe for concurrent use.
type Registry struct {
	db          Querier
	tableName   string
	autoMigrate bool
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

// NewRegistry returns a Registry backed by db.
//
// Options are applied first; if WithAutoMigrate was included, the DDL is then
// executed using the supplied ctx. Finally, a zero-row SELECT validates that
// the table and all required columns exist, so a structural mismatch surfaces
// at initialization rather than at the first data operation.
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

	if err := r.validateSchema(ctx); err != nil {
		return nil, err
	}

	return r, nil
}

// Save persists p. If an entry with the same ID already exists, Save
// overwrites body and execute_at while preserving the original namespace
// assignment — an ID produced by Postarius.Create belongs to exactly one
// namespace for its lifetime.
func (r *Registry) Save(ctx context.Context, p postera.Posterum) error {
	ns := namespaceFrom(ctx)
	_, err := r.db.Exec(ctx,
		`INSERT INTO `+r.tableRef()+` (id, namespace, body, execute_at, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE
			SET body       = EXCLUDED.body,
			    execute_at = EXCLUDED.execute_at`,
		p.ID,
		ns,
		p.Body,
		p.ExecuteAt.UTC(),
		p.CreatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("postgres: save %s: %w", p.ID, err)
	}
	return nil
}

// Get returns the Posterum with the given id within the current namespace.
// If id is found in a different namespace, Get returns an error wrapping
// postera.ErrNotFound — the presence of the id in another namespace is
// never disclosed.
func (r *Registry) Get(ctx context.Context, id string) (postera.Posterum, error) {
	ns := namespaceFrom(ctx)
	row := r.db.QueryRow(ctx,
		`SELECT id, body, execute_at, created_at
		FROM `+r.tableRef()+`
		WHERE id = $1 AND namespace = $2`,
		id, ns,
	)

	var (
		p         postera.Posterum
		executeAt time.Time
		createdAt time.Time
	)
	if err := row.Scan(&p.ID, &p.Body, &executeAt, &createdAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return postera.Posterum{}, fmt.Errorf("postgres: get %s: %w", id, postera.ErrNotFound)
		}
		return postera.Posterum{}, fmt.Errorf("postgres: get %s: %w", id, err)
	}
	p.ExecuteAt = executeAt.UTC()
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
// half-open time range [q.From, q.To), ordered by ExecuteAt ascending.
// A zero q.From omits the lower bound; a zero q.To omits the upper bound.
func (r *Registry) List(ctx context.Context, q postera.Query) ([]postera.Posterum, error) {
	ns := namespaceFrom(ctx)
	sql, args := r.listQuery(ns, q)

	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list: %w", err)
	}
	defer rows.Close()

	var result []postera.Posterum
	for rows.Next() {
		var (
			p         postera.Posterum
			executeAt time.Time
			createdAt time.Time
		)
		if err := rows.Scan(&p.ID, &p.Body, &executeAt, &createdAt); err != nil {
			return nil, fmt.Errorf("postgres: list: %w", err)
		}
		p.ExecuteAt = executeAt.UTC()
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
func (r *Registry) listQuery(namespace string, q postera.Query) (string, []any) {
	args := []any{namespace}
	sql := `SELECT id, body, execute_at, created_at FROM ` + r.tableRef() + ` WHERE namespace = $1`
	if !q.From.IsZero() {
		args = append(args, q.From.UTC())
		sql += fmt.Sprintf(" AND execute_at >= $%d", len(args))
	}
	if !q.To.IsZero() {
		args = append(args, q.To.UTC())
		sql += fmt.Sprintf(" AND execute_at < $%d", len(args))
	}
	sql += " ORDER BY execute_at ASC"
	return sql, args
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

// applyPlaceholders replaces {{table}} and {{index}} in a migration SQL string
// with the safely-quoted identifiers for this Registry's table and its
// composite index. Using explicit placeholders rather than replacing the
// literal table name prevents accidental substitution inside comments or
// string literals that coincidentally contain the default name.
func (r *Registry) applyPlaceholders(sql string) string {
	indexRef := pgx.Identifier{"idx_" + r.tableName + "_namespace_execute_at"}.Sanitize()
	return strings.NewReplacer(
		"{{table}}", r.tableRef(),
		"{{index}}", indexRef,
	).Replace(sql)
}

// validateSchema issues a zero-row SELECT at startup to confirm the table
// and all required columns are present. PostgreSQL rejects the query at
// planning time when the table or any column is absent, giving a clear error
// before any data operation is attempted.
func (r *Registry) validateSchema(ctx context.Context) error {
	rows, err := r.db.Query(ctx,
		`SELECT id, namespace, body, execute_at, created_at FROM `+r.tableRef()+` LIMIT 0`,
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
