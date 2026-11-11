package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

// A24-KB-48 (W1.11/A14) hook tests. The fixture is the candidate env (tenant 1: kbA, kbB; tenant 2: kbO); the
// hook runs with the env's key, a recording audit stub and a fixed clock.

func agent24S4CHook(e *agent24CandidateEnv, audit *agent24AuditStub) *agent24KBRemoval {
	r := newAgent24KBRemoval(agent24CandidateTestKey, e.store, audit)
	r.now = func() time.Time { return e.clock.Add(time.Hour) }
	return r
}

// agent24S4CSubmitted puts a submitted candidate of tenant targeting kb; kid set makes it a revise of kid.
func agent24S4CSubmitted(t *testing.T, e *agent24CandidateEnv, tenant uint64, id, kb, kid string) {
	t.Helper()
	c := &types.Agent24KnowledgeCandidate{
		ID: id, TenantID: tenant, Author: "api_external_user:1:dave", TargetKnowledgeBaseID: kb,
		Mode: types.Agent24CandidateCreate, ContentSHA256: strings.Repeat("c", 63) + id[len(id)-1:], Kind: "sop",
		Title: "secret title", Body: "secret body", SessionRef: strings.Repeat("a", 64), SkillVersion: "s",
		RedactionRulesVersion: 1, Status: types.Agent24CandidateSubmitted, SubmittedAt: e.clock,
	}
	if kid != "" {
		c.Mode, c.KnowledgeID, c.BaseRevision = types.Agent24CandidateRevise, kid, "rev-"+id
	}
	require.NoError(t, e.store.CreateCandidate(context.Background(), c))
}

func agent24S4CDetails(t *testing.T, row *types.AuditLog) agent24KBRemovalAuditDetails {
	t.Helper()
	var d agent24KBRemovalAuditDetails
	require.NoError(t, json.Unmarshal(row.Details, &d), string(row.Details))
	return d
}

// The whole hook in one go: every entry document of the base withdrawn (in-flight work ended as entry.withdraw
// ends it), every submitted candidate targeting it returned target_gone, within run on the same transaction,
// and nothing outside the base or tenant touched.
func TestAgent24S4CBeforeRemoval(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	audit := &agent24AuditStub{}
	hook := agent24S4CHook(e, audit)
	ctx := context.Background()

	revE := e.entryDoc(t, "docE", "kbA", types.ParseStatusCompleted, true)
	e.entryDoc(t, "docP", "kbA", types.ParseStatusProcessing, true)
	e.putJob(t, "job-p", "docP", types.Agent24JobProcessing, types.Agent24EnrichmentNone)
	e.entryDoc(t, "docPlain", "kbA", types.ParseStatusCompleted, false) // ordinary document: not withdrawn
	e.entryDoc(t, "docB", "kbB", types.ParseStatusCompleted, true)      // other base: untouched
	agent24S4CSubmitted(t, e, 1, "cand-create1", "kbA", "")
	agent24S4CSubmitted(t, e, 1, "cand-revise2", "kbA", "docE")
	agent24S4CSubmitted(t, e, 1, "cand-otherkb3", "kbB", "")

	var within int
	require.NoError(t, hook.BeforeRemoval(ctx, 1, "kbA", func(tx *gorm.DB) error {
		within++
		// the hook's writes are visible on the transaction within gets
		var n int64
		require.NoError(t, tx.Model(&types.Agent24EntryWithdrawal{}).Count(&n).Error)
		assert.EqualValues(t, 2, n)
		return nil
	}))
	assert.Equal(t, 1, within)

	rows := e.withdrawalRows(t)
	require.Len(t, rows, 2)
	assert.Equal(t, "docE", rows[0].KnowledgeID)
	assert.Equal(t, "docP", rows[1].KnowledgeID)
	assert.Equal(t, revE, rows[0].SourceRevision)
	for _, w := range rows {
		assert.Equal(t, agent24KBRemovalActor, w.WithdrawnBy)
		assert.True(t, e.clock.Add(time.Hour).Equal(w.WithdrawnAt), w.WithdrawnAt)
	}
	// the in-flight work of docP ended as entry.withdraw ends it
	var job types.Agent24IngestJob
	require.NoError(t, e.db.Where("id = ?", "job-p").Take(&job).Error)
	assert.Equal(t, types.Agent24JobCancelled, job.Status)
	assert.Equal(t, types.Agent24JobCancelWithdrawn, job.CancelReason)
	var k types.Knowledge
	require.NoError(t, e.db.Where("id = ?", "docP").Take(&k).Error)
	assert.Equal(t, types.ParseStatusCancelled, k.ParseStatus)

	for _, id := range []string{"cand-create1", "cand-revise2"} {
		c := e.candidate(t, id)
		assert.Equal(t, types.Agent24CandidateReturned, c.Status, id)
		assert.Equal(t, types.Agent24ReturnTargetGone, c.ReturnReason, id)
		assert.Empty(t, c.Note, id)
		assert.Empty(t, c.Approver, id)
		require.NotNil(t, c.TerminalAt, id)
	}
	assert.Equal(t, types.Agent24CandidateSubmitted, e.candidate(t, "cand-otherkb3").Status)
	assert.Equal(t, types.Agent24CandidateApproved, e.candidate(t, "approved-docB").Status)
}

