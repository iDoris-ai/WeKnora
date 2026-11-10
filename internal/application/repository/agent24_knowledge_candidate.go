// Agent24 fork product patch A24-KB-42 (KB-SOP-02 S1, WeKnora#146, CONTRACT-OPS-v2 W2.5, W3.1–W3.4, W8.2–W8.10).

package repository

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
)

// Agent24CandidateStore reads and writes the knowledge-entry tables: candidates, write idempotency records,
// organisation-administrator grants and entry withdrawals. Bound to a transaction by Transaction, every method
// runs in that transaction.
type Agent24CandidateStore struct {
	db *gorm.DB
}

// NewAgent24CandidateStore returns the store over db.
func NewAgent24CandidateStore(db *gorm.DB) *Agent24CandidateStore {
	return &Agent24CandidateStore{db: db}
}

// Errors of the store.
var (
	// ErrAgent24CandidateDuplicate: Create hit the submitted-dedup unique index (same tenant, author, target
	// knowledge base, mode, knowledge_id, base revision and content_sha256 already submitted). Nothing was written.
	ErrAgent24CandidateDuplicate = errors.New("agent24 candidate: an identical candidate is already submitted")
	// ErrAgent24CandidateIDTaken is CreateCandidate's answer when the candidate id is already in use.
	ErrAgent24CandidateIDTaken = errors.New("agent24 candidate: the candidate id is taken")
	// ErrAgent24IdempotencyExists: PutIdempotency found a record under the same (tenant, principal, op, key).
	// Nothing was written; the caller reads the existing record and replays or rejects it.
	ErrAgent24IdempotencyExists = errors.New("agent24 write idempotency: a record exists for the key")
)

// agent24CandidateTables are the fork tables EnsureAgent24CandidateTables creates, in creation order.
var agent24CandidateTables = []any{
	&types.Agent24KnowledgeCandidate{},
	&types.Agent24WriteIdempotency{},
	&types.Agent24KnowledgeAdmin{},
	&types.Agent24EntryWithdrawal{},
}

// agent24CandidateIndexes are the named indexes of agent24_knowledge_candidates and agent24_write_idempotency.
var agent24CandidateIndexes = map[any][]string{
	&types.Agent24KnowledgeCandidate{}: {
		"idx_agent24_candidates_author",
		"idx_agent24_candidates_target",
		"idx_agent24_candidates_knowledge",
		"idx_agent24_candidates_submitted_dedup",
		"idx_agent24_candidates_terminal",
	},
	&types.Agent24WriteIdempotency{}: {"idx_agent24_write_idempotency_expiry"},
}

// agent24PrincipalColumnSize is the width of every column that stores a Principal.StorageID():
// "api_external_user:" + tenant id + ":" + an external user id of up to 128 bytes.
const agent24PrincipalColumnSize = 255

// EnsureAgent24CandidateTables creates the four tables and their indexes when they are missing, the same way as
// EnsureAgent24IngestJobTable (no AutoMigrate, no numbered migration, re-check after a lost race). It also widens
// agent24_ingest_jobs.principal_user_id to agent24PrincipalColumnSize when it is narrower: an approver's
// StorageID does not fit the 64 it was created with. Call it after EnsureAgent24IngestJobTable.
func EnsureAgent24CandidateTables(db *gorm.DB) error {
	m := db.Migrator()
	for _, model := range agent24CandidateTables {
		if !m.HasTable(model) {
			if err := m.CreateTable(model); err != nil && !m.HasTable(model) {
				return err
			}
		}
	}
	for model, names := range agent24CandidateIndexes {
		for _, name := range names {
			if m.HasIndex(model, name) {
				continue
			}
			if err := m.CreateIndex(model, name); err != nil && !m.HasIndex(model, name) {
				return err
			}
		}
	}
	return agent24WidenIngestJobPrincipal(db)
}

