// Agent24 fork product patch A24-KB-02 (CONTRACT-READONLY-v1 R8.4): scope resolver and index facts.

package handler

import (
	"context"
	"slices"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const agent24ScopeBatch = 500

type agent24DefaultScopeResolver struct {
	kbs interfaces.KnowledgeBaseService
}

// Resolve keeps caller-tenant KBs only (integration mode does not consume org sharing, R2.7; Viewer: g.Viewer()).
// ids (deduped) pass the in-memory API key filter, then batched IN lookups, re-sorted into ids order.
// Batches keep each statement under bind-parameter limits (Postgres 65535, old SQLite 999): one oversized
// IN would fail as a retryable 503 forever.
func (r agent24DefaultScopeResolver) Resolve(ctx context.Context, ids []string) ([]*types.KnowledgeBase, error) {
	tenantID := types.CallerFromContext(ctx).TenantID
	if tenantID == 0 {
		return nil, nil
	}
	pos, allowed := make(map[string]int, len(ids)), make([]string, 0, len(ids))
	for _, id := range ids {
		if types.AuthorizeTenantAPIKeyKnowledgeBases(ctx, id) == nil {
			pos[id], allowed = len(pos), append(allowed, id)
		}
	}
	var kbs []*types.KnowledgeBase
	for batch := range slices.Chunk(allowed, agent24ScopeBatch) {
		got, err := r.kbs.GetKnowledgeBasesByIDsOnly(ctx, batch)
		if err != nil {
			return nil, err
		}
		kbs = append(kbs, got...)
	}
	kbs = slices.DeleteFunc(kbs, func(kb *types.KnowledgeBase) bool {
		if kb == nil {
			return true
		}
		_, asked := pos[kb.ID] // a row matched only by DB collation is not in Q
		return !asked || kb.TenantID != tenantID
	})
	slices.SortFunc(kbs, func(a, b *types.KnowledgeBase) int { return pos[a.ID] - pos[b.ID] })
	return kbs, nil
}

type agent24DBIndexReader struct{ db *gorm.DB }

var agent24ActiveParseStatuses = []string{types.ParseStatusCompleted, types.ParseStatusFinalizing}

// agent24CountedParseStatuses: documents that count toward a KB's readiness (contract §5, r9: not
// deleting, draft or an unknown status).
var agent24CountedParseStatuses = []string{
	types.ParseStatusPending,
	types.ParseStatusProcessing,
	types.ParseStatusFailed,
	types.ParseStatusCancelled,
	types.ParseStatusCompleted,
	types.ParseStatusFinalizing,
}

// agent24BuildingParseStatuses: documents still being built (index_building).
var agent24BuildingParseStatuses = []string{types.ParseStatusPending, types.ParseStatusProcessing}

// Facts counts the KB's counted documents (r9: not deleting, draft or unknown) and whether any has a servable chunk;
// gorm adds deleted_at IS NULL for both models.
func (r agent24DBIndexReader) Facts(ctx context.Context, kb *types.KnowledgeBase) (agent24KBIndexFacts, error) {
	docs := func() *gorm.DB {
		return r.db.WithContext(ctx).Model(&types.Knowledge{}).Where("tenant_id = ? AND knowledge_base_id = ?"+
			" AND parse_status IN ?", kb.TenantID, kb.ID, agent24CountedParseStatuses)
	}
	var f agent24KBIndexFacts
	err := docs().Select("COUNT(*) AS docs, "+
		"COALESCE(SUM(CASE WHEN parse_status IN ? THEN 1 ELSE 0 END), 0) AS active, "+
		"COALESCE(SUM(CASE WHEN parse_status IN ? THEN 1 ELSE 0 END), 0) AS building",
		agent24ActiveParseStatuses, agent24BuildingParseStatuses,
	).Scan(&f).Error
	if err != nil || f.Docs == 0 {
		return f, err
	}
	var ids []string
	err = r.db.WithContext(ctx).Model(&types.Chunk{}).
		Where("tenant_id = ? AND knowledge_base_id = ? AND chunk_type = ? AND is_enabled = ?",
			kb.TenantID, kb.ID, types.ChunkTypeText, true).
		Where("knowledge_id IN (?)", docs().Select("id").Where("parse_status IN ?", agent24ActiveParseStatuses)).
		Limit(1).Pluck("id", &ids).Error
	f.HasTextChunks = len(ids) > 0
	return f, err
}
