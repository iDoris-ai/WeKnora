package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agent24AuditListCall is one AuditLogService.List invocation recorded by the stub.
type agent24AuditListCall struct {
	tenantID uint64
	query    interfaces.AuditLogQuery
}

// agent24AuditListRecorder is the audit_log_test.go stub, plus the queries it received.
type agent24AuditListRecorder struct {
	calls   []agent24AuditListCall
	entries []*types.AuditLog
	err     error
}

func (r *agent24AuditListRecorder) service() *stubAuditService {
	return &stubAuditService{
		list: func(_ context.Context, tenantID uint64, q *interfaces.AuditLogQuery) ([]*types.AuditLog, error) {
			r.calls = append(r.calls, agent24AuditListCall{tenantID: tenantID, query: *q})
			return r.entries, r.err
		},
	}
}

func newAgent24KnowledgeAuditReadRouter(svc interfaces.AuditLogService, auth gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.ErrorHandler())
	if auth != nil {
		router.Use(auth)
	}
	// No role guard in front of the handler: RBAC enforcement off lets RequireRole through,
	// so the handler's own check is what must reject a Viewer.
	h := NewAuditLogHandler(svc)
	router.GET("/tenants/:id/agent24/knowledge/audit", h.ListAgent24KnowledgeAuditTenant)
	router.GET("/system/admin/agent24/knowledge/audit", h.ListAgent24KnowledgeAuditSystem)
	return router
}

func agent24AuditCaller(
	tenantID uint64, role types.TenantRole, systemAdmin bool, scope *types.TenantAPIKeyScope,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := types.WithCaller(c.Request.Context(), types.Caller{TenantID: tenantID, Role: role})
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, role)
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, systemAdmin)
		if scope != nil {
			ctx = types.WithTenantAPIKeyScope(ctx, *scope)
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func serveAgent24AuditRead(
	svc interfaces.AuditLogService, auth gin.HandlerFunc, target string,
) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	newAgent24KnowledgeAuditReadRouter(svc, auth).ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

func assertAgent24AuditForbidden(t *testing.T, w *httptest.ResponseRecorder, calls []agent24AuditListCall) {
	t.Helper()
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", w.Code, w.Body.String())
	}
	if len(calls) != 0 {
		t.Fatalf("List called %d times, want 0", len(calls))
	}
}

func assertAgent24KnowledgeAuditQuery(
	t *testing.T, q interfaces.AuditLogQuery, action types.AuditAction, outcome types.AuditOutcome,
	afterID uint64, limit int,
) {
	t.Helper()
	if q.ScopeType != agent24AuditScopeType || q.UnscopedOnly || q.ActorUserID != "" || q.ScopeID != "" {
		t.Fatalf("query scope = %+v, want scope %s, unscoped false, empty actor and scope id", q, agent24AuditScopeType)
	}
	if q.Action != action || q.Outcome != outcome || q.AfterID != afterID || q.Limit != limit {
		t.Fatalf("query filters = %+v, want action %q outcome %q after %d limit %d", q, action, outcome, afterID, limit)
	}
}

func assertAgent24AuditListBody(t *testing.T, w *httptest.ResponseRecorder, entries []*types.AuditLog) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var got auditLogListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if !got.Success {
		t.Fatalf("expected success=true body=%s", w.Body.String())
	}
	var wantCursor uint64
	if n := len(entries); n > 0 {
		wantCursor = entries[n-1].ID
	}
	if got.NextCursor != wantCursor {
		t.Fatalf("next_cursor = %d, want %d", got.NextCursor, wantCursor)
	}
	if len(got.Data) != len(entries) {
		t.Fatalf("len(data) = %d, want %d", len(got.Data), len(entries))
	}
	for i := range entries {
		if got.Data[i] == nil ||
			got.Data[i].ID != entries[i].ID ||
			got.Data[i].TenantID != entries[i].TenantID ||
			got.Data[i].Action != entries[i].Action ||
			got.Data[i].ScopeType != entries[i].ScopeType ||
			got.Data[i].Outcome != entries[i].Outcome {
			t.Fatalf("row %d = %+v, want %+v", i, got.Data[i], entries[i])
		}
	}
}

