// Agent24 fork product patch A24-KB-41 (WeKnora#136).
package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

type agent24FakePolicy struct{ err error }

func (p *agent24FakePolicy) AllowIngest(context.Context, *types.KnowledgeBase) error { return p.err }

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
		Status: status, Enrichment: types.Agent24EnrichmentNone, SavedAt: now, UpdatedAt: now,
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

func TestAgent24StageGateWithoutOccupyingJobPasses(t *testing.T) {
	h := newAgent24GateHarness(t)
	h.job(t, types.Agent24JobIndexed, types.Agent24JobOpIngestSubmit)
	h.policy.err = ErrAgent24PolicyDenied // not consulted: no job is in flight
	aborted, _ := agent24StageGateAborts(h.ctx, h.knowledge(t))
	require.False(t, aborted)
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
