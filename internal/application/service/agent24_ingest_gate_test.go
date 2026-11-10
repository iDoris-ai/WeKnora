// Agent24 fork product patch A24-KB-41 (WeKnora#136).
package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type agent24FakePolicy struct {
	err   error
	guard types.ProcessingGuard
}

func (p *agent24FakePolicy) AllowIngest(context.Context, *types.KnowledgeBase) error { return p.err }

func (p *agent24FakePolicy) EmbeddingGuard() types.ProcessingGuard { return p.guard }

func (p *agent24FakePolicy) Allow(context.Context, types.ProcessingDestination) error { return p.err }

type agent24GateHarness struct {
	db     *gorm.DB
	jobs   *repository.Agent24IngestJobStore
	policy *agent24FakePolicy
	ctx    context.Context
}

const agent24GateOrigin = `{"agent24_origin":"upload"}`

func newAgent24GateHarness(t *testing.T) *agent24GateHarness {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	for _, m := range []any{&types.Knowledge{}, &types.KnowledgeBase{}, &types.TenantAPIKey{}} {
		require.NoError(t, db.Migrator().CreateTable(m))
	}
	require.NoError(t, repository.EnsureAgent24IngestJobTable(db))
	require.NoError(t, repository.EnsureAgent24CandidateTables(db))
	h := &agent24GateHarness{db: db, jobs: repository.NewAgent24IngestJobStore(db), policy: &agent24FakePolicy{}}
	prev := agent24Gate.Load()
	t.Cleanup(func() { agent24Gate.Store(prev) })
	SetAgent24StageGate(db, h.jobs, h.policy)
	h.ctx = context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))

	tenant := uint64(7)
	require.NoError(t, db.Create(&types.KnowledgeBase{ID: "kb-1", TenantID: 7, Name: "kb"}).Error)
	require.NoError(t, db.Create(&types.TenantAPIKey{
		ID: 45, TenantID: &tenant, ScopeType: types.APIKeyScopeTenant, Name: "probe", KeyHash: "h45",
		KnowledgeBaseIDs: types.StringArray{"kb-1"}, Capabilities: types.StringArray{"retrieve", "ingest"},
	}).Error)
	require.NoError(t, db.Create(&types.Knowledge{
		ID: "k-1", TenantID: 7, KnowledgeBaseID: "kb-1", Type: "file", ParseStatus: types.ParseStatusPending,
		Metadata: types.JSON(agent24GateOrigin),
	}).Error)
	return h
}

func (h *agent24GateHarness) job(t *testing.T, status types.Agent24JobStatus, op types.Agent24JobOp) string {
	t.Helper()
	now := time.Now().UTC()
	j := &types.Agent24IngestJob{
		ID: "j-1", TenantID: 7, KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", OpClass: op, CredentialID: 45,
		Status: status, Enrichment: types.Agent24EnrichmentNone, Attempt: 1, SavedAt: now, UpdatedAt: now,
	}
	require.NoError(t, h.db.Create(j).Error)
	return j.ID
}

func (h *agent24GateHarness) knowledge(t *testing.T) *types.Knowledge {
	t.Helper()
	var k types.Knowledge
	require.NoError(t, h.db.Where("id = ?", "k-1").Take(&k).Error)
	return &k
}

func (h *agent24GateHarness) jobRow(t *testing.T) *types.Agent24IngestJob {
	t.Helper()
	j, err := h.jobs.Get(h.ctx, 7, "j-1")
	require.NoError(t, err)
	return j
}

func TestAgent24StageGateLeavesUpstreamDocumentsAlone(t *testing.T) {
	prev := agent24Gate.Load()
	t.Cleanup(func() { agent24Gate.Store(prev) })
	agent24Gate.Store(nil)
	for _, meta := range []string{"", `{"external_id":"x"}`, `not json`} {
		k := &types.Knowledge{ID: "u", Metadata: types.JSON(meta)}
		aborted, _ := agent24StageGateAborts(context.Background(), k)
		require.False(t, aborted, meta)
	}
	// an Agent24 ingest in a process without the gate does not run unchecked
	aborted, status := agent24StageGateAborts(context.Background(), &types.Knowledge{
		ID: "a", Metadata: types.JSON(agent24GateOrigin),
	})
	require.True(t, aborted)
	require.Equal(t, abortStatusUnreadable, status)
}

