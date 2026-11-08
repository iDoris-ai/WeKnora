// Agent24 fork product patch A24-KB-34 (KB-C2-01c1, WeKnora#133, READONLY-v1 R2.8, UI-BRIDGE-v1 U3.4).

package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agent24UIReadKBRow is the knowledge_bases row the read tests store.
// A full KnowledgeBase AutoMigrate uses jsonb columns sqlite rejects, so the lookup table is this projection.
// indexing_strategy is a blob so IndexingStrategy.Scan receives []byte (sqlite TEXT comes back as a string).
type agent24UIReadKBRow struct {
	ID               string `gorm:"type:varchar(36);primaryKey"`
	Name             string
	Description      string
	Type             string
	TenantID         uint64
	CreatorID        string
	EmbeddingModelID string
	SummaryModelID   string
	IsTemporary      bool
	IndexingStrategy types.IndexingStrategy `gorm:"type:blob"`
	VectorStoreID    *string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        gorm.DeletedAt `gorm:"index"`
}

func (agent24UIReadKBRow) TableName() string { return "knowledge_bases" }

func (row agent24UIReadKBRow) knowledgeBase() *types.KnowledgeBase {
	return &types.KnowledgeBase{
		ID: row.ID, Name: row.Name, Description: row.Description, Type: row.Type, TenantID: row.TenantID,
		CreatorID: row.CreatorID, EmbeddingModelID: row.EmbeddingModelID, SummaryModelID: row.SummaryModelID,
		IsTemporary: row.IsTemporary, IndexingStrategy: row.IndexingStrategy, VectorStoreID: row.VectorStoreID,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

// agent24UIReadKBDB is GetKnowledgeBasesByIDsOnly over sqlite. Find drops soft-deleted rows.
// Resolve then drops another tenant. The handler does not read kb_shares.
type agent24UIReadKBDB struct {
	interfaces.KnowledgeBaseService
	db *gorm.DB
}

func (s agent24UIReadKBDB) GetKnowledgeBasesByIDsOnly(
	ctx context.Context, ids []string,
) ([]*types.KnowledgeBase, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []agent24UIReadKBRow
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*types.KnowledgeBase, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.knowledgeBase())
	}
	return out, nil
}

type agent24UIRejectUser struct{ interfaces.UserService }

func (agent24UIRejectUser) ValidateToken(context.Context, string) (*types.User, uint64, error) {
	return nil, 0, errors.New("rejected")
}

type agent24UIReadRig struct {
	t     *testing.T
	h     *Agent24KnowledgeUIReadHandler
	r     *gin.Engine
	stub  *agent24AuditStub
	calls *int
	token string
	proof string
	raw   string
}

func agent24UIReadMigrate(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(
		&agent24UIReadKBRow{}, &types.Knowledge{}, &types.KnowledgeTag{},
		&types.KnowledgeTagRelation{}, &types.KnowledgeBaseShare{},
	))
}

func agent24UIReadHandler(
	db *gorm.DB, store interfaces.Agent24UISessionStore, keys []*types.TenantAPIKey, scope agent24ScopeResolver,
) *Agent24KnowledgeUIReadHandler {
	if scope == nil {
		scope = agent24DefaultScopeResolver{kbs: agent24UIReadKBDB{db: db}}
	}
	return &Agent24KnowledgeUIReadHandler{
		deps: agent24UISessionDeps{
			store:  store,
			key:    agent24UIExchangeKey(),
			issuer: agent24uisession.DefaultIssuer,
			boot:   agent24uisession.BootID(),
			stored: agent24UIStoredDeps{
				tenants: agent24UITenantStub{tenant: &types.Tenant{ID: 9}},
				keys:    agent24UIKeyStub{keys: keys},
				key:     agent24UIExchangeKey(),
			},
		},
		scope: scope,
		db:    db,
	}
}

func agent24UIReadIssue(
	t *testing.T, ticket *types.Agent24UITicket, ops []string, issuedAt time.Time, life time.Duration,
) string {
	t.Helper()
	claims := agent24UISessionClaims(t, ticket)
	claims.Ops = append([]string(nil), ops...)
	token, err := agent24uisession.Issue(agent24UIExchangeKey(), agent24uisession.DefaultIssuer, claims, issuedAt, life)
	require.NoError(t, err)
	return token
}

func agent24UIReadMount(t *testing.T, h *Agent24KnowledgeUIReadHandler) agent24UIReadRig {
	t.Helper()
	gin.SetMode(gin.TestMode)
	stub := &agent24AuditStub{}
	calls := 0
	r := gin.New()
	r.Use(Agent24KnowledgeUINoStore())
	r.Use(Agent24KnowledgeAudit(stub))
	r.Use(middleware.Auth(nil, agent24UIRejectUser{}, nil, nil, nil))
	r.Use(Agent24IntegrationCredentialGate(nil))
	bump := func(c *gin.Context) {
		calls++
		c.Next()
	}
	r.GET(agent24uisession.UIReadRoutes[0].Path, h.Guard("kb.list"), bump, h.ListKnowledgeBases)
	r.GET(agent24uisession.UIReadRoutes[1].Path, h.Guard("kb.get"), bump, h.GetKnowledgeBase)
	r.GET(agent24uisession.UIReadRoutes[2].Path, h.Guard("tag.list"), bump, h.ListTags)
	r.GET("/api/v1/knowledge-bases", func(c *gin.Context) {
		calls++
		c.Status(http.StatusNoContent)
	})
	r.POST("/api/v1/agent24/knowledge/search", func(c *gin.Context) {
		calls++
		c.Status(http.StatusNoContent)
	})
	return agent24UIReadRig{t: t, h: h, r: r, stub: stub, calls: &calls}
}