// Q3-C: one row per object the transaction changed, after the commit, reason kb_deleted; never a title, body or
// note.
func TestAgent24S4CBeforeRemovalAudit(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	audit := &agent24AuditStub{}
	hook := agent24S4CHook(e, audit)
	revE := e.entryDoc(t, "docE", "kbA", types.ParseStatusCompleted, true)
	e.entryDoc(t, "docW", "kbA", types.ParseStatusCompleted, true)
	_, err := e.store.Withdraw(context.Background(), &types.Agent24EntryWithdrawal{
		TenantID: 1, KnowledgeID: "docW", WithdrawnBy: "x", WithdrawnAt: e.clock,
	})
	require.NoError(t, err) // already withdrawn: no row for it
	agent24S4CSubmitted(t, e, 1, "cand-revise1", "kbA", "docE")

	require.NoError(t, hook.BeforeRemoval(context.Background(), 1, "kbA", nil))

	require.Len(t, audit.entries, 2)
	byAction := map[types.AuditAction]*types.AuditLog{}
	for _, row := range audit.entries {
		assert.Equal(t, uint64(1), row.TenantID)
		assert.Equal(t, agent24AuditScopeType, row.ScopeType)
		assert.Empty(t, row.ActorUserID)
		assert.Equal(t, types.AuditOutcomeSuccess, row.Outcome)
		assert.NotContains(t, string(row.Details), "secret")
		byAction[row.Action] = row
	}
	w := byAction[agent24AuditActionEntryWithdraw]
	require.NotNil(t, w)
	assert.Equal(t, "knowledge", w.TargetType)
	assert.Equal(t, "docE", w.TargetID)
	wd := agent24S4CDetails(t, w)
	assert.Equal(t, agent24KBRemovalReason, wd.Reason)
	assert.Equal(t, "withdrawn", wd.Outcome)
	require.NotNil(t, wd.Resource)
	assert.Equal(t, "docE", wd.Resource.KnowledgeID)
	assert.Equal(t, "kbA", wd.Resource.TargetKnowledgeBaseID)
	assert.Equal(t, revE, wd.Resource.SourceRevision)

	c := byAction[agent24AuditActionCandidateReturn]
	require.NotNil(t, c)
	assert.Equal(t, "candidate", c.TargetType)
	assert.Equal(t, "cand-revise1", c.TargetID)
	cd := agent24S4CDetails(t, c)
	assert.Equal(t, agent24KBRemovalReason, cd.Reason)
	assert.Equal(t, "returned", cd.Outcome)
	assert.Equal(t, uint64(1), cd.TenantID)
	assert.Equal(t, e.clock.Add(time.Hour).Format(time.RFC3339), cd.OccurredAt)
	require.NotNil(t, cd.Resource)
	assert.Equal(t, "cand-revise1", cd.Resource.CandidateID)
	assert.Equal(t, "kbA", cd.Resource.TargetKnowledgeBaseID)
	assert.Equal(t, string(types.Agent24CandidateRevise), cd.Resource.Mode)
	assert.Equal(t, "docE", cd.Resource.KnowledgeID)
	assert.Equal(t, types.Agent24ReturnTargetGone, cd.Resource.ReturnReason)
	assert.Equal(t, "sop", cd.Resource.Kind)
	assert.NotEmpty(t, cd.Resource.ContentSHA256)

	// a second run changes nothing and writes nothing
	audit.entries = nil
	require.NoError(t, hook.BeforeRemoval(context.Background(), 1, "kbA", nil))
	assert.Empty(t, audit.entries)
	assert.Len(t, e.withdrawalRows(t), 2)
}