// The origin marker was wiped by an upstream whole-row save. An occupying job is still an Agent24 ingest: a
// revoked authorization cancels it, and a held one passes and records processing.
func TestAgent24StageGateMarkerLostStillChecks(t *testing.T) {
	t.Run("revoked authorization", func(t *testing.T) {
		h := newAgent24GateHarness(t)
		h.job(t, types.Agent24JobProcessing, types.Agent24JobOpIngestSubmit)
		require.NoError(t, h.db.Model(&types.Knowledge{}).Where("id = ?", "k-1").Updates(map[string]any{
			"parse_status": types.ParseStatusProcessing, "metadata": "",
		}).Error)
		require.NoError(t, h.db.Model(&types.TenantAPIKey{}).Where("id = 45").
			Update("revoked_at", time.Now()).Error)
		k := h.knowledge(t)
		require.Empty(t, k.Metadata)
		aborted, status := agent24StageGateAborts(h.ctx, k)
		require.True(t, aborted)
		require.Equal(t, types.ParseStatusCancelled, status)
		j := h.jobRow(t)
		require.Equal(t, types.Agent24JobCancelled, j.Status)
		require.Equal(t, types.Agent24JobCancelRevoked, j.CancelReason)
	})
	t.Run("authorization holds", func(t *testing.T) {
		h := newAgent24GateHarness(t)
		h.job(t, types.Agent24JobSaved, types.Agent24JobOpIngestSubmit)
		require.NoError(t, h.db.Model(&types.Knowledge{}).Where("id = ?", "k-1").Update("metadata", "").Error)
		k := h.knowledge(t)
		require.Empty(t, k.Metadata)
		aborted, _ := agent24StageGateAborts(h.ctx, k)
		require.False(t, aborted)
		j := h.jobRow(t)
		require.Equal(t, types.Agent24JobProcessing, j.Status)
		require.NotNil(t, j.ProcessingAt)
	})
}

// No marker and no occupying job is an upstream document even with the gate installed: nothing is written. A
// failure to read the occupying job retries, the way an unreadable key lookup does.
func TestAgent24StageGateUnmarkedWithoutJobPasses(t *testing.T) {
	h := newAgent24GateHarness(t)
	require.NoError(t, h.db.Model(&types.Knowledge{}).Where("id = ?", "k-1").Update("metadata", "").Error)
	before := *h.knowledge(t)
	require.Empty(t, before.Metadata)
	var jobs int64
	require.NoError(t, h.db.Model(&types.Agent24IngestJob{}).Count(&jobs).Error)
	require.Zero(t, jobs)
	var kbBefore types.KnowledgeBase
	require.NoError(t, h.db.Where("id = ?", "kb-1").Take(&kbBefore).Error)
	var keyBefore types.TenantAPIKey
	require.NoError(t, h.db.Where("id = ?", 45).Take(&keyBefore).Error)

	aborted, status := agent24StageGateAborts(h.ctx, &before)
	require.False(t, aborted)
	require.Empty(t, status)
	require.Equal(t, before, *h.knowledge(t))
	require.NoError(t, h.db.Model(&types.Agent24IngestJob{}).Count(&jobs).Error)
	require.Zero(t, jobs)
	var kbAfter types.KnowledgeBase
	require.NoError(t, h.db.Where("id = ?", "kb-1").Take(&kbAfter).Error)
	require.Equal(t, kbBefore, kbAfter)
	var keyAfter types.TenantAPIKey
	require.NoError(t, h.db.Where("id = ?", 45).Take(&keyAfter).Error)
	require.Equal(t, keyBefore, keyAfter)

	require.NoError(t, h.db.Migrator().DropTable(&types.Agent24IngestJob{}))
	aborted, status = agent24StageGateAborts(h.ctx, h.knowledge(t))
	require.True(t, aborted)
	require.Equal(t, abortStatusUnreadable, status)
	require.Equal(t, before, *h.knowledge(t))
}

func TestAgent24StageGatePassesAndRecordsProcessing(t *testing.T) {
	h := newAgent24GateHarness(t)
	h.job(t, types.Agent24JobSaved, types.Agent24JobOpIngestSubmit)
	aborted, _ := agent24StageGateAborts(h.ctx, h.knowledge(t))
	require.False(t, aborted)
	j := h.jobRow(t)
	require.Equal(t, types.Agent24JobProcessing, j.Status)
	require.NotNil(t, j.ProcessingAt)
	// a later boundary passes again without changing the job
	aborted, _ = agent24StageGateAborts(h.ctx, h.knowledge(t))
	require.False(t, aborted)
	require.Equal(t, types.Agent24JobProcessing, h.jobRow(t).Status)
}

// A run no job covers: a task delivered after its job settled (here: timed out and the row failed), or an
// upstream reparse of an Agent24 ingest, does not run (W5.8).
func TestAgent24StageGateWithoutOccupyingJobStops(t *testing.T) {
	for _, tc := range []struct {
		row, want string
		job       types.Agent24JobStatus
	}{
		{types.ParseStatusFailed, types.ParseStatusFailed, types.Agent24JobFailed},
		{types.ParseStatusPending, types.ParseStatusFailed, types.Agent24JobCancelled},
		{types.ParseStatusProcessing, types.ParseStatusFailed, types.Agent24JobFailed},
	} {
		t.Run(tc.row, func(t *testing.T) {
			h := newAgent24GateHarness(t)
			h.job(t, tc.job, types.Agent24JobOpIngestSubmit)
			require.NoError(t, h.db.Model(&types.Knowledge{}).Where("id = ?", "k-1").
				Update("parse_status", tc.row).Error)
			aborted, status := agent24StageGateAborts(h.ctx, h.knowledge(t))
			require.True(t, aborted)
			require.Equal(t, types.ParseStatusCancelled, status)
			require.Equal(t, tc.want, h.knowledge(t).ParseStatus)
			require.Equal(t, tc.job, h.jobRow(t).Status, "the settled job is not touched")
		})
	}
}

