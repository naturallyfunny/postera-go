-- Every statement in this file must be idempotent (IF NOT EXISTS) because the
-- migration runner re-executes all files on every startup without state tracking.
--
-- Index creation is intentionally absent from this file. It is handled by
-- 0002_rename_columns.up.sql, which runs immediately after on every startup and
-- creates the correct index for the current schema in all cases (fresh install,
-- upgrade from pre-rename schema, and re-run). Splitting them avoids a re-run
-- hazard: this file's CREATE TABLE is a permanent no-op once the table exists,
-- but a CREATE INDEX here would fail on re-run after 0002 has renamed the column
-- the index references.

CREATE TABLE IF NOT EXISTS "posterum" (
    id         TEXT        NOT NULL,
    namespace  TEXT        NOT NULL DEFAULT '',
    message    TEXT        NOT NULL,
    remind_at  TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (id)
);