// Fail closed: within failing rolls back every write of the hook and writes no audit row; the error comes back.
func TestAgent24S4CBeforeRemovalWithinFailsRollsBack(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	audit := &agent24AuditStub{}
	hook := agent24S4CHook(e, audit)
	e.entryDoc(t, "docE", "kbA", types.ParseStatusProcessing, true)
	e.putJob(t, "job-p", "docE", types.Agent24JobProcessing, types.Agent24EnrichmentNone)
	agent24S4CSubmitted(t, e, 1, "cand-create1", "kbA", "")
	boom := errors.New("soft delete failed")

	err := hook.BeforeRemoval(context.Background(), 1, "kbA", func(*gorm.DB) error { return boom })
	require.ErrorIs(t, err, boom)

	assert.Empty(t, e.withdrawalRows(t))
	assert.Equal(t, types.Agent24CandidateSubmitted, e.candidate(t, "cand-create1").Status)
	var job types.Agent24IngestJob
	require.NoError(t, e.db.Where("id = ?", "job-p").Take(&job).Error)
	assert.Equal(t, types.Agent24JobProcessing, job.Status)
	var k types.Knowledge
	require.NoError(t, e.db.Where("id = ?", "docE").Take(&k).Error)
	assert.Equal(t, types.ParseStatusProcessing, k.ParseStatus)
	assert.Empty(t, audit.entries)
}

// Fail closed without the revision key when there is something to withdraw; with nothing to withdraw the
// candidates are still returned (the key only records a withdrawal's source revision).
func TestAgent24S4CBeforeRemovalWithoutKey(t *testing.T) {
	t.Run("entry documents", func(t *testing.T) {
		e := newAgent24CandidateEnv(t)
		audit := &agent24AuditStub{}
		hook := agent24S4CHook(e, audit)
		hook.key = nil
		e.entryDoc(t, "docE", "kbA", types.ParseStatusCompleted, true)
		agent24S4CSubmitted(t, e, 1, "cand-create1", "kbA", "")
		called := false
		err := hook.BeforeRemoval(context.Background(), 1, "kbA", func(*gorm.DB) error { called = true; return nil })
		require.ErrorIs(t, err, errAgent24KBRemovalNoKey)
		assert.False(t, called)
		assert.Empty(t, e.withdrawalRows(t))
		assert.Equal(t, types.Agent24CandidateSubmitted, e.candidate(t, "cand-create1").Status)
		assert.Empty(t, audit.entries)
	})
	t.Run("nothing of Agent24's (a standalone instance without the key)", func(t *testing.T) {
		// the hook is installed in both modes: a base with no entry documents and no candidates is deleted as
		// before, with no write and no audit row
		e := newAgent24CandidateEnv(t)
		audit := &agent24AuditStub{}
		hook := agent24S4CHook(e, audit)
		hook.key = nil
		e.entryDoc(t, "docPlain", "kbA", types.ParseStatusCompleted, false)
		agent24S4CSubmitted(t, e, 1, "cand-otherkb1", "kbB", "")
		called := false
		mark := func(*gorm.DB) error { called = true; return nil }
		require.NoError(t, hook.BeforeRemoval(context.Background(), 1, "kbA", mark))
		assert.True(t, called)
		assert.Empty(t, e.withdrawalRows(t))
		assert.Equal(t, types.Agent24CandidateSubmitted, e.candidate(t, "cand-otherkb1").Status)
		assert.Empty(t, audit.entries)
		var k types.Knowledge
		require.NoError(t, e.db.Where("id = ?", "docPlain").Take(&k).Error)
		assert.Equal(t, types.ParseStatusCompleted, k.ParseStatus)
	})
	t.Run("no entry documents", func(t *testing.T) {
		e := newAgent24CandidateEnv(t)
		hook := agent24S4CHook(e, &agent24AuditStub{})
		hook.key = nil
		agent24S4CSubmitted(t, e, 1, "cand-create1", "kbA", "")
		require.NoError(t, hook.BeforeRemoval(context.Background(), 1, "kbA", nil))
		assert.Equal(t, types.Agent24CandidateReturned, e.candidate(t, "cand-create1").Status)
	})
}