// agent24WidenIngestJobPrincipal widens agent24_ingest_jobs.principal_user_id when ColumnTypes reports it
// narrower than agent24PrincipalColumnSize: a table A24-KB-41 created before the model tag became varchar(255).
// Only PostgreSQL enforces the length; other dialects are left alone.
func agent24WidenIngestJobPrincipal(db *gorm.DB) error {
	m := db.Migrator()
	if !m.HasTable(&types.Agent24IngestJob{}) {
		return nil
	}
	cols, err := m.ColumnTypes(&types.Agent24IngestJob{})
	if err != nil {
		return err
	}
	narrow := false
	for _, col := range cols {
		if col.Name() != "principal_user_id" {
			continue
		}
		n, ok := col.Length()
		if ok && n < int64(agent24PrincipalColumnSize) {
			narrow = true
		}
	}
	if !narrow || db.Name() != "postgres" {
		return nil
	}
	const widen = "ALTER TABLE agent24_ingest_jobs ALTER COLUMN principal_user_id TYPE varchar(255)"
	return db.Exec(widen).Error
}

// Transaction runs fn over a store bound to one database transaction.
func (s *Agent24CandidateStore) Transaction(ctx context.Context, fn func(tx *Agent24CandidateStore) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&Agent24CandidateStore{db: tx})
	})
}

// DB returns the handle the store writes through (the transaction inside Transaction), so a caller can put its
// own writes (the ingest Stage) in the same transaction.
func (s *Agent24CandidateStore) DB() *gorm.DB { return s.db }

// ---- candidates ----

// CreateCandidate inserts c, which must be submitted. A row identical on the dedup key returns
// ErrAgent24CandidateDuplicate.
func (s *Agent24CandidateStore) CreateCandidate(ctx context.Context, c *types.Agent24KnowledgeCandidate) error {
	if c.Status != types.Agent24CandidateSubmitted {
		return errors.New("agent24 candidate: status must be submitted")
	}
	c.SubmittedAt = c.SubmittedAt.UTC()
	if c.TerminalAt != nil {
		u := c.TerminalAt.UTC()
		c.TerminalAt = &u
	}
	if c.PurgedAt != nil {
		u := c.PurgedAt.UTC()
		c.PurgedAt = &u
	}
	// a savepoint: on PostgreSQL a failed insert aborts the caller's transaction, and the caller still has to
	// read the duplicate in it
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Create(c).Error
	})
	if !agent24IsUniqueViolation(err) {
		return err
	}
	// A24-KB-43 (PR-Daemon on #156): only the dedup index is a duplicate; a taken primary key is not
	taken, lookupErr := agent24RowExists(s.db.WithContext(ctx).Select("id").Where("id = ?", c.ID).
		Take(&types.Agent24KnowledgeCandidate{}).Error)
	switch {
	case lookupErr != nil:
		return lookupErr
	case taken:
		return ErrAgent24CandidateIDTaken
	}
	return ErrAgent24CandidateDuplicate
}

// GetCandidate returns the tenant's candidate id, or nil when there is none.
func (s *Agent24CandidateStore) GetCandidate(
	ctx context.Context, tenantID uint64, id string,
) (*types.Agent24KnowledgeCandidate, error) {
	return s.candidateByID(ctx, tenantID, id, false)
}

// LockCandidate reads the tenant's candidate id under a row lock (inside Transaction), or nil when there is none.
func (s *Agent24CandidateStore) LockCandidate(
	ctx context.Context, tenantID uint64, id string,
) (*types.Agent24KnowledgeCandidate, error) {
	return s.candidateByID(ctx, tenantID, id, true)
}

// FindSubmittedDuplicate returns the submitted candidate identical to c on the dedup key (W8.2: same tenant,
// author, target knowledge base, mode, knowledge_id, base revision and content_sha256), or nil.
func (s *Agent24CandidateStore) FindSubmittedDuplicate(
	ctx context.Context, c *types.Agent24KnowledgeCandidate,
) (*types.Agent24KnowledgeCandidate, error) {
	var found types.Agent24KnowledgeCandidate
	err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND author = ? AND target_knowledge_base_id = ? AND mode = ?",
			c.TenantID, c.Author, c.TargetKnowledgeBaseID, c.Mode).
		Where("knowledge_id = ? AND base_revision = ? AND content_sha256 = ? AND status = ?",
			c.KnowledgeID, c.BaseRevision, c.ContentSHA256, types.Agent24CandidateSubmitted).
		Take(&found).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &found, nil
}

