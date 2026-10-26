// Agent24 fork product patch A24-KB-16 (CONTRACT-READONLY-v1 r10 R10.1–R10.4, WeKnora#41).
//
// One audit row per POST /api/v1/agent24/knowledge/{ready,search,read}. The middleware sits outside Recovery
// and Auth, so the row sees the status and size that were actually written, including a rejection the auth
// envelope rewrote and Recovery's 500.
// Extra fields live in Details. ActorUserID stays empty (R10.4). The query, document text, snippets,
// content_tag and raw credentials are not recorded (R10.3).

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
)

const (
	agent24AuditActionSearch types.AuditAction = "knowledge.search"
	agent24AuditActionRead   types.AuditAction = "knowledge.read"
	agent24AuditActionReady  types.AuditAction = "knowledge.ready"

	agent24AuditScopeType = "agent24_knowledge"

	agent24AuditOK        = "ok"
	agent24AuditPartial   = "partial"
	agent24AuditCancelled = "cancelled"

	agent24AuditPathReady  = "/api/v1/agent24/knowledge/ready"
	agent24AuditPathSearch = "/api/v1/agent24/knowledge/search"
	agent24AuditPathRead   = "/api/v1/agent24/knowledge/read"

	// agent24AuditWriteTimeout bounds the row write. The row is written after the response is flushed, on a
	// context that ignores the client leaving, so a stalled store would otherwise hold the handler forever.
	agent24AuditWriteTimeout = 2 * time.Second
	// Anonymous rows (R10.2) come from requests without a credential, which anyone can send. They share one
	// process-wide token bucket: a burst of agent24AuditAnonymousBurst, then one row per
	// agent24AuditAnonymousEvery. The bucket belongs to the middleware, so each server process has its own.
	// Authenticated rows are not limited.
	agent24AuditAnonymousBurst = 60
	agent24AuditAnonymousEvery = time.Second

	// agent24AuditFactsKey is the gin-context key for *agent24AuditFacts, used only by agent24NoteAudit
	// (handlers record through it) and agent24KnowledgeAuditEntry (the middleware reads the facts back).
	agent24AuditFactsKey = "agent24KnowledgeAuditFacts"
)

// agent24AuditFacts is what a handler records and the middleware reads back after c.Next.
type agent24AuditFacts struct {
	Echo           agent24KnowledgeEcho // request_id and generation; either pointer may be nil
	Outcome        string               // ok, partial, an error code, or cancelled
	Reason         string
	EffectiveScope []string
	Resource       any
	Revision       any
	ResultCount    int
}

type agent24AuditPrincipal struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type agent24AuditMetering struct {
	LatencyMS     int64 `json:"latency_ms"`
	ResultCount   int   `json:"result_count"`
	BytesReturned int   `json:"bytes_returned"`
}

// agent24AuditDetails is the R10.1 whitelist. Absent values are JSON null; an empty reason is omitted.
type agent24AuditDetails struct {
	OccurredAt string  `json:"occurred_at"`
	RequestID  *string `json:"request_id"`
	// HTTPRequestID is the access log's request_id, character for character (the RequestID middleware value
	// after secutils.SanitizeForLog). Longer than agent24RequestIDMaxRunes is null rather than truncated, so
	// the two cannot disagree. Anonymous rows carry it too: it is not an identity field.
	HTTPRequestID  *string                `json:"http_request_id"`
	Generation     *int64                 `json:"generation"`
	Principal      *agent24AuditPrincipal `json:"principal"`
	CredentialID   *string                `json:"credential_id"`
	TenantID       *uint64                `json:"tenant_id"`
	EffectiveScope []string               `json:"effective_scope"`
	Resource       any                    `json:"resource"`
	Revision       any                    `json:"revision"`
	Outcome        string                 `json:"outcome"`
	Reason         string                 `json:"reason,omitempty"`
	Metering       agent24AuditMetering   `json:"metering"`
	// Suppressed is set on an anonymous row only: how many anonymous rows the token bucket dropped since the
	// previous anonymous row was written.
	Suppressed int64 `json:"suppressed,omitempty"`
}

