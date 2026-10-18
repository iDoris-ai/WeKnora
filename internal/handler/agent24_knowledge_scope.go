// Agent24 fork product patch A24-KB-02 (CONTRACT-READONLY-v1 R8.4): scope resolver and index facts.

package handler

import (
	"context"
	"errors"
	"slices"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type agent24DefaultScopeResolver struct {
	kbs interfaces.KnowledgeBaseService
}

// Resolve keeps caller-tenant KBs only: integration mode does not consume org sharing (R2.7), so
// cross-tenant KBs are always omitted. Tenant Viewer role is enforced by the route's g.Viewer().
func (r agent24DefaultScopeResolver) Resolve(ctx context.Context, ids []string) ([]*types.KnowledgeBase, error) {
	tenantID := types.CallerFromContext(ctx).TenantID
	out := make([]*types.KnowledgeBase, 0, len(ids))
	for _, id := range ids {
		if types.AuthorizeTenantAPIKeyKnowledgeBases(ctx, id) != nil {
			continue
		}
		kb, err := r.kbs.GetKnowledgeBaseByID(ctx, id)
		if errors.Is(err, repository.ErrKnowledgeBaseNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if tenantID != 0 && kb.TenantID == tenantID {
			out = append(out, kb)
		}
	}
	return out, nil
}

type agent24DBIndexReader struct{ db *gorm.DB }

var agent24ActiveParseStatuses = []string{types.ParseStatusCompleted, types.ParseStatusFinalizing}

// Cancelled docs may keep already-written chunks, so they count toward usable chunks. Known limitation:
// a cancel after CreateChunks but before BatchIndex leaves unindexed chunks that are counted too; the
// exact source-revision/index-build mapping lands with KB-C1-01c.
var agent24ChunkParseStatuses = append(slices.Clone(agent24ActiveParseStatuses), types.ParseStatusCancelled)

// Facts counts the KB's documents; gorm adds deleted_at IS NULL for both models.
func (r agent24DBIndexReader) Facts(ctx context.Context, kb *types.KnowledgeBase) (agent24KBIndexFacts, error) {
	docs := func() *gorm.DB {
		return r.db.WithContext(ctx).Model(&types.Knowledge{}).Where("tenant_id = ? AND knowledge_base_id = ?"+
			" AND parse_status <> ?", kb.TenantID, kb.ID, types.ParseStatusDeleting)
	}
	var f agent24KBIndexFacts
	err := docs().Select("COUNT(*) AS docs, "+
		"COALESCE(SUM(CASE WHEN parse_status IN ? THEN 1 ELSE 0 END), 0) AS active, "+
		"COALESCE(SUM(CASE WHEN parse_status IN ? THEN 1 ELSE 0 END), 0) AS building",
		agent24ActiveParseStatuses, []string{types.ParseStatusPending, types.ParseStatusProcessing},
	).Scan(&f).Error
	if err != nil || f.Docs == 0 {
		return f, err
	}
	var ids []string
	err = r.db.WithContext(ctx).Model(&types.Chunk{}).
		Where("tenant_id = ? AND knowledge_base_id = ? AND chunk_type = ? AND is_enabled = ?",
			kb.TenantID, kb.ID, types.ChunkTypeText, true).
		Where("knowledge_id IN (?)", docs().Select("id").Where("parse_status IN ?", agent24ChunkParseStatuses)).
		Limit(1).Pluck("id", &ids).Error
	f.HasTextChunks = len(ids) > 0
	return f, err
}