func (g agent24UIReadRig) do(method, path, auth, proof, apiKey string) *httptest.ResponseRecorder {
	g.t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if proof != "" {
		req.Header.Set(agent24UIViewProofHeader, proof)
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	g.r.ServeHTTP(w, req)
	return w
}

func (g agent24UIReadRig) authed(path string) *httptest.ResponseRecorder {
	g.t.Helper()
	return g.do(http.MethodGet, path, "Bearer "+g.token, g.proof, "")
}

func (g agent24UIReadRig) resetCalls() { *g.calls = 0 }

const (
	agent24UISentinelCreator = "creator-sentinel"
	agent24UISentinelEmbed   = "emb-model-sentinel"
	agent24UISentinelSummary = "summary-model-sentinel"
	agent24UISentinelVector  = "vector-store-sentinel"
	agent24UISentinelAgent   = "agent-id-sentinel"
)

func agent24UIReadSeed(t *testing.T, db *gorm.DB) {
	t.Helper()
	vector := agent24UISentinelVector
	rows := []agent24UIReadKBRow{
		{
			ID: "kb-b", Name: "Base B", Description: "desc-b", Type: "wiki", TenantID: 9,
			CreatorID: agent24UISentinelCreator, EmbeddingModelID: agent24UISentinelEmbed,
			SummaryModelID: agent24UISentinelSummary, IsTemporary: true,
			IndexingStrategy: types.IndexingStrategy{WikiEnabled: true}, VectorStoreID: &vector,
		},
		{
			ID: "kb-a", Name: "Base A", Description: "desc-a", Type: "document", TenantID: 9,
			CreatorID: agent24UISentinelCreator, EmbeddingModelID: agent24UISentinelEmbed,
			SummaryModelID: agent24UISentinelSummary, VectorStoreID: &vector,
		},
		{ID: "kb-other", Name: "Other tenant", Description: "other", Type: "document", TenantID: 8},
		{ID: "kb-gone", Name: "Deleted", Description: "gone", Type: "document", TenantID: 9},
		{ID: "kb-local-out", Name: "Outside E", Description: "out", Type: "document", TenantID: 9},
	}
	for i := range rows {
		require.NoError(t, db.Create(&rows[i]).Error)
	}
	require.NoError(t, db.Delete(&agent24UIReadKBRow{}, "id = ?", "kb-gone").Error)

	doc := func(id string, tenant uint64, kb, status string) *types.Knowledge {
		return &types.Knowledge{ID: id, TenantID: tenant, KnowledgeBaseID: kb, ParseStatus: status, Type: "file"}
	}
	docs := []*types.Knowledge{
		doc("doc-completed", 9, "kb-a", types.ParseStatusCompleted),
		doc("doc-finalizing", 9, "kb-a", types.ParseStatusFinalizing),
		doc("doc-draft", 9, "kb-a", types.ManualKnowledgeStatusDraft),
		doc("doc-deleting", 9, "kb-a", types.ParseStatusDeleting),
		doc("doc-unknown", 9, "kb-a", "not-a-status"),
		doc("doc-deleted", 9, "kb-a", types.ParseStatusCompleted),
		doc("doc-foreign", 8, "kb-a", types.ParseStatusCompleted),
		doc("doc-kb-b", 9, "kb-b", types.ParseStatusCompleted),
		doc("doc-out", 9, "kb-local-out", types.ParseStatusCompleted),
	}
	for _, row := range docs {
		require.NoError(t, db.Create(row).Error)
	}
	require.NoError(t, db.Delete(&types.Knowledge{}, "id = ?", "doc-deleted").Error)

	older := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC)
	latest := time.Date(2020, 1, 4, 0, 0, 0, 0, time.UTC)
	tags := []*types.KnowledgeTag{
		{
			ID: "tag-a", SeqID: 1, TenantID: 9, KnowledgeBaseID: "kb-a",
			Name: "Alpha", Color: "red", SortOrder: 1, CreatedAt: newer,
		},
		{
			ID: "tag-b", SeqID: 5, TenantID: 9, KnowledgeBaseID: "kb-a",
			Name: "Beta", Color: "blue", SortOrder: 1, CreatedAt: older,
		},
		{
			ID: "tag-c", SeqID: 3, TenantID: 9, KnowledgeBaseID: "kb-a",
			Name: "Gamma", Color: "green", SortOrder: 2, CreatedAt: latest,
		},
		{
			ID: "tag-foreign", SeqID: 4, TenantID: 9, KnowledgeBaseID: "kb-b",
			Name: "Foreign", Color: "black", SortOrder: 0,
		},
		// A row of another tenant on kb-a: the tag queries filter tenant_id as well as the knowledge base.
		{
			ID: "tag-tenant8", SeqID: 6, TenantID: 8, KnowledgeBaseID: "kb-a",
			Name: "Tenant8", Color: "white", SortOrder: 3,
		},
	}
	for _, tag := range tags {
		require.NoError(t, db.Create(tag).Error)
	}
	for _, docID := range []string{
		"doc-completed", "doc-finalizing", "doc-draft", "doc-deleting", "doc-unknown", "doc-deleted",
		"doc-foreign", "doc-kb-b",
	} {
		require.NoError(t, db.Create(&types.KnowledgeTagRelation{KnowledgeID: docID, TagID: "tag-a"}).Error)
	}
	require.NoError(t, db.Create(&types.KnowledgeTagRelation{KnowledgeID: "doc-draft", TagID: "tag-b"}).Error)

	share := &types.KnowledgeBaseShare{
		ID: "share-1", KnowledgeBaseID: "kb-other", OrganizationID: "org-1", SharedByUserID: "user-1",
		SourceTenantID: 8, Permission: types.OrgMemberRole("viewer"),
	}
	require.NoError(t, db.Create(share).Error)
	var got types.KnowledgeBaseShare
	require.NoError(t, db.First(&got, "id = ?", "share-1").Error)
	require.Equal(t, "kb-other", got.KnowledgeBaseID)
	require.Equal(t, uint64(8), got.SourceTenantID)
}

