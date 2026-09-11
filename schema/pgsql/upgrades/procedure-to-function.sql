BEGIN;

-- A procedure cannot be replaced by CREATE OR REPLACE FUNCTION. Remove only the
-- old procedure, without CASCADE, and keep the operation repeatable for new schemas.
-- prokind and DROP PROCEDURE must be parsed dynamically for PostgreSQL < 11.
DO $$
DECLARE
    actual_version text;
    old_procedure boolean;
BEGIN
    SELECT version INTO actual_version FROM notifications_schema ORDER BY timestamp DESC LIMIT 1;
    IF actual_version IS DISTINCT FROM 'v1.0' THEN
        RAISE EXCEPTION 'Expected notifications schema v1.0, got %', actual_version;
    END IF;

    IF current_setting('server_version_num')::integer >= 110000 THEN
        EXECUTE 'SELECT EXISTS (SELECT 1 FROM pg_proc WHERE oid = to_regprocedure(''assert_correct_schema_version(text)'') AND prokind = ''p'')'
            INTO old_procedure;
        IF old_procedure THEN
            EXECUTE 'DROP PROCEDURE assert_correct_schema_version(text)';
        END IF;
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION assert_correct_schema_version(expected_version text)
    RETURNS void
    LANGUAGE plpgsql
    STABLE
    STRICT
    PARALLEL RESTRICTED
AS $$
DECLARE
    actual_version text;
BEGIN
    SELECT version INTO actual_version FROM notifications_schema ORDER BY timestamp DESC LIMIT 1;

    IF actual_version IS NULL THEN
        RAISE 'Schema version not found in notifications_schema table.';
    ELSIF actual_version != expected_version THEN
        RAISE 'Schema version mismatch: expected %, got %. Please apply all previous upgrade scripts in order before applying this one.', expected_version, actual_version;
    END IF;
END;
$$;

SELECT assert_correct_schema_version('v1.0');
COMMIT;