// A build already servable is upstream's at the post-process boundary (a retried post-process finishes its
// enrichment); at the worker entry a redelivered document task stops without rebuilding it.
func TestAgent24StageGateLeavesAnActivatedBuild(t *testing.T) {
	for _, row := range []string{types.ParseStatusFinalizing, types.ParseStatusCompleted} {
		h := newAgent24GateHarness(t)
		h.job(t, types.Agent24JobIndexed, types.Agent24JobOpIngestSubmit)
		require.NoError(t, h.db.Model(&types.Knowledge{}).Where("id = ?", "k-1").
			Update("parse_status", row).Error)
		h.policy.err = ErrAgent24PolicyDenied // not consulted: no job is in flight
		aborted, _ := agent24PostProcessGateAborts(h.ctx, h.knowledge(t))
		require.False(t, aborted, row)
		aborted, status := agent24StageGateAborts(h.ctx, h.knowledge(t))
		require.True(t, aborted, row)
		require.Equal(t, types.ParseStatusCancelled, status)
		require.Equal(t, row, h.knowledge(t).ParseStatus, "nothing is written")
	}
}

// The build is servable before the reconciler moves the job to indexed: a redelivered document task stops at the
// worker entry while the job is still processing; the post-process boundary still runs its checks.
func TestAgent24StageGateEntryStopsAtAnActivatedBuildWithItsJobInFlight(t *testing.T) {
	h := newAgent24GateHarness(t)
	h.job(t, types.Agent24JobProcessing, types.Agent24JobOpIngestSubmit)
	require.NoError(t, h.db.Model(&types.Knowledge{}).Where("id = ?", "k-1").
		Update("parse_status", types.ParseStatusFinalizing).Error)
	aborted, status := agent24StageGateAborts(h.ctx, h.knowledge(t))
	require.True(t, aborted)
	require.Equal(t, types.ParseStatusCancelled, status)
	require.Equal(t, types.Agent24JobProcessing, h.jobRow(t).Status, "nothing is written")
	require.Equal(t, types.ParseStatusFinalizing, h.knowledge(t).ParseStatus)
	aborted, _ = agent24PostProcessGateAborts(h.ctx, h.knowledge(t))
	require.False(t, aborted, "the post-process boundary checks the in-flight job as before")
}

// A worker that meets a saved job whose attempt is not recorded runs an older attempt (the job's task is not
// enqueued yet): it stops, and the job stays saved for its own Dispatch.
func TestAgent24StageGateStaleWorkerLeavesAnUndispatchedJob(t *testing.T) {
	h := newAgent24GateHarness(t)
	h.job(t, types.Agent24JobSaved, types.Agent24JobOpIngestSubmit)
	require.NoError(t, h.db.Model(&types.Agent24IngestJob{}).Where("id = ?", "j-1").Update("attempt", 0).Error)
	aborted, status := agent24StageGateAborts(h.ctx, h.knowledge(t))
	require.True(t, aborted)
	require.Equal(t, types.ParseStatusCancelled, status)
	j := h.jobRow(t)
	require.Equal(t, types.Agent24JobSaved, j.Status)
	require.Equal(t, types.ParseStatusPending, h.knowledge(t).ParseStatus)
}

// An Agent24 ingest's task context carries the policy's embedding guard; without the gate it refuses everything.
func TestAgent24IngestEmbeddingContext(t *testing.T) {
	h := newAgent24GateHarness(t)
	h.policy.guard = &agent24FakePolicy{}
	k := h.knowledge(t)
	require.Nil(t, types.ProcessingEmbeddingGuardFromContext(
		agent24IngestEmbeddingContext(h.ctx, &types.Knowledge{ID: "u"})), "upstream documents get no guard")
	require.Same(t, h.policy.guard, types.ProcessingEmbeddingGuardFromContext(agent24IngestEmbeddingContext(h.ctx, k)))
	require.Nil(t, types.ProcessingGuardFromContext(agent24IngestEmbeddingContext(h.ctx, k)),
		"other model calls of the task see no processing guard")

	// The marker was wiped; an occupying job still installs the policy guard.
	require.NoError(t, h.db.Model(&types.Knowledge{}).Where("id = ?", "k-1").Update("metadata", "").Error)
	h.job(t, types.Agent24JobSaved, types.Agent24JobOpIngestSubmit)
	wiped := h.knowledge(t)
	require.Empty(t, wiped.Metadata)
	require.Same(t, h.policy.guard, types.ProcessingEmbeddingGuardFromContext(
		agent24IngestEmbeddingContext(h.ctx, wiped)))
	// no marker and no occupying job
	require.NoError(t, h.db.Create(&types.Knowledge{
		ID: "k-plain", TenantID: 7, KnowledgeBaseID: "kb-1", Type: "file", ParseStatus: types.ParseStatusPending,
	}).Error)
	var plain types.Knowledge
	require.NoError(t, h.db.Where("id = ?", "k-plain").Take(&plain).Error)
	require.Nil(t, types.ProcessingEmbeddingGuardFromContext(agent24IngestEmbeddingContext(h.ctx, &plain)))
	// the occupying-job read fails closed
	require.NoError(t, h.db.Migrator().DropTable(&types.Agent24IngestJob{}))
	denied := types.ProcessingEmbeddingGuardFromContext(agent24IngestEmbeddingContext(h.ctx, &plain))
	require.ErrorIs(t, denied.Allow(h.ctx, types.ProcessingDestination{}), types.ErrProcessingDenied)

	agent24Gate.Store(nil)
	g := types.ProcessingEmbeddingGuardFromContext(agent24IngestEmbeddingContext(h.ctx, k))
	require.ErrorIs(t, g.Allow(h.ctx, types.ProcessingDestination{}), types.ErrProcessingDenied)
}

