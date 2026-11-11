package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
)

func agent24Approver(p types.Principal) agent24CandidateAs {
	return agent24CandidateAs{principal: p, tenant: 1, scope: types.TenantAPIKeyScope{
		KeyID: 12, KnowledgeBaseIDs: types.StringArray{"kbA", "kbB"},
		Capabilities: types.StringArray{"retrieve", "kb_candidate_approve"},
	}}
}

func agent24ApproveDoc(t *testing.T, id, sha, requestID string) string {
	t.Helper()
	if requestID == "" {
		requestID = "req-approve"
	}
	return agent24ReviewJSON(t, map[string]any{
		"request_id": requestID, "candidate_id": id, "content_sha256": sha,
	})
}

func (e *agent24CandidateEnv) candidate(t *testing.T, id string) *types.Agent24KnowledgeCandidate {
	t.Helper()
	c, err := e.store.GetCandidate(context.Background(), 1, id)
	require.NoError(t, err)
	require.NotNil(t, c)
	return c
}

func (e *agent24CandidateEnv) approveRecords(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, e.db.Model(&types.Agent24WriteIdempotency{}).
		Where("op = ?", string(Agent24WriteCandidateApprove)).Count(&n).Error)
	return n
}

// agent24FakeIngest records Prepare, Abandon and Dispatch, and Stage writes a knowledge row and a job in the
// caller's transaction.
type agent24FakeIngest struct {
	mu                                     sync.Mutex
	db                                     *gorm.DB
	prepares, abandons, stages, dispatches int
	prepareErr, stageErr, dispatchErr      error
	prepared                               map[*service.Agent24IngestPrepared]service.Agent24IngestRequest
	n                                      int
	lastReq                                service.Agent24IngestRequest
	lastTenant                             uint64
	// dispatchHold, when set, blocks every Dispatch until it is closed; dispatchEntered gets one send per call
	dispatchHold, dispatchEntered chan struct{}
}

func (e *agent24CandidateEnv) installApprove(t *testing.T) *agent24FakeIngest {
	t.Helper()
	f := &agent24FakeIngest{
		db: e.db, prepared: map[*service.Agent24IngestPrepared]service.Agent24IngestRequest{},
	}
	e.h.ingest = f
	return f
}

func (f *agent24FakeIngest) Prepare(
	ctx context.Context, req service.Agent24IngestRequest,
) (*service.Agent24IngestPrepared, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepares++
	f.lastReq = req
	f.lastTenant, _ = types.TenantIDFromContext(ctx)
	if f.prepareErr != nil {
		return nil, f.prepareErr
	}
	p := &service.Agent24IngestPrepared{}
	f.prepared[p] = req
	return p, nil
}

func (f *agent24FakeIngest) Abandon(context.Context, *service.Agent24IngestPrepared) {
	f.mu.Lock()
	f.abandons++
	f.mu.Unlock()
}

func (f *agent24FakeIngest) Stage(
	ctx context.Context, tx *gorm.DB, p *service.Agent24IngestPrepared, who service.Agent24IngestPrincipal,
	sourceRevision func(*types.Knowledge) string,
) (*types.Agent24IngestJob, *types.Knowledge, error) {
	f.mu.Lock()
	f.stages++
	req := f.prepared[p]
	err := f.stageErr
	f.n++
	n := f.n
	f.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	tenant := types.CallerFromContext(ctx).TenantID
	var k *types.Knowledge
	if req.KnowledgeID == "" {
		k = &types.Knowledge{
			ID: fmt.Sprintf("k%d", n), TenantID: tenant, KnowledgeBaseID: req.KnowledgeBaseID, Type: "file",
			ParseStatus: types.ParseStatusPending, Title: req.FileName, FileName: req.FileName,
			FileHash: "fh", FilePath: "fp", Source: "src",
		}
		if err := tx.WithContext(ctx).Create(k).Error; err != nil {
			return nil, nil, err
		}
	} else {
		var row types.Knowledge
		err := tx.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenant, req.KnowledgeID).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, service.ErrAgent24KnowledgeNotFound
		}
		if err != nil {
			return nil, nil, err
		}
		// a revision points the knowledge at a new original, so its source revision moves (like stageRevise)
		row.FileHash = fmt.Sprintf("fh-rev-%d", n)
		if err := tx.WithContext(ctx).Model(&types.Knowledge{}).Where("id = ?", row.ID).
			Update("file_hash", row.FileHash).Error; err != nil {
			return nil, nil, err
		}
		k = &row
	}
	now := time.Now().UTC()
	size := int64(len(req.Content))
	job := &types.Agent24IngestJob{
		ID: fmt.Sprintf("job-%d", n), TenantID: tenant, KnowledgeBaseID: req.KnowledgeBaseID, KnowledgeID: k.ID,
		OpClass: who.OpClass, PrincipalUserID: who.UserID, CredentialID: who.CredentialID,
		AuthzVersion: who.AuthzVersion, PolicyVersion: who.PolicyVersion, ConfigVersion: who.ConfigVersion,
		Generation: who.Generation, SourceRevision: sourceRevision(k), ContentSHA256: req.ContentSHA256,
		Status: types.Agent24JobSaved, Enrichment: types.Agent24EnrichmentNone,
		Cost: types.Agent24JobCost{Items: []types.Agent24JobCostItem{
			{Kind: "storage", Units: &size, UnitType: "bytes", Measured: "exact"},
		}},
		SavedAt: now, UpdatedAt: now,
	}
	if err := repository.NewAgent24IngestJobStore(f.db).Create(ctx, tx, job); err != nil {
		return nil, nil, err
	}
	return job, k, nil
}

