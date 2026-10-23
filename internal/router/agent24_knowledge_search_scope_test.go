package router

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agent24SetTagKey sets the key variable for the test and drops the handler package's cached read.
func agent24SetTagKey(t *testing.T, value string) {
	t.Helper()
	t.Setenv(handler.Agent24KnowledgeTagKeyEnv, value)
	handler.ResetAgent24KnowledgeTagKeyForTest()
	t.Cleanup(handler.ResetAgent24KnowledgeTagKeyForTest)
}

// agent24SearchScopeKBs serves the KB lookup and the one retrieval of the search handler; searched records the
// KB ids each retrieval was asked for.
type agent24SearchScopeKBs struct {
	interfaces.KnowledgeBaseService
	searched [][]string
}

func (s *agent24SearchScopeKBs) GetKnowledgeBasesByIDsOnly(
	_ context.Context, ids []string,
) ([]*types.KnowledgeBase, error) {
	var kbs []*types.KnowledgeBase
	for _, id := range ids {
		kbs = append(kbs, &types.KnowledgeBase{ID: id, TenantID: 1, EmbeddingModelID: "m"})
	}
	return kbs, nil
}

func (s *agent24SearchScopeKBs) HybridSearch(
	_ context.Context, _ string, params types.SearchParams,
) ([]*types.SearchResult, error) {
	s.searched = append(s.searched, slices.Clone(params.KnowledgeBaseIDs))
	var out []*types.SearchResult
	for _, kb := range params.KnowledgeBaseIDs {
		out = append(out, &types.SearchResult{ID: "c-" + kb, Score: 0.5})
	}
	return out, nil
}

// agent24SearchScopeDB holds, for tenant 1, a vector store and one completed document with one servable chunk in
// each of kb-a and kb-b.
func agent24SearchScopeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "scope.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&types.VectorStore{}, &types.Knowledge{}, &types.Chunk{}); err != nil {
		t.Fatal(err)
	}
	rows := []any{&types.VectorStore{ID: "vs", TenantID: 1, Name: "vs", EngineType: "postgres"}}
	for _, kb := range []string{"kb-a", "kb-b"} {
		rows = append(rows,
			&types.Knowledge{
				ID: "k-" + kb, TenantID: 1, KnowledgeBaseID: kb, ParseStatus: types.ParseStatusCompleted, Type: "file",
			},
			&types.Chunk{
				ID: "c-" + kb, TenantID: 1, KnowledgeBaseID: kb, KnowledgeID: "k-" + kb,
				ChunkType: types.ChunkTypeText, IsEnabled: true, Content: kb,
			})
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// Through the registered route and the API key gate, a KB-restricted retrieve key searches only its allowed
// KBs: an allowed and a disallowed KB in one request search the allowed one only, a disallowed KB alone is
// empty_scope, and a key without retrieve stops at the gate. An unrestricted retrieve key searches both.
func TestAgent24KnowledgeSearchRouteKBAllowList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	agent24SetTagKey(t, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	retrieve := types.StringArray{string(types.APIKeyCapabilityRetrieve)}
	restricted := types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"kb-a"}, Capabilities: retrieve}
	for _, tc := range []struct {
		name     string
		scope    types.TenantAPIKeyScope
		kbs      string
		status   int
		scopeIDs []string   // effective_scope on success
		searched [][]string // KB ids handed to the retrieval
		reason   string     // error reason on a refusal
	}{
		{
			"allowed and disallowed", restricted, `"kb-a","kb-b"`, http.StatusOK,
			[]string{"kb-a"},
			[][]string{{"kb-a"}},
			"",
		},
		{"disallowed only", restricted, `"kb-b"`, http.StatusForbidden, nil, nil, `"reason":"empty_scope"`},
		{
			"no retrieve capability",
			types.TenantAPIKeyScope{
				KnowledgeBaseIDs: types.StringArray{"kb-a"},
				Capabilities:     types.StringArray{string(types.APIKeyCapabilityChat)},
			},
			`"kb-a"`, http.StatusForbidden, nil, nil, "API key scope does not allow this operation",
		},
		{
			"unrestricted",
			types.TenantAPIKeyScope{Capabilities: retrieve},
			`"kb-a","kb-b"`, http.StatusOK,
			[]string{"kb-a", "kb-b"},
			[][]string{{"kb-a", "kb-b"}},
			"",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kbs := &agent24SearchScopeKBs{}
			g := &rbacGuards{apiKeyAuthorizer: middleware.NewAPIKeyRouteAuthorizer()}
			engine := gin.New()
			v1 := engine.Group("/api/v1")
			v1.Use(func(c *gin.Context) {
				ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(1))
				c.Request = c.Request.WithContext(types.WithTenantAPIKeyScope(ctx, tc.scope))
				c.Next()
			}, g.apiKeyAuthorizer.Middleware())
			RegisterAgent24KnowledgeRoutes(v1, nil,
				handler.NewAgent24KnowledgeSearchHandler(agent24SearchScopeDB(t), nil, kbs), nil, g)

			body := `{"protocol_version":1,"request_id":"r1","generation":7,"query":"q",` +
				`"scope":{"knowledge_base_ids":[` + tc.kbs + `]},` +
				`"budget":{"deadline_ms":5000,"max_results":5,"max_concurrency":1}}`
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/agent24/knowledge/search",
				strings.NewReader(body)))
			if w.Code != tc.status {
				t.Fatalf("status %d body %s, want %d", w.Code, w.Body.String(), tc.status)
			}
			if !slices.EqualFunc(kbs.searched, tc.searched, slices.Equal[[]string]) {
				t.Fatalf("searched %v, want %v", kbs.searched, tc.searched)
			}
			if tc.status != http.StatusOK {
				if !strings.Contains(w.Body.String(), tc.reason) {
					t.Fatalf("body %s, want %s", w.Body.String(), tc.reason)
				}
				return
			}
			var resp struct {
				Completeness   string `json:"completeness"`
				EffectiveScope struct {
					KnowledgeBaseIDs []string `json:"knowledge_base_ids"`
				} `json:"effective_scope"`
				Hits []struct {
					KnowledgeBaseID string `json:"knowledge_base_id"`
				} `json:"hits"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			var hitKBs []string
			for _, h := range resp.Hits {
				hitKBs = append(hitKBs, h.KnowledgeBaseID)
			}
			if resp.Completeness != "complete" || !slices.Equal(resp.EffectiveScope.KnowledgeBaseIDs, tc.scopeIDs) ||
				!slices.Equal(hitKBs, tc.scopeIDs) {
				t.Fatalf("body %s, want complete with effective_scope and hits in %v", w.Body.String(), tc.scopeIDs)
			}
		})
	}
}