// A gate that read an older job does not stop the knowledge of the revision that superseded it.
func TestAgent24StageGateSettleLosesToANewerRevision(t *testing.T) {
	h := newAgent24GateHarness(t)
	h.job(t, types.Agent24JobProcessing, types.Agent24JobOpIngestSubmit)
	stale := h.jobRow(t)
	// J2 staged meanwhile: J1 superseded, the row reset to pending for J2
	require.NoError(t, h.db.Model(&types.Agent24IngestJob{}).Where("id = ?", "j-1").Updates(map[string]any{
		"status": types.Agent24JobCancelled, "cancel_reason": types.Agent24JobCancelSuperseded,
	}).Error)
	g := agent24Gate.Load()
	aborted, status := g.settle(h.ctx, stale, h.knowledge(t), types.Agent24JobChange{
		Status: types.Agent24JobCancelled, CancelReason: types.Agent24JobCancelRevoked,
	}, types.ParseStatusCancelled, "revoked")
	require.True(t, aborted)
	require.Equal(t, abortStatusUnreadable, status, "the task retries and reads the new state")
	require.Equal(t, types.ParseStatusPending, h.knowledge(t).ParseStatus)
	require.Equal(t, types.Agent24JobCancelSuperseded, h.jobRow(t).CancelReason)
}

func TestAgent24StageGateRevokedAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(db *gorm.DB) error
		op     types.Agent24JobOp
	}{
		{"key revoked", func(db *gorm.DB) error {
			return db.Model(&types.TenantAPIKey{}).Where("id = 45").Update("revoked_at", time.Now()).Error
		}, types.Agent24JobOpIngestSubmit},
		{"key deleted", func(db *gorm.DB) error {
			return db.Where("id = 45").Delete(&types.TenantAPIKey{}).Error
		}, types.Agent24JobOpIngestSubmit},
		{"ingest capability removed", func(db *gorm.DB) error {
			return db.Model(&types.TenantAPIKey{}).Where("id = 45").
				Update("capabilities", types.StringArray{"retrieve"}).Error
		}, types.Agent24JobOpIngestSubmit},
		{"knowledge base removed from the key", func(db *gorm.DB) error {
			return db.Model(&types.TenantAPIKey{}).Where("id = 45").
				Update("knowledge_base_ids", types.StringArray{"kb-2"}).Error
		}, types.Agent24JobOpIngestSubmit},
		{"empty allow-list is no write scope", func(db *gorm.DB) error {
			return db.Model(&types.TenantAPIKey{}).Where("id = 45").
				Update("knowledge_base_ids", types.StringArray{}).Error
		}, types.Agent24JobOpIngestSubmit},
		{"full access is no write scope", func(db *gorm.DB) error {
			return db.Model(&types.TenantAPIKey{}).Where("id = 45").Update("full_access", true).Error
		}, types.Agent24JobOpIngestSubmit},
		{"platform key", func(db *gorm.DB) error {
			return db.Model(&types.TenantAPIKey{}).Where("id = 45").
				Update("scope_type", types.APIKeyScopePlatform).Error
		}, types.Agent24JobOpIngestSubmit},
		{
			"candidate.approve is not held until WeKnora#146", func(*gorm.DB) error { return nil },
			types.Agent24JobOpCandidateApprove,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newAgent24GateHarness(t)
			h.job(t, types.Agent24JobProcessing, tc.op)
			require.NoError(t, h.db.Model(&types.Knowledge{}).Where("id = ?", "k-1").
				Update("parse_status", types.ParseStatusProcessing).Error)
			require.NoError(t, tc.mutate(h.db))
			h.policy.err = ErrAgent24PolicyDenied // ① and ② come before ③
			aborted, status := agent24StageGateAborts(h.ctx, h.knowledge(t))
			require.True(t, aborted)
			require.Equal(t, types.ParseStatusCancelled, status)
			j := h.jobRow(t)
			require.Equal(t, types.Agent24JobCancelled, j.Status)
			require.Equal(t, types.Agent24JobCancelRevoked, j.CancelReason)
			require.NotNil(t, j.TerminalAt)
			require.True(t, j.Cost.Final, "nothing more is spent after the cancel (W1.3)")
			require.Equal(t, types.ParseStatusCancelled, h.knowledge(t).ParseStatus)
		})
	}
}

