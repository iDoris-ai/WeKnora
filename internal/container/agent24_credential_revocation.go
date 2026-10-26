// Agent24 fork product patch A24-KB-20 (KB-C1-03 C3-b2, WeKnora#41, CONTRACT-READONLY-v1 R1.6).

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

// agent24RevocationPurgeTimeout bounds one purge, so a stalled database cannot keep the sweep (and shutdown,
// which waits for it) hanging.
const agent24RevocationPurgeTimeout = time.Minute

// ensureAgent24CredentialRevocations creates the jti revocation table in integration mode and purges
// expired rows until shutdown. The table is not gated by AUTO_MIGRATE and is not recorded in
// schema_migrations: a fork-owned numbered migration would collide with upstream. Standalone mode
// leaves it absent. In integration mode a failure to create it is returned; must panics, so the
// process does not start.
func ensureAgent24CredentialRevocations(db *gorm.DB, cleaner interfaces.ResourceCleaner) error {
	mode, err := handler.Agent24KnowledgeModeFromEnv()
	if err != nil {
		return err
	}
	if mode != handler.Agent24KnowledgeModeIntegration {
		return nil
	}
	if err := repository.EnsureAgent24CredentialRevocationTable(db); err != nil {
		return err
	}
	store := repository.NewAgent24CredentialRevocationStore(db)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		purge := func() {
			ctx, cancel := context.WithTimeout(context.Background(), agent24RevocationPurgeTimeout)
			defer cancel()
			if _, err := store.PurgeExpired(ctx, time.Now()); err != nil {
				logger.Warnf(ctx, "[agent24] credential revocation purge failed: error_len=%d error_type=%T",
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
	cleaner.RegisterWithName("Agent24CredentialRevocationPurge", func() error {
		close(stop)
		if !runtime.WaitFor(done, cleanupStepTimeout) {
			logger.Warnf(context.Background(),
				"[agent24] credential revocation purge still running after %s; continuing shutdown",
				cleanupStepTimeout)
		}
		return nil
	})
	return nil
}
