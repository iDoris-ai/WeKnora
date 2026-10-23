package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// Copied verbatim from Agent24 9121a7b2 (CONTRACT-READONLY-v1 r10); see testdata/agent24_knowledge/README.md.
var agent24KnowledgeSchemaSHA256 = map[string]string{
	"ready.schema.json":  "3670772188f2a0c84f6e7234006977bbf029764ad44e19f11c38791e524dbfdd",
	"search.schema.json": "6f714593bfaaeb1e3b3a5063b1610eaa19da74b3e37f28b1dd2b25c257fba16f",
	"error.schema.json":  "02dea455203cf503f61e0995f62605fffbcd832af627a5fb4a210820cd81649e",
	"common.schema.json": "97de36a9af3a1315bdacda1243c9f8dd84a1f15993f9c757698b73c2bc345f52",
	"read.schema.json":   "de6e14ffb55e6bd5a19b2d1d14ac3209d853a71135d4f9b279bbde2d1e4042ca",
}

// agent24ReadySchemas checks the testdata digests and compiles ready.schema.json's request and response.
func agent24ReadySchemas(t *testing.T) (request, response *jsonschema.Schema) {
	t.Helper()
	c, ids := agent24SchemaCompiler(t)
	request, err := c.Compile(ids["ready.schema.json"] + "#/$defs/request")
	require.NoError(t, err)
	response, err = c.Compile(ids["ready.schema.json"] + "#/$defs/response")
	require.NoError(t, err)
	return request, response
}

// agent24SchemaCompiler checks the testdata digests and loads every schema copy; ids maps file name to $id.
func agent24SchemaCompiler(t *testing.T) (*jsonschema.Compiler, map[string]string) {
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
	return c, ids
}

func agent24Validate(sch *jsonschema.Schema, body []byte) error {
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return err
	}
	return sch.Validate(v)
}

// agent24EveryKB resolves every requested id to a KB: this test checks the response shape, and
// E's tenant and API key filtering is covered by TestAgent24DefaultScopeAndIndex.
type agent24EveryKB struct{}

func (agent24EveryKB) Resolve(_ context.Context, ids []string) (kbs []*types.KnowledgeBase, _ error) {
	for _, id := range ids {
		kbs = append(kbs, &types.KnowledgeBase{ID: id})
	}
	return kbs, nil
}

func TestAgent24KnowledgeReadySchemaPositive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reqSchema, respSchema := agent24ReadySchemas(t)
	down := errors.New("down")
	index := fakeAgent24Index{ // "empty" is absent: zero facts, an empty KB
		"chunks": {Docs: 3, Active: 1, HasTextChunks: true}, "building": {Docs: 2, Building: 1},
		"failed": {Docs: 2}, "unsupported": {Docs: 2, Active: 2},
	}
	resolver := agent24EveryKB{}
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
			(&Agent24KnowledgeReadyHandler{probe: tc.probe, scope: resolver, index: index}).Ready(c)
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

// agent24KnowledgeSchemaDefs is the number of $defs entries in each copy, pinned with its sha256: a
// re-sync updates both.
var agent24KnowledgeSchemaDefs = map[string]int{
	"ready.schema.json": 5, "search.schema.json": 5, "error.schema.json": 9, "common.schema.json": 13,
	"read.schema.json": 4,
}

// Every copy compiles, entry by entry: the roots and each $defs entry, so a broken reference anywhere in a
// copy (even in a definition no other copy uses yet, like common's doc_revision) fails here.
func TestAgent24KnowledgeSchemaCopiesCompile(t *testing.T) {
	c, ids := agent24SchemaCompiler(t)
	names, distinct := []string{}, map[string]bool{}
	for name, id := range ids {
		require.NotEmpty(t, id, name)
		names, distinct[id] = append(names, name), true
	}
	require.ElementsMatch(t, slices.Collect(maps.Keys(agent24KnowledgeSchemaDefs)), names)
	require.Len(t, distinct, len(ids), "every copy has its own $id")
	for name, id := range ids {
		raw, err := os.ReadFile(filepath.Join("testdata", "agent24_knowledge", name))
		require.NoError(t, err)
		var doc struct {
			Defs map[string]json.RawMessage `json:"$defs"`
		}
		require.NoError(t, json.Unmarshal(raw, &doc), name)
		require.Len(t, doc.Defs, agent24KnowledgeSchemaDefs[name], name)
		_, err = c.Compile(id)
		assert.NoError(t, err, id)
		for def := range doc.Defs {
			_, err := c.Compile(id + "#/$defs/" + def)
			assert.NoError(t, err, def)
		}
	}
}