func TestAgent24StageGateExpiredKeyStillHolds(t *testing.T) {
	h := newAgent24GateHarness(t)
	h.job(t, types.Agent24JobSaved, types.Agent24JobOpIngestSubmit)
	require.NoError(t, h.db.Model(&types.TenantAPIKey{}).Where("id = 45").
		Update("expires_at", time.Now().Add(-time.Hour)).Error)
	aborted, _ := agent24StageGateAborts(h.ctx, h.knowledge(t))
	require.False(t, aborted, "expiry is not revocation (W1.3)")
}

func TestAgent24StageGatePolicyDenied(t *testing.T) {
	h := newAgent24GateHarness(t)
	h.job(t, types.Agent24JobProcessing, types.Agent24JobOpIngestSubmit)
	h.policy.err = ErrAgent24PolicyDenied
	aborted, status := agent24StageGateAborts(h.ctx, h.knowledge(t))
	require.True(t, aborted)
	require.Equal(t, types.ParseStatusCancelled, status, "the caller stops as for a settled abort")
	j := h.jobRow(t)
	require.Equal(t, types.Agent24JobFailed, j.Status)
	require.Equal(t, types.Agent24JobFailPolicy, j.FailureReason)
	require.True(t, j.Cost.Final)
	k := h.knowledge(t)
	require.Equal(t, types.ParseStatusFailed, k.ParseStatus)
	require.NotEmpty(t, k.ErrorMessage)
}

func TestAgent24StageGateUnreadableRetries(t *testing.T) {
	h := newAgent24GateHarness(t)
	h.job(t, types.Agent24JobSaved, types.Agent24JobOpIngestSubmit)
	h.policy.err = errors.New("model row unreadable")
	aborted, status := agent24StageGateAborts(h.ctx, h.knowledge(t))
	require.True(t, aborted)
	require.Equal(t, abortStatusUnreadable, status)
	require.Equal(t, types.Agent24JobSaved, h.jobRow(t).Status, "nothing settled on an unanswered check")
	require.Equal(t, types.ParseStatusPending, h.knowledge(t).ParseStatus)

	h.policy.err = nil
	require.NoError(t, h.db.Migrator().DropTable(&types.TenantAPIKey{}))
	aborted, status = agent24StageGateAborts(h.ctx, h.knowledge(t))
	require.True(t, aborted)
	require.Equal(t, abortStatusUnreadable, status, "a key lookup error is not a revocation")
	require.Equal(t, types.Agent24JobSaved, h.jobRow(t).Status)
}

func TestAgent24StageGateDoesNotOverwriteAWinningCancel(t *testing.T) {
	h := newAgent24GateHarness(t)
	h.job(t, types.Agent24JobProcessing, types.Agent24JobOpIngestSubmit)
	require.NoError(t, h.db.Model(&types.Knowledge{}).Where("id = ?", "k-1").
		Update("parse_status", types.ParseStatusCompleted).Error)
	h.policy.err = ErrAgent24PolicyDenied
	aborted, _ := agent24StageGateAborts(h.ctx, h.knowledge(t))
	require.True(t, aborted)
	require.Equal(t, types.ParseStatusCompleted, h.knowledge(t).ParseStatus, "only an in-flight row is moved")
}

// The hook in isKnowledgeAborted: an upstream checkpoint sees the gate's outcome.
func TestAgent24StageGateRunsInsideIsKnowledgeAborted(t *testing.T) {
	h := newAgent24GateHarness(t)
	h.job(t, types.Agent24JobProcessing, types.Agent24JobOpIngestSubmit)
	require.NoError(t, h.db.Model(&types.TenantAPIKey{}).Where("id = 45").Update("revoked_at", time.Now()).Error)
	svc := &knowledgeService{repo: repository.NewKnowledgeRepository(h.db)}
	aborted, status := svc.isKnowledgeAborted(h.ctx, 7, "k-1")
	require.True(t, aborted)
	require.Equal(t, types.ParseStatusCancelled, status)
	require.NoError(t, abortRetryErr(h.ctx, "k-1", status), "a settled abort acks the task")
}

type agent24PostProcessKBs struct {
	reparseFailureKBService
}

func (s *agent24PostProcessKBs) GetKnowledgeBaseByIDOnly(context.Context, string) (*types.KnowledgeBase, error) {
	return s.kb, nil
}

