// Agent24 fork product patch A24-KB-34 (KB-C2-01c1, WeKnora#133, READONLY-v1 R2.8, UI-BRIDGE-v1 U3.4).

package agent24uisession

import "net/http"

// UIRoute is one UI read entry: GET only, Path is the gin FullPath.
type UIRoute struct {
	Method string
	Path   string
	Op     string
}

// UIReadPrefix is the path prefix of the UI read entries.
const UIReadPrefix = "/api/v1/agent24/knowledge/ui"

// UIReadRoutes is what the server registers and what docs/agent24/ui-routes.json lists. Order is fixed.
var UIReadRoutes = []UIRoute{
	{http.MethodGet, UIReadPrefix + "/knowledge-bases", "kb.list"},
	{http.MethodGet, UIReadPrefix + "/knowledge-bases/:kb_id", "kb.get"},
	{http.MethodGet, UIReadPrefix + "/knowledge-bases/:kb_id/tags", "tag.list"},
	// A24-KB-35: documents and chunks, in this order.
	{http.MethodGet, UIReadPrefix + "/knowledge-bases/:kb_id/documents", "doc.list"},
	{http.MethodGet, UIReadPrefix + "/documents/:knowledge_id", "doc.get"},
	{http.MethodGet, UIReadPrefix + "/documents/:knowledge_id/chunks", "chunk.list"},
	// A24-KB-36: wiki pages. These two stay at the end, in this order.
	{http.MethodGet, UIReadPrefix + "/knowledge-bases/:kb_id/wiki/pages", "wiki.list"},
	{http.MethodGet, UIReadPrefix + "/knowledge-bases/:kb_id/wiki/pages/*slug", "wiki.get"},
}

// UIWriteRoutes is the UI session write entries (A24-KB-49, KB-SOP-02 S5, CONTRACT-OPS-v2 W1.6, design §5). Paths are
// the ones agreed with the host (s5-preconditions P2) and are matched by it byte for byte. docs/agent24/ui-routes.json
// lists UIReadRoutes and then these, in this order. They are not read routes: IsUIReadRoute and UIRouteOp do not
// match them, and UIRouteOps does not name them.
var UIWriteRoutes = []UIRoute{
	{http.MethodGet, UIReadPrefix + "/candidates", "candidate.review"},
	{http.MethodGet, UIReadPrefix + "/candidates/:candidate_id", "candidate.review"},
	{http.MethodPost, UIReadPrefix + "/candidates/:candidate_id/return", "candidate.review"},
	{http.MethodPost, UIReadPrefix + "/candidates/:candidate_id/approve", "candidate.approve"},
	{http.MethodPost, UIReadPrefix + "/documents/:knowledge_id/withdraw", "entry.withdraw"},
}

// UIWriteOps is the operations named by UIWriteRoutes, deduplicated, in UIOps order (A24-KB-49).
func UIWriteOps() []string {
	seen := make(map[string]struct{}, len(UIWriteRoutes))
	out := make([]string, 0, len(UIWriteRoutes))
	for _, op := range UIOps {
		for _, rt := range UIWriteRoutes {
			if rt.Op != op {
				continue
			}
			if _, ok := seen[op]; ok {
				break
			}
			seen[op] = struct{}{}
			out = append(out, op)
			break
		}
	}
	return out
}

// IsUIReadRoute reports an exact method and FullPath match. A prefix is not enough.
func IsUIReadRoute(method, fullPath string) bool {
	_, ok := UIRouteOp(method, fullPath)
	return ok
}

// UIRouteOp returns the operation name for an exact method and FullPath match.
func UIRouteOp(method, fullPath string) (string, bool) {
	for _, rt := range UIReadRoutes {
		if rt.Method == method && rt.Path == fullPath {
			return rt.Op, true
		}
	}
	return "", false
}

// UIRouteOps is the operations named by UIReadRoutes, deduplicated, in UIOps order.
// The session JWT carries this list: it is the subset ui-routes.json actually releases.
func UIRouteOps() []string {
	seen := make(map[string]struct{}, len(UIReadRoutes))
	out := make([]string, 0, len(UIReadRoutes))
	for _, op := range UIOps {
		for _, rt := range UIReadRoutes {
			if rt.Op != op {
				continue
			}
			if _, ok := seen[op]; ok {
				break
			}
			seen[op] = struct{}{}
			out = append(out, op)
			break
		}
	}
	return out
}
