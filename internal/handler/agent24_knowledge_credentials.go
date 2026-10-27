// Agent24 fork product patch A24-KB-05 (KB-C1-01 slice 2, CONTRACT-READONLY-v1 R1.2/R1.5).
//
// In integration mode a credential gate runs right after middleware.Auth: it
// rejects platform API keys and tenant API keys whose tenant uses the direct
// (X-External-User-ID) principal mode. JWT users and other tenant keys pass.

package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/types"
)

const agent24KnowledgePathPrefix = "/api/v1/agent24/knowledge/"

// Agent24IntegrationCredentialGate rejects credentials not accepted in integration mode. Reaching it means
// middleware.Auth let the request through, so it first releases the writer held by
// Agent24KnowledgeAuthEnvelope (A24-KB-11).
func Agent24IntegrationCredentialGate() gin.HandlerFunc {
	return func(c *gin.Context) {
		agent24ReleaseHeldWriter(c)
		if agent24CredentialAccepted(c) {
			c.Next()
			return
		}
		if strings.HasPrefix(c.Request.URL.Path, agent24KnowledgePathPrefix) {
			writeAgent24KnowledgeError(c, agent24Unauthorized("credential_invalid"), agent24KnowledgeEcho{})
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized,
			gin.H{"error": "Unauthorized: credential not accepted in integration mode"})
	}
}

func agent24CredentialAccepted(c *gin.Context) bool {
	ctx := c.Request.Context()
	scope, ok := types.TenantAPIKeyScopeFromContext(ctx)
	if !ok {
		return true
	}
	if scope.IsPlatform() {
		return false
	}
	tenant, _ := ctx.Value(types.TenantInfoContextKey).(*types.Tenant)
	if tenant == nil {
		return false
	}
	// R1.6: in integration mode a signed_token credential must carry a jti. Without one the
	// credential is not accepted, and the gate's existing credential_invalid branch answers.
	if tenant.APIPrincipalConfig != nil && tenant.APIPrincipalConfig.Mode == types.APIPrincipalModeSignedToken {
		if _, ok := types.Agent24TokenJTIFromContext(ctx); !ok {
			return false
		}
	}
	return tenant.APIPrincipalConfig == nil || tenant.APIPrincipalConfig.Mode != types.APIPrincipalModeDirect
}