// agent24AnonymousAuditLimiter is the token bucket for anonymous rows, with the count of rows it dropped.
type agent24AnonymousAuditLimiter struct {
	bucket     *rate.Limiter
	suppressed atomic.Int64
}

func newAgent24AnonymousAuditLimiter(every time.Duration, burst int) *agent24AnonymousAuditLimiter {
	return &agent24AnonymousAuditLimiter{bucket: rate.NewLimiter(rate.Every(every), burst)}
}

// admit reports whether an anonymous row may be written and, when it may, how many were dropped before it.
// The count is taken when the row is admitted; a row whose write returns an error gives it back.
func (l *agent24AnonymousAuditLimiter) admit() (bool, int64) {
	if !l.bucket.Allow() {
		l.suppressed.Add(1)
		return false, 0
	}
	return true, l.suppressed.Swap(0)
}

// agent24NoteAudit creates the request's facts on first use, then lets f update them.
func agent24NoteAudit(c *gin.Context, f func(*agent24AuditFacts)) {
	var facts *agent24AuditFacts
	if v, ok := c.Get(agent24AuditFactsKey); ok {
		facts, _ = v.(*agent24AuditFacts)
	}
	if facts == nil {
		facts = &agent24AuditFacts{}
		c.Set(agent24AuditFactsKey, facts)
	}
	f(facts)
}

// agent24NoteCancelled records a client abort. scope is E when it was already computed, otherwise nil.
func agent24NoteCancelled(c *gin.Context, echo agent24KnowledgeEcho, scope []string) {
	agent24NoteAudit(c, func(f *agent24AuditFacts) {
		f.Echo = echo
		f.Outcome = agent24AuditCancelled
		if len(scope) > 0 {
			f.EffectiveScope = append([]string(nil), scope...)
		}
	})
}

// agent24NoteScope records E on an error that happened after the scope was computed. Empty is "no scope".
func agent24NoteScope(c *gin.Context, scope []string) {
	if len(scope) == 0 {
		return
	}
	copied := append([]string(nil), scope...)
	agent24NoteAudit(c, func(f *agent24AuditFacts) { f.EffectiveScope = copied })
}

// agent24CollectedKBIDs is E's ids in resolver order.
func agent24CollectedKBIDs(kbs []*types.KnowledgeBase) []string {
	ids := make([]string, len(kbs))
	for i, kb := range kbs {
		ids[i] = kb.ID
	}
	return ids
}

// agent24SearchAuditResource is the search whitelist: chunk ids and per-hit revision, without content_tag.
func agent24SearchAuditResource(hits []agent24SearchHit) (any, any) {
	ids := make([]string, 0, len(hits))
	revs := make([]map[string]any, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.ChunkID)
		revs = append(revs, map[string]any{
			"chunk_id":        hit.ChunkID,
			"source_revision": hit.Revision.SourceRevision,
			"index_build_id":  hit.Revision.IndexBuildID,
			"chunk_revision":  hit.Revision.ChunkRevision,
		})
	}
	return map[string]any{"chunk_ids": ids, "count": len(hits)}, revs
}

// agent24ReadAuditResource is the read whitelist: the document id, its two revision fields, and each
// segment's chunk revision. Text and content_tag stay out.
func agent24ReadAuditResource(knowledgeID string, rev agent24DocRevision, segs []agent24ReadSegment) (any, any) {
	ids := make([]string, 0, len(segs))
	parts := make([]map[string]any, 0, len(segs))
	for _, seg := range segs {
		ids = append(ids, seg.ChunkID)
		parts = append(parts, map[string]any{
			"chunk_id":       seg.ChunkID,
			"chunk_revision": seg.ChunkRevision,
		})
	}
	resource := map[string]any{"knowledge_id": knowledgeID, "chunk_ids": ids}
	revision := map[string]any{
		"source_revision": rev.SourceRevision,
		"index_build_id":  rev.IndexBuildID,
		"segments":        parts,
	}
	return resource, revision
}

