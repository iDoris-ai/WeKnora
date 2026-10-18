package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// Copied verbatim from Agent24 9623505e (CONTRACT-READONLY-v1 r8); see testdata/agent24_knowledge/README.md.
var agent24KnowledgeSchemaSHA256 = map[string]string{
	"ready.schema.json":  "3670772188f2a0c84f6e7234006977bbf029764ad44e19f11c38791e524dbfdd",
	"common.schema.json": "4d0f6669c4d7e2ad75e513991a86dbcb90e751c681076ba97a929d826008a278",
}

// agent24ReadySchemas checks the testdata digests and compiles ready.schema.json's request and response.
func agent24ReadySchemas(t *testing.T) (request, response *jsonschema.Schema) {
	t.Helper()
	c := jsonschema.NewCompiler()
	ids := map[string]string{}
	for name, want := range agent24KnowledgeSchemaSHA256 {
		raw, err := os.ReadFile(filepath.Join("testdata", "agent24_knowledge", name))
		require.NoError(t, err)
		sum := sha256.Sum256(raw)
		require.Equal(t, want, hex.EncodeToString(sum[:]),
			"%s drifted from Agent24; re-sync per testdata/agent24_knowledge/README.md, never hand-edit", name)
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		require.NoError(t, err)
		ids[name], _ = doc.(map[string]any)["$id"].(string)
		require.NoError(t, c.AddResource(ids[name], doc))
	}
	request, err := c.Compile(ids["ready.schema.json"] + "#/$defs/request")
	require.NoError(t, err)
	response, err = c.Compile(ids["ready.schema.json"] + "#/$defs/response")
	require.NoError(t, err)
	return request, response
}

func agent24Validate(sch *jsonschema.Schema, body []byte) error {
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return err
	}
	return sch.Validate(v)
}

func TestAgent24KnowledgeReadySchemaPositive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reqSchema, respSchema := agent24ReadySchemas(t)
	down := errors.New("down")
	index := fakeAgent24Index{ // "empty" is absent: zero facts, an empty KB
		"chunks": {Docs: 3, Active: 1, HasTextChunks: true}, "building": {Docs: 2, Building: 1},
		"failed": {Docs: 2}, "unsupported": {Docs: 2, Active: 2},
	}
	resolver := fakeAgent24Scope(func(ids []string) (kbs []*types.KnowledgeBase, _ error) {
		for _, id := range ids {
			kbs = append(kbs, &types.KnowledgeBase{ID: id})
		}
		return kbs, nil
	})
	scope := []string{"empty", "chunks", "building", "failed", "unsupported"}
	kbReasons := []string{`"index_building"`, `"index_failed"`, `"unsupported_kb_type"`}
	cases := []struct {
		name       string
		probe      fakeAgent24ReadyProbe
		ids, wants []string
	}{
		{"no scope all ready", fakeAgent24ReadyProbe{}, nil, nil},
		{"no scope storage down", fakeAgent24ReadyProbe{storage: down, engine: down}, nil, nil},
		{"no scope engine down", fakeAgent24ReadyProbe{engine: down}, nil, nil},
		{"scope kb states", fakeAgent24ReadyProbe{}, scope, kbReasons},
		{"scope search down", fakeAgent24ReadyProbe{engine: down}, scope, []string{`"dependency"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"protocol_version":1,"request_id":"r1","generation":7}`
			if tc.ids != nil {
				body = body[:len(body)-1] + `,"scope":{"knowledge_base_ids":["` + strings.Join(tc.ids, `","`) + `"]}}`
			}
			require.NoError(t, agent24Validate(reqSchema, []byte(body)), "request: %s", body)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			(&Agent24KnowledgeReadyHandler{tc.probe, resolver, index}).Ready(c)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.NoError(t, agent24Validate(respSchema, w.Body.Bytes()), "response: %s", w.Body.String())
			for _, s := range tc.wants {
				assert.Contains(t, w.Body.String(), s)
			}
		})
	}
}

func TestAgent24KnowledgeReadySchemaNegative(t *testing.T) {
	_, respSchema := agent24ReadySchemas(t)
	const ready, dep = `{"state":"ready"}`, `{"state":"not_ready","reason":"dependency"}`
	const ingestOff = `{"state":"disabled","reason":"read_only_v1"}`
	resp := func(search, ingest, tail string) string {
		return `{"protocol_version":1,"request_id":"r1","generation":7,"live":true,` +
			`"operations":{"search":` + search + `,"read":` + ready + `,"ingest":` + ingest + `}` + tail + `}`
	}
	kbs := func(search string) string {
		return `,"knowledge_bases":[{"knowledge_base_id":"a","score_space":"kb:a","search":` + search +
			`,"read":` + ready + `}]`
	}
	// Controls: the same shapes are accepted when consistent, so each rejection below is for its own reason.
	require.NoError(t, agent24Validate(respSchema, []byte(resp(ready, ingestOff, kbs(ready)))))
	require.NoError(t, agent24Validate(respSchema, []byte(resp(dep, ingestOff, kbs(dep)))))
	for _, tc := range []struct{ name, body string }{
		{"ready with reason", resp(ready, ingestOff, kbs(`{"state":"ready","reason":"index_building"}`))},
		{"search ready, kb dependency", resp(ready, ingestOff, kbs(dep))},
		{"search not_ready, kb ready", resp(dep, ingestOff, kbs(ready))},
		{"ingest ready", resp(ready, ready, "")},
		{"extra top-level key", resp(ready, ingestOff, `,"x":1`)},
		{"knowledge_bases empty array", resp(ready, ingestOff, `,"knowledge_bases":[]`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Error(t, agent24Validate(respSchema, []byte(tc.body)), tc.body)
		})
	}
}
