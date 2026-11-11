// Agent24 fork product patch A24-KB-43 (KB-SOP-02 S2, WeKnora#146, CONTRACT-OPS-v2 W1.2, W1.7, W1.9, W2.5, W3.1–W3.4,
// W8.2, W8.3, W8.8, W8.12): the candidate.propose routes the Provider calls with C.
//
//	POST /agent24/knowledge/candidates           submit (create or revise)
//	POST /agent24/knowledge/candidates/withdraw  withdraw one's own submitted candidate
//	POST /agent24/knowledge/candidates/get       read one's own candidate
//
// Registered only in integration mode (agent24CandidateRoutesEnabled), where one process serves them: the per-author
// mutex below is what serialises the W8.2 quota count with the insert, and it holds only within a process.
// A multi-instance deployment is not supported (PATCH-LEDGER A24-KB-43).

package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	// agent24CandidateQuota is the W8.2 bound on one principal's submitted candidates.
	agent24CandidateQuota = 20
	// agent24WriteIdempotencyRetention is W3.3 for a write that creates no job.
	agent24WriteIdempotencyRetention = 90 * 24 * time.Hour
	// agent24RequestInProgressRetryMS is retry_after_ms of unavailable/request_in_progress (W3.2).
	agent24RequestInProgressRetryMS = 1000
	// agent24CandidateIDBytes is the randomness of a candidate_id (W8.2: at least 128 bits); hex-encoded it fills
	// the 64 characters of the id column.
	agent24CandidateIDBytes = 32
)

// agent24CandidateRoutesEnabled reports whether the candidate.propose routes are registered: integration mode with
// the revision key (revise compares source revisions, which the key derives). Integration mode requires the key
// (ValidateAgent24IntegrationConfig), so in practice this is integration mode. Ready and capabilities read it too,
// so neither ever declares a route that is not there.
func agent24CandidateRoutesEnabled() bool {
	mode, err := Agent24KnowledgeModeFromEnv()
	return err == nil && mode == Agent24KnowledgeModeIntegration && agent24SearchTagKey() != nil
}

// Agent24KnowledgeCandidatesHandler serves the candidate.propose routes.
type Agent24KnowledgeCandidatesHandler struct {
	key   []byte
	store *repository.Agent24CandidateStore
	auth  agent24WriteAuthorizer
	scope agent24ScopeResolver
	locks *agent24KeyedMutex
	now   func() time.Time
	newID func() (string, error)
	// commit runs the submission transaction; nil is store.Transaction (a test replaces it)
	commit func(ctx context.Context, fn func(tx *repository.Agent24CandidateStore) error) error
	// ingest publishes an approved candidate (A24-KB-45); nil disables candidate.approve
	ingest agent24CandidateIngest
}

// NewAgent24KnowledgeCandidatesHandler returns nil outside integration mode, which leaves the routes unregistered.
func NewAgent24KnowledgeCandidatesHandler(
	store *repository.Agent24CandidateStore, kbService interfaces.KnowledgeBaseService,
	ingest *service.Agent24Ingest,
) *Agent24KnowledgeCandidatesHandler {
	if !agent24CandidateRoutesEnabled() {
		return nil
	}
	scope := agent24DefaultScopeResolver{kbs: kbService}
	h := newAgent24KnowledgeCandidatesHandler(agent24SearchTagKey(), store, scope)
	if ingest != nil {
		h.ingest = ingest
	}
	return h
}

func newAgent24KnowledgeCandidatesHandler(
	key []byte, store *repository.Agent24CandidateStore, scope agent24ScopeResolver,
) *Agent24KnowledgeCandidatesHandler {
	return &Agent24KnowledgeCandidatesHandler{
		key: key, store: store, scope: scope,
		auth:  agent24CandidateAuthorizer(store, scope),
		locks: newAgent24KeyedMutex(),
		now:   time.Now,
		newID: agent24NewCandidateID,
	}
}