func (f *agent24FakeIngest) Dispatch(
	context.Context, *types.Agent24IngestJob, *types.Knowledge, *service.Agent24IngestPrepared,
) error {
	f.mu.Lock()
	hold, entered := f.dispatchHold, f.dispatchEntered
	f.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if hold != nil {
		<-hold
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatches++
	return f.dispatchErr
}

func (f *agent24FakeIngest) counts() (prepares, abandons, stages, dispatches int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prepares, f.abandons, f.stages, f.dispatches
}

func TestAgent24CandidateApproveCreate(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	fake := e.installApprove(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	id := e.submit(t, agent24Proposer(agent24Bob), nil)
	sha := e.candidate(t, id).ContentSHA256
	sch := agent24CandidateSchema(t, "approve_response")
	r := agent24CandidateCall(t, e.h.ReviewApprove, agent24Approver(agent24Alice), agent24ApproveDoc(t, id, sha, ""))
	require.Equal(t, http.StatusOK, r.code, r.raw)
	require.NoError(t, agent24Validate(sch, []byte(r.raw)), r.raw)
	agent24AssertNoGeneration(t, r)
	assert.Equal(t, "approved", r.body["status"])
	assert.NotEmpty(t, r.body["job_id"])
	assert.Equal(t, false, r.body["author_is_approver"])
	assert.Nil(t, r.details())

	got := e.candidate(t, id)
	assert.Equal(t, types.Agent24CandidateApproved, got.Status)
	assert.Equal(t, r.body["knowledge_id"], got.KnowledgeID)
	assert.Equal(t, r.body["source_revision"], got.SourceRevision)
	assert.Equal(t, r.body["job_id"], got.JobID)
	assert.Equal(t, agent24Alice.StorageID(), got.Approver)
	assert.False(t, got.AuthorIsApprover)
	var k types.Knowledge
	require.NoError(t, e.db.Where("id = ?", got.KnowledgeID).Take(&k).Error)
	assert.Equal(t, got.Title, k.Title)
	prepares, abandons, _, dispatches := fake.counts()
	assert.Equal(t, []any{1, 0, 1}, []any{prepares, abandons, dispatches})
	assert.EqualValues(t, 1, e.approveRecords(t))
	// the original is the body, named after the candidate, hashed as bytes (not the entry hash), in the tenant
	assert.Equal(t, id+".md", fake.lastReq.FileName)
	assert.Equal(t, got.Body, string(fake.lastReq.Content))
	assert.Equal(t, agent24SHA256Hex(got.Body), fake.lastReq.ContentSHA256)
	assert.NotEqual(t, got.ContentSHA256, fake.lastReq.ContentSHA256)
	assert.Empty(t, fake.lastReq.KnowledgeID)
	assert.Equal(t, "kbA", fake.lastReq.KnowledgeBaseID)
	assert.Equal(t, service.Agent24OriginEntry, fake.lastReq.Origin)
	assert.EqualValues(t, 1, fake.lastTenant)
	// the approval audit names the result document and separation of duties (W8.12, W2.4)
	a := r.audit()
	assert.Equal(t, got.KnowledgeID, a.KnowledgeID)
	require.NotNil(t, a.AuthorIsApprover)
	assert.False(t, *a.AuthorIsApprover)
	assert.Equal(t, "approved", a.Outcome)
	var job types.Agent24IngestJob
	require.NoError(t, e.db.Where("id = ?", r.body["job_id"]).Take(&job).Error)
	assert.Equal(t, types.Agent24JobOpCandidateApprove, job.OpClass)
	assert.Equal(t, agent24Alice.StorageID(), job.PrincipalUserID)
	assert.Equal(t, uint64(12), job.CredentialID)
}

func TestAgent24CandidateApproveAuthorIsApprover(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	e.installApprove(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	id := e.submit(t, agent24Proposer(agent24Alice), nil)
	sha := e.candidate(t, id).ContentSHA256
	r := agent24CandidateCall(t, e.h.ReviewApprove, agent24Approver(agent24Alice), agent24ApproveDoc(t, id, sha, ""))
	require.Equal(t, http.StatusOK, r.code, r.raw)
	assert.Equal(t, true, r.body["author_is_approver"])
	assert.True(t, e.candidate(t, id).AuthorIsApprover)
	require.NotNil(t, r.audit().AuthorIsApprover)
	assert.True(t, *r.audit().AuthorIsApprover)
}

func TestAgent24CandidateApproveReplay(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	fake := e.installApprove(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	e.grantAdmin(t, agent24Bob, "kbA", false)
	id := e.submit(t, agent24Proposer(agent24Bob), nil)
	sha := e.candidate(t, id).ContentSHA256
	doc := agent24ApproveDoc(t, id, sha, "")
	alice := agent24Approver(agent24Alice)
	first := agent24CandidateCall(t, e.h.ReviewApprove, alice, doc)
	require.Equal(t, http.StatusOK, first.code, first.raw)
	again := agent24CandidateCall(t, e.h.ReviewApprove, alice, doc)
	require.Equal(t, http.StatusOK, again.code, again.raw)
	assert.Equal(t, true, again.details()["replayed"])
	assert.Equal(t, first.body["job_id"], again.body["job_id"])
	assert.Equal(t, first.body["knowledge_id"], again.body["knowledge_id"])
	assert.Equal(t, first.body["source_revision"], again.body["source_revision"])
	prepares, _, _, _ := fake.counts()
	assert.Equal(t, 1, prepares)
	reused := agent24CandidateCall(t, e.h.ReviewApprove, alice, agent24ApproveDoc(t, id, strings.Repeat("a", 64), ""))
	assert.Equal(t, "invalid_request/idempotency_key_reused", reused.outcome())
	other := agent24CandidateCall(t, e.h.ReviewApprove, agent24Approver(agent24Bob), doc)
	assert.Equal(t, "stale/candidate_not_submitted", other.outcome())
	prepares, _, _, _ = fake.counts()
	assert.Equal(t, 1, prepares)
}

func TestAgent24CandidateApproveHashMismatch(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	fake := e.installApprove(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	id := e.submit(t, agent24Proposer(agent24Bob), nil)
	r := agent24CandidateCall(t, e.h.ReviewApprove, agent24Approver(agent24Alice),
		agent24ApproveDoc(t, id, strings.Repeat("a", 64), ""))
	assert.Equal(t, "invalid_request/content_hash_mismatch", r.outcome())
	assert.Equal(t, types.Agent24CandidateSubmitted, e.candidate(t, id).Status)
	prepares, _, _, _ := fake.counts()
	assert.Zero(t, prepares)
	assert.Zero(t, e.approveRecords(t))
}

func TestAgent24CandidateApproveUnsupportedFormat(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	fake := e.installApprove(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	id := e.submit(t, agent24Proposer(agent24Bob), nil)
	cand := e.candidate(t, id)
	body := "see <b>tag"
	sum := agent24EntryHash(cand.Kind, cand.Title, body)
	require.NoError(t, e.db.Model(&types.Agent24KnowledgeCandidate{}).Where("id = ?", id).
		Updates(map[string]any{"body": body, "content_sha256": sum}).Error)
	doc := agent24ApproveDoc(t, id, sum, "")
	alice := agent24Approver(agent24Alice)
	r := agent24CandidateCall(t, e.h.ReviewApprove, alice, doc)
	assert.Equal(t, "invalid_request/unsupported_format", r.outcome())
	got := e.candidate(t, id)
	assert.Equal(t, types.Agent24CandidateReturned, got.Status)
	assert.Equal(t, types.Agent24ReturnUnsupportedFormat, got.ReturnReason)
	_, abandons, _, _ := fake.counts()
	assert.Equal(t, 1, abandons)
	again := agent24CandidateCall(t, e.h.ReviewApprove, alice, doc)
	assert.Equal(t, r.raw, again.raw)
	assert.True(t, again.audit().Replayed)
}

func TestAgent24CandidateApproveReviseTarget(t *testing.T) {
	replay := func(t *testing.T, e *agent24CandidateEnv, id, sha, outcome string) {
		t.Helper()
		doc := agent24ApproveDoc(t, id, sha, "")
		alice := agent24Approver(agent24Alice)
		r := agent24CandidateCall(t, e.h.ReviewApprove, alice, doc)
		assert.Equal(t, outcome, r.outcome(), r.raw)
		assert.Equal(t, types.Agent24CandidateReturned, e.candidate(t, id).Status)
		assert.Equal(t, e.candidate(t, id).ReturnReason, r.audit().ReturnReason)
		assert.False(t, r.audit().Replayed)
		again := agent24CandidateCall(t, e.h.ReviewApprove, alice, doc)
		assert.Equal(t, r.raw, again.raw)
		assert.True(t, again.audit().Replayed)
		assert.EqualValues(t, 1, e.approveRecords(t))
	}
	t.Run("withdrawn", func(t *testing.T) {
		e := newAgent24CandidateEnv(t)
		e.installApprove(t)
		e.grantAdmin(t, agent24Alice, "kbA", false)
		const doc = "docW"
		rev := e.entryDoc(t, doc, "kbA", types.ParseStatusCompleted, true)
		id := e.submit(t, agent24Proposer(agent24Bob), func(m map[string]any) {
			m["mode"], m["knowledge_id"], m["expected_source_revision"] = "revise", doc, rev
			m["idempotency_key"] = agent24KeyN(3)
		})
		ok, err := e.store.Withdraw(context.Background(), &types.Agent24EntryWithdrawal{
			TenantID: 1, KnowledgeID: doc, WithdrawnBy: "x", WithdrawnAt: e.clock,
		})
		require.NoError(t, err)
		require.True(t, ok)
		replay(t, e, id, e.candidate(t, id).ContentSHA256, "not_found")
		assert.Equal(t, types.Agent24ReturnTargetWithdrawn, e.candidate(t, id).ReturnReason)
	})
	t.Run("deleted", func(t *testing.T) {
		e := newAgent24CandidateEnv(t)
		e.installApprove(t)
		e.grantAdmin(t, agent24Alice, "kbA", false)
		const doc = "docD"
		rev := e.entryDoc(t, doc, "kbA", types.ParseStatusCompleted, true)
		id := e.submit(t, agent24Proposer(agent24Bob), func(m map[string]any) {
			m["mode"], m["knowledge_id"], m["expected_source_revision"] = "revise", doc, rev
			m["idempotency_key"] = agent24KeyN(4)
		})
		require.NoError(t, e.db.Where("id = ?", doc).Delete(&types.Knowledge{}).Error)
		replay(t, e, id, e.candidate(t, id).ContentSHA256, "not_found")
		assert.Equal(t, types.Agent24ReturnTargetGone, e.candidate(t, id).ReturnReason)
	})
	t.Run("being deleted", func(t *testing.T) {
		e := newAgent24CandidateEnv(t)
		e.installApprove(t)
		e.grantAdmin(t, agent24Alice, "kbA", false)
		const doc = "docX"
		rev := e.entryDoc(t, doc, "kbA", types.ParseStatusCompleted, true)
		id := e.submit(t, agent24Proposer(agent24Bob), func(m map[string]any) {
			m["mode"], m["knowledge_id"], m["expected_source_revision"] = "revise", doc, rev
			m["idempotency_key"] = agent24KeyN(6)
		})
		require.NoError(t, e.db.Model(&types.Knowledge{}).Where("id = ?", doc).
			Update("parse_status", types.ParseStatusDeleting).Error)
		replay(t, e, id, e.candidate(t, id).ContentSHA256, "not_found")
		assert.Equal(t, types.Agent24ReturnTargetGone, e.candidate(t, id).ReturnReason)
	})
	t.Run("base revision", func(t *testing.T) {
		e := newAgent24CandidateEnv(t)
		e.installApprove(t)
		e.grantAdmin(t, agent24Alice, "kbA", false)
		const doc = "docE"
		rev := e.entryDoc(t, doc, "kbA", types.ParseStatusCompleted, true)
		id := e.submit(t, agent24Proposer(agent24Bob), func(m map[string]any) {
			m["mode"], m["knowledge_id"], m["expected_source_revision"] = "revise", doc, rev
			m["idempotency_key"] = agent24KeyN(5)
		})
		require.NoError(t, e.db.Model(&types.Knowledge{}).Where("id = ?", doc).Update("file_hash", "changed").Error)
		var k types.Knowledge
		require.NoError(t, e.db.Where("id = ?", doc).Take(&k).Error)
		current, err := agent24KnowledgeSourceRevision(agent24CandidateTestKey, &k)
		require.NoError(t, err)
		sha := e.candidate(t, id).ContentSHA256
		docBody := agent24ApproveDoc(t, id, sha, "")
		alice := agent24Approver(agent24Alice)
		r := agent24CandidateCall(t, e.h.ReviewApprove, alice, docBody)
		assert.Equal(t, "stale/base_revision_changed", r.outcome(), r.raw)
		assert.Equal(t, current, r.details()["current_revision"])
		again := agent24CandidateCall(t, e.h.ReviewApprove, alice, docBody)
		assert.Equal(t, r.raw, again.raw)
		assert.True(t, again.audit().Replayed)
		got := e.candidate(t, id)
		assert.Equal(t, types.Agent24CandidateReturned, got.Status)
		assert.Equal(t, types.Agent24ReturnConflicted, got.ReturnReason)
		assert.EqualValues(t, 1, e.approveRecords(t))
	})
}

func TestAgent24CandidateApproveDedup(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	fake := e.installApprove(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	id1 := e.submit(t, agent24Proposer(agent24Bob), nil)
	sha := e.candidate(t, id1).ContentSHA256
	first := agent24CandidateCall(
		t, e.h.ReviewApprove, agent24Approver(agent24Alice), agent24ApproveDoc(t, id1, sha, ""))
	require.Equal(t, http.StatusOK, first.code, first.raw)
	// finalizing is an active build too (agent24HasBuild): the main index is served while enrichment runs
	require.NoError(t, e.db.Model(&types.Knowledge{}).Where("id = ?", first.body["knowledge_id"]).
		Update("parse_status", types.ParseStatusFinalizing).Error)
	id2 := e.submit(t, agent24Proposer(agent24Bob), func(m map[string]any) {
		m["idempotency_key"] = agent24KeyN(4)
	})
	assert.Equal(t, sha, e.candidate(t, id2).ContentSHA256)
	_, _, stages, _ := fake.counts()
	r := agent24CandidateCall(
		t, e.h.ReviewApprove, agent24Approver(agent24Alice), agent24ApproveDoc(t, id2, sha, "req-2"))
	require.Equal(t, http.StatusOK, r.code, r.raw)
	require.NoError(t, agent24Validate(agent24CandidateSchema(t, "approve_response"), []byte(r.raw)), r.raw)
	assert.Equal(t, "existing", r.details()["outcome"])
	assert.Equal(t, "indexed", r.details()["document_status"])
	assert.Nil(t, r.body["job_id"])
	_, _, stagesAfter, _ := fake.counts()
	assert.Equal(t, stages, stagesAfter)
	got := e.candidate(t, id2)
	assert.Equal(t, types.Agent24CandidateApproved, got.Status)
	assert.Equal(t, first.body["knowledge_id"], got.KnowledgeID)
	assert.Empty(t, got.JobID)
	assert.Equal(t, agent24Alice.StorageID(), got.Approver)
	assert.Equal(t, first.body["source_revision"], got.SourceRevision)

	// a document whose build is not complete is no dedup hit: the next approval stages its own
	require.NoError(t, e.db.Model(&types.Knowledge{}).Where("id = ?", first.body["knowledge_id"]).
		Update("parse_status", types.ParseStatusProcessing).Error)
	id3 := e.submit(t, agent24Proposer(agent24Bob), func(m map[string]any) {
		m["idempotency_key"] = agent24KeyN(7)
	})
	third := agent24CandidateCall(
		t, e.h.ReviewApprove, agent24Approver(agent24Alice), agent24ApproveDoc(t, id3, sha, "req-3"))
	require.Equal(t, http.StatusOK, third.code, third.raw)
	assert.NotNil(t, third.body["job_id"])
	assert.NotEqual(t, first.body["knowledge_id"], third.body["knowledge_id"])
}

func TestAgent24CandidateApproveOrder(t *testing.T) {
	sha := strings.Repeat("a", 64)
	body := func(id, requestID string) string {
		return `{"request_id":"` + requestID + `","candidate_id":"` + id + `","content_sha256":"` + sha + `"}`
	}
	t.Run("disabled", func(t *testing.T) {
		h := newAgent24KnowledgeCandidatesHandler(agent24CandidateTestKey, nil, agent24EveryKB{})
		r := agent24CandidateCall(t, h.ReviewApprove, agent24Approver(agent24Alice), body("x", "req-approve"))
		assert.Equal(t, "forbidden/operation_disabled", r.outcome())
		e := newAgent24CandidateEnv(t)
		r = agent24CandidateCall(t, e.h.ReviewApprove, agent24Approver(agent24Alice), body("x", "req-approve"))
		assert.Equal(t, "forbidden/operation_disabled", r.outcome())
	})
	t.Run("no capability", func(t *testing.T) {
		e := newAgent24CandidateEnv(t)
		e.installApprove(t)
		e.grantAdmin(t, agent24Alice, "kbA", false)
		id := e.submit(t, agent24Proposer(agent24Bob), nil)
		r := agent24CandidateCall(t, e.h.ReviewApprove, agent24Reviewer(agent24Alice),
			agent24ApproveDoc(t, id, e.candidate(t, id).ContentSHA256, ""))
		assert.Equal(t, "forbidden/operation_not_allowed", r.outcome())
	})
	t.Run("no admin", func(t *testing.T) {
		e := newAgent24CandidateEnv(t)
		e.installApprove(t)
		id := e.submit(t, agent24Proposer(agent24Bob), nil)
		r := agent24CandidateCall(t, e.h.ReviewApprove, agent24Approver(agent24Alice),
			agent24ApproveDoc(t, id, e.candidate(t, id).ContentSHA256, ""))
		assert.Equal(t, "forbidden/operation_not_allowed", r.outcome())
	})
	t.Run("other kb is not_found", func(t *testing.T) {
		e := newAgent24CandidateEnv(t)
		e.installApprove(t)
		e.grantAdmin(t, agent24Alice, "kbA", false)
		idB := e.submit(t, agent24Proposer(agent24Bob), func(m map[string]any) {
			m["target_knowledge_base_id"] = "kbB"
			m["idempotency_key"] = agent24KeyN(6)
		})
		shaB := e.candidate(t, idB).ContentSHA256
		kbB := agent24CandidateCall(t, e.h.ReviewApprove, agent24Approver(agent24Alice),
			agent24ApproveDoc(t, idB, shaB, "req-same"))
		missing := agent24CandidateCall(t, e.h.ReviewApprove, agent24Approver(agent24Alice),
			agent24ApproveDoc(t, "no-such-candidate", shaB, "req-same"))
		assert.Equal(t, "not_found", kbB.outcome())
		assert.Equal(t, kbB.raw, missing.raw)
	})
	t.Run("generation", func(t *testing.T) {
		e := newAgent24CandidateEnv(t)
		e.installApprove(t)
		e.grantAdmin(t, agent24Alice, "kbA", false)
		raw := body("x", "req-approve")
		raw = raw[:len(raw)-1] + `,"generation":1}`
		r := agent24CandidateCall(t, e.h.ReviewApprove, agent24Approver(agent24Alice), raw)
		assert.Equal(t, "invalid_request", r.outcome())
	})
}

func TestAgent24CandidateApprovePrepareAndStageErrors(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	fake := e.installApprove(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	id := e.submit(t, agent24Proposer(agent24Bob), nil)
	sha := e.candidate(t, id).ContentSHA256
	alice := agent24Approver(agent24Alice)
	fake.prepareErr = service.ErrAgent24IngestUnavailable
	r := agent24CandidateCall(t, e.h.ReviewApprove, alice, agent24ApproveDoc(t, id, sha, ""))
	assert.Equal(t, "unavailable/not_ready", r.outcome())
	assert.Equal(t, types.Agent24CandidateSubmitted, e.candidate(t, id).Status)
	fake.prepareErr = nil
	fake.stageErr = errors.New("stage failed")
	r = agent24CandidateCall(t, e.h.ReviewApprove, alice, agent24ApproveDoc(t, id, sha, ""))
	assert.Equal(t, "unavailable/dependency", r.outcome())
	assert.Equal(t, types.Agent24CandidateSubmitted, e.candidate(t, id).Status)
	assert.Zero(t, e.approveRecords(t))
	_, abandons, _, _ := fake.counts()
	assert.Equal(t, 1, abandons)
}

func TestAgent24CandidateApproveDispatchError(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	fake := e.installApprove(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	id := e.submit(t, agent24Proposer(agent24Bob), nil)
	sha := e.candidate(t, id).ContentSHA256
	fake.dispatchErr = errors.New("dispatch failed")
	r := agent24CandidateCall(t, e.h.ReviewApprove, agent24Approver(agent24Alice), agent24ApproveDoc(t, id, sha, ""))
	require.Equal(t, http.StatusOK, r.code, r.raw)
	assert.Equal(t, types.Agent24CandidateApproved, e.candidate(t, id).Status)
	_, abandons, _, dispatches := fake.counts()
	assert.Equal(t, []any{0, 1}, []any{abandons, dispatches})
}

func TestAgent24CandidateApproveConcurrent(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	var mu sync.Mutex
	n := 0
	e.h.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		n++
		return e.clock.Add(time.Duration(n) * time.Second)
	}
	fake := e.installApprove(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	e.grantAdmin(t, agent24Bob, "kbA", false)
	id := e.submit(t, agent24Proposer(agent24Bob), nil)
	doc := agent24ApproveDoc(t, id, e.candidate(t, id).ContentSHA256, "")
	callers := []agent24CandidateAs{agent24Approver(agent24Alice), agent24Approver(agent24Bob)}
	rs := make([]agent24CandidateReply, len(callers))
	var wg sync.WaitGroup
	wg.Add(len(callers))
	for i := range callers {
		go func(i int) {
			defer wg.Done()
			rs[i] = agent24CandidateCall(t, e.h.ReviewApprove, callers[i], doc)
		}(i)
	}
	wg.Wait()
	ok, stale := 0, 0
	for _, r := range rs {
		switch r.outcome() {
		case "ok":
			ok++
		case "stale/candidate_not_submitted":
			stale++
		default:
			t.Errorf("unexpected %s %s", r.outcome(), r.raw)
		}
	}
	assert.Equal(t, 1, ok)
	assert.Equal(t, 1, stale)
	_, _, stages, _ := fake.counts()
	assert.Equal(t, 1, stages)
}

func TestAgent24CandidateApproveSchemaCases(t *testing.T) {
	var f struct {
		Cases []struct {
			Name   string          `json:"name"`
			Schema string          `json:"schema"`
			Valid  bool            `json:"valid"`
			Doc    json.RawMessage `json:"doc"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(agent24OpsV2Fixture(t, "schema-cases.json"), &f))
	sch := agent24CandidateSchema(t, "approve_request")
	e := newAgent24CandidateEnv(t)
	e.installApprove(t)
	e.grantAdmin(t, agent24Alice, types.Agent24KnowledgeAdminAnyKB, false)
	alice := agent24Approver(agent24Alice)
	n := 0
	for _, c := range f.Cases {
		if c.Schema != "candidate.approve_request" {
			continue
		}
		n++
		require.Equal(t, c.Valid, agent24Validate(sch, c.Doc) == nil, "fixture verdict %s", c.Name)
		r := agent24CandidateCall(t, e.h.ReviewApprove, alice, string(c.Doc))
		switch c.Name {
		case "approve request", "approve request with approved flag":
			assert.Equal(t, "not_found", r.outcome(), c.Name)
		default:
			assert.Equal(t, "invalid_request", r.outcome(), "%s: %s", c.Name, r.raw)
		}
	}
	assert.Equal(t, 6, n)
}

func TestAgent24CandidateApproveAudit(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	e.installApprove(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	id := e.submit(t, agent24Proposer(agent24Bob), nil)
	sha := e.candidate(t, id).ContentSHA256
	r := agent24CandidateCall(t, e.h.ReviewApprove, agent24Approver(agent24Alice), agent24ApproveDoc(t, id, sha, ""))
	require.Equal(t, http.StatusOK, r.code, r.raw)
	raw, err := json.Marshal(r.audit())
	require.NoError(t, err)
	text := string(raw)
	assert.NotContains(t, text, "请假流程")
	assert.NotContains(t, text, "直属主管")
	assert.NotContains(t, text, "title")
	assert.NotContains(t, text, "body")
}

// W3.5 for a revision: the same content already published as another document is not this document, so the
// revision stages into its own target instead of binding to the other one.
func TestAgent24CandidateApproveReviseDoesNotBindAnotherDocument(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	fake := e.installApprove(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	alice := agent24Approver(agent24Alice)
	id1 := e.submit(t, agent24Proposer(agent24Bob), nil)
	sha := e.candidate(t, id1).ContentSHA256
	first := agent24CandidateCall(t, e.h.ReviewApprove, alice, agent24ApproveDoc(t, id1, sha, ""))
	require.Equal(t, http.StatusOK, first.code, first.raw)
	require.NoError(t, e.db.Model(&types.Knowledge{}).Where("id = ?", first.body["knowledge_id"]).
		Update("parse_status", types.ParseStatusCompleted).Error)
	const doc = "docR"
	rev := e.entryDoc(t, doc, "kbA", types.ParseStatusCompleted, true)
	id2 := e.submit(t, agent24Proposer(agent24Bob), func(m map[string]any) {
		m["mode"], m["knowledge_id"], m["expected_source_revision"] = "revise", doc, rev
		m["idempotency_key"] = agent24KeyN(5)
	})
	require.Equal(t, sha, e.candidate(t, id2).ContentSHA256)
	r := agent24CandidateCall(t, e.h.ReviewApprove, alice, agent24ApproveDoc(t, id2, sha, "req-r"))
	require.Equal(t, http.StatusOK, r.code, r.raw)
	assert.Equal(t, doc, r.body["knowledge_id"])
	assert.NotNil(t, r.body["job_id"])
	assert.Nil(t, r.details())
	_, _, stages, _ := fake.counts()
	assert.Equal(t, 2, stages)
	assert.Equal(t, doc, fake.lastReq.KnowledgeID)
}

// W8.5 ⑤2: a stored body that no longer hashes to its content_sha256 is internal; the candidate is unchanged,
// nothing is recorded, and the audit says why.
func TestAgent24CandidateApproveEntryHashRecomputeFails(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	fake := e.installApprove(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	id := e.submit(t, agent24Proposer(agent24Bob), nil)
	sha := e.candidate(t, id).ContentSHA256
	require.NoError(t, e.db.Model(&types.Agent24KnowledgeCandidate{}).Where("id = ?", id).
		Update("body", "tampered").Error)
	r := agent24CandidateCall(t, e.h.ReviewApprove, agent24Approver(agent24Alice), agent24ApproveDoc(t, id, sha, ""))
	assert.Equal(t, "internal", r.outcome(), r.raw)
	assert.Equal(t, "content_hash_recompute_failed", r.audit().Outcome)
	assert.Equal(t, types.Agent24CandidateSubmitted, e.candidate(t, id).Status)
	assert.Zero(t, e.approveRecords(t))
	_, abandons, stages, _ := fake.counts()
	assert.Equal(t, []int{1, 0}, []int{abandons, stages})
}

// design §3.3 step 1: approvals of one knowledge are serialised until Dispatch returns, so a second revision
// does not start (not even Prepare) while the first is between its commit and its dispatch; it then finds the
// base revision moved and is returned as conflicted (W8.7).
func TestAgent24CandidateApproveRevisionsSerialiseUntilDispatch(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	fake := e.installApprove(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	const doc = "docS"
	rev := e.entryDoc(t, doc, "kbA", types.ParseStatusCompleted, true)
	ids := make([]string, 2)
	for i := range ids {
		ids[i] = e.submit(t, agent24Proposer(agent24Bob), func(m map[string]any) {
			m["mode"], m["knowledge_id"], m["expected_source_revision"] = "revise", doc, rev
			m["idempotency_key"] = agent24KeyN(10 + i)
			m["body"] = fmt.Sprintf("修订 %d", i)
		})
	}
	hold, entered := make(chan struct{}), make(chan struct{}, 2)
	fake.mu.Lock()
	fake.dispatchHold, fake.dispatchEntered = hold, entered
	fake.mu.Unlock()
	alice := agent24Approver(agent24Alice)
	replies := make([]agent24CandidateReply, 2)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		replies[0] = agent24CandidateCall(t, e.h.ReviewApprove, alice,
			agent24ApproveDoc(t, ids[0], e.candidate(t, ids[0]).ContentSHA256, "r-0"))
	}()
	<-entered // the first approval committed and is inside Dispatch, holding the knowledge's mutex
	wg.Add(1)
	go func() {
		defer wg.Done()
		replies[1] = agent24CandidateCall(t, e.h.ReviewApprove, alice,
			agent24ApproveDoc(t, ids[1], e.candidate(t, ids[1]).ContentSHA256, "r-1"))
	}()
	time.Sleep(200 * time.Millisecond)
	prepares, _, _, _ := fake.counts()
	assert.Equal(t, 1, prepares, "the second revision waits for the first one's Dispatch")
	close(hold)
	wg.Wait()
	require.Equal(t, http.StatusOK, replies[0].code, replies[0].raw)
	assert.Equal(t, "stale/base_revision_changed", replies[1].outcome(), replies[1].raw)
	assert.Equal(t, types.Agent24ReturnConflicted, e.candidate(t, ids[1]).ReturnReason)
}

// TestAgent24CandidateApproveNeedsTheApproveGrantOnTarget guards #162 N1. A target inside E needs the credential
// to hold candidate.approve, not merely candidate.review. Changing adminVisible's op to review turns the first
// call red: the key carries approve and not review.
func TestAgent24CandidateApproveNeedsTheApproveGrantOnTarget(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	e.installApprove(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	id := e.submit(t, agent24Proposer(agent24Bob), nil)
	sha := e.candidate(t, id).ContentSHA256
	approveOnly := agent24Approver(agent24Alice)
	r := agent24CandidateCall(t, e.h.ReviewApprove, approveOnly, agent24ApproveDoc(t, id, sha, ""))
	require.Equal(t, http.StatusOK, r.code, r.raw)

	id2 := e.submit(t, agent24Proposer(agent24Bob), func(m map[string]any) {
		m["idempotency_key"] = agent24KeyN(2)
	})
	reviewOnly := agent24Reviewer(agent24Alice)
	r = agent24CandidateCall(t, e.h.ReviewApprove, reviewOnly, agent24ApproveDoc(t, id2, sha, "req-review-only"))
	assert.Equal(t, "forbidden/operation_not_allowed", r.outcome(), r.raw)
}

// TestAgent24CandidateApproveDedupSkips: a published document with an active build is not a W3.5 hit once it is
// withdrawn, or once its source revision has moved.
func TestAgent24CandidateApproveDedupSkips(t *testing.T) {
	publish := func(t *testing.T) (*agent24CandidateEnv, string, string) {
		t.Helper()
		e := newAgent24CandidateEnv(t)
		e.installApprove(t)
		e.grantAdmin(t, agent24Alice, "kbA", false)
		id := e.submit(t, agent24Proposer(agent24Bob), nil)
		sha := e.candidate(t, id).ContentSHA256
		first := agent24CandidateCall(
			t, e.h.ReviewApprove, agent24Approver(agent24Alice), agent24ApproveDoc(t, id, sha, ""))
		require.Equal(t, http.StatusOK, first.code, first.raw)
		kid, _ := first.body["knowledge_id"].(string)
		require.NoError(t, e.db.Model(&types.Knowledge{}).Where("id = ?", kid).
			Update("parse_status", types.ParseStatusCompleted).Error)
		return e, sha, kid
	}
	t.Run("withdrawn", func(t *testing.T) {
		e, sha, kid := publish(t)
		ok, err := e.store.Withdraw(context.Background(), &types.Agent24EntryWithdrawal{
			TenantID: 1, KnowledgeID: kid, WithdrawnBy: "x", WithdrawnAt: e.clock,
		})
		require.NoError(t, err)
		require.True(t, ok)
		id := e.submit(t, agent24Proposer(agent24Bob), func(m map[string]any) {
			m["idempotency_key"] = agent24KeyN(4)
		})
		r := agent24CandidateCall(
			t, e.h.ReviewApprove, agent24Approver(agent24Alice), agent24ApproveDoc(t, id, sha, "req-2"))
		require.Equal(t, http.StatusOK, r.code, r.raw)
		assert.NotEmpty(t, r.body["job_id"])
		assert.NotEqual(t, kid, r.body["knowledge_id"])
	})
	t.Run("source revision moved", func(t *testing.T) {
		e, sha, kid := publish(t)
		require.NoError(t, e.db.Model(&types.Knowledge{}).Where("id = ?", kid).Update("file_hash", "changed").Error)
		id := e.submit(t, agent24Proposer(agent24Bob), func(m map[string]any) {
			m["idempotency_key"] = agent24KeyN(5)
		})
		r := agent24CandidateCall(
			t, e.h.ReviewApprove, agent24Approver(agent24Alice), agent24ApproveDoc(t, id, sha, "req-2"))
		require.Equal(t, http.StatusOK, r.code, r.raw)
		assert.NotEmpty(t, r.body["job_id"])
		assert.NotEqual(t, kid, r.body["knowledge_id"])
	})
}