func TestListAgent24KnowledgeAuditTenant_AdminCopiesRows(t *testing.T) {
	entries := []*types.AuditLog{
		{ID: 20, TenantID: 7, Action: agent24AuditActionSearch, ScopeType: agent24AuditScopeType},
		{
			ID: 11, TenantID: 7, Action: agent24AuditActionReady, ScopeType: agent24AuditScopeType,
			Outcome: types.AuditOutcomeDenied,
		},
	}
	rec := &agent24AuditListRecorder{entries: entries}
	w := serveAgent24AuditRead(rec.service(), agent24AuditCaller(7, types.TenantRoleAdmin, false, nil),
		"/tenants/7/agent24/knowledge/audit")
	if len(rec.calls) != 1 || rec.calls[0].tenantID != 7 {
		t.Fatalf("List calls = %+v, want one call for tenant 7", rec.calls)
	}
	assertAgent24KnowledgeAuditQuery(t, rec.calls[0].query, "", "", 0, 0)
	assertAgent24AuditListBody(t, w, entries)
}

func TestListAgent24KnowledgeAuditTenant_ViewerForbidden(t *testing.T) {
	rec := &agent24AuditListRecorder{}
	w := serveAgent24AuditRead(rec.service(), agent24AuditCaller(7, types.TenantRoleViewer, false, nil),
		"/tenants/7/agent24/knowledge/audit")
	assertAgent24AuditForbidden(t, w, rec.calls)
}

func TestListAgent24KnowledgeAuditTenant_TenantMismatchForbidden(t *testing.T) {
	rec := &agent24AuditListRecorder{}
	w := serveAgent24AuditRead(rec.service(), agent24AuditCaller(8, types.TenantRoleAdmin, false, nil),
		"/tenants/7/agent24/knowledge/audit")
	assertAgent24AuditForbidden(t, w, rec.calls)
}

func TestListAgent24KnowledgeAuditTenant_APIKeyForbidden(t *testing.T) {
	rec := &agent24AuditListRecorder{}
	scope := types.TenantAPIKeyScope{KeyID: 3, ScopeType: types.APIKeyScopeTenant}
	w := serveAgent24AuditRead(rec.service(), agent24AuditCaller(7, types.TenantRoleAdmin, false, &scope),
		"/tenants/7/agent24/knowledge/audit")
	assertAgent24AuditForbidden(t, w, rec.calls)
}

func TestListAgent24KnowledgeAuditTenant_ActionFilter(t *testing.T) {
	rec := &agent24AuditListRecorder{}
	w := serveAgent24AuditRead(rec.service(), agent24AuditCaller(7, types.TenantRoleAdmin, false, nil),
		"/tenants/7/agent24/knowledge/audit?action=rbac.member_added")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
	if len(rec.calls) != 0 {
		t.Fatalf("List called for a rejected action")
	}

	entries := []*types.AuditLog{{ID: 4, TenantID: 7, Action: agent24AuditActionRead, ScopeType: agent24AuditScopeType}}
	rec = &agent24AuditListRecorder{entries: entries}
	w = serveAgent24AuditRead(rec.service(), agent24AuditCaller(7, types.TenantRoleAdmin, false, nil),
		"/tenants/7/agent24/knowledge/audit?after_id=15&limit=3&action=knowledge.read&outcome=denied&actor=u-x")
	if len(rec.calls) != 1 || rec.calls[0].tenantID != 7 {
		t.Fatalf("List calls = %+v, want one call for tenant 7", rec.calls)
	}
	assertAgent24KnowledgeAuditQuery(t, rec.calls[0].query, "knowledge.read", types.AuditOutcomeDenied, 15, 3)
	assertAgent24AuditListBody(t, w, entries)
}

func TestListAgent24KnowledgeAuditTenant_ListErrorHidesSentinel(t *testing.T) {
	const sentinel = "sentinel-kb-c1-03-do-not-leak"
	rec := &agent24AuditListRecorder{err: errors.New(sentinel)}
	w := serveAgent24AuditRead(rec.service(), agent24AuditCaller(7, types.TenantRoleAdmin, false, nil),
		"/tenants/7/agent24/knowledge/audit")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, sentinel) {
		t.Fatalf("response leaked the service error: %s", body)
	}
	if !strings.Contains(body, "failed to list audit log") {
		t.Fatalf("response = %s, want the fixed internal error", body)
	}
}