func agent24UIReadFixture(
	t *testing.T, keys []*types.TenantAPIKey, scope agent24ScopeResolver,
) (agent24UIReadRig, *types.Agent24UITicket) {
	t.Helper()
	db, store := agent24UIHandlerDB(t)
	agent24UIReadMigrate(t, db)
	agent24UIReadSeed(t, db)
	proof, vph := agent24UIViewSecret()
	raw := agent24UIRawTicket(90)
	ticket := agent24UISeedTicket(t, store, raw, vph, func(row *types.Agent24UITicket) {
		require.NoError(t, row.SetScopeKnowledgeBaseIDs([]string{"kb-b", "kb-a", "kb-other", "kb-gone"}))
	})
	if keys == nil {
		keys = []*types.TenantAPIKey{agent24UILiveKey(
			[]string{"kb-b", "kb-a", "kb-other", "kb-gone"}, false, nil, nil)}
	}
	h := agent24UIReadHandler(db, store, keys, scope)
	rig := agent24UIReadMount(t, h)
	rig.token = agent24UIReadIssue(t, ticket, agent24uisession.UIRouteOps(), time.Now(), time.Minute)
	rig.proof = proof
	rig.raw = raw
	return rig, ticket
}

func agent24UIJSONMap(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(body, &m), string(body))
	return m
}

func agent24UIKeySet(t *testing.T, m map[string]any, want ...string) {
	t.Helper()
	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	require.ElementsMatch(t, want, got)
}

func agent24UIForbidKeys(t *testing.T, v any) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			lower := strings.ToLower(k)
			switch {
			case lower == "tenant_id" || lower == "creator_id" || lower == "creator_name":
				t.Fatalf("forbidden key %s", k)
			case lower == "share_count" || lower == "deleted_at" || lower == "is_temporary":
				t.Fatalf("forbidden key %s", k)
			case lower == "my_permission" || lower == "capabilities":
				t.Fatalf("forbidden key %s", k)
			case strings.Contains(lower, "model") || strings.HasPrefix(lower, "vector_store"):
				t.Fatalf("forbidden key %s", k)
			case strings.HasPrefix(lower, "storage") || strings.Contains(lower, "vlm") ||
				strings.Contains(lower, "asr"):
				t.Fatalf("forbidden key %s", k)
			case strings.Contains(lower, "extract") || strings.Contains(lower, "faq"):
				t.Fatalf("forbidden key %s", k)
			case strings.Contains(lower, "question_generation") || strings.Contains(lower, "auto_tag"):
				t.Fatalf("forbidden key %s", k)
			}
			agent24UIForbidKeys(t, child)
		}
	case []any:
		for _, child := range x {
			agent24UIForbidKeys(t, child)
		}
	}
}

func agent24UIAssertNoSecrets(t *testing.T, body string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		require.NotContains(t, body, secret)
	}
}

func agent24UIReadOp(
	t *testing.T, stub *agent24AuditStub, n int,
) (op, outcome, reason string, resource map[string]any) {
	t.Helper()
	require.GreaterOrEqual(t, len(stub.entries), n)
	entry := stub.entries[n-1]
	require.Equal(t, types.AuditAction("knowledge.ui.view"), entry.Action)
	var details struct {
		Op       string         `json:"op"`
		Outcome  string         `json:"outcome"`
		Reason   string         `json:"reason"`
		Resource map[string]any `json:"resource"`
	}
	require.NoError(t, json.Unmarshal(entry.Details, &details))
	return details.Op, details.Outcome, details.Reason, details.Resource
}

