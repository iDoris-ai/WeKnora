// Agent24 fork product patch A24-KB-41 (KB-C4-04, WeKnora#136, CONTRACT-OPS-v2 W3.3, W3.4, W5.6, W5.7).

package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

// Agent24IngestJobStore reads and writes agent24_ingest_jobs. Writes that belong to a caller's transaction take
// that transaction; the others use the store's own handle.
type Agent24IngestJobStore struct {
	db *gorm.DB
}

// NewAgent24IngestJobStore returns the job store over db.
func NewAgent24IngestJobStore(db *gorm.DB) *Agent24IngestJobStore {
	return &Agent24IngestJobStore{db: db}
}

// agent24IngestJobIndexes are the indexes the table relies on: the gate and reconciliation look jobs up by
// knowledge, the sweep reads by status and age.
var agent24IngestJobIndexes = []string{
	"idx_agent24_ingest_jobs_knowledge",
	"idx_agent24_ingest_jobs_status",
}

// EnsureAgent24IngestJobTable creates agent24_ingest_jobs and its indexes when they are missing, the same way as
// EnsureAgent24CredentialRevocationTable (no AutoMigrate, no numbered migration, re-check after a lost race).
// Both modes need it: the integration process creates jobs and the standalone worker advances them.
func EnsureAgent24IngestJobTable(db *gorm.DB) error {
	m := db.Migrator()
	if !m.HasTable(&types.Agent24IngestJob{}) {
		if err := m.CreateTable(&types.Agent24IngestJob{}); err != nil && !m.HasTable(&types.Agent24IngestJob{}) {
			return err
		}
	}
	for _, name := range agent24IngestJobIndexes {
		if m.HasIndex(&types.Agent24IngestJob{}, name) {
			continue
		}
		if err := m.CreateIndex(&types.Agent24IngestJob{}, name); err != nil &&
			!m.HasIndex(&types.Agent24IngestJob{}, name) {
			return err
		}
	}
	return nil
}

var agent24OccupyingStatuses = []types.Agent24JobStatus{types.Agent24JobSaved, types.Agent24JobProcessing}

// Create inserts job inside tx.
func (s *Agent24IngestJobStore) Create(ctx context.Context, tx *gorm.DB, job *types.Agent24IngestJob) error {
	if tx == nil {
		return errors.New("agent24 ingest job: a transaction is required")
	}
	return tx.WithContext(ctx).Create(job).Error
}

// SupersedeForKnowledge ends, inside tx, the knowledge's earlier work for a new revision (W5.7): occupying jobs
// become cancelled/superseded and an indexed job's running enrichment stops with superseded. The caller has
// locked the knowledge row in the same transaction, so no other revision can slip in between.
func (s *Agent24IngestJobStore) SupersedeForKnowledge(
	ctx context.Context, tx *gorm.DB, tenantID uint64, knowledgeID string, now time.Time,
) error {
	if tx == nil {
		return errors.New("agent24 ingest job: a transaction is required")
	}
	now = now.UTC()
	q := tx.WithContext(ctx).Model(&types.Agent24IngestJob{}).
		Where("tenant_id = ? AND knowledge_id = ?", tenantID, knowledgeID)
	if err := q.Session(&gorm.Session{}).Where("status IN ?", agent24OccupyingStatuses).Updates(map[string]any{
		"status": types.Agent24JobCancelled, "cancel_reason": types.Agent24JobCancelSuperseded,
		"terminal_at": now, "updated_at": now,
	}).Error; err != nil {
		return err
	}
	return q.Session(&gorm.Session{}).
		Where("status = ? AND enrichment = ?", types.Agent24JobIndexed, types.Agent24EnrichmentRunning).
		Updates(map[string]any{
			"enrichment": types.Agent24EnrichmentStopped, "stop_reason": types.Agent24JobCancelSuperseded,
			"updated_at": now,
		}).Error
}

