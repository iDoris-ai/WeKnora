// Agent24 fork product patch A24-KB-32 (KB-C2-01b1, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.1, U2.13, U3.7).

package handler

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// agent24KnowledgeUIPathPrefix is the prefix of the UI session endpoints (tickets, revocations, and the
// exchange and session probe of the next slice).
const agent24KnowledgeUIPathPrefix = "/api/v1/agent24/knowledge/ui/"

// Agent24KnowledgeUINoStore sets Cache-Control: no-store on every response under the UI session prefix
// (U2.5). It runs before Auth, so refusals that Auth, the envelope or the credential gate write before the
// handler is reached carry the header as well.
func Agent24KnowledgeUINoStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, agent24KnowledgeUIPathPrefix) {
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	}
}
