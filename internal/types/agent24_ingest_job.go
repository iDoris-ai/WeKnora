// Agent24 fork product patch A24-KB-41 (KB-C4-04, WeKnora#136, CONTRACT-OPS-v2 W1.3, W3.3, W5.6–W5.10, §6):
// the job record of an Agent24 ingest and the reconciliation of that record with the upstream knowledge row.
//
// The upstream pipeline keeps its own state on the knowledge row (parse_status); the job is the contract's view of
// one accepted submission. The stage gates of the standalone worker write most transitions; Agent24ReconcileJob
// derives the rest (upstream failures, deletion, stalls) from the row, so a job never stays in a non-terminal
// state after its knowledge has moved on.

package types

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"
)

// Agent24JobOp is the operation class that established a job (CONTRACT-OPS-v2 §2). The stage gates recheck that
// the job's principal still holds it (W1.3 ①).
type Agent24JobOp string

// The operation classes that can establish an Agent24 ingest job.
const (
	// Agent24JobOpIngestSubmit is a direct ingest. No integration route serves it in M3; the ingest probe and
	// the tests use it, under a key that carries the upstream ingest capability.
	Agent24JobOpIngestSubmit Agent24JobOp = "ingest.submit"
	// Agent24JobOpCandidateApprove is the ingest that publishes an approved knowledge-entry candidate (WeKnora#146).
	Agent24JobOpCandidateApprove Agent24JobOp = "candidate.approve"
)

// Agent24JobStatus is the closed job state set of CONTRACT-OPS-v2 §6.
type Agent24JobStatus string

// Job states. saved and processing are occupying: they hold the knowledge's processing slot.
const (
	Agent24JobSaved      Agent24JobStatus = "saved"
	Agent24JobProcessing Agent24JobStatus = "processing"
	Agent24JobIndexed    Agent24JobStatus = "indexed"
	Agent24JobFailed     Agent24JobStatus = "failed"
	Agent24JobCancelled  Agent24JobStatus = "cancelled"
)

// Occupying reports whether s still holds the knowledge's processing slot.
func (s Agent24JobStatus) Occupying() bool { return s == Agent24JobSaved || s == Agent24JobProcessing }

// Terminal reports whether s never changes again (indexed changes only its enrichment, W5.6).
func (s Agent24JobStatus) Terminal() bool { return s == Agent24JobFailed || s == Agent24JobCancelled }

// The failure_reason values of CONTRACT-OPS-v2 §6.
const (
	Agent24JobFailParse     = "parse_failed"
	Agent24JobFailEmbedding = "embedding_failed"
	Agent24JobFailIndex     = "index_failed"
	Agent24JobFailPolicy    = "policy_denied"
	Agent24JobFailBudget    = "budget_exhausted"
	Agent24JobFailStorage   = "storage_failed"
	Agent24JobFailEnqueue   = "enqueue_failed"
	Agent24JobFailInterrupt = "interrupted"
	Agent24JobFailTimeout   = "timeout"
	Agent24JobFailInternal  = "internal"
)

// The cancel_reason values of CONTRACT-OPS-v2 §6.
const (
	Agent24JobCancelUser       = "user"
	Agent24JobCancelRevoked    = "authorization_revoked"
	Agent24JobCancelSuperseded = "superseded"
	Agent24JobCancelDeleted    = "document_deleted"
	Agent24JobCancelWithdrawn  = "document_withdrawn"
)

// The enrichment values of CONTRACT-OPS-v2 §6; stop_reason reuses the cancel and failure names listed there.
const (
	Agent24EnrichmentNone    = "none"
	Agent24EnrichmentRunning = "running"
	Agent24EnrichmentDone    = "done"
	Agent24EnrichmentStopped = "stopped"
	// Agent24StopFailed is the stop_reason of an enrichment whose subtasks failed.
	Agent24StopFailed = "failed"
)

// Agent24JobCost is the W5.10 cost report. Units is nil when unknown; it is never 0 for "unknown".
type Agent24JobCost struct {
	Final bool                 `json:"final"`
	Items []Agent24JobCostItem `json:"items"`
}

// Agent24JobCostItem is one cost line of W5.10.
type Agent24JobCostItem struct {
	Kind        string `json:"kind"`
	Destination string `json:"destination"`
	Units       *int64 `json:"units"`
	UnitType    string `json:"unit_type"`
	Measured    string `json:"measured"`
}

