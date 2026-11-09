// Agent24 fork product patch A24-KB-42 (KB-SOP-02 S1, WeKnora#146, CONTRACT-OPS-v2 W2.5, W3.1–W3.4, W8.2–W8.10,
// §6): the server-side records of knowledge entries. Fork-owned tables, created at start-up the same way as
// agent24_ingest_jobs (no upstream migration):
//
//   - agent24_knowledge_candidates: a submitted entry candidate and its one transition out of submitted (W8.4).
//   - agent24_write_idempotency: the W3.2 records of the write operations.
//   - agent24_knowledge_admins: who holds the organisation-administrator grants of W2.5 (the A side of W1.1 for
//     candidate.review, candidate.approve and entry.withdraw); decision 2026-11-09-146-org-admin-grant.
//   - agent24_entry_withdrawals: which entry documents are withdrawn (W8.10). Kept off the knowledge row, whose
//     metadata an upstream whole-row Save can overwrite.
//
// No route reads these in S1; S2–S5 add the routes.

package types

import "time"

// Agent24CandidateStatus is the server-side candidate state of CONTRACT-OPS-v2 §6. Only submitted moves (W8.4).
type Agent24CandidateStatus string

// Candidate states.
const (
	Agent24CandidateSubmitted Agent24CandidateStatus = "submitted"
	Agent24CandidateApproved  Agent24CandidateStatus = "approved"
	Agent24CandidateReturned  Agent24CandidateStatus = "returned"
	Agent24CandidateWithdrawn Agent24CandidateStatus = "withdrawn"
)

// Terminal reports whether s never changes again: every state but submitted.
func (s Agent24CandidateStatus) Terminal() bool {
	return s == Agent24CandidateApproved || s == Agent24CandidateReturned || s == Agent24CandidateWithdrawn
}

// The return_reason values of CONTRACT-OPS-v2 §6.
const (
	Agent24ReturnReviewer          = "reviewer"
	Agent24ReturnConflicted        = "conflicted"
	Agent24ReturnTargetWithdrawn   = "target_withdrawn"
	Agent24ReturnTargetGone        = "target_gone"
	Agent24ReturnUnsupportedFormat = "unsupported_format"
	Agent24ReturnTooLarge          = "too_large"
)

// Agent24CandidateMode is the W8.2 submission mode.
type Agent24CandidateMode string

// Submission modes.
const (
	Agent24CandidateCreate Agent24CandidateMode = "create"
	Agent24CandidateRevise Agent24CandidateMode = "revise"
)

// The knowledge-entry kind values of CONTRACT-OPS-v2 §6. A kind is a label only.
const (
	Agent24EntryKindSOP  = "sop"
	Agent24EntryKindFAQ  = "faq"
	Agent24EntryKindNote = "note"
)

// Agent24EntryKindKnown reports whether kind is in the §6 closed set.
func Agent24EntryKindKnown(kind string) bool {
	return kind == Agent24EntryKindSOP || kind == Agent24EntryKindFAQ || kind == Agent24EntryKindNote
}

// Agent24KnowledgeCandidate is one row of agent24_knowledge_candidates.
//
// Author and Approver are Principal.StorageID() values. KnowledgeID is set on submission for revise and on
// approval for create. Title, Body and Note are emptied (PurgedAt set) within an hour of a returned or
// withdrawn state (W8.9); the hash and the metadata stay.
type Agent24KnowledgeCandidate struct {
	ID                    string                 `gorm:"type:varchar(64);primaryKey"`
	TenantID              uint64                 `gorm:"not null;index:idx_agent24_candidates_author,priority:1;index:idx_agent24_candidates_target,priority:1;index:idx_agent24_candidates_knowledge,priority:1;uniqueIndex:idx_agent24_candidates_submitted_dedup,priority:1,where:status = 'submitted'"` //nolint:lll // tag
	Author                string                 `gorm:"type:varchar(255);not null;index:idx_agent24_candidates_author,priority:2;uniqueIndex:idx_agent24_candidates_submitted_dedup,priority:2,where:status = 'submitted'"`                                                                                //nolint:lll // tag
	TargetKnowledgeBaseID string                 `gorm:"column:target_knowledge_base_id;type:varchar(64);not null;index:idx_agent24_candidates_target,priority:2;uniqueIndex:idx_agent24_candidates_submitted_dedup,priority:3,where:status = 'submitted'"`                                                 //nolint:lll // tag
	Mode                  Agent24CandidateMode   `gorm:"type:varchar(16);not null;uniqueIndex:idx_agent24_candidates_submitted_dedup,priority:4,where:status = 'submitted'"`                                                                                                                                //nolint:lll // tag
	KnowledgeID           string                 `gorm:"type:varchar(64);not null;default:'';index:idx_agent24_candidates_knowledge,priority:2;uniqueIndex:idx_agent24_candidates_submitted_dedup,priority:5,where:status = 'submitted'"`                                                                   //nolint:lll // tag
	BaseRevision          string                 `gorm:"type:varchar(128);not null;default:'';uniqueIndex:idx_agent24_candidates_submitted_dedup,priority:6,where:status = 'submitted'"`                                                                                                                    //nolint:lll // tag
	ContentSHA256         string                 `gorm:"column:content_sha256;type:varchar(64);not null;uniqueIndex:idx_agent24_candidates_submitted_dedup,priority:7,where:status = 'submitted'"`                                                                                                          //nolint:lll // tag
	Kind                  string                 `gorm:"type:varchar(16);not null"`
	Title                 string                 `gorm:"type:text;not null;default:''"`
	Body                  string                 `gorm:"type:text;not null;default:''"`
	SessionRef            string                 `gorm:"type:varchar(64);not null"`
	MessageFrom           int                    `gorm:"not null"`
	MessageTo             int                    `gorm:"not null"`
	SkillVersion          string                 `gorm:"type:varchar(64);not null"`
	RedactionRulesVersion int64                  `gorm:"not null"`
	Status                Agent24CandidateStatus `gorm:"type:varchar(16);not null;index:idx_agent24_candidates_author,priority:3;index:idx_agent24_candidates_target,priority:3"` //nolint:lll // tag
	ReturnReason          string                 `gorm:"type:varchar(32);not null;default:''"`
	Note                  string                 `gorm:"type:text;not null;default:''"`
	Approver              string                 `gorm:"type:varchar(255);not null;default:''"` // approver or returner
	AuthorIsApprover      bool                   `gorm:"not null;default:false"`
	SourceRevision        string                 `gorm:"type:varchar(128);not null;default:''"`
	JobID                 string                 `gorm:"type:varchar(36);not null;default:''"`
	SubmittedAt           time.Time              `gorm:"not null"`
	TerminalAt            *time.Time             `gorm:"index:idx_agent24_candidates_terminal"`
	PurgedAt              *time.Time             //
}