func agent24UIRequireNoStore(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func agent24UIRequireNullEcho(t *testing.T, body []byte) {
	t.Helper()
	m := agent24UIJSONMap(t, body)
	errObj := m["error"].(map[string]any)
	details := errObj["details"].(map[string]any)
	require.Contains(t, details, "request_id")
	require.Nil(t, details["request_id"])
	require.Contains(t, details, "generation")
	require.Nil(t, details["generation"])
}

func TestAgent24UIReadEntries(t *testing.T) {
	logs := agent24CaptureCredentialLogs(t)
	rig, _ := agent24UIReadFixture(t, nil, nil)
	secrets := []string{
		rig.token, rig.proof, rig.raw, agent24UISentinelCreator,
		agent24UISentinelEmbed, agent24UISentinelSummary, agent24UISentinelVector, agent24UISentinelAgent,
	}
	kbFields := []string{
		"id", "name", "description", "type", "document_count",
		"wiki_enabled", "created_at", "updated_at",
	}

	t.Run("list", func(t *testing.T) {
		rig.resetCalls()
		before := len(rig.stub.entries)
		w := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, 1, *rig.calls)
		agent24UIRequireNoStore(t, w)
		body := agent24UIJSONMap(t, w.Body.Bytes())
		agent24UIKeySet(t, body, "knowledge_bases")
		agent24UIForbidKeys(t, body)
		items := body["knowledge_bases"].([]any)
		require.Len(t, items, 2)
		first := items[0].(map[string]any)
		second := items[1].(map[string]any)
		agent24UIKeySet(t, first, kbFields...)
		agent24UIKeySet(t, second, kbFields...)
		require.Equal(t, "kb-b", first["id"])
		require.Equal(t, "Base B", first["name"])
		require.Equal(t, "desc-b", first["description"])
		require.Equal(t, "wiki", first["type"])
		require.Equal(t, float64(1), first["document_count"])
		require.Equal(t, true, first["wiki_enabled"])
		require.NotEmpty(t, first["created_at"])
		require.NotEmpty(t, first["updated_at"])
		require.Equal(t, "kb-a", second["id"])
		require.Equal(t, "Base A", second["name"])
		require.Equal(t, "document", second["type"])
		require.Equal(t, float64(2), second["document_count"])
		require.Equal(t, false, second["wiki_enabled"])
		for _, id := range []string{"kb-other", "kb-gone", "kb-local-out"} {
			require.NotContains(t, w.Body.String(), id)
		}
		op, outcome, reason, _ := agent24UIReadOp(t, rig.stub, before+1)
		require.Equal(t, "kb.list", op)
		require.Equal(t, "ok", outcome)
		require.Empty(t, reason)
		agent24UIAssertNoSecrets(t, w.Body.String(), secrets...)
		agent24UIAssertNoSecrets(t, agent24AuditBlob(t, rig.stub.entries[before]), secrets...)
		agent24UIAssertNoSecrets(t, logs.String(), secrets...)
	})

	t.Run("list ignores agent_id and page_size", func(t *testing.T) {
		plain := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases")
		noisy := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases?agent_id=" + agent24UISentinelAgent +
			"&agent_source_tenant_id=8&page_size=0&page=abc")
		require.Equal(t, http.StatusOK, noisy.Code, noisy.Body.String())
		require.Equal(t, plain.Body.Bytes(), noisy.Body.Bytes())
		agent24UIAssertNoSecrets(t, noisy.Body.String(), agent24UISentinelAgent)
	})

	t.Run("get", func(t *testing.T) {
		rig.resetCalls()
		before := len(rig.stub.entries)
		w := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, 1, *rig.calls)
		agent24UIRequireNoStore(t, w)
		body := agent24UIJSONMap(t, w.Body.Bytes())
		agent24UIKeySet(t, body, "knowledge_base")
		kb := body["knowledge_base"].(map[string]any)
		agent24UIKeySet(t, kb, kbFields...)
		agent24UIForbidKeys(t, body)
		require.Equal(t, "kb-a", kb["id"])
		require.Equal(t, float64(2), kb["document_count"])
		require.Equal(t, false, kb["wiki_enabled"])
		op, outcome, _, resource := agent24UIReadOp(t, rig.stub, before+1)
		require.Equal(t, "kb.get", op)
		require.Equal(t, "ok", outcome)
		require.Equal(t, "kb-a", resource["kb_id"])
		agent24UIAssertNoSecrets(t, w.Body.String(), secrets...)
		agent24UIAssertNoSecrets(t, agent24AuditBlob(t, rig.stub.entries[before]), secrets...)
	})

	t.Run("get ignores agent_id", func(t *testing.T) {
		plain := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/kb-b")
		noisy := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/kb-b?agent_id=" + agent24UISentinelAgent)
		require.Equal(t, http.StatusOK, noisy.Code, noisy.Body.String())
		require.Equal(t, plain.Body.Bytes(), noisy.Body.Bytes())
		body := agent24UIJSONMap(t, noisy.Body.Bytes())
		kb := body["knowledge_base"].(map[string]any)
		require.Equal(t, true, kb["wiki_enabled"])
		require.Equal(t, float64(1), kb["document_count"])
	})

	t.Run("outside E is one not_found", func(t *testing.T) {
		var first []byte
		for _, id := range []string{"kb-local-out", "kb-other", "missing-kb", "kb-gone"} {
			rig.resetCalls()
			before := len(rig.stub.entries)
			w := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/" + id)
			require.Equal(t, http.StatusNotFound, w.Code, id)
			require.Equal(t, 1, *rig.calls, id)
			agent24UIRequireNoStore(t, w)
			agent24UIRequireNullEcho(t, w.Body.Bytes())
			if first == nil {
				first = append([]byte(nil), w.Body.Bytes()...)
			} else {
				require.Equal(t, first, w.Body.Bytes(), id)
			}
			op, outcome, reason, resource := agent24UIReadOp(t, rig.stub, before+1)
			require.Equal(t, "kb.get", op)
			require.Equal(t, "not_found", outcome)
			require.Empty(t, reason)
			require.Equal(t, id, resource["kb_id"])
			agent24UIAssertNoSecrets(t, w.Body.String(), secrets...)
			agent24UIAssertNoSecrets(t, agent24AuditBlob(t, rig.stub.entries[before]), secrets...)

			tw := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/" + id + "/tags")
			require.Equal(t, http.StatusNotFound, tw.Code, id)
			require.Equal(t, first, tw.Body.Bytes(), id+" tags")
			agent24UIRequireNoStore(t, tw)
		}
	})

	t.Run("tags", func(t *testing.T) {
		rig.resetCalls()
		before := len(rig.stub.entries)
		w := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a/tags?page=1&page_size=2")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, 1, *rig.calls)
		agent24UIRequireNoStore(t, w)
		body := agent24UIJSONMap(t, w.Body.Bytes())
		agent24UIKeySet(t, body, "tags", "total", "page", "page_size")
		agent24UIForbidKeys(t, body)
		require.Equal(t, float64(3), body["total"])
		require.Equal(t, float64(1), body["page"])
		require.Equal(t, float64(2), body["page_size"])
		tags := body["tags"].([]any)
		require.Len(t, tags, 2)
		alpha := tags[0].(map[string]any)
		beta := tags[1].(map[string]any)
		agent24UIKeySet(t, alpha, "id", "name", "color", "document_count")
		require.Equal(t, "tag-a", alpha["id"])
		require.Equal(t, "Alpha", alpha["name"])
		require.Equal(t, "red", alpha["color"])
		require.Equal(t, float64(2), alpha["document_count"])
		require.Equal(t, "tag-b", beta["id"])
		require.Equal(t, "Beta", beta["name"])
		require.Equal(t, float64(0), beta["document_count"])
		require.NotContains(t, w.Body.String(), "tag-foreign")
		require.NotContains(t, w.Body.String(), "Foreign")
		op, outcome, _, resource := agent24UIReadOp(t, rig.stub, before+1)
		require.Equal(t, "tag.list", op)
		require.Equal(t, "ok", outcome)
		require.Equal(t, "kb-a", resource["kb_id"])
		agent24UIAssertNoSecrets(t, w.Body.String(), secrets...)
		agent24UIAssertNoSecrets(t, agent24AuditBlob(t, rig.stub.entries[before]), secrets...)

		page2 := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a/tags?page=2&page_size=2")
		require.Equal(t, http.StatusOK, page2.Code, page2.Body.String())
		p2 := agent24UIJSONMap(t, page2.Body.Bytes())
		require.Equal(t, float64(3), p2["total"])
		got := p2["tags"].([]any)
		require.Len(t, got, 1)
		require.Equal(t, "tag-c", got[0].(map[string]any)["id"])
		require.NotContains(t, page2.Body.String(), "tag-tenant8")
		require.Equal(t, float64(0), got[0].(map[string]any)["document_count"])

		defaults := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a/tags")
		require.Equal(t, http.StatusOK, defaults.Code, defaults.Body.String())
		d := agent24UIJSONMap(t, defaults.Body.Bytes())
		require.Equal(t, float64(1), d["page"])
		require.Equal(t, float64(50), d["page_size"])
		require.Len(t, d["tags"].([]any), 3)

		past := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a/tags?page=9&page_size=50")
		require.Equal(t, http.StatusOK, past.Code, past.Body.String())
		pastBody := agent24UIJSONMap(t, past.Body.Bytes())
		require.Equal(t, float64(3), pastBody["total"])
		require.Empty(t, pastBody["tags"].([]any))
	})

	t.Run("tags ignore agent_id", func(t *testing.T) {
		plain := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a/tags?page=1&page_size=2")
		noisy := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a/tags?page=1&page_size=2&agent_id=" +
			agent24UISentinelAgent + "&keyword=Alpha")
		require.Equal(t, http.StatusOK, noisy.Code, noisy.Body.String())
		require.JSONEq(t, plain.Body.String(), noisy.Body.String())
		agent24UIAssertNoSecrets(t, noisy.Body.String(), agent24UISentinelAgent)
	})
}