// CountSubmitted counts the author's submitted candidates in the tenant (the W8.2 quota of 20).
func (s *Agent24CandidateStore) CountSubmitted(ctx context.Context, tenantID uint64, author string) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&types.Agent24KnowledgeCandidate{}).
		Where("tenant_id = ? AND author = ? AND status = ?", tenantID, author, types.Agent24CandidateSubmitted).
		Count(&n).Error
	return n, err
}

// TransitionCandidate moves the tenant's candidate id from submitted to t.To in one conditional write (W8.4) and
// reports whether it did. A candidate that is not submitted any more is left as it is and the result is false.
// t.To must be terminal. The write sets terminal_at to now and the fields of t that apply to t.To:
//   - approved: approver, author_is_approver, source_revision, job_id, and knowledge_id when t.KnowledgeID is set;
//   - returned: return_reason, note, and approver when set (approver on approve, returner when returned/reviewer);
//   - withdrawn: nothing more.
func (s *Agent24CandidateStore) TransitionCandidate(
	ctx context.Context, tenantID uint64, id string, t types.Agent24CandidateTransition, now time.Time,
) (bool, error) {
	if !t.To.Terminal() {
		return false, errors.New("agent24 candidate: transition target is not terminal")
	}
	set := map[string]any{
		"status":      t.To,
		"terminal_at": now.UTC(),
	}
	switch t.To {
	case types.Agent24CandidateApproved:
		set["approver"] = t.Approver
		set["author_is_approver"] = t.AuthorIsApprover
		set["source_revision"] = t.SourceRevision
		set["job_id"] = t.JobID
		if t.KnowledgeID != "" {
			set["knowledge_id"] = t.KnowledgeID
		}
	case types.Agent24CandidateReturned:
		set["return_reason"] = t.ReturnReason
		set["note"] = t.Note
		if t.Approver != "" {
			set["approver"] = t.Approver
		}
	}
	res := s.db.WithContext(ctx).Model(&types.Agent24KnowledgeCandidate{}).
		Where("tenant_id = ? AND id = ? AND status = ?", tenantID, id, types.Agent24CandidateSubmitted).
		Updates(set)
	return res.RowsAffected == 1, res.Error
}

// Agent24CandidatePage is a page of candidates, oldest submission first; Next is the cursor of the next page, or ""
// when there is none.
type Agent24CandidatePage struct {
	Candidates []types.Agent24KnowledgeCandidate
	Next       string
}

