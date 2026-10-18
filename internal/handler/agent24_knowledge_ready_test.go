package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type fakeAgent24ReadyProbe struct{ storage, engine error }

func (f fakeAgent24ReadyProbe) Storage(context.Context) error        { return f.storage }
func (f fakeAgent24ReadyProbe) RetrieveEngine(context.Context) error { return f.engine }

func TestAgent24KnowledgeReady(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const ok = `{"protocol_version":1,"request_id":"r1","generation":7`
	secret := errors.New("SECRET-dsn")
	ready := map[string]any{"state": "ready"}
	notReady := map[string]any{"state": "not_ready", "reason": "dependency"}
	allDown := fakeAgent24ReadyProbe{storage: secret, engine: secret}
	engDown := fakeAgent24ReadyProbe{engine: secret}
	const pv2 = `{"protocol_version":2,"request_id":"r1","generation":7}`
	cases := []struct {
		name, body       string
		probe            fakeAgent24ReadyProbe
		status           int
		search, read     map[string]any
		reason, rid, gen any
	}{
		{name: "all ready", body: ok + `}`, status: 200, search: ready, read: ready},
		{name: "storage down", body: ok + `}`, probe: allDown, status: 200, search: notReady, read: notReady},
		{name: "engine down", body: ok + `}`, probe: engDown, status: 200, search: notReady, read: ready},
		{name: "pv 2", body: pv2, status: 400, reason: "unsupported_protocol_version", rid: "r1", gen: 7.0},
		{name: "extra key", body: ok + `,"x":1}`, status: 400, rid: "r1", gen: 7.0},
		{name: "scope", body: ok + `,"scope":{}}`, status: 400, rid: "r1", gen: 7.0},
		{name: "not json", body: `nope`, status: 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			(&Agent24KnowledgeReadyHandler{probe: tc.probe}).Ready(c)
			require.Equal(t, tc.status, w.Code)
			assert.NotContains(t, w.Body.String(), "SECRET")
			var got map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			if tc.status != 200 {
				e := got["error"].(map[string]any)
				d := e["details"].(map[string]any)
				assert.Equal(t, []any{"invalid_request", tc.rid, tc.gen, tc.reason},
					[]any{e["code"], d["request_id"], d["generation"], d["reason"]})
				if tc.reason != nil {
					assert.Equal(t, []any{1.0}, d["supported"])
				}
				return
			}
			assert.Len(t, got, 5)
			assert.Equal(t, []any{1.0, "r1", 7.0, true},
				[]any{got["protocol_version"], got["request_id"], got["generation"], got["live"]})
			ingest := map[string]any{"state": "disabled", "reason": "read_only_v1"}
			assert.Equal(t, map[string]any{"search": tc.search, "read": tc.read, "ingest": ingest}, got["operations"])
		})
	}
}

type emptyAgent24Registry struct {
	interfaces.RetrieveEngineRegistry
}

func (emptyAgent24Registry) GetAllRetrieveEngineServices() []interfaces.RetrieveEngineService {
	return nil
}

func TestAgent24DefaultProbeRetrieveEngineDBStores(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "vs.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.VectorStore{}))
	p := agent24DefaultReadyProbe{db: db, registry: emptyAgent24Registry{}}
	require.Error(t, p.RetrieveEngine(context.Background()))
	require.NoError(t, db.Create(&types.VectorStore{Name: "vs", EngineType: "qdrant"}).Error)
	require.NoError(t, p.RetrieveEngine(context.Background()))
}