// Get returns the tenant's job id, or nil when there is none.
func (s *Agent24IngestJobStore) Get(ctx context.Context, tenantID uint64, id string) (*types.Agent24IngestJob, error) {
	var job types.Agent24IngestJob
	err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).Take(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// Occupying returns the knowledge's occupying job (saved or processing), or nil. A knowledge without one is not
// an Agent24 ingest in flight: the stage gates let it through unchanged.
func (s *Agent24IngestJobStore) Occupying(
	ctx context.Context, tenantID uint64, knowledgeID string,
) (*types.Agent24IngestJob, error) {
	var job types.Agent24IngestJob
	err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_id = ? AND status IN ?", tenantID, knowledgeID, agent24OccupyingStatuses).
		Order("saved_at DESC").Take(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// Apply writes change to the job when it is still in the state it was read in (status and enrichment), and
// reports whether it did. A job another writer moved first is left as that writer left it.
func (s *Agent24IngestJobStore) Apply(
	ctx context.Context, job types.Agent24IngestJob, change types.Agent24JobChange, now time.Time,
) (bool, error) {
	now = now.UTC()
	set := map[string]any{"status": change.Status, "enrichment": change.Enrichment, "updated_at": now}
	if change.FailureReason != "" {
		set["failure_reason"] = change.FailureReason
	}
	if change.CancelReason != "" {
		set["cancel_reason"] = change.CancelReason
	}
	if change.StopReason != "" {
		set["stop_reason"] = change.StopReason
	}
	switch {
	case change.Status == types.Agent24JobProcessing && job.ProcessingAt == nil:
		set["processing_at"] = now
	case change.Status == types.Agent24JobIndexed && job.IndexedAt == nil:
		set["indexed_at"] = now
	case change.Status.Terminal():
		set["terminal_at"] = now
	}
	res := s.db.WithContext(ctx).Model(&types.Agent24IngestJob{}).
		Where("tenant_id = ? AND id = ? AND status = ? AND enrichment = ?",
			job.TenantID, job.ID, job.Status, job.Enrichment).
		Updates(set)
	return res.RowsAffected == 1, res.Error
}

// SetAttempt records the upstream processing attempt a job's task carries.
func (s *Agent24IngestJobStore) SetAttempt(ctx context.Context, tenantID uint64, id string, attempt int) error {
	return s.db.WithContext(ctx).Model(&types.Agent24IngestJob{}).
		Where("tenant_id = ? AND id = ?", tenantID, id).Update("attempt", attempt).Error
}

// SetIndexBuild records the build a job activated.
func (s *Agent24IngestJobStore) SetIndexBuild(ctx context.Context, tenantID uint64, id, build string) error {
	return s.db.WithContext(ctx).Model(&types.Agent24IngestJob{}).
		Where("tenant_id = ? AND id = ?", tenantID, id).Update("index_build_id", build).Error
}

// SetCost replaces the job's cost report.
func (s *Agent24IngestJobStore) SetCost(
	ctx context.Context, tenantID uint64, id string, cost types.Agent24JobCost,
) error {
	return s.db.WithContext(ctx).Model(&types.Agent24IngestJob{}).
		Where("tenant_id = ? AND id = ?", tenantID, id).Update("cost", cost).Error
}

// ListUnsettled returns up to limit jobs reconciliation may still move: occupying ones and indexed ones whose
// enrichment is running, oldest first.
func (s *Agent24IngestJobStore) ListUnsettled(ctx context.Context, limit int) ([]types.Agent24IngestJob, error) {
	var jobs []types.Agent24IngestJob
	err := s.db.WithContext(ctx).
		Where("status IN ? OR (status = ? AND enrichment = ?)", agent24OccupyingStatuses,
			types.Agent24JobIndexed, types.Agent24EnrichmentRunning).
		Order("updated_at ASC").Limit(limit).Find(&jobs).Error
	return jobs, err
}

// PurgeSettled deletes jobs that ended before before (W3.3: kept at least 90 days after the terminal state):
// failed and cancelled ones by terminal_at, indexed ones whose enrichment is no longer running by updated_at.
func (s *Agent24IngestJobStore) PurgeSettled(ctx context.Context, before time.Time) (int64, error) {
	before = before.UTC()
	res := s.db.WithContext(ctx).
		Where("(status IN ? AND terminal_at < ?) OR (status = ? AND enrichment <> ? AND updated_at < ?)",
			[]types.Agent24JobStatus{types.Agent24JobFailed, types.Agent24JobCancelled}, before,
			types.Agent24JobIndexed, types.Agent24EnrichmentRunning, before).
		Delete(&types.Agent24IngestJob{})
	return res.RowsAffected, res.Error
}

// KnowledgeView reads what reconciliation needs about a job's knowledge: the row's state, the latest upstream
// processing attempt, and which stage of that attempt failed. A missing row is Exists false.
func (s *Agent24IngestJobStore) KnowledgeView(
	ctx context.Context, tenantID uint64, knowledgeID string,
) (types.Agent24KnowledgeView, error) {
	var row struct {
		ParseStatus  string
		ErrorMessage string
	}
	res := s.db.WithContext(ctx).Model(&types.Knowledge{}).Select("parse_status", "error_message").
		Where("tenant_id = ? AND id = ?", tenantID, knowledgeID).Limit(1).Scan(&row)
	if res.Error != nil {
		return types.Agent24KnowledgeView{}, res.Error
	}
	if res.RowsAffected == 0 {
		return types.Agent24KnowledgeView{}, nil
	}
	view := types.Agent24KnowledgeView{Exists: true, ParseStatus: row.ParseStatus}
	if err := s.db.WithContext(ctx).Model(&types.KnowledgeProcessingSpan{}).
		Where("knowledge_id = ?", knowledgeID).Select("COALESCE(MAX(attempt), 0)").
		Row().Scan(&view.LatestAttempt); err != nil {
		return types.Agent24KnowledgeView{}, err
	}
	if row.ParseStatus != types.ParseStatusFailed {
		return view, nil
	}
	var stage string
	if err := s.db.WithContext(ctx).Model(&types.KnowledgeProcessingSpan{}).Select("name").
		Where("knowledge_id = ? AND attempt = ? AND kind = ? AND status = ?",
			knowledgeID, view.LatestAttempt, types.SpanKindStage, types.SpanStatusFailed).
		Order("updated_at DESC").Limit(1).Scan(&stage).Error; err != nil {
		return types.Agent24KnowledgeView{}, err
	}
	view.FailureHint = agent24FailureHint(row.ErrorMessage, stage)
	return view, nil
}

// agent24FailureHint classifies an upstream failure from the row's message (the enqueue and housekeeping
// messages are fixed upstream strings) and the failed stage span.
func agent24FailureHint(message, stage string) types.Agent24FailureHint {
	switch {
	case message == "Failed to enqueue processing task":
		return types.Agent24FailureHintEnqueue
	case strings.Contains(message, "recovered by housekeeping"):
		return types.Agent24FailureHintStalled
	}
	switch stage {
	case types.StageDocReader, types.StageChunking, types.StageMultimodal:
		return types.Agent24FailureHintParse
	case types.StageEmbedding:
		return types.Agent24FailureHintEmbedding
	case types.StagePostProcess:
		return types.Agent24FailureHintIndex
	}
	return types.Agent24FailureHintUnknown
}

// FailPendingKnowledge fails a knowledge row that is still pending: the row of a job that timed out in saved.
// A row a worker has picked up in the meantime is left alone.
func (s *Agent24IngestJobStore) FailPendingKnowledge(
	ctx context.Context, tenantID uint64, knowledgeID, message string, now time.Time,
) error {
	return s.db.WithContext(ctx).Model(&types.Knowledge{}).
		Where("tenant_id = ? AND id = ? AND parse_status = ?", tenantID, knowledgeID, types.ParseStatusPending).
		Updates(map[string]any{
			"parse_status": types.ParseStatusFailed, "error_message": message, "updated_at": now.UTC(),
		}).
		Error
}
