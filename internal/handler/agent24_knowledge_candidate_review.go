// Agent24 fork product patch A24-KB-44 (KB-SOP-02 S3, WeKnora 原 #146, CONTRACT-OPS-v2 W1.9, W8.3, W8.4, W8.6,
// W8.12): candidate.review, served to a reviewer's UI session. S5 registers the routes and puts the session's
// effective scope E (A ∩ C_now ∩ the ticket scope) on the request context as its TenantAPIKeyScope; these handlers
// only read the context. A UI request has no generation (CONTRACT-OPS-v2 W1.4), and neither has its response.

package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	agent24ReviewListDefaultLimit = 50
	agent24ReviewListMaxLimit     = 100
	// agent24ReviewNoteMaxBytes is W8.6's 1 KiB, counted in bytes: stricter than the schema's 1024 characters.
	agent24ReviewNoteMaxBytes = 1024
)

// ReviewList is W8.3: the submitted candidates whose target the reviewer may review, oldest first, paged.
func (h *Agent24KnowledgeCandidatesHandler) ReviewList(c *gin.Context) {
	req, audit, ok := agent24ReviewParsed(c, "after", "limit")
	if !ok {
		return
	}
	after, limit, kerr := agent24ReviewListArgs(req)
	if kerr != nil {
		agent24CandidateRefuse(c, kerr, req.echo, audit)
		return
	}
	caller, ok := h.reviewAuth(c, req, audit)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	kbs, err := h.reviewableKnowledgeBases(ctx)
	if err != nil {
		agent24ReviewStorage(c, req.echo, audit, err)
		return
	}
	page, err := h.store.ListSubmittedForReview(ctx, caller.tenant, kbs, after, limit)
	if err != nil {
		agent24ReviewStorage(c, req.echo, audit, err)
		return
	}
	views := make([]any, 0, len(page.Candidates))
	for i := range page.Candidates {
		views = append(views, agent24CandidateView(&page.Candidates[i]))
	}
	var next any
	if page.Next != "" {
		next = page.Next
	}
	agent24WriteUIOK(c, req.echo, map[string]any{
		"authz_version": audit.AuthzVersion, "candidates": views, "next": next,
	}, audit)
}

// ReviewGet is W8.4: one candidate the reviewer may review; any other id is not_found, byte for byte.
func (h *Agent24KnowledgeCandidatesHandler) ReviewGet(c *gin.Context) {
	_, cand, req, audit, ok := h.reviewOne(c, nil)
	if !ok {
		return
	}
	agent24WriteUIOK(c, req.echo, map[string]any{
		"authz_version": audit.AuthzVersion, "candidate": agent24CandidateView(cand),
	}, audit)
}

// ReviewReturn is W8.6: returns a submitted candidate as returned/reviewer with an optional note. The same
// reviewer returning it again gets already; anyone else, or any other state, gets stale.
func (h *Agent24KnowledgeCandidatesHandler) ReviewReturn(c *gin.Context) {
	var note string
	caller, cand, req, audit, ok := h.reviewOne(c, &note)
	if !ok {
		return
	}
	audit.ReturnReason = types.Agent24ReturnReviewer
	var already, stale bool
	err := h.transaction(c.Request.Context(), func(tx *repository.Agent24CandidateStore) error {
		var err error
		already, stale, err = h.reviewReturnIn(c.Request.Context(), tx, caller, cand.ID, note)
		return err
	})
	if err != nil {
		agent24ReviewStorage(c, req.echo, audit, err)
		return
	}
	if stale {
		agent24CandidateRefuse(c, agent24StaleWrite("candidate_not_submitted", nil), req.echo, audit)
		return
	}
	body := map[string]any{
		"authz_version": audit.AuthzVersion, "candidate_id": cand.ID,
		"status": string(types.Agent24CandidateReturned), "return_reason": types.Agent24ReturnReviewer,
	}
	if already {
		body["details"] = map[string]any{"already": true}
		audit.Already = true
	}
	agent24WriteUIOK(c, req.echo, body, audit)
}

func agent24WriteUIOK(c *gin.Context, echo agent24KnowledgeEcho, body map[string]any, a *agent24CandidateAudit) {
	body["request_id"] = echo.RequestID
	delete(body, "generation")
	agent24NoteAudit(c, func(f *agent24AuditFacts) {
		f.Echo = echo
		f.Outcome = agent24AuditOK
		f.Resource = a
	})
	c.JSON(http.StatusOK, body)
}