// ListSubmittedForReview returns up to limit of the tenant's submitted candidates whose target knowledge base is in
// kbIDs, oldest first, from the cursor after (an opaque string ListSubmittedForReview returned, or ""). An empty
// kbIDs returns no candidates: an empty list is never "all".
func (s *Agent24CandidateStore) ListSubmittedForReview(
	ctx context.Context, tenantID uint64, kbIDs []string, after string, limit int,
) (Agent24CandidatePage, error) {
	if len(kbIDs) == 0 {
		return Agent24CandidatePage{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	q := s.db.WithContext(ctx).
		Where("tenant_id = ? AND status = ? AND target_knowledge_base_id IN ?",
			tenantID, types.Agent24CandidateSubmitted, kbIDs)
	if after != "" {
		at, id, err := agent24DecodeCandidateCursor(after)
		if err != nil {
			return Agent24CandidatePage{}, err
		}
		q = q.Where("(submitted_at > ?) OR (submitted_at = ? AND id > ?)", at, at, id)
	}
	var rows []types.Agent24KnowledgeCandidate
	err := q.Order("submitted_at ASC, id ASC").Limit(limit + 1).Find(&rows).Error
	if err != nil {
		return Agent24CandidatePage{}, err
	}
	page := Agent24CandidatePage{Candidates: rows}
	if len(rows) > limit {
		page.Candidates = rows[:limit]
		last := rows[limit-1]
		page.Next = agent24EncodeCandidateCursor(last.SubmittedAt, last.ID)
	}
	return page, nil
}

// ListSubmittedForKnowledgeBase returns every submitted candidate of the tenant whose target is kbID (W1.11/A14:
// the candidates a knowledge base deletion returns).
func (s *Agent24CandidateStore) ListSubmittedForKnowledgeBase(
	ctx context.Context, tenantID uint64, kbID string,
) ([]types.Agent24KnowledgeCandidate, error) {
	var list []types.Agent24KnowledgeCandidate
	err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND status = ? AND target_knowledge_base_id = ?",
			tenantID, types.Agent24CandidateSubmitted, kbID).
		Find(&list).Error
	return list, err
}

// IsEntryDocument reports whether knowledgeID is a knowledge entry (origin "entry"): an approved candidate of the
// tenant is bound to it. Withdrawal is a separate question (IsWithdrawn).
func (s *Agent24CandidateStore) IsEntryDocument(
	ctx context.Context, tenantID uint64, knowledgeID string,
) (bool, error) {
	if knowledgeID == "" {
		return false, nil
	}
	err := s.db.WithContext(ctx).Select("id").
		Where("tenant_id = ? AND knowledge_id = ? AND status = ?",
			tenantID, knowledgeID, types.Agent24CandidateApproved).
		Take(&types.Agent24KnowledgeCandidate{}).Error
	return agent24RowExists(err)
}

// FindApprovedByContent returns the tenant's approved candidates in kbID whose content_sha256 is sha, most recently
// approved first: the W3.5 dedup candidates of an approval (the caller checks the bound documents).
func (s *Agent24CandidateStore) FindApprovedByContent(
	ctx context.Context, tenantID uint64, kbID, sha string,
) ([]types.Agent24KnowledgeCandidate, error) {
	var list []types.Agent24KnowledgeCandidate
	err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND target_knowledge_base_id = ? AND content_sha256 = ?", tenantID, kbID, sha).
		Where("status = ? AND knowledge_id <> ''", types.Agent24CandidateApproved).
		Order("terminal_at DESC, id ASC").
		Find(&list).Error
	return list, err
}

// PurgeCandidateContent empties title, body and note of returned and withdrawn candidates that reached that state
// before before and are not purged yet, and sets purged_at to now (W8.9). It returns how many it purged.
func (s *Agent24CandidateStore) PurgeCandidateContent(ctx context.Context, before, now time.Time) (int64, error) {
	res := s.db.WithContext(ctx).Model(&types.Agent24KnowledgeCandidate{}).
		Where("status IN ? AND terminal_at < ? AND purged_at IS NULL",
			[]types.Agent24CandidateStatus{types.Agent24CandidateReturned, types.Agent24CandidateWithdrawn},
			before.UTC()).
		Updates(map[string]any{
			"title": "", "body": "", "note": "", "purged_at": now.UTC(),
		})
	return res.RowsAffected, res.Error
}

// DeleteSettledCandidates deletes returned and withdrawn candidates that reached that state before before (W8.9:
// metadata kept 90 days after the terminal state). Submitted candidates are never deleted, and neither are approved
// ones: an approved candidate is what makes its knowledge_id an entry document (IsEntryDocument), for as long as the
// document exists, and its body is the document's original (W8.9: handled by #997, not by this retention).
func (s *Agent24CandidateStore) DeleteSettledCandidates(ctx context.Context, before time.Time) (int64, error) {
	res := s.db.WithContext(ctx).
		Where("status IN ? AND terminal_at IS NOT NULL AND terminal_at < ?",
			[]types.Agent24CandidateStatus{types.Agent24CandidateReturned, types.Agent24CandidateWithdrawn},
			before.UTC()).
		Delete(&types.Agent24KnowledgeCandidate{})
	return res.RowsAffected, res.Error
}

// ---- write idempotency ----

// GetIdempotency returns the record under (tenant, principal, op, key) that has not expired at now, or nil.
func (s *Agent24CandidateStore) GetIdempotency(
	ctx context.Context, tenantID uint64, principal, op, key string, now time.Time,
) (*types.Agent24WriteIdempotency, error) {
	var rec types.Agent24WriteIdempotency
	err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND principal = ? AND op = ? AND idem_key = ? AND expires_at > ?",
			tenantID, principal, op, key, now.UTC()).
		Take(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// PutIdempotency inserts rec. A record under the same (tenant, principal, op, key) that has expired at
// rec.CreatedAt is replaced (W3.3: past the retention the same key is a new request); one that has not returns
// ErrAgent24IdempotencyExists, including when a concurrent insert of the same key wins.
func (s *Agent24CandidateStore) PutIdempotency(ctx context.Context, rec *types.Agent24WriteIdempotency) error {
	rec.CreatedAt = rec.CreatedAt.UTC()
	rec.ExpiresAt = rec.ExpiresAt.UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("tenant_id = ? AND principal = ? AND op = ? AND idem_key = ? AND expires_at <= ?",
			rec.TenantID, rec.Principal, rec.Op, rec.Key, rec.CreatedAt).
			Delete(&types.Agent24WriteIdempotency{}).Error
		if err != nil {
			return err
		}
		err = tx.Create(rec).Error
		if agent24IsUniqueViolation(err) {
			return ErrAgent24IdempotencyExists
		}
		return err
	})
}

