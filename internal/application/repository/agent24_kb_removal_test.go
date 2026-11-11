package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// agent24S4CKnowledge inserts a knowledge row of tenant in kb.
func agent24S4CKnowledge(t *testing.T, s *Agent24CandidateStore, tenant uint64, id, kb, status string) {
	t.Helper()
	require.NoError(t, s.DB().Create(&types.Knowledge{
		ID: id, TenantID: tenant, KnowledgeBaseID: kb, ParseStatus: status, Title: id,
	}).Error)
}

// agent24S4CBind inserts a candidate of tenant with status, targeting target and bound to knowledgeID.
func agent24S4CBind(
	t *testing.T, s *Agent24CandidateStore, tenant uint64, id, target, knowledgeID string,
	status types.Agent24CandidateStatus,
) {
	t.Helper()
	c := agent24TestCandidate(id, "1:alice")
	c.TenantID, c.TargetKnowledgeBaseID, c.KnowledgeID, c.Status = tenant, target, knowledgeID, status
	if knowledgeID != "" {
		c.Mode = types.Agent24CandidateRevise
		c.BaseRevision = "r-" + id
	}
	require.NoError(t, s.DB().Create(c).Error)
}

func TestAgent24S4CListEntryDocumentsForKnowledgeBase(t *testing.T) {
	db, s := agent24CandidateDB(t)
	require.NoError(t, db.AutoMigrate(&types.Knowledge{}))
	ctx := context.Background()

	// entry documents of kb1: approved binding, not withdrawn; any parse status, deleting included
	agent24S4CKnowledge(t, s, 1, "d1", "kb1", types.ParseStatusCompleted)
	agent24S4CBind(t, s, 1, "c1", "kb1", "d1", types.Agent24CandidateApproved)
	agent24S4CKnowledge(t, s, 1, "d2", "kb1", types.ParseStatusDeleting)
	agent24S4CBind(t, s, 1, "c2", "kb1", "d2", types.Agent24CandidateApproved)
	// two approved candidates bound to one document: listed once
	agent24S4CBind(t, s, 1, "c2b", "kb1", "d2", types.Agent24CandidateApproved)
	// moved: approved with target kb1, but the row is in kb2 now -> listed for kb2, not kb1
	agent24S4CKnowledge(t, s, 1, "d3", "kb2", types.ParseStatusCompleted)
	agent24S4CBind(t, s, 1, "c3", "kb1", "d3", types.Agent24CandidateApproved)

	// not entry documents of kb1
	agent24S4CKnowledge(t, s, 1, "plain", "kb1", types.ParseStatusCompleted) // no binding
	agent24S4CKnowledge(t, s, 1, "sub", "kb1", types.ParseStatusCompleted)
	agent24S4CBind(t, s, 1, "c4", "kb1", "sub", types.Agent24CandidateSubmitted) // only submitted
	agent24S4CKnowledge(t, s, 1, "ret", "kb1", types.ParseStatusCompleted)
	agent24S4CBind(t, s, 1, "c5", "kb1", "ret", types.Agent24CandidateReturned) // only returned
	agent24S4CKnowledge(t, s, 1, "wd", "kb1", types.ParseStatusCompleted)
	agent24S4CBind(t, s, 1, "c6", "kb1", "wd", types.Agent24CandidateApproved)
	wrote, err := s.Withdraw(ctx, &types.Agent24EntryWithdrawal{
		TenantID: 1, KnowledgeID: "wd", WithdrawnBy: "x", WithdrawnAt: agent24T0,
	})
	require.NoError(t, err)
	require.True(t, wrote) // withdrawn
	agent24S4CKnowledge(t, s, 1, "gone", "kb1", types.ParseStatusCompleted)
	agent24S4CBind(t, s, 1, "c7", "kb1", "gone", types.Agent24CandidateApproved)
	require.NoError(t, s.DB().Where("id = ?", "gone").Delete(&types.Knowledge{}).Error) // soft-deleted row
	// another tenant: same kb id, approved binding from its own tenant
	agent24S4CKnowledge(t, s, 2, "o1", "kb1", types.ParseStatusCompleted)
	agent24S4CBind(t, s, 2, "c8", "kb1", "o1", types.Agent24CandidateApproved)
	// a binding from another tenant does not make tenant 1's row an entry document
	agent24S4CKnowledge(t, s, 1, "x1", "kb1", types.ParseStatusCompleted)
	agent24S4CBind(t, s, 2, "c9", "kb1", "x1", types.Agent24CandidateApproved)

	ids, err := s.ListEntryDocumentsForKnowledgeBase(ctx, 1, "kb1")
	require.NoError(t, err)
	require.Equal(t, []string{"d1", "d2"}, ids)

	ids, err = s.ListEntryDocumentsForKnowledgeBase(ctx, 1, "kb2")
	require.NoError(t, err)
	require.Equal(t, []string{"d3"}, ids)

	ids, err = s.ListEntryDocumentsForKnowledgeBase(ctx, 2, "kb1")
	require.NoError(t, err)
	require.Equal(t, []string{"o1"}, ids)

	ids, err = s.ListEntryDocumentsForKnowledgeBase(ctx, 1, "kb-empty")
	require.NoError(t, err)
	require.Empty(t, ids)
}
