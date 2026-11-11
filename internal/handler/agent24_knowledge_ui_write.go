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
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/types"
)

// agent24UIWriteIDParam is the path parameter a write route names its target with: "" (the review list),
// "candidate_id" or "knowledge_id". It is the last ":name" segment of the route path, after which nothing but a
// fixed segment ("return", "approve", "withdraw") may follow.
func agent24UIWriteIDParam(rt agent24uisession.UIRoute) string {
	name := ""
	for _, seg := range strings.Split(rt.Path, "/") {
		rest, ok := strings.CutPrefix(seg, ":")
		if !ok {
			continue
		}
		name = rest
	}
	return name
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
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		knowledgeID := ""
		if agent24UIWriteIDParam(rt) == "knowledge_id" {
			knowledgeID = c.Param("knowledge_id")
		}
		claims, ticket, scope, kerr := agent24UIVerifySession(
			c.Request.Context(), h.deps, c.Request.Header, time.Now())
		if ticket != nil {
			agent24UINoteTicket(c, ticket, "", false)
		}
		// NoteTicket replaces the resource map, so op and the resource id are merged after it.
		agent24UIReadNoteOp(c, rt.Op, "", knowledgeID)
		if kerr != nil {
			writeAgent24KnowledgeError(c, kerr, agent24KnowledgeEcho{})
			return
		}
		if claims == nil {
			writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
			return
		}
		if berr := agent24UIWriteBody(c, rt); berr != nil {
			writeAgent24KnowledgeError(c, berr, agent24KnowledgeEcho{})
			return
		}
		if !agent24UIOpEnabled(claims.Ops, rt.Op) {
			writeAgent24KnowledgeError(c, agent24Forbidden("operation_not_allowed"), agent24KnowledgeEcho{})
			return
		}
		e, ok := h.agent24UIWriteScope(c, claims, scope)
		if !ok {
			return
		}
		principal, pok := agent24PrincipalFromStorageID(claims.Subject)
		if !pok {
			writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
			return
		}
		ctx := types.WithPrincipal(c.Request.Context(), principal)
		ctx = types.WithCaller(ctx, types.Caller{TenantID: claims.TenantID})
		ctx = types.WithTenantAPIKeyScope(ctx, agent24UIWriteScopeFor(scope, e))
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
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
	switch c.Request.Method {
	case http.MethodGet:
		return agent24UIWriteGetBody(c, agent24UIWriteIDParam(rt))
	case http.MethodPost:
		return agent24UIWritePostBody(c, agent24UIWriteIDParam(rt))
	default:
		return agent24InvalidRequest()
	}
}

// agent24UIWriteGetBody builds the JSON object a review GET handler parses.
// The list is the route with no id parameter; one candidate is any other GET.
func agent24UIWriteGetBody(c *gin.Context, idParam string) *agent24KnowledgeError {
	if kerr := agent24UIWriteRequireEmptyBody(c); kerr != nil {
		return kerr
	}
	list := idParam == ""
	q := c.Request.URL.Query()
	if kerr := agent24UIWriteQueryKeys(q, list); kerr != nil {
		return kerr
	}
	var fields [][]byte
	if vs, present := q["request_id"]; present {
		field, kerr := agent24UIWriteStringField("request_id", vs[0])
		if kerr != nil {
			return kerr
		}
		fields = append(fields, field)
	}
	if list {
		if vs, present := q["after"]; present {
			field, kerr := agent24UIWriteStringField("after", vs[0])
			if kerr != nil {
				return kerr
			}
			fields = append(fields, field)
		}
		if vs, present := q["limit"]; present {
			if !agent24UIWriteLimitToken(vs[0]) {
				return agent24InvalidRequest()
			}
			field, kerr := agent24UIWriteRawField("limit", []byte(vs[0]))
			if kerr != nil {
				return kerr
			}
			fields = append(fields, field)
		}
	} else {
		field, kerr := agent24UIWriteStringField(idParam, c.Param(idParam))
		if kerr != nil {
			return kerr
		}
		fields = append(fields, field)
	}
	agent24UIWriteInstallBody(c, agent24UIWriteObject(fields))
	return nil
}

// agent24UIWritePostBody puts the same bytes back on the request. A JSON object must name the path id
// as a JSON string; a body that is not a JSON object is left unchanged.
func agent24UIWritePostBody(c *gin.Context, idParam string) *agent24KnowledgeError {
	body, ok := agent24ReadBody(c)
	if !ok {
		return agent24InvalidRequest()
	}
	agent24UIWriteInstallBody(c, body)
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return nil
	}
	raw, present := fields[idParam]
	if !present {
		return agent24InvalidRequest()
	}
	value, isString := agent24UIWriteJSONStringValue(raw)
	if !isString || value != c.Param(idParam) {
		return agent24InvalidRequest()
	}
	return nil
}

