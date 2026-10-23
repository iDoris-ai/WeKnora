// Agent24 fork product patch A24-KB-11 (CONTRACT-READONLY-v1 R6.1, WeKnora#63).
//
// In integration mode middleware.Auth answers its early rejections in the upstream shape. For the knowledge
// endpoints a wrapper installed right before Auth holds back whatever Auth writes; the credential gate, which
// runs right after Auth, hands the real writer back once Auth has let the request through. A request that
// comes back with the writer still held was stopped by Auth, and its rejection is rewritten into the contract
// envelope, echo null (rejected before the request was parsed). Successful responses are never buffered.

package handler

import (
	"bufio"
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// agent24HeldWriter keeps the status and body written through it and sends nothing. Header() is the real
// header map: nothing reaches the client until a status or body is written through the real writer.
type agent24HeldWriter struct {
	gin.ResponseWriter // the real writer
	status             int
	size               int
}

func (w *agent24HeldWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *agent24HeldWriter) WriteHeaderNow() {}

func (w *agent24HeldWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.size += len(b)
	return len(b), nil
}

func (w *agent24HeldWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (w *agent24HeldWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *agent24HeldWriter) Size() int     { return w.size }
func (w *agent24HeldWriter) Written() bool { return w.status != 0 }
func (w *agent24HeldWriter) Flush()        {}

func (w *agent24HeldWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, http.ErrNotSupported
}

// Agent24KnowledgeAuthEnvelope must be installed, in integration mode only, right before middleware.Auth;
// Agent24IntegrationCredentialGate (right after Auth) releases the writer.
func Agent24KnowledgeAuthEnvelope() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !strings.HasPrefix(c.Request.URL.Path, agent24KnowledgePathPrefix) {
			c.Next()
			return
		}
		held := &agent24HeldWriter{ResponseWriter: c.Writer}
		c.Writer = held
		defer func() { // a panic below must reach Recovery with the real writer
			if c.Writer == held {
				c.Writer = held.ResponseWriter
			}
		}()
		c.Next()
		if c.Writer != held {
			return // the gate released the writer: Auth let the request through
		}
		c.Writer = held.ResponseWriter
		writeAgent24KnowledgeError(c, agent24AuthRejection(c, held.status), agent24KnowledgeEcho{})
	}
}

// agent24ReleaseHeldWriter gives back the real writer if the auth envelope is holding it.
func agent24ReleaseHeldWriter(c *gin.Context) {
	if held, ok := c.Writer.(*agent24HeldWriter); ok {
		c.Writer = held.ResponseWriter
	}
}

// agent24AuthRejection maps an Auth rejection status to the contract. Only unauthorized, internal and
// invalid_request may echo null (R6.1), so every credential or workspace refusal (401, 403, 409) is
// unauthorized: credential_missing when the request presented no credential, credential_invalid otherwise.
// A malformed header (400) is invalid_request. Anything else, including a rejection that wrote nothing, is
// internal.
func agent24AuthRejection(c *gin.Context, status int) *agent24KnowledgeError {
	switch status {
	case http.StatusBadRequest:
		return agent24InvalidRequest()
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict:
		if c.GetHeader("Authorization") == "" && c.GetHeader("X-API-Key") == "" {
			return agent24Unauthorized("credential_missing")
		}
		return agent24Unauthorized("credential_invalid")
	}
	return agent24Internal()
}
