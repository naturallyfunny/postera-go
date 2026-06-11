DO $$
DECLARE
    posterum_has_rows BOOLEAN;
BEGIN
    IF to_regclass('"postera"') IS NULL AND to_regclass('"posterum"') IS NOT NULL THEN
        ALTER TABLE "posterum" RENAME TO "postera";
    ELSIF to_regclass('"postera"') IS NOT NULL AND to_regclass('"posterum"') IS NOT NULL THEN
        EXECUTE 'SELECT EXISTS (SELECT 1 FROM "posterum")' INTO posterum_has_rows;
        IF posterum_has_rows THEN
            RAISE EXCEPTION 'postgres: cannot migrate both "postera" and non-empty "posterum" tables';
        END IF;
        DROP TABLE "posterum";
    END IF;

    IF to_regclass('"idx_postera_trigger_at"') IS NULL AND to_regclass('"idx_posterum_trigger_at"') IS NOT NULL THEN
        ALTER INDEX "idx_posterum_trigger_at" RENAME TO "idx_postera_trigger_at";
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS "idx_postera_trigger_at" ON "postera" (trigger_at ASC);
