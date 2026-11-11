package repository

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// ListEntryDocumentsForKnowledgeBase returns the ids of the tenant's knowledge rows that are in kbID now, are entry
// documents (an approved candidate of the tenant is bound to the id, design §1.5) and are not withdrawn
// (agent24_entry_withdrawals), in ascending id order, each id once (W1.11/A14; A24-KB-48).
//
// The base is the knowledge row's knowledge_base_id, not the candidate's target: a document moved to another base
// is listed where it is now. Every parse status is listed, deleting included; the caller locks each row and skips
// what it may not withdraw. Soft-deleted knowledge rows are not listed (GORM's default scope). One query, no
// per-row round trip.
func (s *Agent24CandidateStore) ListEntryDocumentsForKnowledgeBase(
	ctx context.Context, tenantID uint64, kbID string,
) ([]string, error) {
	var ids []string
	// One row of knowledges per id, so two approved candidates bound to the same document list it once.
	// GORM's default scope on Knowledge excludes soft-deleted rows; deleted_at is not written here.
	err := s.db.WithContext(ctx).Model(&types.Knowledge{}).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Where("EXISTS (SELECT 1 FROM agent24_knowledge_candidates AS c "+
			"WHERE c.tenant_id = ? AND c.status = ? AND c.knowledge_id = knowledges.id)",
			tenantID, types.Agent24CandidateApproved).
		Where("NOT EXISTS (SELECT 1 FROM agent24_entry_withdrawals AS w "+
			"WHERE w.tenant_id = ? AND w.knowledge_id = knowledges.id)", tenantID).
		Order("knowledges.id ASC").
		Pluck("knowledges.id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}
