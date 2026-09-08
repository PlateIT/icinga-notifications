package source_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/icinga/icinga-notifications/internal/daemon"
	"github.com/icinga/icinga-notifications/internal/source"
	"github.com/icinga/icinga-notifications/internal/testutils"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestSyncConfiguredIsHAIdempotentAndReactivatesDeletedSource(t *testing.T) {
	testutils.SkipTestIfDBConfigIsMissing(t)
	daemon.InjectTestConfig(func(configFile *daemon.ConfigFile) { testutils.LoadTestConfig(t, configFile) })
	db := testutils.GetTestDB(t.Context(), t, &daemon.Config().Database)
	logger := testutils.GetTestLogging(t).GetChildLogger("source-sync")
	username := "source-review-" + uuid.NewString()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := db.ExecContext(ctx, db.Rebind(`DELETE FROM "source" WHERE "listener_username" = ?`), username)
		require.NoError(t, err)
	})

	configured := []source.Config{{Type: "icingadb", Name: "review", Username: username, Password: "initial-secret"}}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for range 3 {
		wg.Go(func() { errs <- source.SyncConfigured(t.Context(), db, configured, logger) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	var count int
	require.NoError(t, db.GetContext(t.Context(), &count,
		db.Rebind(`SELECT COUNT(*) FROM "source" WHERE "listener_username" = ?`), username))
	require.Equal(t, 1, count)

	_, err := db.ExecContext(t.Context(), db.Rebind(`UPDATE "source" SET "deleted" = 'y' WHERE "listener_username" = ?`), username)
	require.NoError(t, err)
	configured[0].Name = "review-reactivated"
	configured[0].Password = "rotated-secret"
	require.NoError(t, source.SyncConfigured(t.Context(), db, configured, logger))

	var row struct {
		Name     string `db:"name"`
		Password string `db:"listener_password_hash"`
		Deleted  string `db:"deleted"`
	}
	require.NoError(t, db.GetContext(t.Context(), &row, db.Rebind(
		`SELECT "name", "listener_password_hash", "deleted" FROM "source" WHERE "listener_username" = ?`), username))
	require.Equal(t, "review-reactivated", row.Name)
	require.Equal(t, "n", row.Deleted)
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(row.Password), []byte("rotated-secret")))
}