func TestAgent24UIReadAudience(t *testing.T) {
	rig, _ := agent24UIReadFixture(t, nil, nil)
	paths := []string{
		"/api/v1/agent24/knowledge/ui/knowledge-bases",
		"/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a",
		"/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a/tags",
	}
	for _, path := range paths {
		rig.resetCalls()
		// C plus a valid view proof: the entry reads only the session JWT, so C is never a credential here.
		w := rig.do(http.MethodGet, path, "", rig.proof, "sk-live-real")
		require.Equal(t, http.StatusUnauthorized, w.Code, path+" "+w.Body.String())
		require.Equal(t, 0, *rig.calls, path)
		agent24UIRequireNoStore(t, w)
		agent24UIRequireNullEcho(t, w.Body.Bytes())
		m := agent24UIJSONMap(t, w.Body.Bytes())
		errObj := m["error"].(map[string]any)
		require.Equal(t, "unauthorized", errObj["code"])
		require.Equal(t, "credential_invalid", errObj["details"].(map[string]any)["reason"])
		require.NotContains(t, w.Body.String(), "invalid or expired token")
		require.NotContains(t, w.Body.String(), "sk-live-real")

		// A signed_token C (external user id and token headers) with a valid proof is refused the same way.
		rig.resetCalls()
		sreq := httptest.NewRequest(http.MethodGet, path, nil)
		sreq.Header.Set("X-External-User-ID", "user-1")
		sreq.Header.Set("X-External-User-Token", "signed-token-c")
		sreq.Header.Set(agent24UIViewProofHeader, rig.proof)
		sw := httptest.NewRecorder()
		rig.r.ServeHTTP(sw, sreq)
		require.Equal(t, http.StatusUnauthorized, sw.Code, path+" "+sw.Body.String())
		require.Equal(t, 0, *rig.calls, path)
		require.Equal(t, w.Body.Bytes(), sw.Body.Bytes(), path)

		rig.resetCalls()
		w = rig.do(http.MethodGet, path, "Bearer upstream-user-jwt", rig.proof, "")
		require.Equal(t, http.StatusUnauthorized, w.Code, path+" "+w.Body.String())
		require.Equal(t, 0, *rig.calls, path)
		errObj = agent24UIJSONMap(t, w.Body.Bytes())["error"].(map[string]any)
		require.Equal(t, "credential_invalid", errObj["details"].(map[string]any)["reason"])
		require.NotContains(t, w.Body.String(), "invalid or expired token")
		require.NotContains(t, w.Body.String(), "upstream-user-jwt")
		agent24UIRequireNoStore(t, w)
	}

	rig.resetCalls()
	w := rig.do(http.MethodGet, "/api/v1/knowledge-bases", "Bearer "+rig.token, rig.proof, "")
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	require.Equal(t, 0, *rig.calls)
	require.Contains(t, w.Body.String(), "invalid or expired token")
	require.NotContains(t, w.Body.String(), rig.token)
	require.NotContains(t, w.Body.String(), rig.proof)

	w = rig.do(http.MethodPost, "/api/v1/agent24/knowledge/search", "Bearer "+rig.token, rig.proof, "")
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "invalid or expired token")
	require.Equal(t, 0, *rig.calls)
	require.NotContains(t, w.Body.String(), rig.token)
}

