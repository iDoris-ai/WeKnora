package handler

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

// agent24Withdrawer is a UI session whose credential carries entry.withdraw (kb_entry_withdraw) and not review.
func agent24Withdrawer(p types.Principal) agent24CandidateAs {
	return agent24CandidateAs{principal: p, tenant: 1, scope: types.TenantAPIKeyScope{
		KeyID: 13, KnowledgeBaseIDs: types.StringArray{"kbA", "kbB"},
		Capabilities: types.StringArray{"retrieve", string(agent24WriteCapabilities[Agent24WriteEntryWithdraw])},
	}}
}

func agent24WithdrawDoc(t *testing.T, id, requestID string) string {
	t.Helper()
	if requestID == "" {
		requestID = "req-withdraw"
	}
	return agent24ReviewJSON(t, map[string]any{"request_id": requestID, "knowledge_id": id})
}

func agent24SortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (e *agent24CandidateEnv) withdrawalRows(t *testing.T) []types.Agent24EntryWithdrawal {
	t.Helper()
	var rows []types.Agent24EntryWithdrawal
	require.NoError(t, e.db.Order("knowledge_id").Find(&rows).Error)
	return rows
}

func (e *agent24CandidateEnv) putJob(
	t *testing.T, id, kid string, status types.Agent24JobStatus, enrichment string,
) {
	t.Helper()
	job := &types.Agent24IngestJob{
		ID: id, TenantID: 1, KnowledgeBaseID: "kbA", KnowledgeID: kid,
		OpClass: types.Agent24JobOpCandidateApprove, Status: status, Enrichment: enrichment,
		SavedAt: e.clock, UpdatedAt: e.clock,
	}
	if status == types.Agent24JobCancelled {
		job.CancelReason = types.Agent24JobCancelUser
	}
	require.NoError(t, repository.NewAgent24IngestJobStore(e.db).Create(context.Background(), e.db, job))
}

func TestAgent24EntryWithdraw(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	const doc = "docE"
	rev := e.entryDoc(t, doc, "kbA", types.ParseStatusCompleted, true)
	r := agent24CandidateCall(t, e.h.EntryWithdraw, agent24Withdrawer(agent24Alice), agent24WithdrawDoc(t, doc, ""))
	require.Equal(t, 200, r.code, r.raw)
	assert.Equal(t, true, r.body["withdrawn"])
	_, hasDetails := r.body["details"]
	assert.False(t, hasDetails, r.raw)
	agent24AssertNoGeneration(t, r)
	rows := e.withdrawalRows(t)
	require.Len(t, rows, 1)
	assert.Equal(t, agent24Alice.StorageID(), rows[0].WithdrawnBy)
	assert.Equal(t, rev, rows[0].SourceRevision)
	a := r.audit()
	require.NotNil(t, a)
	assert.Equal(t, doc, a.KnowledgeID)
	assert.Equal(t, rev, a.SourceRevision)
	assert.Equal(t, "withdrawn", a.Outcome)
	var k types.Knowledge
	require.NoError(t, e.db.Where("id = ?", doc).Take(&k).Error)
	assert.Equal(t, types.ParseStatusCompleted, k.ParseStatus)
}

func TestAgent24EntryWithdrawAlready(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	e.grantAdmin(t, agent24Bob, "kbA", false)
	const doc = "docE"
	rev := e.entryDoc(t, doc, "kbA", types.ParseStatusCompleted, true)
	alice := agent24Withdrawer(agent24Alice)
	first := agent24CandidateCall(t, e.h.EntryWithdraw, alice, agent24WithdrawDoc(t, doc, "req-1"))
	require.Equal(t, 200, first.code, first.raw)
	require.NoError(t, e.db.Model(&types.Knowledge{}).Where("id = ?", doc).Update("file_hash", "changed").Error)
	again := agent24CandidateCall(t, e.h.EntryWithdraw, alice, agent24WithdrawDoc(t, doc, "req-2"))
	require.Equal(t, 200, again.code, again.raw)
	assert.Equal(t, map[string]any{"already": true}, again.details())
	rows := e.withdrawalRows(t)
	require.Len(t, rows, 1)
	assert.Equal(t, rev, rows[0].SourceRevision)
	assert.Equal(t, rev, again.audit().SourceRevision)
	bobKey := agent24Withdrawer(agent24Bob)
	bob := agent24CandidateCall(t, e.h.EntryWithdraw, bobKey, agent24WithdrawDoc(t, doc, "req-3"))
	require.Equal(t, 200, bob.code, bob.raw)
	assert.Equal(t, map[string]any{"already": true}, bob.details())
	assert.Len(t, e.withdrawalRows(t), 1)
	assert.Equal(t, rev, e.withdrawalRows(t)[0].SourceRevision)
}