func TestListAgent24KnowledgeAuditSystem_AdminQueriesTenantZero(t *testing.T) {
	entries := []*types.AuditLog{
		{ID: 8, TenantID: 0, Action: agent24AuditActionSearch, ScopeType: agent24AuditScopeType},
		{
			ID: 3, TenantID: 0, Action: agent24AuditActionRead, ScopeType: agent24AuditScopeType,
			Outcome: types.AuditOutcomeSuccess,
		},
	}
	rec := &agent24AuditListRecorder{entries: entries}
	w := serveAgent24AuditRead(rec.service(), agent24AuditCaller(7, types.TenantRoleViewer, true, nil),
		"/system/admin/agent24/knowledge/audit?after_id=4&limit=2&outcome=success&actor=ignored")
	if len(rec.calls) != 1 || rec.calls[0].tenantID != 0 {
		t.Fatalf("List calls = %+v, want one call for tenant 0", rec.calls)
	}
	assertAgent24KnowledgeAuditQuery(t, rec.calls[0].query, "", types.AuditOutcomeSuccess, 4, 2)
	assertAgent24AuditListBody(t, w, entries)
}

func TestListAgent24KnowledgeAuditSystem_NonAdminForbidden(t *testing.T) {
	rec := &agent24AuditListRecorder{}
	w := serveAgent24AuditRead(rec.service(), agent24AuditCaller(7, types.TenantRoleAdmin, false, nil),
		"/system/admin/agent24/knowledge/audit")
	assertAgent24AuditForbidden(t, w, rec.calls)
}

func TestListAgent24KnowledgeAuditSystem_PlatformKey(t *testing.T) {
	entries := []*types.AuditLog{
		{ID: 6, TenantID: 0, Action: agent24AuditActionReady, ScopeType: agent24AuditScopeType},
	}
	for _, tc := range []struct {
		name        string
		caps        types.StringArray
		systemAdmin bool
		allowed     bool
	}{
		{
			name:    "with system_audit_read",
			caps:    types.StringArray{string(types.APIKeyCapabilitySystemAuditRead)},
			allowed: true,
		},
		{
			name:        "without system_audit_read",
			caps:        types.StringArray{string(types.APIKeyCapabilityRetrieve)},
			systemAdmin: true,
			allowed:     false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &agent24AuditListRecorder{entries: entries}
			scope := types.TenantAPIKeyScope{KeyID: 9, ScopeType: types.APIKeyScopePlatform, Capabilities: tc.caps}
			w := serveAgent24AuditRead(rec.service(), agent24AuditCaller(0, "", tc.systemAdmin, &scope),
				"/system/admin/agent24/knowledge/audit")
			if !tc.allowed {
				assertAgent24AuditForbidden(t, w, rec.calls)
				return
			}
			if len(rec.calls) != 1 || rec.calls[0].tenantID != 0 {
				t.Fatalf("List calls = %+v, want one call for tenant 0", rec.calls)
			}
			assertAgent24KnowledgeAuditQuery(t, rec.calls[0].query, "", "", 0, 0)
			assertAgent24AuditListBody(t, w, entries)
		})
	}
}

func TestListAgent24KnowledgeAuditSystem_TenantKeyForbidden(t *testing.T) {
	rec := &agent24AuditListRecorder{}
	// Carries the capability and is a system administrator; the key is still not a platform key.
	scope := types.TenantAPIKeyScope{
		KeyID:        11,
		ScopeType:    types.APIKeyScopeTenant,
		Capabilities: types.StringArray{string(types.APIKeyCapabilitySystemAuditRead)},
	}
	w := serveAgent24AuditRead(rec.service(), agent24AuditCaller(7, types.TenantRoleAdmin, true, &scope),
		"/system/admin/agent24/knowledge/audit")
	assertAgent24AuditForbidden(t, w, rec.calls)
}