// DeleteExpiredIdempotency deletes records that expired before now.
func (s *Agent24CandidateStore) DeleteExpiredIdempotency(ctx context.Context, now time.Time) (int64, error) {
	res := s.db.WithContext(ctx).
		Where("expires_at < ?", now.UTC()).
		Delete(&types.Agent24WriteIdempotency{})
	return res.RowsAffected, res.Error
}

// ---- organisation-administrator grants (W2.5, the A side of W1.1) ----

// AdminHolds reports whether principal holds the organisation-administrator grant on kbID of the tenant: an
// unrevoked row for kbID or for types.Agent24KnowledgeAdminAnyKB. An empty principal or kbID holds nothing.
func (s *Agent24CandidateStore) AdminHolds(ctx context.Context, tenantID uint64, principal, kbID string) (bool, error) {
	if principal == "" || kbID == "" {
		return false, nil
	}
	err := s.db.WithContext(ctx).Select("principal").
		Where("tenant_id = ? AND principal = ? AND revoked_at IS NULL AND knowledge_base_id IN ?",
			tenantID, principal, []string{kbID, types.Agent24KnowledgeAdminAnyKB}).
		Take(&types.Agent24KnowledgeAdmin{}).Error
	return agent24RowExists(err)
}

// AdminHoldsAnywhere reports whether principal holds an unrevoked organisation-administrator grant on any
// knowledge base of the tenant (W1.9 ③(a): "holds the operation class in no scope"). An empty principal holds
// nothing, and a row with an empty knowledge_base_id grants nothing (AdminHolds never matches it either).
func (s *Agent24CandidateStore) AdminHoldsAnywhere(
	ctx context.Context, tenantID uint64, principal string,
) (bool, error) {
	if principal == "" {
		return false, nil
	}
	err := s.db.WithContext(ctx).Select("principal").
		Where("tenant_id = ? AND principal = ? AND revoked_at IS NULL AND knowledge_base_id <> ''",
			tenantID, principal).
		Take(&types.Agent24KnowledgeAdmin{}).Error
	return agent24RowExists(err)
}

