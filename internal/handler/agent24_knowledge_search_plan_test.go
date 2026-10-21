package handler

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestAgent24ParseSearchRequest(t *testing.T) {
	const scope = `"scope":{"knowledge_base_ids":["a","b","a"]}`
	const budget = `"budget":{"deadline_ms":8000,"max_results":100,"max_concurrency":2}`
	body := func(fields ...string) string {
		return `{"protocol_version":1,"request_id":"r1","generation":3,` + strings.Join(fields, ",") + `}`
	}
	rid, gen := "r1", int64(3)
	q := `"query":"q"`
	echo := agent24KnowledgeEcho{RequestID: &rid, Generation: &gen}

	req, gotEcho, kerr := parseAgent24SearchRequest([]byte(body(`"query":"报销"`, scope, budget)))
	assert.Nil(t, kerr)
	assert.Equal(t, echo, gotEcho)
	assert.Equal(t, agent24SearchRequest{
		Query: "报销", KBIDs: []string{"a", "b"},
		Budget: agent24SearchBudget{DeadlineMS: 8000, MaxResults: 50, MaxConcurrency: 2},
	}, req)

	req, _, kerr = parseAgent24SearchRequest([]byte(body(`"query":"`+strings.Repeat("é", 4096)+`"`, scope,
		`"budget":{"deadline_ms":999999,"max_results":1,"max_concurrency":9}`)))
	assert.Nil(t, kerr)
	assert.Equal(t, agent24SearchBudget{DeadlineMS: 30000, MaxResults: 1, MaxConcurrency: 4}, req.Budget)

	// JSON Schema 2020-12 integers of any magnitude are clamped, not rejected (R3.3).
	req, _, kerr = parseAgent24SearchRequest([]byte(body(q, scope,
		`"budget":{"deadline_ms":9223372036854775808,"max_results":8e3,"max_concurrency":3.0}`)))
	assert.Nil(t, kerr)
	assert.Equal(t, agent24SearchBudget{DeadlineMS: 30000, MaxResults: 50, MaxConcurrency: 3}, req.Budget)
	long := `"` + strings.Repeat("é", agent24IDMaxRunes) + `"`
	req, _, kerr = parseAgent24SearchRequest([]byte(body(q, `"scope":{"knowledge_base_ids":[`+long+`]}`, budget)))
	assert.Nil(t, kerr)
	assert.Len(t, req.KBIDs, 1)

	for name, b := range map[string]string{
		"query missing":      body(scope, budget),
		"query empty":        body(`"query":""`, scope, budget),
		"query not a string": body(`"query":7`, scope, budget),
		"query too long":     body(`"query":"`+strings.Repeat("é", 4097)+`"`, scope, budget),
		"scope missing":      body(q, budget),
		"scope empty":        body(q, `"scope":{"knowledge_base_ids":[]}`, budget),
		"scope id too long": body(q, `"scope":{"knowledge_base_ids":["a",`+
			`"`+strings.Repeat("é", agent24IDMaxRunes+1)+`"]}`, budget),
		"budget missing":     body(q, scope),
		"budget null":        body(q, scope, `"budget":null`),
		"budget key missing": body(q, scope, `"budget":{"deadline_ms":1,"max_results":1}`),
		"budget extra key": body(q, scope,
			`"budget":{"deadline_ms":1,"max_results":1,"max_concurrency":1,"x":1}`),
		"budget zero":     body(q, scope, `"budget":{"deadline_ms":0,"max_results":1,"max_concurrency":1}`),
		"budget negative": body(q, scope, `"budget":{"deadline_ms":1,"max_results":-1,"max_concurrency":1}`),
		"budget fraction": body(q, scope, `"budget":{"deadline_ms":1,"max_results":1.5,"max_concurrency":1}`),
		"budget fraction above": body(q, scope,
			`"budget":{"deadline_ms":30000.5,"max_results":1,"max_concurrency":1}`),
		"budget below one": body(q, scope,
			`"budget":{"deadline_ms":1,"max_results":1,"max_concurrency":5e-1}`),
		"budget string":           body(q, scope, `"budget":{"deadline_ms":1,"max_results":"5","max_concurrency":1}`),
		"unknown top-level field": body(q, scope, budget, `"filters":{}`),
	} {
		_, gotEcho, kerr := parseAgent24SearchRequest([]byte(b))
		assert.Equal(t, agent24InvalidRequest(), kerr, name)
		assert.Equal(t, echo, gotEcho, name)
	}

	// The common fields are checked first, by parseAgent24KnowledgeCommon.
	_, _, kerr = parseAgent24SearchRequest([]byte(`{"protocol_version":2,"request_id":"r1","generation":3}`))
	assert.Equal(t, agent24UnsupportedProtocolVersion(agent24KnowledgeSupportedProtocolVersions), kerr)
	_, gotEcho, kerr = parseAgent24SearchRequest([]byte(`{"protocol_version":1,"generation":3,` + q + `,` +
		scope + `,` + budget + `}`))
	assert.Equal(t, agent24InvalidRequest(), kerr)
	assert.Equal(t, agent24KnowledgeEcho{Generation: &gen}, gotEcho)
}

