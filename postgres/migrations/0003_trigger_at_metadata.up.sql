-- This migration is idempotent: all structural changes are guarded by
-- column-existence checks or use IF EXISTS / IF NOT EXISTS.

DO $$
BEGIN
    -- Drop the namespace column. After this migration the primary table is
    -- identity-agnostic; isolation is delegated to the metadata table.
    IF EXISTS (
        SELECT 1 FROM pg_catalog.pg_attribute
        WHERE attrelid = '"posterum"'::regclass
          AND attname   = 'namespace'
          AND attnum    > 0
          AND NOT attisdropped
    ) THEN
        ALTER TABLE "posterum" DROP COLUMN namespace;
    END IF;

    -- Rename remind_at → trigger_at. The domain-agnostic name reflects that
    -- this library is a general prospective-memory orchestrator, not merely a
    -- reminder service.
    IF EXISTS (
        SELECT 1 FROM pg_catalog.pg_attribute
        WHERE attrelid = '"posterum"'::regclass
          AND attname   = 'remind_at'
          AND attnum    > 0
          AND NOT attisdropped
    ) THEN
        ALTER TABLE "posterum" RENAME COLUMN remind_at TO trigger_at;
    END IF;

    -- Drop the namespace+remind_at index created by migration 0002.
    DROP INDEX IF EXISTS "idx_posterum_namespace_remind_at";
END $$;

-- Create the new index on trigger_at only; no-op if already exists.
CREATE INDEX IF NOT EXISTS "idx_posterum_trigger_at" ON "posterum" (trigger_at ASC);

-- Create the detached metadata table. The schema here is minimal: additional
-- identity columns are added on demand via WithColumnMappingAutoMigrate.
CREATE TABLE IF NOT EXISTS "posterum_metadata" (
    posterum_id TEXT NOT NULL,
    PRIMARY KEY (posterum_id),
    FOREIGN KEY (posterum_id) REFERENCES "posterum"(id) ON DELETE CASCADE
);