func agent24ReviewParsed(c *gin.Context, allowed ...string) (agent24WriteRequest, *agent24CandidateAudit, bool) {
	body, ok := agent24ReadBody(c)
	if !ok {
		writeAgent24KnowledgeError(c, agent24InvalidRequest(), agent24KnowledgeEcho{})
		return agent24WriteRequest{}, nil, false
	}
	req, kerr := parseAgent24WriteCommon(body, false, allowed...)
	audit := &agent24CandidateAudit{StrippedFields: req.stripped}
	if kerr != nil {
		agent24CandidateRefuse(c, kerr, req.echo, audit)
		return req, audit, false
	}
	return req, audit, true
}

func (h *Agent24KnowledgeCandidatesHandler) reviewOne(
	c *gin.Context, noteOut *string,
) (agent24CandidateCaller, *types.Agent24KnowledgeCandidate, agent24WriteRequest, *agent24CandidateAudit, bool) {
	allowed := []string{"candidate_id"}
	if noteOut != nil {
		allowed = append(allowed, "note")
	}
	req, audit, ok := agent24ReviewParsed(c, allowed...)
	if !ok {
		return agent24CandidateCaller{}, nil, req, audit, false
	}
	id, ok := agent24WireID(req.fields, "candidate_id")
	if !ok {
		agent24CandidateRefuse(c, agent24InvalidRequest(), req.echo, audit)
		return agent24CandidateCaller{}, nil, req, audit, false
	}
	audit.CandidateID = id
	if noteOut != nil {
		note, kerr := agent24ReviewNote(req)
		if kerr != nil {
			agent24CandidateRefuse(c, kerr, req.echo, audit)
			return agent24CandidateCaller{}, nil, req, audit, false
		}
		*noteOut = note
	}
	caller, ok := h.reviewAuth(c, req, audit)
	if !ok {
		return caller, nil, req, audit, false
	}
	cand, ok := h.adminVisible(c, Agent24WriteCandidateReview, caller, id, req, audit)
	if !ok {
		return caller, nil, req, audit, false
	}
	agent24CandidateAuditFill(audit, cand)
	return caller, cand, req, audit, true
}

func (h *Agent24KnowledgeCandidatesHandler) reviewAuth(
	c *gin.Context, req agent24WriteRequest, audit *agent24CandidateAudit,
) (agent24CandidateCaller, bool) {
	return h.adminAuth(c, Agent24WriteCandidateReview, req, audit)
}

// adminAuth is W1.9 from disabled to ③(a) for an organisation-administrator operation (review, approve), and the
// authz_version over the credential, the principal and the principal's grants (W1.2).
func (h *Agent24KnowledgeCandidatesHandler) adminAuth(
	c *gin.Context, op Agent24WriteOp, req agent24WriteRequest, audit *agent24CandidateAudit,
) (agent24CandidateCaller, bool) {
	ctx := c.Request.Context()
	caller, has := agent24CandidateCallerOf(ctx)
	refuse := func(kerr *agent24KnowledgeError) (agent24CandidateCaller, bool) {
		agent24CandidateRefuse(c, kerr, req.echo, audit)
		return caller, false
	}
	switch {
	case h.store == nil:
		return refuse(agent24Forbidden("operation_disabled"))
	case !has:
		return refuse(agent24Forbidden("operation_not_allowed"))
	}
	if kerr, gone := h.auth.Authorize(ctx, op, ""); gone {
		agent24CandidateGone(c, req.echo, audit)
		return caller, false
	} else if kerr != nil {
		return refuse(kerr)
	}
	// the grants are what ③ read beyond the credential, so they are part of authz_version (W1.2)
	grants, err := h.store.AdminGrants(ctx, caller.tenant, caller.author)
	if err != nil {
		agent24ReviewStorage(c, req.echo, audit, err)
		return caller, false
	}
	audit.AuthzVersion = agent24AuthzVersion(ctx, caller, "admins:"+strings.Join(grants, ","))
	return caller, true
}