// Agent24KnowledgeAudit writes one audit row for the three knowledge endpoints; anonymous rows go through a
// token bucket (see agent24AuditAnonymousBurst). A nil service, and every other request, is passed through.
// Mount it outside Auth: the row is built after the handler chain, the response is flushed, and only then is
// the row written, on a context that ignores client cancellation and ends after agent24AuditWriteTimeout;
// Log's own error is ignored. Mount it outside Recovery too: a panic further down then reaches here as
// Recovery's 500, recorded as internal. Being outside Recovery, a panic while writing the row is contained
// here: the row is lost, the response already written is kept.
func Agent24KnowledgeAudit(svc interfaces.AuditLogService) gin.HandlerFunc {
	return agent24KnowledgeAudit(svc,
		newAgent24AnonymousAuditLimiter(agent24AuditAnonymousEvery, agent24AuditAnonymousBurst))
}

func agent24KnowledgeAudit(svc interfaces.AuditLogService, anonymous *agent24AnonymousAuditLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil || !agent24KnowledgeAuditPath(c) {
			c.Next()
			return
		}
		started := time.Now()
		c.Next()
		agent24WriteKnowledgeAudit(c, svc, anonymous, started)
	}
}

func agent24WriteKnowledgeAudit(
	c *gin.Context, svc interfaces.AuditLogService, anonymous *agent24AnonymousAuditLimiter, started time.Time,
) {
	base := context.WithoutCancel(c.Request.Context())
	defer func() {
		if r := recover(); r != nil {
			logger.Errorf(base, "agent24 knowledge audit: panic while writing the row: %s", logger.PanicValue(r))
		}
	}()
	entry, suppressed := agent24KnowledgeAuditEntry(c, started, anonymous)
	if entry == nil {
		return
	}
	agent24FlushResponse(c)
	// The deadline starts after the flush: net/http reads any unread request body before it sends the
	// headers, and a client that stalls that body must not run the row's time out.
	ctx, cancel := context.WithTimeout(base, agent24AuditWriteTimeout)
	defer cancel()
	if err := svc.Log(ctx, entry); err != nil && suppressed > 0 {
		// The row may not have landed: the next one carries its count. When the store committed but the
		// reply was lost, the count is reported twice; suppressed errs on the high side.
		anonymous.suppressed.Add(suppressed)
	}
}

// agent24FlushResponse sends the status and body before the row is written. The handler has not returned,
// so net/http sends them chunked and the final chunk follows the write (at most agent24AuditWriteTimeout
// later). A writer that cannot flush panics in gin; the response then goes out when the handler returns.
func agent24FlushResponse(c *gin.Context) {
	defer func() { _ = recover() }()
	c.Writer.Flush()
}

func agent24KnowledgeAuditPath(c *gin.Context) bool {
	if c.Request.Method != http.MethodPost {
		return false
	}
	switch c.Request.URL.Path {
	case agent24AuditPathReady, agent24AuditPathSearch, agent24AuditPathRead:
		return true
	default:
		return false
	}
}

func agent24AuditAction(path string) types.AuditAction {
	switch path {
	case agent24AuditPathSearch:
		return agent24AuditActionSearch
	case agent24AuditPathRead:
		return agent24AuditActionRead
	default:
		return agent24AuditActionReady
	}
}

func agent24AuditSucceeded(outcome string) bool {
	return outcome == agent24AuditOK || outcome == agent24AuditPartial
}

func agent24AuditColumnOutcome(outcome string) types.AuditOutcome {
	switch outcome {
	case agent24AuditOK:
		return types.AuditOutcomeSuccess
	case agent24AuditPartial:
		return types.AuditOutcomePartial
	case agent24AuditCancelled:
		return types.AuditOutcomeCanceled
	case string(agent24CodeUnauthorized), string(agent24CodeForbidden):
		return types.AuditOutcomeDenied
	default:
		return types.AuditOutcomeFailed
	}
}