func TestAgent24EntryWithdrawOrdinaryDocument(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	e.entryDoc(t, "docP", "kbA", types.ParseStatusCompleted, false)
	r := agent24CandidateCall(t, e.h.EntryWithdraw, agent24Withdrawer(agent24Alice), agent24WithdrawDoc(t, "docP", ""))
	assert.Equal(t, "forbidden/operation_not_allowed", r.outcome(), r.raw)
	assert.Empty(t, e.withdrawalRows(t))
}

func TestAgent24EntryWithdrawNotFound(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	e.entryDoc(t, "docB", "kbB", types.ParseStatusCompleted, true)
	e.entryDoc(t, "docD", "kbA", types.ParseStatusDeleting, true)
	who := agent24Withdrawer(agent24Alice)
	hidden := agent24CandidateCall(t, e.h.EntryWithdraw, who, agent24WithdrawDoc(t, "docB", "same"))
	missing := agent24CandidateCall(t, e.h.EntryWithdraw, who, agent24WithdrawDoc(t, "no-such", "same"))
	assert.Equal(t, "not_found", hidden.outcome(), hidden.raw)
	assert.Equal(t, hidden.raw, missing.raw)
	deleting := agent24CandidateCall(t, e.h.EntryWithdraw, who, agent24WithdrawDoc(t, "docD", "same"))
	assert.Equal(t, "not_found", deleting.outcome(), deleting.raw)
	assert.Empty(t, e.withdrawalRows(t))
}

func TestAgent24EntryWithdrawOrder(t *testing.T) {
	body := func(id string) string {
		return `{"request_id":"req-withdraw","knowledge_id":"` + id + `"}`
	}
	t.Run("disabled", func(t *testing.T) {
		h := newAgent24KnowledgeCandidatesHandler(agent24CandidateTestKey, nil, agent24EveryKB{})
		r := agent24CandidateCall(t, h.EntryWithdraw, agent24Withdrawer(agent24Alice), body("docE"))
		assert.Equal(t, "forbidden/operation_disabled", r.outcome())
	})
	t.Run("no capability", func(t *testing.T) {
		e := newAgent24CandidateEnv(t)
		e.grantAdmin(t, agent24Alice, "kbA", false)
		e.entryDoc(t, "docE", "kbA", types.ParseStatusCompleted, true)
		r := agent24CandidateCall(t, e.h.EntryWithdraw, agent24Approver(agent24Alice), body("docE"))
		assert.Equal(t, "forbidden/operation_not_allowed", r.outcome())
		assert.Empty(t, e.withdrawalRows(t))
	})
	t.Run("no admin", func(t *testing.T) {
		e := newAgent24CandidateEnv(t)
		e.entryDoc(t, "docE", "kbA", types.ParseStatusCompleted, true)
		r := agent24CandidateCall(t, e.h.EntryWithdraw, agent24Withdrawer(agent24Alice), body("docE"))
		assert.Equal(t, "forbidden/operation_not_allowed", r.outcome())
		assert.Empty(t, e.withdrawalRows(t))
	})
	t.Run("generation", func(t *testing.T) {
		e := newAgent24CandidateEnv(t)
		e.grantAdmin(t, agent24Alice, "kbA", false)
		raw := body("docE")
		raw = raw[:len(raw)-1] + `,"generation":1}`
		r := agent24CandidateCall(t, e.h.EntryWithdraw, agent24Withdrawer(agent24Alice), raw)
		assert.Equal(t, "invalid_request", r.outcome(), r.raw)
	})
	t.Run("missing knowledge_id", func(t *testing.T) {
		e := newAgent24CandidateEnv(t)
		e.grantAdmin(t, agent24Alice, "kbA", false)
		r := agent24CandidateCall(t, e.h.EntryWithdraw, agent24Withdrawer(agent24Alice),
			`{"request_id":"req-withdraw"}`)
		assert.Equal(t, "invalid_request", r.outcome(), r.raw)
	})
}