// TableName pins the fork-owned table name.
func (Agent24KnowledgeCandidate) TableName() string { return "agent24_knowledge_candidates" }

// Agent24CandidateTransition is the one write that moves a submitted candidate to a terminal state (W8.4). The
// fields that do not apply to To stay zero.
type Agent24CandidateTransition struct {
	To               Agent24CandidateStatus
	ReturnReason     string
	Note             string
	Approver         string
	AuthorIsApprover bool
	KnowledgeID      string
	SourceRevision   string
	JobID            string
}

// Agent24WriteIdempotency is one row of agent24_write_idempotency: the outcome of a write request, kept so that the
// same key and fingerprint replays it (W3.2). Key is the request's idempotency_key, or the candidate_id for
// candidate.approve (W3.1). Response is the response body without request_id and generation, which the replay
// fills in from the retry.
type Agent24WriteIdempotency struct {
	TenantID    uint64    `gorm:"primaryKey;autoIncrement:false"`
	Principal   string    `gorm:"type:varchar(255);primaryKey"`
	Op          string    `gorm:"type:varchar(32);primaryKey"`
	Key         string    `gorm:"column:idem_key;type:varchar(128);primaryKey"`
	Fingerprint string    `gorm:"type:varchar(64);not null"`
	StatusCode  int       `gorm:"not null"`
	Response    string    `gorm:"type:text;not null"`
	CreatedAt   time.Time `gorm:"not null"`
	ExpiresAt   time.Time `gorm:"not null;index:idx_agent24_write_idempotency_expiry"`
}

// TableName pins the fork-owned table name.
func (Agent24WriteIdempotency) TableName() string { return "agent24_write_idempotency" }

// Agent24KnowledgeAdminAnyKB is the KnowledgeBaseID of a grant that covers every knowledge base of its tenant.
const Agent24KnowledgeAdminAnyKB = "*"

// Agent24KnowledgeAdmin is one row of agent24_knowledge_admins: Principal (a Principal.StorageID()) holds the
// organisation-administrator operation classes of W2.5 on KnowledgeBaseID of TenantID, or on all of the tenant's
// knowledge bases when it is Agent24KnowledgeAdminAnyKB, until RevokedAt. M3 has no route that writes it; the
// operator writes it.
type Agent24KnowledgeAdmin struct {
	TenantID        uint64     `gorm:"primaryKey;autoIncrement:false"`
	Principal       string     `gorm:"type:varchar(255);primaryKey"`
	KnowledgeBaseID string     `gorm:"type:varchar(64);primaryKey"`
	CreatedAt       time.Time  `gorm:"not null"`
	RevokedAt       *time.Time //
}

// TableName pins the fork-owned table name.
func (Agent24KnowledgeAdmin) TableName() string { return "agent24_knowledge_admins" }

// Agent24EntryWithdrawal is one row of agent24_entry_withdrawals: the entry document KnowledgeID was withdrawn
// (W8.10). A withdrawal is final: a withdrawn document never comes back.
type Agent24EntryWithdrawal struct {
	TenantID       uint64    `gorm:"primaryKey;autoIncrement:false"`
	KnowledgeID    string    `gorm:"type:varchar(64);primaryKey"`
	WithdrawnBy    string    `gorm:"type:varchar(255);not null"`
	SourceRevision string    `gorm:"type:varchar(128);not null;default:''"`
	WithdrawnAt    time.Time `gorm:"not null"`
}

// TableName pins the fork-owned table name.
func (Agent24EntryWithdrawal) TableName() string { return "agent24_entry_withdrawals" }
