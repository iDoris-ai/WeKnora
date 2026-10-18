// Agent24 fork product patch A24-KB-03 (CONTRACT-READONLY-v1 §6).
//
// Error envelope of the Agent24 knowledge integration endpoints. It is a
// separate envelope used only by those endpoints; the upstream error envelope
// is not changed. Messages are fixed per code and never carry raw error text,
// queries, content, credentials or hints about resource existence (R6.3).

package handler

import "github.com/gin-gonic/gin"

type agent24KnowledgeCode string

const (
	agent24CodeInvalidRequest agent24KnowledgeCode = "invalid_request"
	agent24CodeUnauthorized   agent24KnowledgeCode = "unauthorized"
	agent24CodeForbidden      agent24KnowledgeCode = "forbidden"
	agent24CodeNotFound       agent24KnowledgeCode = "not_found"
	agent24CodeStale          agent24KnowledgeCode = "stale"
	agent24CodeTimeout        agent24KnowledgeCode = "timeout"
	agent24CodeUnavailable    agent24KnowledgeCode = "unavailable"
	agent24CodeInternal       agent24KnowledgeCode = "internal"
)

var agent24KnowledgeCodeMeta = map[agent24KnowledgeCode]struct {
	status  int
	message string
}{
	agent24CodeInvalidRequest: {400, "invalid request"},
	agent24CodeUnauthorized:   {401, "unauthorized"},
	agent24CodeForbidden:      {403, "forbidden"},
	agent24CodeNotFound:       {404, "not found"},
	agent24CodeStale:          {409, "revision is not current"},
	agent24CodeTimeout:        {504, "deadline exceeded"},
	agent24CodeUnavailable:    {503, "service unavailable"},
	agent24CodeInternal:       {500, "internal error"},
}

var (
	agent24UnauthorizedReasons = map[string]bool{
		"credential_missing": true, "credential_invalid": true,
		"credential_expired": true, "credential_revoked": true,
	}
	agent24ForbiddenReasons = map[string]bool{
		"empty_scope": true, "operation_not_allowed": true,
		"operation_disabled": true, "policy_denied": true,
	}
	// Value: retryable.
	agent24UnavailableReasons = map[string]bool{
		"not_ready": true, "dependency": true, "overloaded": true,
		"index_building": true, "index_pending": true, "unsupported_kb_type": false, "index_failed": false,
	}
)

// agent24KnowledgeError is a classified error; it never holds raw error text.
type agent24KnowledgeError struct {
	Code      agent24KnowledgeCode
	Reason    string
	Retryable bool
	Extra     map[string]any
}

func agent24InvalidRequest() *agent24KnowledgeError {
	return &agent24KnowledgeError{Code: agent24CodeInvalidRequest}
}

func agent24UnsupportedProtocolVersion(supported []int) *agent24KnowledgeError {
	return &agent24KnowledgeError{
		Code: agent24CodeInvalidRequest, Reason: "unsupported_protocol_version",
		Extra: map[string]any{"supported": append([]int{}, supported...)},
	}
}

func agent24Unauthorized(reason string) *agent24KnowledgeError {
	if !agent24UnauthorizedReasons[reason] {
		return agent24Internal()
	}
	return &agent24KnowledgeError{Code: agent24CodeUnauthorized, Reason: reason}
}

func agent24Forbidden(reason string) *agent24KnowledgeError {
	if !agent24ForbiddenReasons[reason] {
		return agent24Internal()
	}
	return &agent24KnowledgeError{Code: agent24CodeForbidden, Reason: reason}
}

func agent24NotFound() *agent24KnowledgeError {
	return &agent24KnowledgeError{Code: agent24CodeNotFound}
}

func agent24Timeout() *agent24KnowledgeError {
	return &agent24KnowledgeError{Code: agent24CodeTimeout, Retryable: true}
}

// agent24Unavailable: empty reason means the default (dependency) and is omitted.
func agent24Unavailable(reason string) *agent24KnowledgeError {
	if reason == "" {
		return &agent24KnowledgeError{Code: agent24CodeUnavailable, Retryable: true}
	}
	retryable, ok := agent24UnavailableReasons[reason]
	if !ok {
		return agent24Internal()
	}
	return &agent24KnowledgeError{Code: agent24CodeUnavailable, Reason: reason, Retryable: retryable}
}

func agent24Internal() *agent24KnowledgeError {
	return &agent24KnowledgeError{Code: agent24CodeInternal}
}

// agent24KnowledgeEcho holds request_id/generation echoed back; nil means null (R6.1).
type agent24KnowledgeEcho struct {
	RequestID  *string
	Generation *int64
}

func writeAgent24KnowledgeError(c *gin.Context, e *agent24KnowledgeError, echo agent24KnowledgeEcho) {
	if e == nil {
		e = agent24Internal()
	}
	meta, ok := agent24KnowledgeCodeMeta[e.Code]
	if !ok {
		e = agent24Internal()
		meta = agent24KnowledgeCodeMeta[e.Code]
	}
	details := map[string]any{}
	for k, v := range e.Extra {
		details[k] = v
	}
	details["retryable"] = e.Retryable
	details["request_id"] = echo.RequestID
	details["generation"] = echo.Generation
	delete(details, "reason")
	if e.Reason != "" {
		details["reason"] = e.Reason
	}
	c.AbortWithStatusJSON(meta.status, gin.H{"error": gin.H{
		"code": e.Code, "message": meta.message, "details": details,
	}})
}