func TestAgent24EntryWithdrawJobs(t *testing.T) {
	t.Run("processing", func(t *testing.T) {
		e := newAgent24CandidateEnv(t)
		e.grantAdmin(t, agent24Alice, "kbA", false)
		const doc = "docP"
		e.entryDoc(t, doc, "kbA", types.ParseStatusCompleted, true)
		e.putJob(t, "job-p", doc, types.Agent24JobProcessing, types.Agent24EnrichmentNone)
		r := agent24CandidateCall(t, e.h.EntryWithdraw, agent24Withdrawer(agent24Alice), agent24WithdrawDoc(t, doc, ""))
		require.Equal(t, 200, r.code, r.raw)
		var job types.Agent24IngestJob
		require.NoError(t, e.db.Where("id = ?", "job-p").Take(&job).Error)
		assert.Equal(t, types.Agent24JobCancelled, job.Status)
		assert.Equal(t, types.Agent24JobCancelWithdrawn, job.CancelReason)
	})
	t.Run("enrichment running", func(t *testing.T) {
		e := newAgent24CandidateEnv(t)
		e.grantAdmin(t, agent24Alice, "kbA", false)
		const doc = "docI"
		e.entryDoc(t, doc, "kbA", types.ParseStatusCompleted, true)
		e.putJob(t, "job-i", doc, types.Agent24JobIndexed, types.Agent24EnrichmentRunning)
		r := agent24CandidateCall(t, e.h.EntryWithdraw, agent24Withdrawer(agent24Alice), agent24WithdrawDoc(t, doc, ""))
		require.Equal(t, 200, r.code, r.raw)
		var job types.Agent24IngestJob
		require.NoError(t, e.db.Where("id = ?", "job-i").Take(&job).Error)
		assert.Equal(t, types.Agent24JobIndexed, job.Status)
		assert.Equal(t, types.Agent24EnrichmentStopped, job.Enrichment)
		assert.Equal(t, types.Agent24JobCancelWithdrawn, job.StopReason)
	})
	t.Run("occupying and running enrichment both end", func(t *testing.T) {
		// W5.7: every in-flight job of the document ends, not only the occupying one
		e := newAgent24CandidateEnv(t)
		e.grantAdmin(t, agent24Alice, "kbA", false)
		const doc = "docB"
		e.entryDoc(t, doc, "kbA", types.ParseStatusCompleted, true)
		e.putJob(t, "job-b-old", doc, types.Agent24JobIndexed, types.Agent24EnrichmentRunning)
		e.putJob(t, "job-b-new", doc, types.Agent24JobSaved, types.Agent24EnrichmentNone)
		r := agent24CandidateCall(t, e.h.EntryWithdraw, agent24Withdrawer(agent24Alice), agent24WithdrawDoc(t, doc, ""))
		require.Equal(t, 200, r.code, r.raw)
		var old, cur types.Agent24IngestJob
		require.NoError(t, e.db.Where("id = ?", "job-b-old").Take(&old).Error)
		require.NoError(t, e.db.Where("id = ?", "job-b-new").Take(&cur).Error)
		assert.Equal(t, types.Agent24EnrichmentStopped, old.Enrichment)
		assert.Equal(t, types.Agent24JobCancelWithdrawn, old.StopReason)
		assert.Equal(t, types.Agent24JobCancelled, cur.Status)
		assert.Equal(t, types.Agent24JobCancelWithdrawn, cur.CancelReason)
	})
	t.Run("terminal", func(t *testing.T) {
		e := newAgent24CandidateEnv(t)
		e.grantAdmin(t, agent24Alice, "kbA", false)
		const doc = "docT"
		e.entryDoc(t, doc, "kbA", types.ParseStatusCompleted, true)
		e.putJob(t, "job-t", doc, types.Agent24JobCancelled, types.Agent24EnrichmentNone)
		r := agent24CandidateCall(t, e.h.EntryWithdraw, agent24Withdrawer(agent24Alice), agent24WithdrawDoc(t, doc, ""))
		require.Equal(t, 200, r.code, r.raw)
		var job types.Agent24IngestJob
		require.NoError(t, e.db.Where("id = ?", "job-t").Take(&job).Error)
		assert.Equal(t, types.Agent24JobCancelled, job.Status)
		assert.Equal(t, types.Agent24JobCancelUser, job.CancelReason)
		assert.Equal(t, types.Agent24EnrichmentNone, job.Enrichment)
		assert.Empty(t, job.StopReason)
	})
}

// An in-flight build does not activate after the withdrawal commits (W8.10; S4a Codex C1/C2): the row is
// cancelled in the same transaction, so a worker that passed its gate before the commit finds both activation
// writes refused.
func TestAgent24EntryWithdrawStopsTheInFlightBuild(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	const doc = "docF"
	e.entryDoc(t, doc, "kbA", types.ParseStatusProcessing, true)
	e.putJob(t, "job-f", doc, types.Agent24JobProcessing, types.Agent24EnrichmentNone)
	r := agent24CandidateCall(t, e.h.EntryWithdraw, agent24Withdrawer(agent24Alice), agent24WithdrawDoc(t, doc, ""))
	require.Equal(t, 200, r.code, r.raw)
	var k types.Knowledge
	require.NoError(t, e.db.Where("id = ?", doc).Take(&k).Error)
	assert.Equal(t, types.ParseStatusCancelled, k.ParseStatus)
	assert.Equal(t, agent24EntryWithdrawnMessage, k.ErrorMessage)
	repo := repository.NewKnowledgeRepository(e.db)
	promoted, err := repo.SetFinalizing(context.Background(), doc, 1)
	require.NoError(t, err)
	assert.False(t, promoted)
	completed, err := repo.CompleteProcessingWithoutSubtasks(context.Background(), doc)
	require.NoError(t, err)
	assert.False(t, completed)
	require.NoError(t, e.db.Where("id = ?", doc).Take(&k).Error)
	assert.Equal(t, types.ParseStatusCancelled, k.ParseStatus)
}