func TestAgent24UIReadEmptyScope(t *testing.T) {
	t.Run("allowlist disjoint", func(t *testing.T) {
		counter := &agent24UIScopeCalls{inner: nil}
		keys := []*types.TenantAPIKey{agent24UILiveKey([]string{"kb-nope"}, false, nil, nil)}
		// inner nil: a call returns no rows. The counter proves Resolve ran.
		db, store := agent24UIHandlerDB(t)
		agent24UIReadMigrate(t, db)
		proof, vph := agent24UIViewSecret()
		ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(91), vph, nil)
		h := agent24UIReadHandler(db, store, keys, counter)
		rig := agent24UIReadMount(t, h)
		rig.token = agent24UIReadIssue(t, ticket, agent24uisession.UIRouteOps(), time.Now(), time.Minute)
		rig.proof = proof
		w := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases")
		require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
		require.Equal(t, 0, *rig.calls)
		require.Equal(t, 1, counter.calls)
		m := agent24UIJSONMap(t, w.Body.Bytes())
		details := m["error"].(map[string]any)["details"].(map[string]any)
		require.Equal(t, "empty_scope", details["reason"])
		agent24UIRequireNullEcho(t, w.Body.Bytes())
		agent24UIRequireNoStore(t, w)
		op, outcome, reason, _ := agent24UIReadOp(t, rig.stub, 1)
		require.Equal(t, "kb.list", op)
		require.Equal(t, "forbidden", outcome)
		require.Equal(t, "empty_scope", reason)
	})

	t.Run("full access", func(t *testing.T) {
		counter := &agent24UIScopeCalls{}
		keys := []*types.TenantAPIKey{agent24UILiveKey(nil, true, nil, nil)}
		db, store := agent24UIHandlerDB(t)
		proof, vph := agent24UIViewSecret()
		ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(92), vph, nil)
		h := agent24UIReadHandler(db, store, keys, counter)
		rig := agent24UIReadMount(t, h)
		rig.token = agent24UIReadIssue(t, ticket, agent24uisession.UIRouteOps(), time.Now(), time.Minute)
		rig.proof = proof
		for _, path := range []string{
			"/api/v1/agent24/knowledge/ui/knowledge-bases",
			"/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a",
			"/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a/tags",
		} {
			rig.resetCalls()
			w := rig.authed(path)
			require.Equal(t, http.StatusForbidden, w.Code, path+" "+w.Body.String())
			require.Equal(t, 0, *rig.calls, path)
			details := agent24UIJSONMap(t, w.Body.Bytes())["error"].(map[string]any)["details"].(map[string]any)
			require.Equal(t, "empty_scope", details["reason"])
		}
		require.Equal(t, 0, counter.calls)
	})
}

