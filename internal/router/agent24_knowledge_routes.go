// Package router registers the HTTP API routes.
package router

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
)

// RegisterAgent24KnowledgeRoutes registers the Agent24 knowledge integration
// endpoints (fork patches A24-KB-02, A24-KB-10, A24-KB-15, A24-KB-32, A24-KB-33, A24-KB-34, A24-KB-43).
// A nil handler skips its route.
func RegisterAgent24KnowledgeRoutes(r *gin.RouterGroup, h *handler.Agent24KnowledgeReadyHandler,
	s *handler.Agent24KnowledgeSearchHandler, rd *handler.Agent24KnowledgeReadHandler,
	tickets *handler.Agent24KnowledgeUITicketsHandler, revocations *handler.Agent24KnowledgeUIRevocationsHandler,
	exchange *handler.Agent24KnowledgeUIExchangeHandler, session *handler.Agent24KnowledgeUISessionHandler,
	read *handler.Agent24KnowledgeUIReadHandler, // A24-KB-34
	candidates *handler.Agent24KnowledgeCandidatesHandler, // A24-KB-43
	g *rbacGuards,
) {
	if h != nil {
		g.apiKeyRoute(r, http.MethodPost, "/agent24/knowledge/ready", apiKeyRetrieve(apiKeyAny()), g.Viewer(), h.Ready)
	}
	if s != nil {
		g.apiKeyRoute(r, http.MethodPost, "/agent24/knowledge/search", apiKeyRetrieve(apiKeyAny()), g.Viewer(),
			s.Search)
	}
	if rd != nil {
		g.apiKeyRoute(r, http.MethodPost, "/agent24/knowledge/read", apiKeyRetrieve(apiKeyAny()), g.Viewer(), rd.Read)
	}
	// A24-KB-43: candidate.propose with C. Registered only when the handler is non-nil, which is integration mode.
	// The route asks for no capability: the handler checks kb_candidate_propose itself, after the shape (W1.9 ②
	// before ③), and answers in the contract envelope. A propose-only key is valid (W2.5: the target in E is all
	// candidate.propose needs).
	if candidates != nil {
		g.apiKeyRoute(r, http.MethodPost, "/agent24/knowledge/candidates", apiKeyAny(), g.Viewer(), candidates.Submit)
		g.apiKeyRoute(r, http.MethodPost, "/agent24/knowledge/candidates/withdraw", apiKeyAny(), g.Viewer(),
			candidates.Withdraw)
		g.apiKeyRoute(r, http.MethodPost, "/agent24/knowledge/candidates/get", apiKeyAny(), g.Viewer(), candidates.Get)
	}
	// A24-KB-32: registered only when the handler is non-nil, which is when the UI session key is configured.
	if tickets != nil {
		g.apiKeyRoute(r, http.MethodPost, "/agent24/knowledge/ui/tickets", apiKeyRetrieve(apiKeyAny()), g.Viewer(),
			tickets.Issue)
	}
	if revocations != nil {
		g.apiKeyRoute(r, http.MethodPost, "/agent24/knowledge/ui/revocations",
			apiKeyRetrieve(apiKeyAny()), g.Viewer(), revocations.Revoke)
	}
	// A24-KB-33: no API key and no role. The ticket or the session JWT is the credential.
	// Registered only when the handler is non-nil, which is when the UI session key is configured.
	if exchange != nil {
		r.POST("/agent24/knowledge/ui/exchange", exchange.Exchange)
	}
	if session != nil {
		r.GET("/agent24/knowledge/ui/session", session.Probe)
	}
	// A24-KB-34: no API key and no role. The session JWT plus the view proof is the credential.
	// Registered only when the handler is non-nil, which is when the UI session key is configured.
	// Paths come from UIReadRoutes so the registered FullPath stays the table's path.
	if read != nil {
		// A24-KB-38: with UI origins configured, each UI read path also gets an OPTIONS route so the
		// preflight has a FullPath. agent24UICORS answers it before Auth; the handler is never reached.
		preflight := len(agent24UIOrigins()) > 0
		for _, rt := range agent24uisession.UIReadRoutes {
			if rt.Method != http.MethodGet || !strings.HasPrefix(rt.Path, "/api/v1/") {
				panic("agent24 ui read route is not a GET under /api/v1: " + rt.Method + " " + rt.Path)
			}
			next := agent24UIReadDispatch(read, rt.Op)
			if next == nil {
				panic("agent24 ui read route has no handler: " + rt.Op)
			}
			r.GET(strings.TrimPrefix(rt.Path, "/api/v1"), read.Guard(rt.Op), next)
			if preflight {
				r.OPTIONS(strings.TrimPrefix(rt.Path, "/api/v1"), agent24UIPreflightUnreachable)
			}
		}
	}
	// A24-KB-50 (KB-SOP-02 S5b): the UI write entries, each behind WriteGuard. Only with the UI read routes (session
	// key, integration mode) and the candidate routes, i.e. when handler.Agent24UIWriteRoutesEnabled.
	if read != nil && candidates != nil {
		handler.RegisterAgent24UIWriteRoutes(r, read, candidates, len(agent24UIOrigins()) > 0,
			agent24UIPreflightUnreachable)
	}
}

func agent24UIReadDispatch(read *handler.Agent24KnowledgeUIReadHandler, op string) gin.HandlerFunc {
	switch op {
	case "kb.list":
		return read.ListKnowledgeBases
	case "kb.get":
		return read.GetKnowledgeBase
	case "tag.list":
		return read.ListTags
	case "doc.list": // A24-KB-35
		return read.ListDocuments
	case "doc.get": // A24-KB-35
		return read.GetDocument
	case "chunk.list": // A24-KB-35
		return read.ListChunks
	case "wiki.list": // A24-KB-36
		return read.ListWikiPages
	case "wiki.get": // A24-KB-36
		return read.GetWikiPage
	default:
		return nil
	}
}

// RegisterAgent24KnowledgeAuditRoutes registers the read paths for the agent24_knowledge audit rows (fork patch
// A24-KB-19); a nil handler skips them. The tenant route repeats the per-tenant group guard (PathTenantMatch) and
// is undeclared for API keys; the system route sits behind SystemAdmin and admits platform keys holding
// system_audit_read. Both handlers check the caller again.
func RegisterAgent24KnowledgeAuditRoutes(r *gin.RouterGroup, h *handler.AuditLogHandler, g *rbacGuards) {
	if h == nil {
		return
	}
	tenantByID := r.Group("/tenants/:id", g.PathTenantMatch())
	tenantByID.GET("/agent24/knowledge/audit", g.Admin(), h.ListAgent24KnowledgeAuditTenant)
	admin := r.Group("/system/admin", g.SystemAdmin())
	g.apiKeyRoute(admin, http.MethodGet, "/agent24/knowledge/audit",
		apiKeyPlatform(types.APIKeyCapabilitySystemAuditRead), h.ListAgent24KnowledgeAuditSystem)
}
