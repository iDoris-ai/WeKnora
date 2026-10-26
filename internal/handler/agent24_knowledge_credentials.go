// Agent24 fork product patch A24-KB-05 (KB-C1-01 slice 2, CONTRACT-READONLY-v1 R1.2/R1.5).
//
// In integration mode a credential gate runs right after middleware.Auth: it
// rejects platform API keys and tenant API keys whose tenant uses the direct
// (X-External-User-ID) principal mode. JWT users and other tenant keys pass.

package handler

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const agent24KnowledgePathPrefix = "/api/v1/agent24/knowledge/"

// Agent24IntegrationCredentialGate rejects credentials not accepted in integration mode. Reaching it means
// middleware.Auth let the request through, so it first releases the writer held by
// Agent24KnowledgeAuthEnvelope (A24-KB-11).
//
// The jti list is consulted only in integration mode (this gate is mounted only then), only after the
// signature was accepted (Auth has let the request through, which is why a jti is on the context), and a
// check that cannot be answered is a refusal.
func Agent24IntegrationCredentialGate(revocations interfaces.Agent24CredentialRevocationStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		agent24ReleaseHeldWriter(c)
		if !agent24CredentialAccepted(c) {
			if strings.HasPrefix(c.Request.URL.Path, agent24KnowledgePathPrefix) {
				writeAgent24KnowledgeError(c, agent24Unauthorized("credential_invalid"), agent24KnowledgeEcho{})
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized,
				gin.H{"error": "Unauthorized: credential not accepted in integration mode"})
			return
		}
		ctx := c.Request.Context()
		jti, ok := types.Agent24TokenJTIFromContext(ctx)
		if !ok {
			c.Next()
			return
		}
		tenantID := types.CallerFromContext(ctx).TenantID
		if revocations == nil {
			agent24RevocationCheckFailed(c, tenantID, nil)
			return
		}
		revoked, err := revocations.IsRevoked(ctx, tenantID, jti, time.Now())
		if err != nil {
			agent24RevocationCheckFailed(c, tenantID, err)
			return
		}
		if !revoked {
			c.Next()
			return
		}
		if strings.HasPrefix(c.Request.URL.Path, agent24KnowledgePathPrefix) {
			writeAgent24KnowledgeError(c, agent24Unauthorized("credential_revoked"), agent24KnowledgeEcho{})
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized,
			gin.H{"error": "Unauthorized: credential not accepted in integration mode"})
	}
}

// agent24RevocationCheckFailed refuses the request when the jti list cannot be read. err is nil when the
// store itself is nil; the log records that nil and never the jti or the error text.
func agent24RevocationCheckFailed(c *gin.Context, tenantID uint64, err error) {
	logger.Warnf(c.Request.Context(),
		"[agent24] revocation check failed: tenant=%d error_len=%d error_type=%T",
		tenantID, len(fmt.Sprint(err)), err)
	if strings.HasPrefix(c.Request.URL.Path, agent24KnowledgePathPrefix) {
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "credential check unavailable"})
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