// A deleting document (the upstream already removing it) is skipped, not withdrawn and not an error; an empty
// base with no candidates still runs within.
func TestAgent24S4CBeforeRemovalSkipsAndEmpty(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	hook := agent24S4CHook(e, &agent24AuditStub{})
	e.entryDoc(t, "docD", "kbA", types.ParseStatusDeleting, true)
	require.NoError(t, hook.BeforeRemoval(context.Background(), 1, "kbA", nil))
	assert.Empty(t, e.withdrawalRows(t))

	called := false
	require.NoError(t, hook.BeforeRemoval(context.Background(), 1, "kbEmpty", func(*gorm.DB) error {
		called = true
		return nil
	}))
	assert.True(t, called)
}

// The tenant is part of every read and write: the same base id in another tenant is not touched.
func TestAgent24S4CBeforeRemovalTenant(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	hook := agent24S4CHook(e, &agent24AuditStub{})
	agent24S4CSubmitted(t, e, 2, "cand-other1", "kbA", "")
	agent24S4CSubmitted(t, e, 1, "cand-mine2", "kbA", "")
	require.NoError(t, hook.BeforeRemoval(context.Background(), 1, "kbA", nil))
	other, err := e.store.GetCandidate(context.Background(), 2, "cand-other1") // e.candidate reads tenant 1 only
	require.NoError(t, err)
	require.NotNil(t, other)
	assert.Equal(t, types.Agent24CandidateSubmitted, other.Status)
	assert.Equal(t, types.Agent24CandidateReturned, e.candidate(t, "cand-mine2").Status)
}

// A delete already committed still gets its audit rows when the caller goes away after the commit: the caller's
// context is cancelled while the first row is written, and the second row is written all the same.
type agent24S4CCtxAudit struct {
	agent24AuditStub
	cancel func()
	errs   []error
}

func (a *agent24S4CCtxAudit) Log(ctx context.Context, entry *types.AuditLog) error {
	a.cancel()
	a.errs = append(a.errs, ctx.Err())
	return a.agent24AuditStub.Log(ctx, entry)
}

func TestAgent24S4CBeforeRemovalAuditOutlivesTheCaller(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	audit := &agent24S4CCtxAudit{cancel: cancel}
	hook := newAgent24KBRemoval(agent24CandidateTestKey, e.store, audit)
	agent24S4CSubmitted(t, e, 1, "cand-create1", "kbA", "")
	agent24S4CSubmitted(t, e, 1, "cand-create2", "kbA", "")
	require.NoError(t, hook.BeforeRemoval(ctx, 1, "kbA", nil))
	require.Len(t, audit.entries, 2)
	assert.Equal(t, []error{nil, nil}, audit.errs)
}