func agent24TestKB(id, emb, vs string) *types.KnowledgeBase {
	kb := &types.KnowledgeBase{ID: id, EmbeddingModelID: emb}
	if vs != "" {
		kb.VectorStoreID = &vs
	}
	return kb
}

func TestAgent24PlanSearch(t *testing.T) {
	kbs := []*types.KnowledgeBase{
		agent24TestKB("a", "e1", ""), agent24TestKB("b", "e1", ""),
		agent24TestKB("c", "e1", ""), agent24TestKB("d", "e1", ""), agent24TestKB("e", "e1", ""),
	}
	facts := []agent24KBIndexFacts{
		{Docs: 2, Active: 1, HasTextChunks: true}, {}, {Docs: 1, Building: 1}, {Docs: 1}, {Docs: 1, Active: 1},
	}
	ready := agent24OperationState{State: "ready"}
	plan, kerr := agent24PlanSearch(ready, kbs, facts)
	assert.Nil(t, kerr)
	assert.Equal(t, agent24SearchPlan{
		Scope: []string{"a", "b", "c", "d", "e"}, ScoreSpace: "emb:e1",
		Searchable: []string{"a"}, Empty: []string{"b"},
		Failures: []agent24SearchFailure{
			{KnowledgeBaseID: "c", Code: "unavailable", Reason: "index_building", Retryable: true},
			{KnowledgeBaseID: "d", Code: "unavailable", Reason: "index_failed"},
			{KnowledgeBaseID: "e", Code: "unavailable", Reason: "unsupported_kb_type"},
		},
	}, plan)

	// Infrastructure not ready: every KB fails as dependency, the empty one too (R3.4).
	plan, kerr = agent24PlanSearch(agent24State(false), kbs[:2], facts[:2])
	assert.Nil(t, kerr)
	dep := func(id string) agent24SearchFailure {
		return agent24SearchFailure{KnowledgeBaseID: id, Code: "unavailable", Reason: "dependency", Retryable: true}
	}
	assert.Equal(t, agent24SearchPlan{
		Scope: []string{"a", "b"}, ScoreSpace: "emb:e1", Failures: []agent24SearchFailure{dep("a"), dep("b")},
	}, plan)

	// Groups keep first-occurrence order and list only E; facts do not matter.
	mixed := []*types.KnowledgeBase{
		agent24TestKB("a", "e1", ""), agent24TestKB("b", "e2", ""),
		agent24TestKB("c", "e1", ""), agent24TestKB("d", "", ""), agent24TestKB("v", "e1", "s1"),
	}
	_, kerr = agent24PlanSearch(ready, mixed, make([]agent24KBIndexFacts, len(mixed)))
	assert.Equal(t, &agent24KnowledgeError{
		Code: agent24CodeInvalidRequest, Reason: "mixed_score_space",
		Extra: map[string]any{"groups": []agent24ScoreSpaceGroup{
			{ScoreSpace: "emb:e1", KnowledgeBaseIDs: []string{"a", "c"}},
			{ScoreSpace: "emb:e2", KnowledgeBaseIDs: []string{"b"}},
			{ScoreSpace: "kb:d", KnowledgeBaseIDs: []string{"d"}},
			{ScoreSpace: "emb:e1|vs:s1", KnowledgeBaseIDs: []string{"v"}},
		}},
	}, kerr)

	// The smallest mixed case: two KBs, two score spaces.
	_, kerr = agent24PlanSearch(ready, mixed[:2], make([]agent24KBIndexFacts, 2))
	assert.Equal(t, &agent24KnowledgeError{
		Code: agent24CodeInvalidRequest, Reason: "mixed_score_space",
		Extra: map[string]any{"groups": []agent24ScoreSpaceGroup{
			{ScoreSpace: "emb:e1", KnowledgeBaseIDs: []string{"a"}},
			{ScoreSpace: "emb:e2", KnowledgeBaseIDs: []string{"b"}},
		}},
	}, kerr)
}