// The hook in the post-process handler: a revoked job's knowledge never reaches finalizing (the build never
// becomes servable), and no enrichment subtask is enqueued.
func TestAgent24StageGateRunsBeforePostProcessFinalizes(t *testing.T) {
	h := newAgent24GateHarness(t)
	h.job(t, types.Agent24JobProcessing, types.Agent24JobOpIngestSubmit)
	require.NoError(t, h.db.Model(&types.Knowledge{}).Where("id = ?", "k-1").
		Update("parse_status", types.ParseStatusProcessing).Error)
	require.NoError(t, h.db.Model(&types.TenantAPIKey{}).Where("id = 45").Update("revoked_at", time.Now()).Error)
	events := []string{}
	svc := &KnowledgePostProcessService{
		knowledgeRepo: repository.NewKnowledgeRepository(h.db),
		kbService: &agent24PostProcessKBs{
			reparseFailureKBService{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 7}},
		},
		taskEnqueuer: &replaceFileEnqueuer{events: &events},
	}
	payload, err := json.Marshal(types.KnowledgePostProcessPayload{
		TenantID: 7, KnowledgeID: "k-1", KnowledgeBaseID: "kb-1",
	})
	require.NoError(t, err)
	require.NoError(t, svc.Handle(h.ctx, asynq.NewTask(types.TypeKnowledgePostProcess, payload)))
	require.Equal(t, types.ParseStatusCancelled, h.knowledge(t).ParseStatus)
	require.Equal(t, types.Agent24JobCancelRevoked, h.jobRow(t).CancelReason)
	require.Empty(t, events, "no subtask enqueued")
}

type agent24ChunkProbe struct {
	interfaces.ChunkRepository
	listed bool
}

var errAgent24ChunkProbe = errors.New("chunk listing reached")

func (c *agent24ChunkProbe) ListChunksByKnowledgeIDAndTypes(
	context.Context, uint64, string, []types.ChunkType,
) ([]*types.Chunk, error) {
	c.listed = true
	return nil, errAgent24ChunkProbe
}

// The post-process hook takes an activated build with no occupying job on to its enrichment (a retried
// post-process); the worker-entry version of the gate would stop it.
func TestAgent24PostProcessGoesOnForAnActivatedBuild(t *testing.T) {
	h := newAgent24GateHarness(t)
	h.job(t, types.Agent24JobIndexed, types.Agent24JobOpIngestSubmit)
	require.NoError(t, h.db.Model(&types.Knowledge{}).Where("id = ?", "k-1").
		Update("parse_status", types.ParseStatusFinalizing).Error)
	chunks := &agent24ChunkProbe{}
	svc := &KnowledgePostProcessService{
		knowledgeRepo: repository.NewKnowledgeRepository(h.db),
		kbService: &agent24PostProcessKBs{
			reparseFailureKBService{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 7}},
		},
		chunkRepo:    chunks,
		taskEnqueuer: &replaceFileEnqueuer{events: &[]string{}},
	}
	payload, err := json.Marshal(types.KnowledgePostProcessPayload{
		TenantID: 7, KnowledgeID: "k-1", KnowledgeBaseID: "kb-1",
	})
	require.NoError(t, err)
	err = svc.Handle(h.ctx, asynq.NewTask(types.TypeKnowledgePostProcess, payload))
	require.ErrorIs(t, err, errAgent24ChunkProbe)
	require.True(t, chunks.listed, "the post-process went past the gate")
	require.Equal(t, types.ParseStatusFinalizing, h.knowledge(t).ParseStatus)
}

type agent24CtxTracker struct {
	noopSpanTracker
	ctx context.Context
}

func (t *agent24CtxTracker) LatestAttempt(ctx context.Context, _ string) int {
	t.ctx = ctx
	return 3 // the task (attempt 2) is superseded, so ProcessDocument stops right after
}

// The hook in ProcessDocument: an Agent24 ingest's task context carries the embedding guard from the start of the
// task; an upstream document's does not.
func TestAgent24ProcessDocumentCarriesTheEmbeddingGuard(t *testing.T) {
	h := newAgent24GateHarness(t)
	h.policy.guard = &agent24FakePolicy{}
	for _, tc := range []struct {
		meta  string
		guard bool
	}{{agent24GateOrigin, true}, {"", false}} {
		k := processingKnowledge()
		k.Metadata = types.JSON(tc.meta)
		tr := &agent24CtxTracker{}
		svc := &knowledgeService{
			repo:        &embedFailureKnowledgeRepo{knowledge: k},
			tenantRepo:  &summaryRefreshTenantRepo{tenant: &types.Tenant{ID: 1}},
			spanTracker: tr,
		}
		require.NoError(t, svc.ProcessDocument(context.Background(), newDocumentProcessTask(t, 2)))
		require.NotNil(t, tr.ctx)
		require.Equal(t, tc.guard, types.ProcessingEmbeddingGuardFromContext(tr.ctx) != nil, tc.meta)
	}
}

