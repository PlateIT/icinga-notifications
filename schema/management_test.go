package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrationPath(t *testing.T) {
	available := []migration{{from: "v1.0", to: "v1.1"}, {from: "v1.1", to: "v1.2"}}

	path, err := migrationPath("v1.0", "v1.2", available)
	require.NoError(t, err)
	require.Equal(t, available, path)

	_, err = migrationPath("v0.9", "v1.2", available)
	require.EqualError(t, err, "no supported schema migration from v0.9 to v1.2")

	cycle := []migration{{from: "v1.0", to: "v1.1"}, {from: "v1.1", to: "v1.0"}}
	_, err = migrationPath("v1.0", "v1.2", cycle)
	require.EqualError(t, err, "schema migration cycle at version v1.0")
}

func TestSplitPostgreSQLStatements(t *testing.T) {
	sql := `CREATE TABLE example (value text);
CREATE FUNCTION example() RETURNS void AS $body$
BEGIN
    PERFORM 'semicolon; inside string';
END;
$body$ LANGUAGE plpgsql;
INSERT INTO example VALUES ('one;two');`

	statements := splitPostgreSQLStatements(sql)
	require.Len(t, statements, 3)
	require.Contains(t, statements[1], "PERFORM 'semicolon; inside string';")
	require.Equal(t, "INSERT INTO example VALUES ('one;two');", statements[2])
}