func TestAgent24EntryWithdrawConcurrent(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	e.grantAdmin(t, agent24Bob, "kbA", false)
	const doc = "docE"
	e.entryDoc(t, doc, "kbA", types.ParseStatusCompleted, true)
	body := agent24WithdrawDoc(t, doc, "req-race")
	callers := []agent24CandidateAs{agent24Withdrawer(agent24Alice), agent24Withdrawer(agent24Bob)}
	rs := make([]agent24CandidateReply, len(callers))
	var wg sync.WaitGroup
	wg.Add(len(callers))
	for i := range callers {
		go func(i int) {
			defer wg.Done()
			rs[i] = agent24CandidateCall(t, e.h.EntryWithdraw, callers[i], body)
		}(i)
	}
	wg.Wait()
	fresh, already := 0, 0
	for _, r := range rs {
		require.Equal(t, 200, r.code, r.raw)
		if d := r.details(); d != nil && d["already"] == true {
			already++
		} else {
			fresh++
			assert.Nil(t, r.details())
		}
	}
	assert.Equal(t, 1, fresh)
	assert.Equal(t, 1, already)
	assert.Len(t, e.withdrawalRows(t), 1)
}

func TestAgent24EntryWithdrawSchemaCases(t *testing.T) {
	var f struct {
		Cases []struct {
			Name   string          `json:"name"`
			Schema string          `json:"schema"`
			Valid  bool            `json:"valid"`
			Doc    json.RawMessage `json:"doc"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(agent24OpsV2Fixture(t, "schema-cases.json"), &f))
	e := newAgent24CandidateEnv(t)
	e.grantAdmin(t, agent24Alice, types.Agent24KnowledgeAdminAnyKB, false)
	who := agent24Withdrawer(agent24Alice)
	n := 0
	var alreadyDoc map[string]any
	for _, c := range f.Cases {
		if c.Schema != "entry.withdraw_request" && c.Name != "entry withdraw response already" {
			continue
		}
		if c.Schema == "entry.withdraw_request" {
			n++
			r := agent24CandidateCall(t, e.h.EntryWithdraw, who, string(c.Doc))
			switch c.Name {
			case "entry withdraw request":
				assert.True(t, c.Valid)
				assert.Equal(t, "not_found", r.outcome(), r.raw)
			case "entry withdraw request from the UI session with a generation":
				assert.False(t, c.Valid)
				assert.Equal(t, "invalid_request", r.outcome(), r.raw)
			default:
				t.Errorf("unhandled entry.withdraw_request case %s", c.Name)
			}
			continue
		}
		require.NoError(t, json.Unmarshal(c.Doc, &alreadyDoc))
	}
	assert.Equal(t, 2, n)
	require.NotNil(t, alreadyDoc)

	const doc = "docE"
	rev := e.entryDoc(t, doc, "kbA", types.ParseStatusCompleted, true)
	ok := agent24CandidateCall(t, e.h.EntryWithdraw, who, agent24WithdrawDoc(t, doc, "req_01HZX3"))
	require.Equal(t, 200, ok.code, ok.raw)
	assert.Equal(t, []string{"authz_version", "knowledge_id", "request_id", "withdrawn"}, agent24SortedKeys(ok.body))
	assert.Equal(t, true, ok.body["withdrawn"])
	assert.Equal(t, doc, ok.body["knowledge_id"])
	_, hasDetails := ok.body["details"]
	assert.False(t, hasDetails)
	agent24AssertNoGeneration(t, ok)
	assert.Equal(t, rev, ok.audit().SourceRevision)

	again := agent24CandidateCall(t, e.h.EntryWithdraw, who, agent24WithdrawDoc(t, doc, "req_01HZX3"))
	require.Equal(t, 200, again.code, again.raw)
	assert.Equal(t, agent24SortedKeys(alreadyDoc), agent24SortedKeys(again.body))
	docDetails, _ := alreadyDoc["details"].(map[string]any)
	assert.Equal(t, agent24SortedKeys(docDetails), agent24SortedKeys(again.details()))
	assert.Equal(t, true, again.details()["already"])
	assert.Equal(t, true, again.body["withdrawn"])
	agent24AssertNoGeneration(t, again)
}