func TestAgent24SearchOutcome(t *testing.T) {
	fail := func(id, code, reason string, retryable bool) agent24SearchFailure {
		return agent24SearchFailure{KnowledgeBaseID: id, Code: code, Reason: reason, Retryable: retryable}
	}
	building := fail("c", "unavailable", "index_building", true)
	failed := fail("c", "unavailable", "index_failed", false)
	withFailures := func(e *agent24KnowledgeError, fs ...agent24SearchFailure) *agent24KnowledgeError {
		e.Extra = map[string]any{"partial_failures": fs}
		return e
	}
	for _, tc := range []struct {
		name         string
		plan         agent24SearchPlan
		searchErr    *agent24KnowledgeError
		completeness string
		failures     []agent24SearchFailure
		err          *agent24KnowledgeError
	}{
		{
			name: "all searched", plan: agent24SearchPlan{Scope: []string{"a"}, Searchable: []string{"a"}},
			completeness: "complete",
		},
		{
			name: "only empty KBs", plan: agent24SearchPlan{Scope: []string{"b"}, Empty: []string{"b"}},
			completeness: "complete",
		},
		{
			name: "unsearchable KB beside a success",
			plan: agent24SearchPlan{
				Scope: []string{"a", "b", "c"}, Searchable: []string{"a"}, Empty: []string{"b"},
				Failures: []agent24SearchFailure{building},
			},
			completeness: "partial", failures: []agent24SearchFailure{building},
		},
		{
			name: "search failed, an empty KB succeeded, E order",
			plan: agent24SearchPlan{
				Scope: []string{"c", "a", "b"}, Searchable: []string{"a"}, Empty: []string{"b"},
				Failures: []agent24SearchFailure{building},
			},
			searchErr:    agent24Unavailable("dependency"),
			completeness: "partial",
			failures:     []agent24SearchFailure{building, fail("a", "unavailable", "dependency", true)},
		},
		{
			name: "a non-retryable failure beside a success stays partial",
			plan: agent24SearchPlan{
				Scope: []string{"a", "c"}, Searchable: []string{"a"},
				Failures: []agent24SearchFailure{failed},
			},
			completeness: "partial", failures: []agent24SearchFailure{failed},
		},
		{
			name:      "timeout beside a successful empty KB",
			plan:      agent24SearchPlan{Scope: []string{"a", "b"}, Searchable: []string{"a"}, Empty: []string{"b"}},
			searchErr: agent24Timeout(), completeness: "partial",
			failures: []agent24SearchFailure{fail("a", "timeout", "timeout", true)},
		},
		{
			name:      "default unavailable reads as dependency",
			plan:      agent24SearchPlan{Scope: []string{"a", "b"}, Searchable: []string{"a"}, Empty: []string{"b"}},
			searchErr: agent24Unavailable(""), completeness: "partial",
			failures: []agent24SearchFailure{fail("a", "unavailable", "dependency", true)},
		},
		{
			name:      "every failure is a timeout",
			plan:      agent24SearchPlan{Scope: []string{"a", "b"}, Searchable: []string{"a", "b"}},
			searchErr: agent24Timeout(),
			err: withFailures(agent24Timeout(), fail("a", "timeout", "timeout", true),
				fail("b", "timeout", "timeout", true)),
		},
		{
			name: "a non-retryable failure decides",
			plan: agent24SearchPlan{
				Scope: []string{"a", "c"}, Searchable: []string{"a"},
				Failures: []agent24SearchFailure{failed},
			},
			searchErr: agent24Timeout(),
			err: withFailures(agent24Unavailable("index_failed"),
				fail("a", "timeout", "timeout", true), failed),
		},
		{
			name: "nothing searchable, the non-retryable reason wins",
			plan: agent24SearchPlan{Scope: []string{"c", "d"}, Failures: []agent24SearchFailure{
				building,
				fail("d", "unavailable", "unsupported_kb_type", false),
			}},
			err: withFailures(agent24Unavailable("unsupported_kb_type"), building,
				fail("d", "unavailable", "unsupported_kb_type", false)),
		},
		{
			name: "first retryable unavailable reason",
			plan: agent24SearchPlan{
				Scope: []string{"a", "c"}, Searchable: []string{"a"},
				Failures: []agent24SearchFailure{building},
			},
			searchErr: agent24Unavailable("overloaded"),
			err: withFailures(agent24Unavailable("overloaded"), fail("a", "unavailable", "overloaded", true),
				building),
		},
		{
			name: "timeout and a building KB",
			plan: agent24SearchPlan{
				Scope: []string{"a", "c"}, Searchable: []string{"a"},
				Failures: []agent24SearchFailure{building},
			},
			searchErr: agent24Timeout(),
			err: withFailures(agent24Unavailable("index_building"), fail("a", "timeout", "timeout", true),
				building),
		},
		{
			name:      "other errors pass through",
			plan:      agent24SearchPlan{Scope: []string{"a", "b"}, Searchable: []string{"a"}, Empty: []string{"b"}},
			searchErr: agent24Internal(), err: agent24Internal(),
		},
	} {
		completeness, failures, err := agent24SearchOutcome(tc.plan, tc.searchErr)
		assert.Equal(t, tc.completeness, completeness, tc.name)
		assert.Equal(t, tc.failures, failures, tc.name)
		assert.Equal(t, tc.err, err, tc.name)
	}
}

// Exact decimal reading: no rounding, any magnitude clamped, linear in the literal's length.
func TestAgent24JSONPositiveInt(t *testing.T) {
	for raw, want := range map[string]int{
		"1": 1, "7": 7, "50": 50, "51": 50, "8e3": 50, "8000.0": 50, "100e-2": 1, "4.0e1": 40, "1E1": 10,
		"9223372036854775808": 50, "1e99999999999": 50, strings.Repeat("9", 200000): 50,
		"0.0000000001e10": 1,
	} {
		got, ok := agent24JSONPositiveInt([]byte(raw), 50)
		assert.Equal(t, []any{want, true}, []any{got, ok}, raw)
	}
	for _, raw := range []string{
		"0", "-1", "-0", "0.0", "0e5", "0.5", "1.5", "5e-1", "1e-99999999999", "1." + strings.Repeat("0", 80) + "1",
		"0." + strings.Repeat("9", 100), "51.5", `"5"`, "null", "true", "[1]",
	} {
		_, ok := agent24JSONPositiveInt([]byte(raw), 50)
		assert.False(t, ok, raw)
	}
}