// W1.3 ①② for a candidate.approve job: the key still carries kb_candidate_approve and the approver still holds
// an unrevoked administrator grant. Either missing cancels; an unreadable grant table retries.
func TestAgent24StageGateApproveHolds(t *testing.T) {
	const principal = "api_external_user:7:approver"
	ready := func(t *testing.T) *agent24GateHarness {
		t.Helper()
		h := newAgent24GateHarness(t)
		require.NoError(t, h.db.Model(&types.TenantAPIKey{}).Where("id = 45").Update("capabilities",
			types.StringArray{"retrieve", "ingest", "kb_candidate_approve"}).Error)
		h.job(t, types.Agent24JobSaved, types.Agent24JobOpCandidateApprove)
		require.NoError(t, h.db.Model(&types.Agent24IngestJob{}).Where("id = ?", "j-1").
			Update("principal_user_id", principal).Error)
		require.NoError(t, h.db.Create(&types.Agent24KnowledgeAdmin{
			TenantID: 7, Principal: principal, KnowledgeBaseID: "kb-1", CreatedAt: time.Now().UTC(),
		}).Error)
		return h
	}
	t.Run("pass", func(t *testing.T) {
		h := ready(t)
		aborted, status := agent24StageGateAborts(h.ctx, h.knowledge(t))
		require.False(t, aborted)
		require.Empty(t, status)
		j := h.jobRow(t)
		require.Equal(t, types.Agent24JobProcessing, j.Status)
		require.NotNil(t, j.ProcessingAt)
	})
	t.Run("admin revoked", func(t *testing.T) {
		h := ready(t)
		require.NoError(t, h.db.Model(&types.Agent24KnowledgeAdmin{}).
			Where("principal = ?", principal).Update("revoked_at", time.Now().UTC()).Error)
		aborted, status := agent24StageGateAborts(h.ctx, h.knowledge(t))
		require.True(t, aborted)
		require.Equal(t, types.ParseStatusCancelled, status)
		j := h.jobRow(t)
		require.Equal(t, types.Agent24JobCancelled, j.Status)
		require.Equal(t, types.Agent24JobCancelRevoked, j.CancelReason)
	})
	t.Run("capability removed", func(t *testing.T) {
		h := ready(t)
		require.NoError(t, h.db.Model(&types.TenantAPIKey{}).Where("id = 45").
			Update("capabilities", types.StringArray{"retrieve", "ingest"}).Error)
		aborted, status := agent24StageGateAborts(h.ctx, h.knowledge(t))
		require.True(t, aborted)
		require.Equal(t, types.ParseStatusCancelled, status)
		require.Equal(t, types.Agent24JobCancelRevoked, h.jobRow(t).CancelReason)
	})
	t.Run("admin table dropped", func(t *testing.T) {
		h := ready(t)
		require.NoError(t, h.db.Migrator().DropTable(&types.Agent24KnowledgeAdmin{}))
		aborted, status := agent24StageGateAborts(h.ctx, h.knowledge(t))
		require.True(t, aborted)
		require.Equal(t, abortStatusUnreadable, status)
		require.Equal(t, types.Agent24JobSaved, h.jobRow(t).Status)
	})
}