// Value stores the cost as JSON.
func (c Agent24JobCost) Value() (driver.Value, error) { return json.Marshal(c) }

// Scan reads the cost from JSON.
func (c *Agent24JobCost) Scan(value any) error {
	var b []byte
	switch v := value.(type) {
	case nil:
		*c = Agent24JobCost{}
		return nil
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return errors.New("agent24 job cost: unsupported column type")
	}
	return json.Unmarshal(b, c)
}

// Agent24IngestJob is one row of agent24_ingest_jobs.
type Agent24IngestJob struct {
	ID              string           `gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64           `gorm:"not null;index:idx_agent24_ingest_jobs_knowledge,priority:1"`
	KnowledgeBaseID string           `gorm:"type:varchar(64);not null"`
	KnowledgeID     string           `gorm:"type:varchar(64);not null;index:idx_agent24_ingest_jobs_knowledge,priority:2"` //nolint:lll // tag
	OpClass         Agent24JobOp     `gorm:"type:varchar(32);not null"`
	PrincipalUserID string           `gorm:"type:varchar(64);not null;default:''"`
	CredentialID    uint64           `gorm:"not null;default:0"`
	AuthzVersion    string           `gorm:"type:varchar(128);not null;default:''"`
	PolicyVersion   string           `gorm:"type:varchar(128);not null;default:''"`
	ConfigVersion   string           `gorm:"type:varchar(128);not null;default:''"`
	Generation      *int64           //
	SourceRevision  string           `gorm:"type:varchar(128);not null;default:''"`
	ContentSHA256   string           `gorm:"column:content_sha256;type:varchar(64);not null;default:''"`
	FileType        string           `gorm:"type:varchar(32);not null;default:''"`
	Attempt         int              `gorm:"not null;default:0"`
	Status          Agent24JobStatus `gorm:"type:varchar(16);not null;index:idx_agent24_ingest_jobs_status,priority:1"`
	FailureReason   string           `gorm:"type:varchar(32);not null;default:''"`
	CancelReason    string           `gorm:"type:varchar(32);not null;default:''"`
	Enrichment      string           `gorm:"type:varchar(16);not null;default:'none'"`
	StopReason      string           `gorm:"type:varchar(32);not null;default:''"`
	IndexBuildID    string           `gorm:"type:varchar(128);not null;default:''"`
	Cost            Agent24JobCost   `gorm:"type:text"`
	SavedAt         time.Time        `gorm:"not null"`
	ProcessingAt    *time.Time       //
	IndexedAt       *time.Time       //
	TerminalAt      *time.Time       //
	UpdatedAt       time.Time        `gorm:"not null;index:idx_agent24_ingest_jobs_status,priority:2"`
}

// TableName pins the fork-owned table name.
func (Agent24IngestJob) TableName() string { return "agent24_ingest_jobs" }

// Agent24FailureHint is what the knowledge row says about an upstream failure that did not go through a gate.
// The repository derives it from the last failed processing span (its stage) and the row's error message.
type Agent24FailureHint string

// Failure hints, from the upstream stage that failed.
const (
	Agent24FailureHintUnknown   Agent24FailureHint = ""
	Agent24FailureHintParse     Agent24FailureHint = "parse"
	Agent24FailureHintEmbedding Agent24FailureHint = "embedding"
	Agent24FailureHintIndex     Agent24FailureHint = "index"
	Agent24FailureHintEnqueue   Agent24FailureHint = "enqueue"
	Agent24FailureHintStalled   Agent24FailureHint = "stalled"
	Agent24FailureHintStorage   Agent24FailureHint = "storage"
)

// Agent24KnowledgeView is the part of the upstream knowledge row that reconciliation reads.
type Agent24KnowledgeView struct {
	Exists        bool
	ParseStatus   string
	LatestAttempt int
	FailureHint   Agent24FailureHint
}

// Agent24ReconcileLimits are the W5.7 time limits.
type Agent24ReconcileLimits struct {
	Saved      time.Duration // a job still saved after this fails with timeout ("含停在 saved")
	Enrichment time.Duration // enrichment still running this long after indexed stops with timeout
}

// Agent24JobChange is the outcome of reconciling one job: the fields to write, or none.
type Agent24JobChange struct {
	Status        Agent24JobStatus
	FailureReason string
	CancelReason  string
	Enrichment    string
	StopReason    string
	// FailKnowledge asks the caller to mark the knowledge row failed too (a job that timed out in saved has a
	// pending row that nothing will pick up).
	FailKnowledge bool
}

// Agent24ReconcileJob derives the job's next state from the knowledge row. ok is false when nothing changes.
// It never moves a terminal job, and an indexed job changes only its enrichment (W5.6).
func Agent24ReconcileJob(
	job Agent24IngestJob, k Agent24KnowledgeView, now time.Time, lim Agent24ReconcileLimits,
) (change Agent24JobChange, ok bool) {
	if job.Status.Terminal() {
		return Agent24JobChange{}, false
	}
	if job.Status == Agent24JobIndexed {
		return agent24ReconcileEnrichment(job, k, now, lim)
	}
	cancel := func(reason string) (Agent24JobChange, bool) {
		return Agent24JobChange{Status: Agent24JobCancelled, CancelReason: reason, Enrichment: job.Enrichment}, true
	}
	switch {
	case !k.Exists || k.ParseStatus == ParseStatusDeleting:
		return cancel(Agent24JobCancelDeleted)
	case k.LatestAttempt > job.Attempt:
		return cancel(Agent24JobCancelSuperseded)
	case k.ParseStatus == ParseStatusCancelled:
		// a gate writes authorization_revoked before it cancels the row, so a cancelled row with a still
		// occupying job was cancelled by an upstream management entry
		return cancel(Agent24JobCancelUser)
	case k.ParseStatus == ParseStatusFailed:
		return Agent24JobChange{
			Status: Agent24JobFailed, FailureReason: agent24FailureReason(k.FailureHint), Enrichment: job.Enrichment,
		}, true
	case k.ParseStatus == ParseStatusFinalizing:
		return Agent24JobChange{Status: Agent24JobIndexed, Enrichment: Agent24EnrichmentRunning}, true
	case k.ParseStatus == ParseStatusCompleted:
		return Agent24JobChange{Status: Agent24JobIndexed, Enrichment: Agent24EnrichmentNone}, true
	case job.Status == Agent24JobSaved && k.ParseStatus == ParseStatusProcessing:
		return Agent24JobChange{Status: Agent24JobProcessing, Enrichment: job.Enrichment}, true
	case job.Status == Agent24JobSaved && lim.Saved > 0 && now.Sub(job.SavedAt) > lim.Saved:
		return Agent24JobChange{
			Status: Agent24JobFailed, FailureReason: Agent24JobFailTimeout, Enrichment: job.Enrichment,
			FailKnowledge: true,
		}, true
	}
	return Agent24JobChange{}, false
}

func agent24ReconcileEnrichment(
	job Agent24IngestJob, k Agent24KnowledgeView, now time.Time, lim Agent24ReconcileLimits,
) (Agent24JobChange, bool) {
	if job.Enrichment != Agent24EnrichmentRunning {
		return Agent24JobChange{}, false
	}
	stop := func(reason string) (Agent24JobChange, bool) {
		return Agent24JobChange{
			Status: Agent24JobIndexed, Enrichment: Agent24EnrichmentStopped, StopReason: reason,
		}, true
	}
	switch {
	case !k.Exists || k.ParseStatus == ParseStatusDeleting:
		return stop(Agent24JobCancelDeleted)
	case k.LatestAttempt > job.Attempt:
		return stop(Agent24JobCancelSuperseded)
	case k.ParseStatus == ParseStatusCancelled:
		return stop(Agent24JobCancelUser)
	case k.ParseStatus == ParseStatusFailed:
		return stop(Agent24StopFailed)
	case k.ParseStatus == ParseStatusCompleted:
		return Agent24JobChange{Status: Agent24JobIndexed, Enrichment: Agent24EnrichmentDone}, true
	case lim.Enrichment > 0 && job.IndexedAt != nil && now.Sub(*job.IndexedAt) > lim.Enrichment:
		return stop(Agent24JobFailTimeout)
	}
	return Agent24JobChange{}, false
}

func agent24FailureReason(h Agent24FailureHint) string {
	switch h {
	case Agent24FailureHintParse:
		return Agent24JobFailParse
	case Agent24FailureHintEmbedding:
		return Agent24JobFailEmbedding
	case Agent24FailureHintIndex:
		return Agent24JobFailIndex
	case Agent24FailureHintEnqueue:
		return Agent24JobFailEnqueue
	case Agent24FailureHintStalled:
		return Agent24JobFailInterrupt
	case Agent24FailureHintStorage:
		return Agent24JobFailStorage
	default:
		return Agent24JobFailInternal
	}
}