// AdminGrants returns the knowledge bases (types.Agent24KnowledgeAdminAnyKB included) on which principal holds an
// unrevoked organisation-administrator grant, sorted: the facts the W1.2 authz_version of a decision over the
// grants digests.
func (s *Agent24CandidateStore) AdminGrants(ctx context.Context, tenantID uint64, principal string) ([]string, error) {
	var kbs []string
	if principal == "" {
		return kbs, nil
	}
	err := s.db.WithContext(ctx).Model(&types.Agent24KnowledgeAdmin{}).
		Where("tenant_id = ? AND principal = ? AND revoked_at IS NULL AND knowledge_base_id <> ''",
			tenantID, principal).
		Order("knowledge_base_id ASC").Pluck("knowledge_base_id", &kbs).Error
	return kbs, err
}

// ---- entry withdrawals (W8.10) ----

// Withdraw records w and reports whether it did. A document already withdrawn keeps its first record and the
// result is false (the W8.10 "already").
func (s *Agent24CandidateStore) Withdraw(ctx context.Context, w *types.Agent24EntryWithdrawal) (bool, error) {
	w.WithdrawnAt = w.WithdrawnAt.UTC()
	res := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(w)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// IsWithdrawn reports whether the tenant's knowledgeID is withdrawn.
func (s *Agent24CandidateStore) IsWithdrawn(ctx context.Context, tenantID uint64, knowledgeID string) (bool, error) {
	if knowledgeID == "" {
		return false, nil
	}
	err := s.db.WithContext(ctx).Select("knowledge_id").
		Where("tenant_id = ? AND knowledge_id = ?", tenantID, knowledgeID).
		Take(&types.Agent24EntryWithdrawal{}).Error
	return agent24RowExists(err)
}

// WithdrawnAmong returns the subset of knowledgeIDs of the tenant that is withdrawn, for the read surfaces that
// filter a page of documents or hits in one query. An empty input returns an empty, non-nil set.
func (s *Agent24CandidateStore) WithdrawnAmong(
	ctx context.Context, tenantID uint64, knowledgeIDs []string,
) (map[string]struct{}, error) {
	if len(knowledgeIDs) == 0 {
		return map[string]struct{}{}, nil
	}
	var rows []types.Agent24EntryWithdrawal
	err := s.db.WithContext(ctx).Select("knowledge_id").
		Where("tenant_id = ? AND knowledge_id IN ?", tenantID, knowledgeIDs).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		set[row.KnowledgeID] = struct{}{}
	}
	return set, nil
}

// candidateByID reads the tenant's candidate. lock adds FOR UPDATE; SQLite drops that clause.
func (s *Agent24CandidateStore) candidateByID(
	ctx context.Context, tenantID uint64, id string, lock bool,
) (*types.Agent24KnowledgeCandidate, error) {
	q := s.db.WithContext(ctx)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var c types.Agent24KnowledgeCandidate
	err := q.Where("tenant_id = ? AND id = ?", tenantID, id).Take(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// agent24IsUniqueViolation reports a unique-index conflict. TranslateError is not required:
// gorm.ErrDuplicatedKey when it is on, otherwise the SQLite or PostgreSQL error text.
func agent24IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "23505") ||
		strings.Contains(msg, "duplicate key value")
}

func agent24RowExists(err error) (bool, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func agent24EncodeCandidateCursor(submittedAt time.Time, id string) string {
	raw := submittedAt.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// CheckAgent24CandidateCursor reports whether after is a cursor ListSubmittedForReview could have returned, so
// a caller can refuse a malformed one as a request shape error before it reads anything.
func CheckAgent24CandidateCursor(after string) error {
	_, _, err := agent24DecodeCandidateCursor(after)
	return err
}

func agent24DecodeCandidateCursor(after string) (time.Time, string, error) {
	buf, err := base64.RawURLEncoding.DecodeString(after)
	if err != nil {
		return time.Time{}, "", err
	}
	stamp, id, ok := strings.Cut(string(buf), "|")
	if !ok {
		return time.Time{}, "", errors.New("agent24 candidate: malformed cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return time.Time{}, "", err
	}
	return at, id, nil
}
