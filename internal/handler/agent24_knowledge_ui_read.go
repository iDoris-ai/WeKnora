// Agent24 fork product patch A24-KB-34 (KB-C2-01c1, WeKnora#133, READONLY-v1 R2.8, UI-BRIDGE-v1 U3.4).
//
// GET has no request body, so request_id and generation are JSON null (agent24KnowledgeEcho{}).
// READONLY R6.1's null cases do not cover GET. This is the UI read convention and a contract-revision candidate.

package handler

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	agent24UITagPageDefault     = 1
	agent24UITagPageSizeDefault = 50
	agent24UITagPageSizeMax     = 200
)

// agent24UIReadStateKey is the request-context key for *agent24UIReadState. Handlers behind Guard read it.
type agent24UIReadStateKey struct{}

// agent24UIReadState is what Guard computed for one UI read. E is ordered. kbs are the rows Resolve returned.
type agent24UIReadState struct {
	tenantID  uint64
	e         []string
	principal types.Principal
	kbs       []*types.KnowledgeBase
	page      int
	pageSize  int
}

// Agent24KnowledgeUIReadHandler serves the UI read entries (kb.list, kb.get, tag.list).
type Agent24KnowledgeUIReadHandler struct {
	deps  agent24UISessionDeps
	scope agent24ScopeResolver
	db    *gorm.DB
}

// NewAgent24KnowledgeUIReadHandler builds the UI read handler.
// An unset UI session key, or standalone mode, returns a nil handler so the routes are not registered.
// A key that is set but invalid returns an error so the container fails closed.
// The signing key and issuer are read once here. A nil database or knowledge-base service does not fail
// construction; the request fails closed.
func NewAgent24KnowledgeUIReadHandler(
	kbService interfaces.KnowledgeBaseService,
	tenants interfaces.TenantService, keys interfaces.TenantAPIKeyService,
	revocations interfaces.Agent24CredentialRevocationStore,
	store interfaces.Agent24UISessionStore,
	db *gorm.DB,
) (*Agent24KnowledgeUIReadHandler, error) {
	on, err := agent24UISessionKeyGate()
	if err != nil || !on {
		return nil, err
	}
	key, configured, err := agent24uisession.KeyFromEnv()
	if err != nil {
		return nil, err
	}
	if !configured || len(key) == 0 {
		return nil, errors.New("ui session key is not configured")
	}
	copied := make([]byte, len(key))
	copy(copied, key)
	return &Agent24KnowledgeUIReadHandler{
		deps: agent24UISessionDeps{
			store:  store,
			key:    copied,
			issuer: agent24uisession.IssuerFromEnv(),
			stored: agent24UIStoredDeps{tenants: tenants, keys: keys, revocations: revocations, key: copied},
			boot:   agent24uisession.BootID(),
		},
		scope: agent24DefaultScopeResolver{kbs: kbService},
		db:    db,
	}, nil
}

// Guard is the per-entry check (R6.4): session, shape, operation, then E. A failure does not call Next.
// op is the route's operation, not a client field. Query keys other than the ones that entry lists are ignored,
// including agent_id and agent_source_tenant_id. page and page_size are recognized only on tag.list.
func (h *Agent24KnowledgeUIReadHandler) Guard(op string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		kbID := ""
		if op == "kb.get" || op == "tag.list" {
			kbID = c.Param("kb_id")
		}
		claims, ticket, scope, kerr := agent24UIVerifySession(
			c.Request.Context(), h.deps, c.Request.Header, time.Now())
		if ticket != nil {
			agent24UINoteTicket(c, ticket, "", false)
		}
		// NoteTicket replaces the resource map, so op and kb_id are merged after it.
		agent24UIReadNoteOp(c, op, kbID)
		if kerr != nil {
			writeAgent24KnowledgeError(c, kerr, agent24KnowledgeEcho{})
			return
		}
		if claims == nil {
			writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
			return
		}
		page, pageSize, serr := agent24UIReadShape(c, op)
		if serr != nil {
			writeAgent24KnowledgeError(c, serr, agent24KnowledgeEcho{})
			return
		}
		if !agent24UIOpEnabled(claims.Ops, op) {
			writeAgent24KnowledgeError(c, agent24Forbidden("operation_disabled"), agent24KnowledgeEcho{})
			return
		}
		kbs, e, eerr := h.agent24UIReadScope(c, claims, scope)
		if c.IsAborted() {
			return
		}
		if eerr != nil {
			writeAgent24KnowledgeError(c, eerr, agent24KnowledgeEcho{})
			return
		}
		principal, _ := agent24PrincipalFromStorageID(claims.Subject)
		st := &agent24UIReadState{
			tenantID: claims.TenantID, e: e, principal: principal, kbs: kbs, page: page, pageSize: pageSize,
		}
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), agent24UIReadStateKey{}, st))
		c.Next()
	}
}

