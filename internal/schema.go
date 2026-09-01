package internal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"

	"github.com/icinga/icinga-go-library/backoff"
	"github.com/icinga/icinga-go-library/database"
	"github.com/icinga/icinga-go-library/retry"
	"github.com/jmoiron/sqlx"
)

const (
	// Both MySQL and PostgreSQL schema versions are currently the same, but they can
	// evolve independently in the future, so can't be merged into a single constant.
	expectedMysqlSchemaVersion    = "v1.0"
	expectedPostgresSchemaVersion = "v1.0"
)

// ErrSchemaNotExists indicates that the database has not been initialized yet.
var ErrSchemaNotExists = errors.New("notifications database schema does not exist")

// CheckSchema verifies that the database schema version matches the expected version for the database driver.
func CheckSchema(ctx context.Context, db *database.DB) error {
	var expectedSchemaVersion string
	switch db.DriverName() {
	case database.MySQL:
		expectedSchemaVersion = expectedMysqlSchemaVersion
	case database.PostgreSQL:
		expectedSchemaVersion = expectedPostgresSchemaVersion
	default:
		return fmt.Errorf("unsupported database driver %q", db.DriverName())
	}

	if hasSchemaTable, err := db.HasTable(ctx, "notifications_schema"); err != nil {
		return fmt.Errorf("cannot verify existence of database schema table: %w", err)
	} else if !hasSchemaTable {
		return fmt.Errorf("%w; initialize the database before starting the service", ErrSchemaNotExists)
	}

	var dbResult []string
	err := retry.WithBackoff(
		ctx,
		func(ctx context.Context) error {
			qs := `SELECT version FROM notifications_schema ORDER BY timestamp DESC LIMIT 1`
			if err := db.SelectContext(ctx, &dbResult, qs); err != nil {
				return database.CantPerformQuery(err, qs)
			}
			return nil
		},
		retry.Retryable,
		backoff.DefaultBackoff,
		db.GetDefaultRetrySettings(),
	)
	if err != nil {
		return err
	}

	if len(dbResult) == 0 {
		return errors.New("no database schema version found")
	}

	if actualSchemaVersion := dbResult[0]; actualSchemaVersion != expectedSchemaVersion {
		return fmt.Errorf(
			"unexpected database schema version: %s (expected %s), please make sure you have applied all"+
				" database migrations after upgrading Icinga Notifications",
			actualSchemaVersion, expectedSchemaVersion,
		)
	}

	return nil
}

// ImportSchema initializes an empty database from the schema shipped with the
// application image. It deliberately does not apply upgrades to an existing
// database; those remain an explicit operator action.
func ImportSchema(ctx context.Context, db *database.DB, schemaDir string) error {
	var driverDir string
	switch db.DriverName() {
	case database.MySQL:
		driverDir = "mysql"
	case database.PostgreSQL:
		driverDir = "pgsql"
	default:
		return fmt.Errorf("unsupported database driver %q", db.DriverName())
	}

	schemaFile := path.Join(schemaDir, driverDir, "schema.sql")
	schema, err := os.ReadFile(schemaFile) // #nosec G304 -- operator-provided trusted schema root
	if err != nil {
		return fmt.Errorf("cannot read database schema %q: %w", schemaFile, err)
	}

	var importErr error
	switch db.DriverName() {
	case database.PostgreSQL:
		importErr = importPostgresSchema(ctx, db, string(schema))
	case database.MySQL:
		importErr = importMySQLSchema(ctx, db, database.MysqlSplitStatements(string(schema)))
	}
	if importErr != nil {
		return fmt.Errorf("cannot import database schema %q: %w", schemaFile, importErr)
	}

	return nil
}

func importPostgresSchema(ctx context.Context, db *database.DB, schema string) error {
	return db.ExecTx(ctx, nil, func(ctx context.Context, tx *sqlx.Tx) error {
		// Serialize greenfield initialization across all HA replicas. The lock is
		// transaction-scoped and is released automatically on commit or rollback.
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(4850185123065736270)"); err != nil {
			return fmt.Errorf("cannot acquire PostgreSQL schema initialization lock: %w", err)
		}

		var initialized bool
		if err := tx.GetContext(ctx, &initialized,
			"SELECT to_regclass('notifications_schema') IS NOT NULL"); err != nil {
			return fmt.Errorf("cannot recheck PostgreSQL schema after locking: %w", err)
		}
		if initialized {
			return nil
		}

		if _, err := tx.ExecContext(ctx, schema); err != nil {
			return database.CantPerformQuery(err, schema)
		}
		return nil
	})
}

func importMySQLSchema(ctx context.Context, db *database.DB, queries []string) error {
	// MySQL DDL performs implicit commits, so use one dedicated connection and
	// a session lock instead of pretending the import is transactional.
	conn, err := db.Connx(ctx)
	if err != nil {
		return fmt.Errorf("cannot reserve MySQL schema initialization connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	var locked bool
	if err := conn.GetContext(ctx, &locked,
		"SELECT GET_LOCK('icinga_notifications_schema_init', 60)"); err != nil {
		return fmt.Errorf("cannot acquire MySQL schema initialization lock: %w", err)
	}
	if !locked {
		return errors.New("timed out acquiring MySQL schema initialization lock")
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx),
			"SELECT RELEASE_LOCK('icinga_notifications_schema_init')")
	}()

	var initialized bool
	if err := conn.GetContext(ctx, &initialized, `
		SELECT COUNT(*) > 0
		FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_name = 'notifications_schema'
	`); err != nil {
		return fmt.Errorf("cannot recheck MySQL schema after locking: %w", err)
	}
	if initialized {
		return nil
	}

	for _, query := range queries {
		if _, err := conn.ExecContext(ctx, query); err != nil {
			return database.CantPerformQuery(err, query)
		}
	}
	return nil
}
