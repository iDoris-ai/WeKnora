package service

import (
	"context"
	"sync/atomic"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// Agent24KBRemovalHook is the W1.11/A14 fork hook (CONTRACT-OPS-v2 W1.11, decision #A14; A24-KB-48): before a
// knowledge base is deleted or cleared by any route, it withdraws the base's entry documents (W8.10) and returns
// every submitted candidate that targets it as returned/target_gone (W8.6), with the audit reason kb_deleted.
//
// Evidence ruling 2026-11-11-s4c-hook-placement (R0 unanimous): the soft delete of a deleted base runs inside the
// hook's transaction (Q1-B); the clear runs the hook after the move check and before the empty answer and the
// enqueue (Q2-A); one agent24_knowledge audit row per changed object, after the commit (Q3-C).
type Agent24KBRemovalHook interface {
	// BeforeRemoval does the hook's writes in one transaction and then calls within (when not nil) on that same
	// transaction. Any error rolls all of it back and comes back: the caller then removes nothing (fail closed).
	// After the commit it writes the audit rows of the objects it actually changed.
	BeforeRemoval(ctx context.Context, tenantID uint64, kbID string, within func(tx *gorm.DB) error) error
}

type agent24KBRemovalBox struct{ hook Agent24KBRemovalHook }

var agent24KBRemoval atomic.Pointer[agent24KBRemovalBox]

// SetAgent24KBRemovalHook installs the hook. The container calls it once in both modes: the two processes of an
// integration deployment share one database, and either may serve the upstream delete and clear routes. nil
// uninstalls (tests).
func SetAgent24KBRemovalHook(h Agent24KBRemovalHook) {
	if h == nil {
		agent24KBRemoval.Store(nil)
		return
	}
	agent24KBRemoval.Store(&agent24KBRemovalBox{hook: h})
}

// Agent24KBRemovalHookInstalled is the installed hook, or nil when none is (upstream unit tests build services
// without the container).
func Agent24KBRemovalHookInstalled() Agent24KBRemovalHook {
	if b := agent24KBRemoval.Load(); b != nil {
		return b.hook
	}
	return nil
}

// agent24DeleteKnowledgeBaseRecord is DeleteKnowledgeBase's step 1 (the soft delete) with the hook in front: the
// hook's writes and the soft delete commit together or not at all (Q1-B). Without a hook it is the upstream call.
// tenantID is the base's own tenant.
func agent24DeleteKnowledgeBaseRecord(
	ctx context.Context, repo interfaces.KnowledgeBaseRepository, tenantID uint64, id string,
) error {
	hook := Agent24KBRemovalHookInstalled()
	if hook == nil {
		return repo.DeleteKnowledgeBase(ctx, id)
	}
	return hook.BeforeRemoval(ctx, tenantID, id, func(tx *gorm.DB) error {
		// the statement of repository/knowledgebase.go DeleteKnowledgeBase, on the hook's transaction
		return tx.WithContext(ctx).Where("id = ?", id).Delete(&types.KnowledgeBase{}).Error
	})
}