// A withdrawn entry document stops at the next stage boundary (W8.10). A missing withdrawal table is unreadable.
// A document that is not withdrawn goes through.
func TestAgent24StageGateStopsWithdrawnEntry(t *testing.T) {
	t.Run("withdrawn", func(t *testing.T) {
		h := newAgent24GateHarness(t)
		h.job(t, types.Agent24JobProcessing, types.Agent24JobOpIngestSubmit)
		require.NoError(t, h.db.Create(&types.Agent24EntryWithdrawal{
			TenantID: 7, KnowledgeID: "k-1", WithdrawnBy: "x", WithdrawnAt: time.Now().UTC(),
		}).Error)
		aborted, status := agent24StageGateAborts(h.ctx, h.knowledge(t))
		require.True(t, aborted)
		require.Equal(t, types.ParseStatusCancelled, status)
		j := h.jobRow(t)
		require.Equal(t, types.Agent24JobCancelled, j.Status)
		require.Equal(t, types.Agent24JobCancelWithdrawn, j.CancelReason)
	})
	t.Run("unreadable", func(t *testing.T) {
		h := newAgent24GateHarness(t)
		h.job(t, types.Agent24JobProcessing, types.Agent24JobOpIngestSubmit)
		require.NoError(t, h.db.Migrator().DropTable(&types.Agent24EntryWithdrawal{}))
		aborted, status := agent24StageGateAborts(h.ctx, h.knowledge(t))
		require.True(t, aborted)
		require.Equal(t, abortStatusUnreadable, status)
		require.Equal(t, types.Agent24JobProcessing, h.jobRow(t).Status)
	})
	t.Run("unmarked upstream document with unreadable withdrawals", func(t *testing.T) {
		h := newAgent24GateHarness(t)
		require.NoError(t, h.db.Model(&types.Knowledge{}).Where("id = ?", "k-1").Update("metadata", "").Error)
		before := *h.knowledge(t)
		var jobs int64
		require.NoError(t, h.db.Model(&types.Agent24IngestJob{}).Count(&jobs).Error)
		require.Zero(t, jobs)
		require.NoError(t, h.db.Migrator().DropTable(&types.Agent24EntryWithdrawal{}))
		// With no marker or job, an upstream document cannot be distinguished from an entry whose marker
		// was wiped after withdrawal ended its job. A failed withdrawal read must not fail open (F3).
		for _, gate := range []func(context.Context, *types.Knowledge) (bool, string){
			agent24StageGateAborts, agent24PostProcessGateAborts,
		} {
			aborted, status := gate(h.ctx, &before)
			require.True(t, aborted)
			require.Equal(t, abortStatusUnreadable, status)
		}
		ctx := agent24IngestEmbeddingContext(h.ctx, &before)
		guard := types.ProcessingEmbeddingGuardFromContext(ctx)
		require.NotNil(t, guard)
		require.ErrorIs(t, guard.Allow(ctx, types.ProcessingDestination{}), types.ErrProcessingDenied)
		require.Equal(t, before, *h.knowledge(t))
		require.NoError(t, h.db.Model(&types.Agent24IngestJob{}).Count(&jobs).Error)
		require.Zero(t, jobs)
	})
	// the state entry.withdraw leaves (#S4a Codex C1): the job already cancelled/document_withdrawn, so no occupying
	// job; the run stops as cancelled with the withdrawn message, not as an orphan (failed)
	afterWithdraw := func(t *testing.T, status string) *agent24GateHarness {
		t.Helper()
		h := newAgent24GateHarness(t)
		h.job(t, types.Agent24JobProcessing, types.Agent24JobOpCandidateApprove)
		require.NoError(t, h.db.Model(&types.Agent24IngestJob{}).Where("id = ?", "j-1").Updates(map[string]any{
			"status": types.Agent24JobCancelled, "cancel_reason": types.Agent24JobCancelWithdrawn,
		}).Error)
		require.NoError(t, h.db.Model(&types.Knowledge{}).Where("id = ?", "k-1").
			Update("parse_status", status).Error)
		require.NoError(t, h.db.Create(&types.Agent24EntryWithdrawal{
			TenantID: 7, KnowledgeID: "k-1", WithdrawnBy: "x", WithdrawnAt: time.Now().UTC(),
		}).Error)
		return h
	}
	t.Run("withdrawn with no occupying job", func(t *testing.T) {
		h := afterWithdraw(t, types.ParseStatusProcessing)
		aborted, status := agent24StageGateAborts(h.ctx, h.knowledge(t))
		require.True(t, aborted)
		require.Equal(t, types.ParseStatusCancelled, status)
		k := h.knowledge(t)
		require.Equal(t, types.ParseStatusCancelled, k.ParseStatus)
		require.Equal(t, agent24WithdrawnMessage, k.ErrorMessage)
	})
	t.Run("withdrawn with the origin marker wiped", func(t *testing.T) {
		// S4a Codex R2 C1: an upstream whole-row save wiped the marker (G1) and wrote processing back over the
		// cancelled row; the withdrawal alone makes it an Agent24 ingest, so the next boundary stops it again
		h := afterWithdraw(t, types.ParseStatusProcessing)
		require.NoError(t, h.db.Model(&types.Knowledge{}).Where("id = ?", "k-1").
			Update("metadata", types.JSON(`{}`)).Error)
		for _, gate := range []func(context.Context, *types.Knowledge) (bool, string){
			agent24StageGateAborts, agent24PostProcessGateAborts,
		} {
			aborted, status := gate(h.ctx, h.knowledge(t))
			require.True(t, aborted)
			require.Equal(t, types.ParseStatusCancelled, status)
		}
		require.Equal(t, types.ParseStatusCancelled, h.knowledge(t).ParseStatus)
	})
	t.Run("withdrawn build stops before its enrichment", func(t *testing.T) {
		h := afterWithdraw(t, types.ParseStatusFinalizing)
		aborted, status := agent24PostProcessGateAborts(h.ctx, h.knowledge(t))
		require.True(t, aborted)
		require.Equal(t, types.ParseStatusCancelled, status)
		require.Equal(t, types.ParseStatusFinalizing, h.knowledge(t).ParseStatus)
	})
	t.Run("not withdrawn", func(t *testing.T) {
		h := newAgent24GateHarness(t)
		h.job(t, types.Agent24JobSaved, types.Agent24JobOpIngestSubmit)
		aborted, status := agent24StageGateAborts(h.ctx, h.knowledge(t))
		require.False(t, aborted)
		require.Empty(t, status)
		require.Equal(t, types.Agent24JobProcessing, h.jobRow(t).Status)
	})
}