// agent24AuditInfer classifies a request that recorded no facts: a rejection written before the handler (Auth,
// the API-key capability gate, the Viewer guard) or Recovery's 500. A 401 with no reason is credential_invalid;
// a 403 is forbidden without a reason, since those guards do not say which contract reason applies.
func agent24AuditInfer(c *gin.Context) (outcome, reason string) {
	switch c.Writer.Status() {
	case http.StatusUnauthorized:
		// The envelope normally records the reason through agent24NoteAudit. This is the fallback for a 401
		// that noted a verified-but-unusable credential and wrote no facts.
		if r, ok := agent24CredentialRejectionOf(c); ok {
			return string(agent24CodeUnauthorized), r.Reason
		}
		return string(agent24CodeUnauthorized), "credential_invalid"
	case http.StatusForbidden:
		return string(agent24CodeForbidden), ""
	case http.StatusBadRequest:
		return string(agent24CodeInvalidRequest), ""
	}
	if errors.Is(c.Request.Context().Err(), context.Canceled) {
		return agent24AuditCancelled, ""
	}
	return string(agent24CodeInternal), ""
}

func agent24GinPrincipal(c *gin.Context) (types.Principal, bool) {
	v, ok := c.Get(types.PrincipalContextKey.String())
	if !ok {
		return types.Principal{}, false
	}
	p, ok := v.(types.Principal)
	return p, ok
}

// agent24AuditAnonymous: R10.2 records principal, credential_id and tenant_id as null for credential_missing
// and credential_invalid, even when Auth had already bound them (the integration credential gate rejects
// platform keys and direct-mode tenant keys after Auth).
func agent24AuditAnonymous(outcome, reason string) bool {
	return outcome == string(agent24CodeUnauthorized) &&
		(reason == "" || reason == "credential_missing" || reason == "credential_invalid")
}

// agent24KnowledgeAuditEntry builds the row and returns the suppressed count it carries, or returns nil when
// the row is anonymous and the token bucket drops it.
func agent24KnowledgeAuditEntry(
	c *gin.Context, started time.Time, limiter *agent24AnonymousAuditLimiter,
) (*types.AuditLog, int64) {
	var facts *agent24AuditFacts
	if v, ok := c.Get(agent24AuditFactsKey); ok {
		facts, _ = v.(*agent24AuditFacts)
	}
	outcome, reason := "", ""
	switch {
	case facts != nil && facts.Outcome != "" &&
		(c.Writer.Status() < http.StatusInternalServerError || !agent24AuditSucceeded(facts.Outcome)):
		outcome, reason = facts.Outcome, facts.Reason
	default: // includes a success noted before a panic that Recovery answered with 500
		outcome, reason = agent24AuditInfer(c)
	}
	path := c.FullPath()
	if path == "" {
		path = c.Request.URL.Path
	}
	anonymous := agent24AuditAnonymous(outcome, reason)
	var suppressed int64
	if anonymous && limiter != nil {
		var ok bool
		if ok, suppressed = limiter.admit(); !ok {
			return nil, 0
		}
	}
	details := agent24AuditDetail(c, facts, outcome, reason, started, anonymous)
	details.Suppressed = suppressed
	raw, err := json.Marshal(details)
	if err != nil {
		raw = []byte("{}")
	}
	var tenant uint64
	if !anonymous {
		if r, ok := agent24CredentialRejectionOf(c); ok {
			tenant = r.TenantID
		} else {
			tenant = agent24AuditTenantColumn(c)
		}
	}
	return &types.AuditLog{
		TenantID:      tenant,
		ActorUserID:   "", // R10.4: not a user row, and not a synthesized one
		Action:        agent24AuditAction(c.Request.URL.Path),
		ScopeType:     agent24AuditScopeType,
		RequestPath:   path,
		RequestMethod: c.Request.Method,
		Outcome:       agent24AuditColumnOutcome(outcome),
		Details:       types.JSON(raw),
	}, suppressed
}

