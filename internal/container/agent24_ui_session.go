// Agent24 fork product patch A24-KB-31 (KB-C2-01a, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2–U3).

package container

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/runtime"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agent24UISessionPurgeTimeout bounds one purge, so a stalled database cannot keep the sweep (and shutdown,
// which waits for it) hanging.
const agent24UISessionPurgeTimeout = time.Minute

// ensureAgent24UISessions creates the UI ticket and view-family tables in integration mode and purges
// expired rows until shutdown. The tables are not gated by AUTO_MIGRATE and are not recorded in
// schema_migrations: a fork-owned numbered migration would collide with upstream. Standalone mode
// leaves them absent. In integration mode a failure to create them is returned; must panics, so the
// process does not start.
func ensureAgent24UISessions(db *gorm.DB, cleaner interfaces.ResourceCleaner) error {
	mode, err := handler.Agent24KnowledgeModeFromEnv()
	if err != nil {
		return err
	}
	if mode != handler.Agent24KnowledgeModeIntegration {
		return nil
	}
	if err := repository.EnsureAgent24UISessionTables(db); err != nil {
		return err
	}
	store := repository.NewAgent24UISessionStore(db)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		purge := func() {
			ctx, cancel := context.WithTimeout(context.Background(), agent24UISessionPurgeTimeout)
			defer cancel()
			if _, err := store.PurgeExpired(ctx, time.Now()); err != nil {
				logger.Warnf(ctx, "[agent24] ui session purge failed: error_len=%d error_type=%T",
					len(fmt.Sprint(err)), err)
			}
		}
		purge()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				purge()
			case <-stop:
				return
			}
		}
	}()
	// Same shutdown shape as startAuditLogRetention: a stop channel, a done channel, and a wait
	// bounded by cleanupStepTimeout so a purge still inside the database does not hold every later hook.
	cleaner.RegisterWithName("Agent24UISessionPurge", func() error {
		close(stop)
		if !runtime.WaitFor(done, cleanupStepTimeout) {
			logger.Warnf(context.Background(),
				"[agent24] ui session purge still running after %s; continuing shutdown",
				cleanupStepTimeout)
		}
		return nil
	})
	return nil
}
