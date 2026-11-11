// Agent24 fork product patch A24-KB-49 (KB-SOP-02 S5a, WeKnora 原 #146, CONTRACT-OPS-v2 W1.6, W1.9, design §5,
// s5-preconditions P2–P3): the guard in front of the UI session write entries (agent24uisession.UIWriteRoutes).
// S5a adds the guard only; S5b registers the routes and lets the exchange issue the write operations.
//
// The guard verifies the session exactly as the read guard does, adapts the transport to the JSON body the
// candidate handlers already parse, requires the route's operation to be in the session JWT's Ops, recomputes E,
// and hands the request to the handler with the session's principal, tenant and credential scope (narrowed to E)
// on the context. Everything after that — operation_disabled, W1.9 ③(a) on side A, ③(b), replay, the operation's
// own checks — stays in the handler (evidence decision 2026-11-11-s5-ui-write-guard, Q1 = A, Q2 = B).

package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/types"
)

// agent24UIWriteIDParam is the path parameter a write route names its target with: "" (the review list),
// "candidate_id" or "knowledge_id". It is the last ":name" segment of the route path, after which nothing but a
// fixed segment ("return", "approve", "withdraw") may follow.
func agent24UIWriteIDParam(rt agent24uisession.UIRoute) string {
	panic("TODO(grok): A24-KB-49 agent24UIWriteIDParam")
}

// WriteGuard is the guard for one UI write route. Order (the first that fails answers):
//  1. Cache-Control: no-store; the ticket and the operation are noted for the audit, as in Guard.
//  2. agent24UIVerifySession — the same refusals as a read entry (401 unauthorized with credential_invalid,
//     credential_expired or credential_revoked; internal on a storage failure).
//  3. transport shape (agent24UIWriteBody) → invalid_request.
//  4. rt.Op not in the session JWT's Ops → 403 forbidden/operation_not_allowed (P3; W1.9 ③(a): the session is
//     the credential and the operation is outside its range). Not operation_disabled, which is what a read entry
//     answers for an operation it does not carry.
//  5. E = A ∩ C_now ∩ the JWT scope (agent24UIWriteScope). An empty E is not refused here (Q2 = B).
//  6. The context gets Principal (claims.Subject), Caller (claims.TenantID) and the TenantAPIKeyScope of the
//     ticket's credential with FullAccess false and KnowledgeBaseIDs = E; then c.Next().
//
// A refusal writes the contract error with an empty echo: a UI write has no generation (W1.4).
func (h *Agent24KnowledgeUIReadHandler) WriteGuard(rt agent24uisession.UIRoute) gin.HandlerFunc {
	panic("TODO(grok): A24-KB-49 WriteGuard")
}

// agent24UIWriteBody adapts the transport to the JSON object body the handlers parse (Q1 = A). It returns a
// non-nil error to answer with and leaves c.Request.Body readable by the handler.
//
// GET (the review list and one candidate):
//   - a non-empty request body → invalid_request;
//   - the query may carry request_id, and for the list after and limit; any other key, or a key given more than
//     once → invalid_request;
//   - the body becomes a JSON object with exactly the keys present: "request_id" and "after" as JSON strings,
//     "limit" as the query value written verbatim as a JSON number token — so "1.0" or "1e0" reach the handler as
//     such and are refused there — and a value that is not a JSON number token → invalid_request; for one
//     candidate also "candidate_id" from the path.
//
// POST (return, approve, withdraw):
//   - the body is read (agent24ReadBody's limit) and given back to the handler byte for byte; it is never
//     re-encoded, so the handler's duplicate-key, NUL and lone-surrogate checks and its stripping of approved,
//     author and approver (with the audit) see what the client sent;
//   - a body that does not decode as a JSON object is passed through, and the handler refuses it;
//   - otherwise the object must carry the route's id parameter as a JSON string equal to the path value; missing,
//     not a string, or different → invalid_request (W8.5: approve's body must carry candidate_id).
func agent24UIWriteBody(c *gin.Context, rt agent24uisession.UIRoute) *agent24KnowledgeError {
	panic("TODO(grok): A24-KB-49 agent24UIWriteBody")
}

// agent24UIWriteScope is E for a write entry: like agent24UIReadScope, except that an empty E is a result, not a
// refusal. Full access or an empty allow-list on the ticket's credential gives an empty E without Resolve
// (R2.2–R2.4: never "all"); so does a JWT scope that resolves to nothing. ok is false only when the response has
// been written (the client went away, which writes nothing, or internal).
func (h *Agent24KnowledgeUIReadHandler) agent24UIWriteScope(
	c *gin.Context, claims *agent24uisession.Claims, scope types.TenantAPIKeyScope,
) (e []string, ok bool) {
	panic("TODO(grok): A24-KB-49 agent24UIWriteScope")
}

// agent24UIWriteScopeFor is the scope a write handler reads: the ticket credential's scope with full access off and
// the knowledge-base list replaced by e (possibly empty, never nil-as-all). KeyID and ScopeType are kept, so
// CredentialIDFromContext names the ticket's credential and the authz_version covers it.
func agent24UIWriteScopeFor(scope types.TenantAPIKeyScope, e []string) types.TenantAPIKeyScope {
	panic("TODO(grok): A24-KB-49 agent24UIWriteScopeFor")
}