func (h *Agent24KnowledgeCandidatesHandler) reviewableKnowledgeBases(ctx context.Context) ([]string, error) {
	scope, ok := types.TenantAPIKeyScopeFromContext(ctx)
	if !ok {
		return []string{}, nil
	}
	seen := make(map[string]struct{}, len(scope.KnowledgeBaseIDs))
	out := make([]string, 0)
	for _, kb := range scope.KnowledgeBaseIDs {
		if _, dup := seen[kb]; dup {
			continue
		}
		seen[kb] = struct{}{}
		held, err := h.auth.HoldsOn(ctx, Agent24WriteCandidateReview, kb)
		if err != nil {
			return nil, err
		}
		if held {
			out = append(out, kb)
		}
	}
	return out, nil
}

// adminVisible is W1.9 ③(b) for a candidate: it exists and op is held on its target knowledge base, or one not_found.
func (h *Agent24KnowledgeCandidatesHandler) adminVisible(
	c *gin.Context, op Agent24WriteOp, caller agent24CandidateCaller, id string, req agent24WriteRequest,
	audit *agent24CandidateAudit,
) (*types.Agent24KnowledgeCandidate, bool) {
	ctx := c.Request.Context()
	cand, err := h.store.GetCandidate(ctx, caller.tenant, id)
	var held bool
	if err == nil && cand != nil {
		held, err = h.auth.HoldsOn(ctx, op, cand.TargetKnowledgeBaseID)
	}
	if err != nil {
		agent24ReviewStorage(c, req.echo, audit, err)
		return nil, false
	}
	if cand == nil || !held {
		agent24CandidateRefuse(c, agent24NotFound(), req.echo, audit)
		return nil, false
	}
	return cand, true
}

func agent24ReviewStorage(c *gin.Context, echo agent24KnowledgeEcho, audit *agent24CandidateAudit, err error) {
	ctx := c.Request.Context()
	e, gone := agent24StorageError(ctx, err)
	if gone {
		agent24CandidateGone(c, echo, audit)
		return
	}
	agent24CandidateRefuse(c, e, echo, audit)
}

func agent24ReviewListArgs(req agent24WriteRequest) (after string, limit int, kerr *agent24KnowledgeError) {
	limit = agent24ReviewListDefaultLimit
	if _, present := req.fields["after"]; present {
		s, ok := agent24WireString(req.fields, "after")
		if !ok || repository.CheckAgent24CandidateCursor(s) != nil {
			return "", 0, agent24InvalidRequest()
		}
		after = s
	}
	if raw, present := req.fields["limit"]; present {
		n, ok := agent24JSONInt64(raw)
		if !ok || n < 1 || n > agent24ReviewListMaxLimit {
			return "", 0, agent24InvalidRequest()
		}
		limit = int(n)
	}
	return after, limit, nil
}

func agent24ReviewNote(req agent24WriteRequest) (string, *agent24KnowledgeError) {
	if _, present := req.fields["note"]; !present {
		return "", nil
	}
	s, ok := agent24WireString(req.fields, "note")
	if !ok || len(s) < 1 || len(s) > agent24ReviewNoteMaxBytes || strings.Contains(s, "\r") {
		return "", agent24InvalidRequest()
	}
	return s, nil
}

func (h *Agent24KnowledgeCandidatesHandler) reviewReturnIn(
	ctx context.Context, tx *repository.Agent24CandidateStore, caller agent24CandidateCaller, id, note string,
) (already, stale bool, err error) {
	locked, err := tx.LockCandidate(ctx, caller.tenant, id)
	if err != nil {
		return false, false, err
	}
	if locked != nil && locked.Status == types.Agent24CandidateSubmitted {
		var moved bool
		moved, err = tx.TransitionCandidate(ctx, caller.tenant, id, types.Agent24CandidateTransition{
			To: types.Agent24CandidateReturned, ReturnReason: types.Agent24ReturnReviewer,
			Note: note, Approver: caller.author,
		}, h.now())
		if err != nil || moved {
			return false, false, err
		}
		locked, err = tx.GetCandidate(ctx, caller.tenant, id)
		if err != nil {
			return false, false, err
		}
	}
	if locked == nil {
		// only a settled candidate is ever deleted (retention), so one that vanished was not submitted any more
		return false, true, nil
	}
	if locked.Status == types.Agent24CandidateReturned && locked.ReturnReason == types.Agent24ReturnReviewer &&
		locked.Approver == caller.author {
		return true, false, nil
	}
	return false, true, nil
}