// agent24AuditTenantColumn is the caller's tenant after authentication, otherwise 0 (the column is NOT NULL;
// Details carries null for that case).
func agent24AuditTenantColumn(c *gin.Context) uint64 {
	if _, ok := c.Request.Context().Value(types.CallerContextKey).(types.Caller); !ok {
		return 0
	}
	return types.CallerFromContext(c.Request.Context()).TenantID
}

func agent24AuditDetail(
	c *gin.Context, facts *agent24AuditFacts, outcome, reason string, started time.Time, anonymous bool,
) agent24AuditDetails {
	var echo agent24KnowledgeEcho
	var resource, revision any
	var scope []string
	count := 0
	if facts != nil {
		echo = facts.Echo
		resource, revision = facts.Resource, facts.Revision
		if len(facts.EffectiveScope) > 0 {
			scope = facts.EffectiveScope
		}
		count = facts.ResultCount
	}
	n := c.Writer.Size()
	if n < 0 {
		n = 0
	}
	d := agent24AuditDetails{
		OccurredAt:     time.Now().UTC().Format(time.RFC3339Nano),
		RequestID:      echo.RequestID,
		HTTPRequestID:  agent24AuditHTTPRequestID(c),
		Generation:     echo.Generation,
		EffectiveScope: scope,
		Resource:       resource,
		Revision:       revision,
		Outcome:        outcome,
		Reason:         reason,
		Metering: agent24AuditMetering{
			LatencyMS: time.Since(started).Milliseconds(), ResultCount: count, BytesReturned: n,
		},
	}
	if !anonymous {
		if r, ok := agent24CredentialRejectionOf(c); ok {
			if r.Principal != (types.Principal{}) {
				d.Principal = &agent24AuditPrincipal{Type: r.Principal.Type, ID: r.Principal.ID}
			}
			credentialID := r.CredentialID
			d.CredentialID = &credentialID
			if r.TenantID != 0 {
				tenantID := r.TenantID
				d.TenantID = &tenantID
			}
		} else {
			d.Principal = agent24AuditPrincipalOf(c)
			d.CredentialID = agent24AuditCredentialID(c)
			d.TenantID = agent24AuditTenantDetail(c)
		}
	}
	return d
}

// agent24AuditHTTPRequestID is the access log's request_id field. Empty after sanitizing, or longer than
// agent24RequestIDMaxRunes, is null: the value is not truncated, so it stays identical to that field or is absent.
func agent24AuditHTTPRequestID(c *gin.Context) *string {
	id := secutils.SanitizeForLog(c.GetString(types.RequestIDContextKey.String()))
	if id == "" || utf8.RuneCountInString(id) > agent24RequestIDMaxRunes {
		return nil
	}
	return &id
}

func agent24AuditPrincipalOf(c *gin.Context) *agent24AuditPrincipal {
	p, ok := agent24GinPrincipal(c)
	if !ok {
		return nil
	}
	return &agent24AuditPrincipal{Type: p.Type, ID: p.ID}
}

// agent24AuditCredentialID is "jti:<jti>" when the signed_token carries a jti, otherwise "key:<id>" for a
// tenant API key. Anything else is null.
func agent24AuditCredentialID(c *gin.Context) *string {
	id, ok := types.CredentialIDFromContext(c.Request.Context())
	if !ok {
		return nil
	}
	return &id
}

func agent24AuditTenantDetail(c *gin.Context) *uint64 {
	if _, ok := c.Request.Context().Value(types.CallerContextKey).(types.Caller); !ok {
		return nil
	}
	id := types.CallerFromContext(c.Request.Context()).TenantID
	return &id
}
