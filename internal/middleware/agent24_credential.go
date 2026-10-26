// Agent24 fork product patch A24-KB-18 (KB-C1-03 C3-b, WeKnora#41, CONTRACT-READONLY-v1 R1.6/R10.2).

package middleware

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/types"
)

func noteAgent24CredentialRejection(c *gin.Context, r types.Agent24CredentialRejection) {
	c.Set(types.Agent24CredentialRejectionKey, r)
}