// agent24CandidateAuthorizer is W1.9 ③ over the organisation-administrator grants of store (D1), or the default
// deny of agent24DefaultWriteGrants when there is no store.
func agent24CandidateAuthorizer(
	store *repository.Agent24CandidateStore, scope agent24ScopeResolver,
) agent24WriteAuthorizer {
	if store == nil {
		return agent24WriteAuthorizer{grants: agent24DefaultWriteGrants{}, scope: scope}
	}
	return agent24WriteAuthorizer{grants: agent24StoreWriteGrants{store: store}, admins: store, scope: scope}
}

func agent24NewCandidateID() (string, error) {
	b := make([]byte, agent24CandidateIDBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (h *Agent24KnowledgeCandidatesHandler) transaction(
	ctx context.Context, fn func(tx *repository.Agent24CandidateStore) error,
) error {
	if h.commit != nil {
		return h.commit(ctx, fn)
	}
	return h.store.Transaction(ctx, fn)
}

// agent24CandidateCaller is who sends a candidate.propose request: tenant, principal and its StorageID (the author).
type agent24CandidateCaller struct {
	tenant    uint64
	principal types.Principal
	author    string
}

// caller reads the authenticated principal. A request that reaches the handler always has one (Auth); one without
// is operation_not_allowed, never a write.
func agent24CandidateCallerOf(ctx context.Context) (agent24CandidateCaller, bool) {
	p, ok := types.PrincipalFromContext(ctx)
	if !ok || p.StorageID() == "" {
		return agent24CandidateCaller{}, false
	}
	return agent24CandidateCaller{
		tenant: types.CallerFromContext(ctx).TenantID, principal: p.Normalize(), author: p.StorageID(),
	}, true
}

// agent24AuthzVersion is the W1.2 authz_version: an opaque digest of what the decision read, principal × resource.
// It changes whenever the credential (id, capabilities, knowledge-base list, full access) or the principal changes;
// extra carries further facts a decision read (S3: the organisation-administrator grants). W1.2 allows it to change
// more often than the decision does.
func agent24AuthzVersion(ctx context.Context, caller agent24CandidateCaller, extra ...string) string {
	scope, _ := types.TenantAPIKeyScopeFromContext(ctx)
	credential, _ := types.CredentialIDFromContext(ctx)
	capabilities := slices.Clone([]string(scope.Capabilities))
	slices.Sort(capabilities)
	kbs := slices.Clone([]string(scope.KnowledgeBaseIDs))
	slices.Sort(kbs)
	parts := []string{
		"agent24-authz/1", strconv.FormatUint(caller.tenant, 10), caller.author, credential,
		strconv.FormatBool(scope.FullAccess), strings.Join(capabilities, ","), strings.Join(kbs, ","),
	}
	parts = append(parts, extra...)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "az1_" + hex.EncodeToString(sum[:16])
}

// agent24CandidateAudit is the Resource of a candidate row (W8.12): no title, body or note.
type agent24CandidateAudit struct {
	CandidateID           string   `json:"candidate_id,omitempty"`
	ContentSHA256         string   `json:"content_sha256,omitempty"`
	TargetKnowledgeBaseID string   `json:"target_knowledge_base_id,omitempty"`
	Mode                  string   `json:"mode,omitempty"`
	KnowledgeID           string   `json:"knowledge_id,omitempty"`
	BaseRevision          string   `json:"base_revision,omitempty"`
	Kind                  string   `json:"kind,omitempty"`
	ReturnReason          string   `json:"return_reason,omitempty"` // W8.12; A24-KB-44
	SessionRef            string   `json:"session_ref,omitempty"`
	SkillVersion          string   `json:"skill_version,omitempty"`
	RedactionRulesVersion int64    `json:"redaction_rules_version,omitempty"`
	IdempotencyKeySHA256  string   `json:"idempotency_key_sha256,omitempty"`
	AuthzVersion          string   `json:"authz_version,omitempty"`
	Outcome               string   `json:"outcome,omitempty"`
	Replayed              bool     `json:"replayed,omitempty"`
	Already               bool     `json:"already,omitempty"`
	AuthorIsApprover      *bool    `json:"author_is_approver,omitempty"` // W8.12, W2.4; A24-KB-45
	SourceRevision        string   `json:"source_revision,omitempty"`    // W8.10 entry.withdraw; A24-KB-46
	StrippedFields        []string `json:"stripped_fields,omitempty"`
}

func agent24SHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// submitAudit is what a submission's row may record before and after it is decided. kind is recorded only when it
// is in the §6 set: an unknown kind is free text of the caller's.
func agent24SubmitAudit(s agent24CandidateSubmit, req agent24WriteRequest) *agent24CandidateAudit {
	a := &agent24CandidateAudit{
		ContentSHA256: s.ContentSHA256, TargetKnowledgeBaseID: s.TargetKnowledgeBaseID, Mode: s.Mode,
		KnowledgeID: s.KnowledgeID, BaseRevision: s.ExpectedRevision, SessionRef: s.SessionRef,
		SkillVersion: s.SkillVersion, RedactionRulesVersion: s.RedactionRulesVersion,
		StrippedFields: req.stripped,
	}
	if types.Agent24EntryKindKnown(s.Kind) {
		a.Kind = s.Kind
	}
	if s.IdempotencyKey != "" {
		a.IdempotencyKeySHA256 = agent24SHA256Hex(s.IdempotencyKey)
	}
	return a
}

// refuse writes kerr and records the audit resource gathered so far.
func agent24CandidateRefuse(
	c *gin.Context, kerr *agent24KnowledgeError, echo agent24KnowledgeEcho, a *agent24CandidateAudit,
) {
	if a != nil {
		agent24NoteAudit(c, func(f *agent24AuditFacts) { f.Resource = a })
	}
	writeAgent24KnowledgeError(c, kerr, echo)
}

// agent24CandidateGone records a request whose caller went away or whose deadline passed; nothing is written back.
func agent24CandidateGone(c *gin.Context, echo agent24KnowledgeEcho, a *agent24CandidateAudit) {
	if a != nil {
		agent24NoteAudit(c, func(f *agent24AuditFacts) { f.Resource = a })
	}
	agent24NoteCancelled(c, echo, nil)
}

// writeStored answers with a stored response body (a W3.2 record or a fresh one): request_id and generation from
// this request, and details.replayed when it replays.
func agent24WriteStored(
	c *gin.Context, echo agent24KnowledgeEcho, status int, stored string, replayed bool, a *agent24CandidateAudit,
) {
	var body map[string]any
	if json.Unmarshal([]byte(stored), &body) != nil || body == nil {
		agent24CandidateRefuse(c, agent24Internal(), echo, a)
		return
	}
	body["request_id"] = echo.RequestID
	body["generation"] = echo.Generation
	if replayed {
		details, _ := body["details"].(map[string]any)
		if details == nil {
			details = map[string]any{}
		}
		details["replayed"] = true
		body["details"] = details
	}
	if a != nil {
		a.Replayed = replayed
		details, _ := body["details"].(map[string]any)
		if outcome, ok := details["outcome"].(string); ok {
			a.Outcome = outcome
		}
		if id, ok := body["candidate_id"].(string); ok {
			a.CandidateID = id
		}
		// the row keeps this request's authz_version (W1.7): a replay answers with the first one (W3.2)
		if v, ok := body["authz_version"].(string); ok && a.AuthzVersion == "" {
			a.AuthzVersion = v
		}
	}
	agent24NoteAudit(c, func(f *agent24AuditFacts) {
		f.Echo = echo
		f.Outcome = agent24AuditOK
		f.Resource = a
	})
	c.JSON(status, body)
}

// agent24SubmitFingerprint is the W3.2 fingerprint of a submission: the operation class, the target, the claimed
// hash and the parameters that shape the result.
func agent24SubmitFingerprint(s agent24CandidateSubmit) string {
	raw, _ := json.Marshal([]any{
		string(Agent24WriteCandidatePropose), "submit", s.TargetKnowledgeBaseID, s.Mode, s.KnowledgeID,
		s.ExpectedRevision, s.ContentSHA256, s.Kind, s.SessionRef, s.MessageFrom, s.MessageTo, s.SkillVersion,
		s.RedactionRulesVersion,
	})
	return agent24SHA256Hex(string(raw))
}

// errAgent24IdempotencyRace ends a submission transaction whose W3.2 record another writer inserted first.
var errAgent24IdempotencyRace = errors.New("agent24 candidate: idempotency record written concurrently")

// agent24SubmitResult is the outcome of the submission transaction: a refusal, or the response to write.
type agent24SubmitResult struct {
	kerr     *agent24KnowledgeError
	status   int
	stored   string
	replayed bool
}

// Submit serves candidate.propose's submission (W8.2). Order (W1.9): ② shape (parseAgent24CandidateSubmit) and the
// disabled operation; ③ C and A for candidate.propose and the target in E (Authorize); then, under the author's
// mutex and in one transaction, ④ replay (W3.2), ⑤ W8.2 1–3, the content dedup (an existing submitted candidate
// adds nothing, so it passes the quota), ⑤4 the quota, the insert and the W3.2 record, written last (W3.4).
func (h *Agent24KnowledgeCandidatesHandler) Submit(c *gin.Context) {
	body, ok := agent24ReadBody(c)
	if !ok {
		writeAgent24KnowledgeError(c, agent24InvalidRequest(), agent24KnowledgeEcho{})
		return
	}
	sub, req, kerr := parseAgent24CandidateSubmit(body)
	audit := agent24SubmitAudit(sub, req)
	if kerr == nil && h.store == nil {
		kerr = agent24Forbidden("operation_disabled")
	}
	if kerr != nil {
		agent24CandidateRefuse(c, kerr, req.echo, audit)
		return
	}
	ctx := c.Request.Context()
	caller, ok := agent24CandidateCallerOf(ctx)
	if !ok {
		agent24CandidateRefuse(c, agent24Forbidden("operation_not_allowed"), req.echo, audit)
		return
	}
	if kerr, gone := h.auth.Authorize(ctx, Agent24WriteCandidatePropose, sub.TargetKnowledgeBaseID); gone {
		agent24CandidateGone(c, req.echo, audit)
		return
	} else if kerr != nil {
		agent24CandidateRefuse(c, kerr, req.echo, audit)
		return
	}
	audit.AuthzVersion = agent24AuthzVersion(ctx, caller)
	unlock := h.locks.Lock(strconv.FormatUint(caller.tenant, 10) + "\x00" + caller.author)
	defer unlock()
	fingerprint := agent24SubmitFingerprint(sub)
	var res agent24SubmitResult
	err := h.transaction(ctx, func(tx *repository.Agent24CandidateStore) error {
		var err error
		res, err = h.submitIn(ctx, tx, caller, sub, fingerprint, audit.AuthzVersion)
		return err
	})
	if errors.Is(err, errAgent24IdempotencyRace) {
		res, err = h.replayAfterRace(ctx, caller, sub.IdempotencyKey, fingerprint)
	}
	if err != nil {
		if e, gone := agent24StorageError(ctx, err); gone {
			agent24CandidateGone(c, req.echo, audit)
		} else {
			agent24CandidateRefuse(c, e, req.echo, audit)
		}
		return
	}
	if res.kerr != nil {
		agent24CandidateRefuse(c, res.kerr, req.echo, audit)
		return
	}
	audit.Outcome = "created" // replaced by details.outcome when the response has one
	agent24WriteStored(c, req.echo, res.status, res.stored, res.replayed, audit)
}

// replayAfterRace answers a submission whose record another writer committed between this one's read and write:
// that record replays, a different fingerprint is key reuse, and a record still not visible is request_in_progress.
func (h *Agent24KnowledgeCandidatesHandler) replayAfterRace(
	ctx context.Context, caller agent24CandidateCaller, key, fingerprint string,
) (agent24SubmitResult, error) {
	op := string(Agent24WriteCandidatePropose)
	rec, err := h.store.GetIdempotency(ctx, caller.tenant, caller.author, op, key, h.now())
	if err != nil {
		return agent24SubmitResult{}, err
	}
	return agent24ReplayOf(rec, fingerprint), nil
}

// agent24ReplayOf is W3.2 for a record read back: nil is still in progress.
func agent24ReplayOf(rec *types.Agent24WriteIdempotency, fingerprint string) agent24SubmitResult {
	switch {
	case rec == nil:
		e := agent24Unavailable("request_in_progress")
		e.Extra = map[string]any{"retry_after_ms": agent24RequestInProgressRetryMS}
		return agent24SubmitResult{kerr: e}
	case rec.Fingerprint != fingerprint:
		return agent24SubmitResult{kerr: agent24InvalidWrite("idempotency_key_reused")}
	default:
		return agent24SubmitResult{status: rec.StatusCode, stored: rec.Response, replayed: true}
	}
}

func (h *Agent24KnowledgeCandidatesHandler) submitIn(
	ctx context.Context, tx *repository.Agent24CandidateStore, caller agent24CandidateCaller,
	sub agent24CandidateSubmit, fingerprint, authz string,
) (agent24SubmitResult, error) {
	now := h.now()
	op := string(Agent24WriteCandidatePropose)
	rec, err := tx.GetIdempotency(ctx, caller.tenant, caller.author, op, sub.IdempotencyKey, now)
	if err != nil {
		return agent24SubmitResult{}, err
	}
	if rec != nil {
		return agent24ReplayOf(rec, fingerprint), nil
	}
	if kerr := agent24CheckEntryContent(sub); kerr != nil {
		return agent24SubmitResult{kerr: kerr}, nil
	}
	if sub.Mode == string(types.Agent24CandidateRevise) {
		kerr, err := h.checkReviseTarget(ctx, tx, caller.tenant, sub)
		if err != nil || kerr != nil {
			return agent24SubmitResult{kerr: kerr}, err
		}
	}
	cand := &types.Agent24KnowledgeCandidate{
		TenantID: caller.tenant, Author: caller.author, TargetKnowledgeBaseID: sub.TargetKnowledgeBaseID,
		Mode: types.Agent24CandidateMode(sub.Mode), KnowledgeID: sub.KnowledgeID, BaseRevision: sub.ExpectedRevision,
		ContentSHA256: sub.ContentSHA256, Kind: sub.Kind, Title: sub.Title, Body: sub.Body,
		SessionRef: sub.SessionRef, MessageFrom: sub.MessageFrom, MessageTo: sub.MessageTo,
		SkillVersion: sub.SkillVersion, RedactionRulesVersion: sub.RedactionRulesVersion,
		Status: types.Agent24CandidateSubmitted, SubmittedAt: now,
	}
	existing, err := tx.FindSubmittedDuplicate(ctx, cand)
	if err != nil {
		return agent24SubmitResult{}, err
	}
	if existing == nil {
		n, err := tx.CountSubmitted(ctx, caller.tenant, caller.author)
		if err != nil {
			return agent24SubmitResult{}, err
		}
		if n >= agent24CandidateQuota {
			return agent24SubmitResult{kerr: agent24Forbidden("quota_exceeded")}, nil
		}
		if cand.ID, err = h.newID(); err != nil {
			return agent24SubmitResult{}, err
		}
		if err := tx.CreateCandidate(ctx, cand); errors.Is(err, repository.ErrAgent24CandidateDuplicate) {
			// another process inserted the same content first (the mutex is per process)
			if existing, err = tx.FindSubmittedDuplicate(ctx, cand); err != nil {
				return agent24SubmitResult{}, err
			}
			if existing == nil {
				return agent24SubmitResult{}, errors.New("agent24 candidate: duplicate not found after a conflict")
			}
		} else if err != nil {
			return agent24SubmitResult{}, err
		}
	}
	resp := map[string]any{
		"authz_version": authz, "status": string(types.Agent24CandidateSubmitted),
	}
	if existing != nil {
		resp["candidate_id"] = existing.ID
		resp["content_sha256"] = existing.ContentSHA256
		resp["submitted_at"] = agent24WireTime(existing.SubmittedAt)
		resp["details"] = map[string]any{"outcome": "existing"}
	} else {
		resp["candidate_id"] = cand.ID
		resp["content_sha256"] = cand.ContentSHA256
		resp["submitted_at"] = agent24WireTime(cand.SubmittedAt)
	}
	stored, err := json.Marshal(resp)
	if err != nil {
		return agent24SubmitResult{}, err
	}
	err = tx.PutIdempotency(ctx, &types.Agent24WriteIdempotency{
		TenantID: caller.tenant, Principal: caller.author, Op: op, Key: sub.IdempotencyKey,
		Fingerprint: fingerprint, StatusCode: http.StatusOK, Response: string(stored),
		CreatedAt: now, ExpiresAt: now.Add(agent24WriteIdempotencyRetention),
	})
	if errors.Is(err, repository.ErrAgent24IdempotencyExists) {
		return agent24SubmitResult{}, errAgent24IdempotencyRace
	}
	if err != nil {
		return agent24SubmitResult{}, err
	}
	return agent24SubmitResult{status: http.StatusOK, stored: string(stored)}, nil
}

// agent24CheckEntryContent is W8.2 ⑤1 and ⑤2: kind in the §6 set, the title and body lengths, then the hash the
// server recomputes (W8.13); a mismatch is not saved.
func agent24CheckEntryContent(sub agent24CandidateSubmit) *agent24KnowledgeError {
	if !types.Agent24EntryKindKnown(sub.Kind) || !agent24EntryTitleValid(sub.Title) {
		return agent24InvalidRequest()
	}
	if len(sub.Body) > agent24EntryBodyMaxBytes {
		return agent24InvalidWrite("too_large")
	}
	if agent24EntryHash(sub.Kind, sub.Title, sub.Body) != sub.ContentSHA256 {
		return agent24InvalidWrite("content_hash_mismatch")
	}
	return nil
}

// agent24KnowledgeSourceRevision is the current source revision, shared by revise submit and approve.
func agent24KnowledgeSourceRevision(key []byte, k *types.Knowledge) (string, error) {
	mv, err := agent24ManualVersion(k)
	if err != nil {
		return "", err
	}
	return agent24SourceRevision(key, k.ID, agent24SourceFingerprint{
		FileHash: k.FileHash, FilePath: k.FilePath, Source: k.Source, ManualVersion: mv,
	}), nil
}

// checkReviseTarget is W8.2 ⑤3: the document is in target_knowledge_base_id (already in E, Authorize), exists, is not
// withdrawn and not being deleted, all one not_found (F1); it is an entry document (forbidden/operation_not_allowed
// otherwise); and its current source revision is the expected one (stale/base_revision_changed with
// current_revision otherwise).
func (h *Agent24KnowledgeCandidatesHandler) checkReviseTarget(
	ctx context.Context, tx *repository.Agent24CandidateStore, tenant uint64, sub agent24CandidateSubmit,
) (*agent24KnowledgeError, error) {
	var k types.Knowledge
	err := tx.DB().WithContext(ctx).Where("tenant_id = ? AND id = ?", tenant, sub.KnowledgeID).Take(&k).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return agent24NotFound(), nil
	}
	if err != nil {
		return nil, err
	}
	if k.KnowledgeBaseID != sub.TargetKnowledgeBaseID || k.ParseStatus == types.ParseStatusDeleting {
		return agent24NotFound(), nil
	}
	withdrawn, err := tx.IsWithdrawn(ctx, tenant, k.ID)
	if err != nil || withdrawn {
		return agent24NotFound(), err
	}
	entry, err := tx.IsEntryDocument(ctx, tenant, k.ID)
	if err != nil {
		return nil, err
	}
	if !entry {
		return agent24Forbidden("operation_not_allowed"), nil
	}
	current, err := agent24KnowledgeSourceRevision(h.key, &k)
	if err != nil {
		return nil, err
	}
	if current != sub.ExpectedRevision {
		return agent24StaleWrite("base_revision_changed", map[string]any{"current_revision": current}), nil
	}
	return nil, nil
}

// agent24WireTime is the ops-v2 timestamp: UTC RFC 3339, at most nine fractional digits.
func agent24WireTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// ownCandidate is W8.3 for the Provider: the tenant's candidate id, submitted by the caller, whose target knowledge
// base is in the caller's E. Anything else is nil (not_found): another's candidate is indistinguishable from none.
func (h *Agent24KnowledgeCandidatesHandler) ownCandidate(
	ctx context.Context, caller agent24CandidateCaller, id string,
) (*types.Agent24KnowledgeCandidate, error) {
	cand, err := h.store.GetCandidate(ctx, caller.tenant, id)
	if err != nil || cand == nil || cand.Author != caller.author {
		return nil, err
	}
	in, err := h.auth.InScope(ctx, cand.TargetKnowledgeBaseID)
	if err != nil || !in {
		return nil, err
	}
	return cand, nil
}

// candidateRef is the common start of withdraw and get: shape, disabled, ③(a), then the caller's own candidate
// (③(b), not_found otherwise). false means the response is written.
func (h *Agent24KnowledgeCandidatesHandler) candidateRef(
	c *gin.Context,
) (agent24CandidateCaller, *types.Agent24KnowledgeCandidate, agent24WriteRequest, *agent24CandidateAudit, bool) {
	var caller agent24CandidateCaller
	body, ok := agent24ReadBody(c)
	if !ok {
		writeAgent24KnowledgeError(c, agent24InvalidRequest(), agent24KnowledgeEcho{})
		return caller, nil, agent24WriteRequest{}, nil, false
	}
	id, req, kerr := parseAgent24CandidateRef(body)
	audit := &agent24CandidateAudit{CandidateID: id, StrippedFields: req.stripped}
	if kerr == nil && h.store == nil {
		kerr = agent24Forbidden("operation_disabled")
	}
	if kerr != nil {
		agent24CandidateRefuse(c, kerr, req.echo, audit)
		return caller, nil, req, audit, false
	}
	ctx := c.Request.Context()
	caller, ok = agent24CandidateCallerOf(ctx)
	if !ok {
		agent24CandidateRefuse(c, agent24Forbidden("operation_not_allowed"), req.echo, audit)
		return caller, nil, req, audit, false
	}
	if kerr, gone := h.auth.Authorize(ctx, Agent24WriteCandidatePropose, ""); gone {
		agent24CandidateGone(c, req.echo, audit)
		return caller, nil, req, audit, false
	} else if kerr != nil {
		agent24CandidateRefuse(c, kerr, req.echo, audit)
		return caller, nil, req, audit, false
	}
	audit.AuthzVersion = agent24AuthzVersion(ctx, caller)
	cand, err := h.ownCandidate(ctx, caller, id)
	if err != nil {
		if e, gone := agent24StorageError(ctx, err); gone {
			agent24CandidateGone(c, req.echo, audit)
		} else {
			agent24CandidateRefuse(c, e, req.echo, audit)
		}
		return caller, nil, req, audit, false
	}
	if cand == nil {
		agent24CandidateRefuse(c, agent24NotFound(), req.echo, audit)
		return caller, nil, req, audit, false
	}
	agent24CandidateAuditFill(audit, cand)
	return caller, cand, req, audit, true
}

func agent24CandidateAuditFill(a *agent24CandidateAudit, cand *types.Agent24KnowledgeCandidate) {
	a.ContentSHA256, a.TargetKnowledgeBaseID, a.Mode = cand.ContentSHA256, cand.TargetKnowledgeBaseID, string(cand.Mode)
	a.KnowledgeID, a.BaseRevision, a.Kind = cand.KnowledgeID, cand.BaseRevision, cand.Kind
	a.SessionRef, a.SkillVersion = cand.SessionRef, cand.SkillVersion
	a.RedactionRulesVersion = cand.RedactionRulesVersion
}

// Withdraw serves W8.8: the author moves their own submitted candidate to withdrawn in one conditional write. A
// candidate already withdrawn answers already: true (W3.1, naturally idempotent); any other state is
// stale/candidate_not_submitted.
func (h *Agent24KnowledgeCandidatesHandler) Withdraw(c *gin.Context) {
	caller, cand, req, audit, ok := h.candidateRef(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	moved, err := h.store.TransitionCandidate(ctx, caller.tenant, cand.ID,
		types.Agent24CandidateTransition{To: types.Agent24CandidateWithdrawn}, h.now())
	if err == nil && !moved {
		cand, err = h.store.GetCandidate(ctx, caller.tenant, cand.ID)
	}
	if err == nil && cand == nil {
		err = errors.New("agent24 candidate: candidate vanished during withdraw")
	}
	if err != nil {
		if e, gone := agent24StorageError(ctx, err); gone {
			agent24CandidateGone(c, req.echo, audit)
		} else {
			agent24CandidateRefuse(c, e, req.echo, audit)
		}
		return
	}
	resp := map[string]any{
		"authz_version": audit.AuthzVersion, "candidate_id": cand.ID,
		"status": string(types.Agent24CandidateWithdrawn),
	}
	switch {
	case moved:
	case cand.Status == types.Agent24CandidateWithdrawn:
		resp["details"] = map[string]any{"already": true}
		audit.Already = true
	default:
		agent24CandidateRefuse(c, agent24StaleWrite("candidate_not_submitted", nil), req.echo, audit)
		return
	}
	stored, _ := json.Marshal(resp)
	agent24WriteStored(c, req.echo, http.StatusOK, string(stored), false, audit)
}

// Get serves the author's read of their own candidate (W2.1, W8.3): {request_id, generation, authz_version,
// candidate}, candidate being the ops-v2 view.
func (h *Agent24KnowledgeCandidatesHandler) Get(c *gin.Context) {
	_, cand, req, audit, ok := h.candidateRef(c)
	if !ok {
		return
	}
	resp := map[string]any{"authz_version": audit.AuthzVersion, "candidate": agent24CandidateView(cand)}
	stored, err := json.Marshal(resp)
	if err != nil {
		agent24CandidateRefuse(c, agent24Internal(), req.echo, audit)
		return
	}
	agent24WriteStored(c, req.echo, http.StatusOK, string(stored), false, audit)
}

// agent24CandidateView is the ops-v2 view of a candidate. title, body and note are left out once W8.9 purged them;
// return_reason (and a note) only when returned; source_revision and author_is_approver only when approved;
// knowledge_id when known; base_source_revision for revise. updated_at is the terminal time, or the submission's.
func agent24CandidateView(cand *types.Agent24KnowledgeCandidate) map[string]any {
	v := map[string]any{
		"candidate_id": cand.ID, "status": string(cand.Status), "kind": cand.Kind,
		"content_sha256": cand.ContentSHA256, "target_knowledge_base_id": cand.TargetKnowledgeBaseID,
		"mode": string(cand.Mode), "submitted_at": agent24WireTime(cand.SubmittedAt),
		"updated_at": agent24WireTime(cand.SubmittedAt),
	}
	if cand.TerminalAt != nil {
		v["updated_at"] = agent24WireTime(*cand.TerminalAt)
	}
	if cand.PurgedAt == nil {
		v["title"], v["body"] = cand.Title, cand.Body
	}
	if cand.KnowledgeID != "" {
		v["knowledge_id"] = cand.KnowledgeID
	}
	if cand.Mode == types.Agent24CandidateRevise {
		v["base_source_revision"] = cand.BaseRevision
	}
	switch cand.Status {
	case types.Agent24CandidateReturned:
		v["return_reason"] = cand.ReturnReason
		if cand.Note != "" {
			v["note"] = cand.Note
		}
	case types.Agent24CandidateApproved:
		v["source_revision"] = cand.SourceRevision
		v["author_is_approver"] = cand.AuthorIsApprover
	}
	return v
}

// agent24KeyedMutex is a set of mutexes by key, each kept only while held or awaited.
type agent24KeyedMutex struct {
	mu    sync.Mutex
	locks map[string]*agent24KeyedLock
}

type agent24KeyedLock struct {
	mu   sync.Mutex
	refs int
}

func newAgent24KeyedMutex() *agent24KeyedMutex {
	return &agent24KeyedMutex{locks: map[string]*agent24KeyedLock{}}
}

// Lock takes key's mutex and returns its unlock.
func (k *agent24KeyedMutex) Lock(key string) func() {
	k.mu.Lock()
	l := k.locks[key]
	if l == nil {
		l = &agent24KeyedLock{}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}
