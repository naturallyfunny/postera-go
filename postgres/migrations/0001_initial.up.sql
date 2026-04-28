-- {{table}} is replaced at runtime with the sanitized, quoted table identifier.
-- {{index}} is replaced at runtime with the sanitized, quoted index identifier.
-- Every statement in this file must be idempotent (IF NOT EXISTS) because the
-- migration runner re-executes all files on every startup without state tracking.

CREATE TABLE IF NOT EXISTS {{table}} (
    id         TEXT        NOT NULL,
    namespace  TEXT        NOT NULL DEFAULT '',
    body       BYTEA       NOT NULL,
    execute_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (id)
);

CREATE INDEX IF NOT EXISTS {{index}} ON {{table}} (namespace, execute_at ASC);