func TestAgent24UIReadRefusals(t *testing.T) {
	proof, vph := agent24UIViewSecret()
	live := []*types.TenantAPIKey{agent24UILiveKey([]string{"kb-b", "kb-a"}, false, nil, nil)}

	issue := func(
		t *testing.T, store interfaces.Agent24UISessionStore, ops []string, at time.Time,
	) (*Agent24KnowledgeUIReadHandler, string) {
		t.Helper()
		ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(93), vph, nil)
		h := agent24UIReadHandler(nil, store, live, agent24UIResolver(agent24UIKBs(9, "kb-a", "kb-b")...))
		return h, agent24UIReadIssue(t, ticket, ops, at, time.Minute)
	}

	t.Run("credential revoked", func(t *testing.T) {
		_, store := agent24UIHandlerDB(t)
		ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(94), vph, nil)
		h := agent24UIReadHandler(nil, store, nil, agent24UIResolver(agent24UIKBs(9, "kb-a")...))
		rig := agent24UIReadMount(t, h)
		rig.token = agent24UIReadIssue(t, ticket, agent24uisession.UIRouteOps(), time.Now(), time.Minute)
		rig.proof = proof
		w := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a")
		require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
		require.Equal(t, 0, *rig.calls)
		details := agent24UIJSONMap(t, w.Body.Bytes())["error"].(map[string]any)["details"].(map[string]any)
		require.Equal(t, "credential_revoked", details["reason"])
		agent24UIRequireNullEcho(t, w.Body.Bytes())
		agent24UIRequireNoStore(t, w)
		require.NotContains(t, w.Body.String(), rig.token)
		require.NotContains(t, w.Body.String(), proof)
		op, outcome, reason, _ := agent24UIReadOp(t, rig.stub, 1)
		require.Equal(t, "kb.get", op)
		require.Equal(t, "unauthorized", outcome)
		require.Equal(t, "credential_revoked", reason)
	})

	t.Run("family revoked", func(t *testing.T) {
		_, store := agent24UIHandlerDB(t)
		ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(95), vph, nil)
		require.NoError(t, store.RevokeFamily(context.Background(), 9, ticket.Principal, vph, time.Now()))
		h := agent24UIReadHandler(nil, store, live, agent24UIResolver(agent24UIKBs(9, "kb-a")...))
		rig := agent24UIReadMount(t, h)
		rig.token = agent24UIReadIssue(t, ticket, agent24uisession.UIRouteOps(), time.Now(), time.Minute)
		rig.proof = proof
		w := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases")
		require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
		require.Equal(t, 0, *rig.calls)
		details := agent24UIJSONMap(t, w.Body.Bytes())["error"].(map[string]any)["details"].(map[string]any)
		require.Equal(t, "credential_invalid", details["reason"])
		require.NotContains(t, details, "restarted")
		op, _, reason, _ := agent24UIReadOp(t, rig.stub, 1)
		require.Equal(t, "kb.list", op)
		require.Equal(t, "credential_invalid", reason)
		agent24UIAssertNoSecrets(t, agent24AuditBlob(t, rig.stub.entries[0]), rig.token, proof)
	})

	t.Run("other boot", func(t *testing.T) {
		_, store := agent24UIHandlerDB(t)
		h, token := issue(t, store, agent24uisession.UIRouteOps(), time.Now())
		h.deps.boot = "next-process-boot"
		rig := agent24UIReadMount(t, h)
		rig.token = token
		rig.proof = proof
		w := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a/tags")
		require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
		require.Equal(t, 0, *rig.calls)
		details := agent24UIJSONMap(t, w.Body.Bytes())["error"].(map[string]any)["details"].(map[string]any)
		require.Equal(t, "credential_invalid", details["reason"])
		require.Equal(t, true, details["restarted"])
		agent24UIRequireNoStore(t, w)
		require.NotContains(t, w.Body.String(), token)
		require.NotContains(t, w.Body.String(), proof)
		op, _, reason, _ := agent24UIReadOp(t, rig.stub, 1)
		require.Equal(t, "tag.list", op)
		require.Equal(t, "credential_invalid", reason)
	})

	t.Run("expired", func(t *testing.T) {
		_, store := agent24UIHandlerDB(t)
		h, token := issue(t, store, agent24uisession.UIRouteOps(), time.Now().Add(-2*time.Minute))
		rig := agent24UIReadMount(t, h)
		rig.token = token
		rig.proof = proof
		w := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases")
		require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
		require.Equal(t, 0, *rig.calls)
		details := agent24UIJSONMap(t, w.Body.Bytes())["error"].(map[string]any)["details"].(map[string]any)
		require.Equal(t, "credential_expired", details["reason"])
		require.NotContains(t, details, "restarted")
		op, _, reason, _ := agent24UIReadOp(t, rig.stub, 1)
		require.Equal(t, "kb.list", op)
		require.Equal(t, "credential_expired", reason)
		require.NotContains(t, w.Body.String(), token)
	})

	t.Run("operation disabled", func(t *testing.T) {
		_, store := agent24UIHandlerDB(t)
		h, token := issue(t, store, []string{"kb.list"}, time.Now())
		rig := agent24UIReadMount(t, h)
		rig.token = token
		rig.proof = proof
		w := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a/tags")
		require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
		require.Equal(t, 0, *rig.calls)
		details := agent24UIJSONMap(t, w.Body.Bytes())["error"].(map[string]any)["details"].(map[string]any)
		require.Equal(t, "operation_disabled", details["reason"])
		op, _, reason, _ := agent24UIReadOp(t, rig.stub, 1)
		require.Equal(t, "tag.list", op)
		require.Equal(t, "operation_disabled", reason)
	})

	t.Run("shape before operation", func(t *testing.T) {
		_, store := agent24UIHandlerDB(t)
		h, token := issue(t, store, []string{"kb.list"}, time.Now())
		rig := agent24UIReadMount(t, h)
		rig.token = token
		rig.proof = proof
		w := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a/tags?page=abc")
		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		require.Equal(t, 0, *rig.calls)
		errObj := agent24UIJSONMap(t, w.Body.Bytes())["error"].(map[string]any)
		require.Equal(t, "invalid_request", errObj["code"])
		require.NotContains(t, errObj["details"].(map[string]any), "reason")
	})
}

