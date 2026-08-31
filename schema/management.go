package schema

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/icinga/icinga-go-library/backoff"
	"github.com/icinga/icinga-go-library/database"
	"github.com/icinga/icinga-go-library/retry"
)

const (
	currentMySQLVersion      = "v1.0"
	currentPostgreSQLVersion = "v1.0"
)

// schemaFiles contains the complete schema for a fresh installation.
//
//go:embed mysql/schema.sql pgsql/schema.sql
var schemaFiles embed.FS

// migration describes one supported schema transition. Future schema changes
// must add their SQL explicitly here, so unrelated historical upgrade scripts
// can never be applied by accident.
type migration struct {
	from       string
	to         string
	mysql      string
	postgresql string
}

var migrations = []migration{}

// Ensure initializes an empty database, applies all registered migrations to an
// existing database and verifies that it has the schema expected by this binary.
func Ensure(ctx context.Context, db *database.DB) error {
	expectedVersion, err := expectedVersion(db.DriverName())
	if err != nil {
		return err
	}

	hasVersionTable, err := db.HasTable(ctx, "notifications_schema")
	if err != nil {
		return fmt.Errorf("cannot verify existence of database schema table: %w", err)
	}

	if !hasVersionTable {
		empty, err := isEmpty(ctx, db)
		if err != nil {
			return err
		}
		if !empty {
			return errors.New("database contains an unversioned or partially initialized Icinga Notifications schema")
		}
		if err := initialize(ctx, db); err != nil {
			return fmt.Errorf("cannot initialize database schema: %w", err)
		}
	}

	actualVersion, err := latestVersion(ctx, db)
	if err != nil {
		return err
	}
	if actualVersion != expectedVersion {
		if err := update(ctx, db, actualVersion, expectedVersion); err != nil {
			return err
		}
		actualVersion, err = latestVersion(ctx, db)
		if err != nil {
			return err
		}
	}

	if actualVersion != expectedVersion {
		return fmt.Errorf("unexpected database schema version: %s (expected %s)", actualVersion, expectedVersion)
	}

	return nil
}

func expectedVersion(driver string) (string, error) {
	switch driver {
	case database.MySQL:
		return currentMySQLVersion, nil
	case database.PostgreSQL:
		return currentPostgreSQLVersion, nil
	default:
		return "", fmt.Errorf("unsupported database driver %q", driver)
	}
}

func isEmpty(ctx context.Context, db *database.DB) (bool, error) {
	for _, table := range []string{"available_channel_type", "channel", "incident", "source"} {
		exists, err := db.HasTable(ctx, table)
		if err != nil {
			return false, fmt.Errorf("cannot check whether database is empty: %w", err)
		}
		if exists {
			return false, nil
		}
	}
	return true, nil
}

func initialize(ctx context.Context, db *database.DB) error {
	var path string
	switch db.DriverName() {
	case database.MySQL:
		path = "mysql/schema.sql"
	case database.PostgreSQL:
		path = "pgsql/schema.sql"
	default:
		return fmt.Errorf("unsupported database driver %q", db.DriverName())
	}

	sql, err := schemaFiles.ReadFile(path)
	if err != nil {
		return err
	}
	return execute(ctx, db, string(sql))
}

func latestVersion(ctx context.Context, db *database.DB) (string, error) {
	var versions []string
	query := `SELECT version FROM notifications_schema ORDER BY timestamp DESC LIMIT 1`
	err := retry.WithBackoff(ctx, func(ctx context.Context) error {
		if err := db.SelectContext(ctx, &versions, query); err != nil {
			return database.CantPerformQuery(err, query)
		}
		return nil
	}, retry.Retryable, backoff.DefaultBackoff, db.GetDefaultRetrySettings())
	if err != nil {
		return "", err
	}
	if len(versions) == 0 {
		return "", errors.New("no database schema version found")
	}
	return versions[0], nil
}

func update(ctx context.Context, db *database.DB, from, to string) error {
	path, err := migrationPath(from, to, migrations)
	if err != nil {
		return err
	}
	for _, migration := range path {
		var sql string
		switch db.DriverName() {
		case database.MySQL:
			sql = migration.mysql
		case database.PostgreSQL:
			sql = migration.postgresql
		}
		if strings.TrimSpace(sql) == "" {
			return fmt.Errorf("migration from %s to %s has no SQL for %s", migration.from, migration.to, db.DriverName())
		}
		if err := execute(ctx, db, sql); err != nil {
			return fmt.Errorf("cannot migrate database schema from %s to %s: %w", migration.from, migration.to, err)
		}
	}
	return nil
}

func migrationPath(from, to string, available []migration) ([]migration, error) {
	var path []migration
	seen := map[string]bool{}
	for from != to {
		if seen[from] {
			return nil, fmt.Errorf("schema migration cycle at version %s", from)
		}
		seen[from] = true

		found := false
		for _, candidate := range available {
			if candidate.from == from {
				path = append(path, candidate)
				from = candidate.to
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("no supported schema migration from %s to %s", from, to)
		}
	}
	return path, nil
}

func execute(ctx context.Context, db *database.DB, sql string) error {
	var statements []string
	switch db.DriverName() {
	case database.MySQL:
		statements = database.MysqlSplitStatements(sql)
	case database.PostgreSQL:
		statements = splitPostgreSQLStatements(sql)
	default:
		return fmt.Errorf("unsupported database driver %q", db.DriverName())
	}

	for index, statement := range statements {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("statement %d failed: %w", index+1, err)
		}
	}
	return nil
}

func splitPostgreSQLStatements(sql string) []string {
	var statements []string
	start := 0
	dollarQuote := ""
	inSingleQuote := false
	inDoubleQuote := false

	for index := 0; index < len(sql); index++ {
		switch sql[index] {
		case '\'':
			if dollarQuote == "" && !inDoubleQuote {
				if inSingleQuote && index+1 < len(sql) && sql[index+1] == '\'' {
					index++
				} else {
					inSingleQuote = !inSingleQuote
				}
			}
		case '"':
			if dollarQuote == "" && !inSingleQuote {
				inDoubleQuote = !inDoubleQuote
			}
		case '$':
			if inSingleQuote || inDoubleQuote {
				continue
			}
			end := strings.IndexByte(sql[index+1:], '$')
			if end < 0 {
				continue
			}
			end += index + 1
			tag := sql[index : end+1]
			if dollarQuote == "" {
				dollarQuote = tag
				index = end
			} else if tag == dollarQuote {
				dollarQuote = ""
				index = end
			}
		case ';':
			if dollarQuote == "" && !inSingleQuote && !inDoubleQuote {
				statements = append(statements, strings.TrimSpace(sql[start:index+1]))
				start = index + 1
			}
		}
	}
	if tail := strings.TrimSpace(sql[start:]); tail != "" {
		statements = append(statements, tail)
	}
	return statements
}
