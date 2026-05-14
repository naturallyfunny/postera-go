-- This migration is idempotent: column-existence checks via pg_catalog.pg_attribute
-- prevent re-running ALTER TABLE statements that have already been applied.
-- The DROP INDEX uses IF EXISTS for the same reason.
--
-- The CREATE INDEX is guarded by namespace column existence: after migration
-- 0003 removes the namespace column, re-running this file must not attempt to
-- recreate an index that references a dropped column.
--
-- pg_catalog.pg_attribute is used instead of information_schema.columns to
-- avoid ambiguity around search paths.

DO $$
BEGIN
    -- Rename body → message if the old column still exists.
    -- The column was originally BYTEA; after renaming it becomes TEXT via the
    -- ALTER COLUMN below. Both steps are inside the same conditional so the
    -- type change only runs when a rename is needed.
    IF EXISTS (
        SELECT 1 FROM pg_catalog.pg_attribute
        WHERE attrelid = '"posterum"'::regclass
          AND attname   = 'body'
          AND attnum    > 0
          AND NOT attisdropped
    ) THEN
        ALTER TABLE "posterum" RENAME COLUMN body TO message;
        ALTER TABLE "posterum" ALTER COLUMN message TYPE TEXT
            USING convert_from(message, 'UTF8');
    END IF;

    -- Rename execute_at → remind_at if the old column still exists.
    IF EXISTS (
        SELECT 1 FROM pg_catalog.pg_attribute
        WHERE attrelid = '"posterum"'::regclass
          AND attname   = 'execute_at'
          AND attnum    > 0
          AND NOT attisdropped
    ) THEN
        ALTER TABLE "posterum" RENAME COLUMN execute_at TO remind_at;
    END IF;

    -- Drop the pre-rename index if it still exists; no-op otherwise.
    DROP INDEX IF EXISTS "idx_posterum_namespace_execute_at";

    -- Create the namespace+remind_at index only when the namespace column
    -- still exists. After migration 0003 removes namespace this guard
    -- prevents re-creating an index on a dropped column.
    IF EXISTS (
        SELECT 1 FROM pg_catalog.pg_attribute
        WHERE attrelid = '"posterum"'::regclass
          AND attname   = 'namespace'
          AND attnum    > 0
          AND NOT attisdropped
    ) THEN
        CREATE INDEX IF NOT EXISTS "idx_posterum_namespace_remind_at" ON "posterum" (namespace, remind_at ASC);
    END IF;
END $$;
