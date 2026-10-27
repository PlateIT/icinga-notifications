-- Run only against a disposable test database, with psql -v ON_ERROR_STOP=1.
CREATE SCHEMA notifications_upgrade_fixture;
SET search_path TO notifications_upgrade_fixture;
CREATE TABLE notifications_schema (version text, timestamp bigint);
INSERT INTO notifications_schema VALUES ('v1.0', 1);

DO $$
BEGIN
    IF current_setting('server_version_num')::integer >= 110000 THEN
        EXECUTE 'CREATE PROCEDURE assert_correct_schema_version(expected_version text) LANGUAGE plpgsql AS ''BEGIN NULL; END;''';
    END IF;
END;
$$;

\ir ../upgrades/procedure-to-function.sql
\ir ../upgrades/procedure-to-function.sql

SELECT assert_correct_schema_version('v1.0');
DO $$
BEGIN
    BEGIN
        PERFORM assert_correct_schema_version('wrong-version');
        RAISE EXCEPTION 'The upgraded function accepted a wrong schema version';
    EXCEPTION WHEN others THEN
        IF SQLERRM NOT LIKE 'Schema version mismatch: expected wrong-version, got v1.0.%' THEN
            RAISE;
        END IF;
    END;
END;
$$;

RESET search_path;
DROP SCHEMA notifications_upgrade_fixture CASCADE;