// agent24UIWriteRequireEmptyBody accepts a body of length 0. A nil body has no bytes; reading it panics.
func agent24UIWriteRequireEmptyBody(c *gin.Context) *agent24KnowledgeError {
	if c.Request.Body == nil {
		return nil
	}
	// Review fix: the same size limit as a POST body, so a GET cannot make the guard buffer an unbounded body.
	b, ok := agent24ReadBody(c)
	if !ok || len(b) != 0 {
		return agent24InvalidRequest()
	}
	return nil
}

// agent24UIWriteQueryKeys accepts each allowed key once. The list may also carry after and limit.
func agent24UIWriteQueryKeys(q map[string][]string, list bool) *agent24KnowledgeError {
	for key, values := range q {
		switch {
		case key == "request_id":
		case list && (key == "after" || key == "limit"):
		default:
			return agent24InvalidRequest()
		}
		if len(values) != 1 {
			return agent24InvalidRequest()
		}
	}
	return nil
}

// agent24UIWriteLimitToken reports a JSON number token. json.Valid accepts any JSON value, so the
// first byte has to be '-' or a digit; the token is copied unchanged, and "1.0" and "1e0" stay as written.
func agent24UIWriteLimitToken(raw string) bool {
	if raw == "" {
		return false
	}
	if raw[0] != '-' && (raw[0] < '0' || raw[0] > '9') {
		return false
	}
	return json.Valid([]byte(raw))
}

func agent24UIWriteStringField(key, value string) ([]byte, *agent24KnowledgeError) {
	// Review fix: json.Marshal would replace invalid UTF-8 with U+FFFD, so the handler would check a value the client
	// never sent. Refuse it here instead.
	if !utf8.ValidString(value) {
		return nil, agent24InvalidRequest()
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, agent24InvalidRequest()
	}
	return agent24UIWriteRawField(key, raw)
}

func agent24UIWriteRawField(key string, raw []byte) ([]byte, *agent24KnowledgeError) {
	name, err := json.Marshal(key)
	if err != nil {
		return nil, agent24InvalidRequest()
	}
	buf := make([]byte, 0, len(name)+1+len(raw))
	buf = append(buf, name...)
	buf = append(buf, ':')
	buf = append(buf, raw...)
	return buf, nil
}

func agent24UIWriteObject(fields [][]byte) []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, field := range fields {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(field)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

func agent24UIWriteInstallBody(c *gin.Context, b []byte) {
	c.Request.Body = io.NopCloser(bytes.NewReader(b))
	c.Request.ContentLength = int64(len(b))
}

// agent24UIWriteJSONStringValue reads a JSON string. Unmarshal into a Go string accepts null and leaves "".
func agent24UIWriteJSONStringValue(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

// agent24UIWriteScope is E for a write entry: like agent24UIReadScope, except that an empty E is a result, not a
// refusal. Full access or an empty allow-list on the ticket's credential gives an empty E without Resolve
// (R2.2–R2.4: never "all"); so does a JWT scope that resolves to nothing. ok is false only when the response has
// been written (the client went away, which writes nothing, or internal).
func (h *Agent24KnowledgeUIReadHandler) agent24UIWriteScope(
	c *gin.Context, claims *agent24uisession.Claims, scope types.TenantAPIKeyScope,
) (e []string, ok bool) {
	if scope.FullAccess || len(scope.KnowledgeBaseIDs) == 0 {
		return nil, true
	}
	if !agent24UIReadScopeReady(h.scope) {
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return nil, false
	}
	ctx := types.WithCaller(c.Request.Context(), types.Caller{TenantID: claims.TenantID})
	ctx = types.WithTenantAPIKeyScope(ctx, scope)
	kbs, err := h.scope.Resolve(ctx, claims.Scope.KnowledgeBaseIDs)
	if err != nil {
		kerr, gone := agent24StorageError(ctx, err)
		if gone {
			agent24NoteCancelled(c, agent24KnowledgeEcho{}, nil)
			c.Abort()
			return nil, false
		}
		writeAgent24KnowledgeError(c, kerr, agent24KnowledgeEcho{})
		return nil, false
	}
	if len(kbs) == 0 {
		return []string{}, true
	}
	return agent24CollectedKBIDs(kbs), true
}

// agent24UIWriteScopeFor is the scope a write handler reads: the ticket credential's scope with full access off and
// the knowledge-base list replaced by e (possibly empty, never nil-as-all). KeyID and ScopeType are kept, so
// CredentialIDFromContext names the ticket's credential and the authz_version covers it.
func agent24UIWriteScopeFor(scope types.TenantAPIKeyScope, e []string) types.TenantAPIKeyScope {
	out := scope
	out.FullAccess = false
	if e == nil {
		out.KnowledgeBaseIDs = types.StringArray{}
	} else {
		out.KnowledgeBaseIDs = types.StringArray(e)
	}
	return out
}