func TestAgent24UIReadTagQuery(t *testing.T) {
	rig, _ := agent24UIReadFixture(t, nil, nil)
	path := "/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a/tags"
	for _, q := range []string{
		"?page_size=0", "?page_size=201", "?page=abc", "?page=01", "?page=1&page=2",
		"?page=" + strconv.FormatInt(int64(math.MaxInt), 10) + "&page_size=2", "?page_size=",
	} {
		rig.resetCalls()
		w := rig.authed(path + q)
		require.Equal(t, http.StatusBadRequest, w.Code, q+" "+w.Body.String())
		require.Equal(t, 0, *rig.calls, q)
		errObj := agent24UIJSONMap(t, w.Body.Bytes())["error"].(map[string]any)
		require.Equal(t, "invalid_request", errObj["code"])
		agent24UIRequireNullEcho(t, w.Body.Bytes())
		agent24UIRequireNoStore(t, w)
		require.NotContains(t, w.Body.String(), rig.token)
		require.NotContains(t, w.Body.String(), rig.proof)
	}
}

func TestAgent24UIReadStorageError(t *testing.T) {
	db, store := agent24UIHandlerDB(t)
	proof, vph := agent24UIViewSecret()
	ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(96), vph, nil)
	keys := []*types.TenantAPIKey{agent24UILiveKey([]string{"kb-b", "kb-a"}, false, nil, nil)}
	h := agent24UIReadHandler(db, store, keys, agent24UIErrScope{err: errors.New("db down")})
	rig := agent24UIReadMount(t, h)
	rig.token = agent24UIReadIssue(t, ticket, agent24uisession.UIRouteOps(), time.Now(), time.Minute)
	rig.proof = proof
	w := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases")
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	require.Equal(t, 0, *rig.calls)
	details := agent24UIJSONMap(t, w.Body.Bytes())["error"].(map[string]any)["details"].(map[string]any)
	require.Equal(t, "dependency", details["reason"])
	require.NotContains(t, w.Body.String(), "db down")
}

func TestAgent24UIReadHandlerKeyGate(t *testing.T) {
	t.Setenv(Agent24KnowledgeModeEnv, Agent24KnowledgeModeIntegration)
	t.Setenv(agent24uisession.KeyEnv, "")
	t.Setenv(Agent24KnowledgeTagKeyEnv, "")
	h, err := NewAgent24KnowledgeUIReadHandler(nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	require.Nil(t, h)

	t.Setenv(agent24uisession.KeyEnv, "!!!")
	h, err = NewAgent24KnowledgeUIReadHandler(nil, nil, nil, nil, nil, nil)
	require.Error(t, err)
	require.Nil(t, h)

	t.Setenv(agent24uisession.KeyEnv, base64.StdEncoding.EncodeToString(bytesRepeat9()))
	t.Setenv(Agent24KnowledgeTagKeyEnv, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32)))
	h, err = NewAgent24KnowledgeUIReadHandler(nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, h)

	t.Setenv(Agent24KnowledgeModeEnv, Agent24KnowledgeModeStandalone)
	h, err = NewAgent24KnowledgeUIReadHandler(nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	require.Nil(t, h)
}

// Codex review of c1 (Medium) and #140 PR-Daemon gate: E uses C_now, not only the JWT scope. The JWT still
// names kb-b and kb-a; the key's allow-list has since shrunk. The real resolver must apply the shrunk list.
func TestAgent24UIReadNarrowedCredential(t *testing.T) {
	t.Run("partial", func(t *testing.T) {
		rig, _ := agent24UIReadFixture(t, []*types.TenantAPIKey{
			agent24UILiveKey([]string{"kb-a"}, false, nil, nil),
		}, nil)
		w := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		items := agent24UIJSONMap(t, w.Body.Bytes())["knowledge_bases"].([]any)
		require.Len(t, items, 1)
		require.Equal(t, "kb-a", items[0].(map[string]any)["id"])
		require.NotContains(t, w.Body.String(), "kb-b")

		missing := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/missing-kb")
		require.Equal(t, http.StatusNotFound, missing.Code, missing.Body.String())
		for _, path := range []string{
			"/api/v1/agent24/knowledge/ui/knowledge-bases/kb-b",
			"/api/v1/agent24/knowledge/ui/knowledge-bases/kb-b/tags",
		} {
			rig.resetCalls()
			got := rig.authed(path)
			require.Equal(t, http.StatusNotFound, got.Code, path+" "+got.Body.String())
			require.Equal(t, missing.Body.Bytes(), got.Body.Bytes(), path)
		}
		ok := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a")
		require.Equal(t, http.StatusOK, ok.Code, ok.Body.String())
	})

	t.Run("disjoint", func(t *testing.T) {
		rig, _ := agent24UIReadFixture(t, []*types.TenantAPIKey{
			agent24UILiveKey([]string{"kb-nope"}, false, nil, nil),
		}, nil)
		for _, path := range []string{
			"/api/v1/agent24/knowledge/ui/knowledge-bases",
			"/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a",
			"/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a/tags",
		} {
			rig.resetCalls()
			w := rig.authed(path)
			require.Equal(t, http.StatusForbidden, w.Code, path+" "+w.Body.String())
			require.Equal(t, 0, *rig.calls, path)
			details := agent24UIJSONMap(t, w.Body.Bytes())["error"].(map[string]any)["details"].(map[string]any)
			require.Equal(t, "empty_scope", details["reason"])
		}
	})
}