// agent24UIReadShape checks the query keys this entry recognizes. Anything else is ignored.
// page and page_size apply only to tag.list. A missing key takes the default. A present key must be one
// canonical positive integer, and page_size must be at most 200. (page-1)*page_size must fit in an int.
func agent24UIReadShape(c *gin.Context, op string) (page, pageSize int, err *agent24KnowledgeError) {
	if op != "tag.list" {
		return 0, 0, nil
	}
	q := c.Request.URL.Query()
	page, pageSize = agent24UITagPageDefault, agent24UITagPageSizeDefault
	if vs, ok := q["page"]; ok {
		n, good := agent24UIOnePositive(vs, 0)
		if !good {
			return 0, 0, agent24InvalidRequest()
		}
		page = n
	}
	if vs, ok := q["page_size"]; ok {
		n, good := agent24UIOnePositive(vs, agent24UITagPageSizeMax)
		if !good {
			return 0, 0, agent24InvalidRequest()
		}
		pageSize = n
	}
	if page > 1 && int64(page-1) > int64(math.MaxInt)/int64(pageSize) {
		return 0, 0, agent24InvalidRequest()
	}
	return page, pageSize, nil
}

// agent24UIOnePositive accepts exactly one canonical positive integer. upper 0 means no upper bound.
func agent24UIOnePositive(values []string, upper int) (int, bool) {
	if len(values) != 1 {
		return 0, false
	}
	raw := values[0]
	if raw == "" {
		return 0, false
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	if len(raw) > 1 && raw[0] == '0' {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || strconv.Itoa(n) != raw {
		return 0, false
	}
	if upper > 0 && n > upper {
		return 0, false
	}
	return n, true
}

func agent24UIOpEnabled(ops []string, op string) bool {
	return slices.Contains(ops, op)
}

// agent24UIReadScope recomputes E = A ∩ C_now ∩ the JWT scope. Full access and an empty allow-list are
// empty_scope before Resolve: an unrestricted scope is not "all" (R2.8). A client cancel writes nothing.
func (h *Agent24KnowledgeUIReadHandler) agent24UIReadScope(
	c *gin.Context, claims *agent24uisession.Claims, scope types.TenantAPIKeyScope,
) ([]*types.KnowledgeBase, []string, *agent24KnowledgeError) {
	if scope.FullAccess || len(scope.KnowledgeBaseIDs) == 0 {
		return nil, nil, agent24Forbidden("empty_scope")
	}
	if !agent24UIReadScopeReady(h.scope) {
		return nil, nil, agent24Internal()
	}
	ctx := types.WithCaller(c.Request.Context(), types.Caller{TenantID: claims.TenantID})
	ctx = types.WithTenantAPIKeyScope(ctx, scope)
	kbs, err := h.scope.Resolve(ctx, claims.Scope.KnowledgeBaseIDs)
	if err != nil {
		e, gone := agent24StorageError(ctx, err)
		if gone {
			agent24NoteCancelled(c, agent24KnowledgeEcho{}, nil)
			c.Abort()
			return nil, nil, nil
		}
		return nil, nil, e
	}
	if len(kbs) == 0 {
		return nil, nil, agent24Forbidden("empty_scope")
	}
	return kbs, agent24CollectedKBIDs(kbs), nil
}

func agent24UIReadScopeReady(scope agent24ScopeResolver) bool {
	if scope == nil {
		return false
	}
	resolved, ok := scope.(agent24DefaultScopeResolver)
	return !ok || resolved.kbs != nil
}

func agent24UIReadFrom(c *gin.Context) (*agent24UIReadState, bool) {
	st, _ := c.Request.Context().Value(agent24UIReadStateKey{}).(*agent24UIReadState)
	return st, st != nil
}

// agent24UIReadNoteOp records the operation name and, for a :kb_id route, that id. Nothing else from the
// path or the query is recorded.
func agent24UIReadNoteOp(c *gin.Context, op, kbID string) {
	agent24NoteAudit(c, func(f *agent24AuditFacts) {
		if op != "" {
			f.Op = op
		}
		if kbID == "" {
			return
		}
		resource, _ := f.Resource.(map[string]any)
		if resource == nil {
			resource = map[string]any{}
		}
		resource["kb_id"] = kbID
		f.Resource = resource
	})
}

func agent24UIReadFinish(c *gin.Context, st *agent24UIReadState) {
	agent24NoteAudit(c, func(f *agent24AuditFacts) {
		f.Outcome = agent24AuditOK
		if st != nil && len(st.e) > 0 {
			f.EffectiveScope = append([]string(nil), st.e...)
		}
	})
}

func agent24UIReadFail(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	e, gone := agent24StorageError(c.Request.Context(), err)
	if gone {
		agent24NoteCancelled(c, agent24KnowledgeEcho{}, nil)
		c.Abort()
		return true
	}
	writeAgent24KnowledgeError(c, e, agent24KnowledgeEcho{})
	return true
}

// agent24UIKnowledgeBase is the whitelist for one knowledge base. Every field is always present.
type agent24UIKnowledgeBase struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	Type          string    `json:"type"`
	DocumentCount int64     `json:"document_count"`
	WikiEnabled   bool      `json:"wiki_enabled"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type agent24UIKnowledgeBaseList struct {
	KnowledgeBases []agent24UIKnowledgeBase `json:"knowledge_bases"`
}

type agent24UIKnowledgeBaseOne struct {
	KnowledgeBase agent24UIKnowledgeBase `json:"knowledge_base"`
}

// agent24UITagView is the whitelist for one tag. document_count counts visible documents in this knowledge base.
type agent24UITagView struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Color         string `json:"color"`
	DocumentCount int64  `json:"document_count"`
}

type agent24UITagList struct {
	Tags     []agent24UITagView `json:"tags"`
	Total    int64              `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
}

func agent24UIKBView(kb *types.KnowledgeBase, docs int64) agent24UIKnowledgeBase {
	return agent24UIKnowledgeBase{
		ID: kb.ID, Name: kb.Name, Description: kb.Description, Type: kb.Type,
		DocumentCount: docs, WikiEnabled: kb.IsWikiEnabled(), CreatedAt: kb.CreatedAt, UpdatedAt: kb.UpdatedAt,
	}
}

// ListKnowledgeBases is GET …/ui/knowledge-bases (kb.list). The order is E's order.
// Counts use tenant_id and knowledge_base_id ∈ E. The handler does not call the upstream list.
func (h *Agent24KnowledgeUIReadHandler) ListKnowledgeBases(c *gin.Context) {
	st, ok := agent24UIReadFrom(c)
	if !ok || h.db == nil {
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	counts, err := h.agent24UIDocumentCounts(c.Request.Context(), st.tenantID, st.e)
	if agent24UIReadFail(c, err) {
		return
	}
	byID := agent24UIKBByID(st.kbs)
	out := make([]agent24UIKnowledgeBase, 0, len(st.e))
	for _, id := range st.e {
		kb := byID[id]
		if kb == nil {
			continue
		}
		out = append(out, agent24UIKBView(kb, counts[id]))
	}
	agent24UIReadFinish(c, st)
	c.JSON(http.StatusOK, agent24UIKnowledgeBaseList{KnowledgeBases: out})
}

// GetKnowledgeBase is GET …/ui/knowledge-bases/:kb_id (kb.get). An id outside E, including another tenant,
// a missing id and a soft-deleted row, is the same not_found.
func (h *Agent24KnowledgeUIReadHandler) GetKnowledgeBase(c *gin.Context) {
	st, ok := agent24UIReadFrom(c)
	if !ok || h.db == nil {
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	id := c.Param("kb_id")
	kb := agent24UIKBByID(st.kbs)[id]
	if kb == nil || !slices.Contains(st.e, id) {
		writeAgent24KnowledgeError(c, agent24NotFound(), agent24KnowledgeEcho{})
		return
	}
	counts, err := h.agent24UIDocumentCounts(c.Request.Context(), st.tenantID, []string{id})
	if agent24UIReadFail(c, err) {
		return
	}
	agent24UIReadFinish(c, st)
	c.JSON(http.StatusOK, agent24UIKnowledgeBaseOne{KnowledgeBase: agent24UIKBView(kb, counts[id])})
}

// ListTags is GET …/ui/knowledge-bases/:kb_id/tags (tag.list). An id outside E is the same not_found.
// total is every tag of that knowledge base. document_count counts visible documents that carry the tag.
func (h *Agent24KnowledgeUIReadHandler) ListTags(c *gin.Context) {
	st, ok := agent24UIReadFrom(c)
	if !ok || h.db == nil {
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	id := c.Param("kb_id")
	if agent24UIKBByID(st.kbs)[id] == nil || !slices.Contains(st.e, id) {
		writeAgent24KnowledgeError(c, agent24NotFound(), agent24KnowledgeEcho{})
		return
	}
	tags, total, err := h.agent24UIListTags(c.Request.Context(), st.tenantID, id, st.page, st.pageSize)
	if agent24UIReadFail(c, err) {
		return
	}
	counts, err := h.agent24UITagDocumentCounts(c.Request.Context(), st.tenantID, id, agent24UITagIDs(tags))
	if agent24UIReadFail(c, err) {
		return
	}
	out := make([]agent24UITagView, 0, len(tags))
	for _, tag := range tags {
		if tag == nil {
			continue
		}
		out = append(out, agent24UITagView{
			ID: tag.ID, Name: tag.Name, Color: tag.Color, DocumentCount: counts[tag.ID],
		})
	}
	agent24UIReadFinish(c, st)
	c.JSON(http.StatusOK, agent24UITagList{Tags: out, Total: total, Page: st.page, PageSize: st.pageSize})
}

func agent24UIKBByID(kbs []*types.KnowledgeBase) map[string]*types.KnowledgeBase {
	out := make(map[string]*types.KnowledgeBase, len(kbs))
	for _, kb := range kbs {
		if kb != nil {
			out[kb.ID] = kb
		}
	}
	return out
}

func agent24UITagIDs(tags []*types.KnowledgeTag) []string {
	ids := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag != nil {
			ids = append(ids, tag.ID)
		}
	}
	return ids
}

type agent24UIDocCountRow struct {
	KnowledgeBaseID string `gorm:"column:knowledge_base_id"`
	N               int64  `gorm:"column:n"`
}

// agent24UIDocumentCounts counts documents visible under R2.6: counted parse statuses, not soft-deleted.
// The filter is tenant_id and knowledge_base_id ∈ E. gorm adds deleted_at IS NULL for the model.
func (h *Agent24KnowledgeUIReadHandler) agent24UIDocumentCounts(
	ctx context.Context, tenantID uint64, ids []string,
) (map[string]int64, error) {
	out := map[string]int64{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []agent24UIDocCountRow
	err := h.db.WithContext(ctx).Model(&types.Knowledge{}).
		Select("knowledge_base_id, COUNT(*) AS n").
		Where("tenant_id = ? AND knowledge_base_id IN ? AND parse_status IN ?",
			tenantID, ids, agent24CountedParseStatuses).
		Group("knowledge_base_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.KnowledgeBaseID] = row.N
	}
	return out, nil
}

// agent24UIListTags lists one page the same way ListByKB does: sort_order ASC, created_at DESC, seq_id DESC.
func (h *Agent24KnowledgeUIReadHandler) agent24UIListTags(
	ctx context.Context, tenantID uint64, kbID string, page, pageSize int,
) ([]*types.KnowledgeTag, int64, error) {
	var total int64
	err := h.db.WithContext(ctx).Model(&types.KnowledgeTag{}).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Count(&total).Error
	if err != nil {
		return nil, 0, err
	}
	var tags []*types.KnowledgeTag
	err = h.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Order("sort_order ASC, created_at DESC, seq_id DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&tags).Error
	return tags, total, err
}

type agent24UITagCountRow struct {
	TagID string `gorm:"column:tag_id"`
	Count int64  `gorm:"column:count"`
}

// agent24UITagDocumentCounts counts visible documents in this knowledge base that carry the tag.
// It does not use the upstream count, which includes documents the read contract does not show.
func (h *Agent24KnowledgeUIReadHandler) agent24UITagDocumentCounts(
	ctx context.Context, tenantID uint64, kbID string, tagIDs []string,
) (map[string]int64, error) {
	out := map[string]int64{}
	if len(tagIDs) == 0 {
		return out, nil
	}
	var rows []agent24UITagCountRow
	err := h.db.WithContext(ctx).
		Table("knowledge_tag_relations AS ktr").
		Select("ktr.tag_id AS tag_id, COUNT(*) AS count").
		Joins("JOIN knowledges AS k ON ktr.knowledge_id = k.id AND k.deleted_at IS NULL").
		Where("k.tenant_id = ? AND k.knowledge_base_id = ? AND k.parse_status IN ? AND ktr.tag_id IN ?",
			tenantID, kbID, agent24CountedParseStatuses, tagIDs).
		Group("ktr.tag_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.TagID] = row.Count
	}
	return out, nil
}
