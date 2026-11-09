package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Copied verbatim from Agent24 aa98363a (CONTRACT-OPS-v2 r2); see testdata/agent24_knowledge_ops_v2/README.md.
var agent24OpsV2FixtureSHA256 = map[string]string{
	"candidate.schema.json":   "54ad995b7550fedf130cfe84fcf889e15b57f975a26998f68230ad36c3539e9e",
	"schema-cases.json":       "8ec64a631b6550a3dc1a123a7ea92bcab5f0f581c4f9df09d266b682f96e81f4",
	"entry-hash.samples.json": "c8069d60b6ee2e5789aa2cfa7a4dfb1c08228cfef16ca78bbdf8fedc2cbf5b2e",
}

func agent24OpsV2Fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "agent24_knowledge_ops_v2", name))
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	require.Equal(t, agent24OpsV2FixtureSHA256[name], hex.EncodeToString(sum[:]),
		"%s drifted from Agent24; re-sync per testdata/agent24_knowledge_ops_v2/README.md, never hand-edit", name)
	return raw
}

// agent24CandidateSchema compiles candidate.schema.json#/$defs/<def>.
func agent24CandidateSchema(t *testing.T, def string) *jsonschema.Schema {
	t.Helper()
	raw := agent24OpsV2Fixture(t, "candidate.schema.json")
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)
	id, _ := doc.(map[string]any)["$id"].(string)
	c := jsonschema.NewCompiler()
	require.NoError(t, c.AddResource(id, doc))
	sch, err := c.Compile(id + "#/$defs/" + def)
	require.NoError(t, err)
	return sch
}

// W8.13: the server's hash equals every Agent24 sample, and so does its serialization.
func TestAgent24EntryHashSamples(t *testing.T) {
	var f struct {
		Scheme  int `json:"scheme"`
		Samples []struct {
			Name, Kind, Title, Body, SHA256, Serialized string
		} `json:"samples"`
	}
	require.NoError(t, json.Unmarshal(agent24OpsV2Fixture(t, "entry-hash.samples.json"), &f))
	require.Equal(t, 1, f.Scheme)
	require.GreaterOrEqual(t, len(f.Samples), 4)
	for _, s := range f.Samples {
		assert.Equal(t, s.SHA256, agent24EntryHash(s.Kind, s.Title, s.Body), s.Name)
		sum := sha256.Sum256([]byte(s.Serialized))
		assert.Equal(t, s.SHA256, hex.EncodeToString(sum[:]), s.Name)
	}
}

// What encoding/json would escape and ECMAScript does not, and the controls it would write differently.
func TestAgent24EntryHashECMAScriptSerialization(t *testing.T) {
	var b strings.Builder
	agent24ECMAScriptQuote(&b, "<a>&\u2028\u2029\x7f\x00\x1f\b\f\n\r\t\"\\é🙂")
	assert.Equal(t, `"<a>&`+"\u2028\u2029\x7f"+`\u0000\u001f\b\f\n\r\t\"\\é🙂"`, b.String())
	want := sha256.Sum256([]byte(`[1,"note","t","<b>"]`))
	assert.Equal(t, hex.EncodeToString(want[:]), agent24EntryHash("note", "t", "<b>"))
}

// Every candidate.submit_request case of the Agent24 fixtures gets the schema's verdict from the server's shape
// and content checks (②, ⑤1, ⑤2), except the host-approval flag, which W1.6 strips instead of refusing.
func TestAgent24CandidateSubmitSchemaCases(t *testing.T) {
	var f struct {
		Cases []struct {
			Name   string          `json:"name"`
			Schema string          `json:"schema"`
			Valid  bool            `json:"valid"`
			Doc    json.RawMessage `json:"doc"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(agent24OpsV2Fixture(t, "schema-cases.json"), &f))
	sch := agent24CandidateSchema(t, "submit_request")
	n := 0
	for _, c := range f.Cases {
		if c.Schema != "candidate.submit_request" {
			continue
		}
		n++
		require.Equal(t, c.Valid, agent24Validate(sch, c.Doc) == nil, "fixture verdict %s", c.Name)
		sub, req, kerr := parseAgent24CandidateSubmit(c.Doc)
		if kerr == nil {
			kerr = agent24CheckEntryContent(sub)
		}
		switch c.Name {
		case "submit carries an approved flag":
			assert.Nil(t, kerr, c.Name)
			assert.Equal(t, []string{"approved"}, req.stripped)
		default:
			assert.Equal(t, c.Valid, kerr == nil, "%s: %v", c.Name, kerr)
		}
	}
	assert.GreaterOrEqual(t, n, 20)
}

func agent24SubmitDoc(mut func(map[string]any)) []byte {
	m := map[string]any{
		"request_id": "req-1", "generation": 3, "idempotency_key": "k7Qx2mN4pR8sT1vW3yZ5bA",
		"target_knowledge_base_id": "kbA", "mode": "create", "kind": "sop", "title": "请假流程 🌿",
		"body":           "## 步骤\n\n1. 打开「人事」系统\n2. 提交申请 🙂\n\n> 审批人：直属主管",
		"content_sha256": "cd192d152a12cfe33ea1ae7687e0653d911e24971edfc4bca2146cf8bbfbf30f",
		"provenance": map[string]any{
			"source": "agent24_conversation", "session_ref": strings.Repeat("a", 64), "message_range": []int{2, 7},
			"skill_version": "knowledge-entry/1", "redaction_rules_version": 1,
		},
	}
	if mut != nil {
		mut(m)
	}
	raw, _ := json.Marshal(m)
	return raw
}

func agent24Outcome(e *agent24KnowledgeError) string {
	if e == nil {
		return "ok"
	}
	if e.Reason == "" {
		return string(e.Code)
	}
	return string(e.Code) + "/" + e.Reason
}

func TestAgent24CandidateSubmitShape(t *testing.T) {
	prov := func(k string, v any) func(map[string]any) {
		return func(m map[string]any) { m["provenance"].(map[string]any)[k] = v }
	}
	set := func(k string, v any) func(map[string]any) { return func(m map[string]any) { m[k] = v } }
	del := func(k string) func(map[string]any) { return func(m map[string]any) { delete(m, k) } }
	for _, tc := range []struct {
		name string
		mut  func(map[string]any)
		want string
	}{
		{"valid", nil, "ok"},
		{"no idempotency key", del("idempotency_key"), "invalid_request/idempotency_key_missing"},
		{"idempotency key short", set("idempotency_key", "short"), "invalid_request"},
		{"idempotency key not a string", set("idempotency_key", 7), "invalid_request"},
		{"no request_id", del("request_id"), "invalid_request"},
		{"no generation", del("generation"), "invalid_request"},
		{"negative generation", set("generation", -1), "invalid_request"},
		{"unknown field", set("expected_policy_version", "p"), "invalid_request"},
		{"no target", del("target_knowledge_base_id"), "invalid_request"},
		{"empty target", set("target_knowledge_base_id", ""), "invalid_request"},
		{"target too long", set("target_knowledge_base_id", strings.Repeat("k", 257)), "invalid_request"},
		{"no mode", del("mode"), "invalid_request"},
		{"unknown mode", set("mode", "replace"), "invalid_request"},
		{"create with knowledge_id", set("knowledge_id", "d"), "invalid_request"},
		{"create with base", set("expected_source_revision", "r"), "invalid_request"},
		{"revise without base", func(m map[string]any) {
			m["mode"], m["knowledge_id"] = "revise", "d"
		}, "invalid_request"},
		{"revise without knowledge", func(m map[string]any) {
			m["mode"], m["expected_source_revision"] = "revise", "r"
		}, "invalid_request"},
		{"revise", func(m map[string]any) {
			m["mode"], m["knowledge_id"], m["expected_source_revision"] = "revise", "d", "r"
		}, "ok"},
		{"no kind", del("kind"), "invalid_request"},
		{"kind not a string", set("kind", 1), "invalid_request"},
		{"kind with CR", set("kind", "s\rop"), "invalid_request"},
		{"title with CR", set("title", "a\rb"), "invalid_request"},
		{"body with CR", set("body", "a\r\nb"), "invalid_request"},
		{"no body", del("body"), "invalid_request"},
		{"null body", set("body", nil), "invalid_request"}, // Codex S2 r1 C3
		{"null title", set("title", nil), "invalid_request"},
		{"empty body", set("body", ""), "ok"},
		{"hash uppercase", set("content_sha256", strings.Repeat("A", 64)), "invalid_request"},
		{"hash short", set("content_sha256", "abc"), "invalid_request"},
		{"no provenance", del("provenance"), "invalid_request"},
		{"provenance not an object", set("provenance", "x"), "invalid_request"},
		{"provenance other source", prov("source", "chat"), "invalid_request"},
		{"provenance extra field", prov("text", "hello"), "invalid_request"},
		{"session_ref not hex", prov("session_ref", strings.Repeat("g", 64)), "invalid_request"},
		{"range reversed", prov("message_range", []int{7, 2}), "invalid_request"},
		{"range negative", prov("message_range", []int{-1, 2}), "invalid_request"},
		{"range too high", prov("message_range", []int{0, 100001}), "invalid_request"},
		{"range at bounds", prov("message_range", []int{0, 100000}), "ok"},
		{"range of three", prov("message_range", []int{1, 2, 3}), "invalid_request"},
		{"range fraction", prov("message_range", []float64{1.5, 2}), "invalid_request"},
		{"skill version bad", prov("skill_version", "Knowledge"), "invalid_request"},
		{"rules version zero", prov("redaction_rules_version", 0), "invalid_request"},
		{"rules version max", prov("redaction_rules_version", 2147483647), "ok"},
		{"rules version over", prov("redaction_rules_version", 2147483648), "invalid_request"},
		{"raw html", set("body", "hi <script>x</script>"), "invalid_request/unsupported_format"},
		{"image", set("body", "![a](https://x.example/a.png)"), "invalid_request/unsupported_format"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, kerr := parseAgent24CandidateSubmit(agent24SubmitDoc(tc.mut))
			assert.Equal(t, tc.want, agent24Outcome(kerr))
		})
	}
}

// W1.6/F2: approved, author and approver are stripped and reported, never taken as unknown fields.
func TestAgent24CandidateSubmitStripsHostApproval(t *testing.T) {
	raw := agent24SubmitDoc(func(m map[string]any) { m["approved"], m["author"], m["approver"] = true, "x", "y" })
	sub, req, kerr := parseAgent24CandidateSubmit(raw)
	require.Nil(t, kerr)
	assert.Equal(t, []string{"approved", "author", "approver"}, req.stripped)
	assert.Equal(t, "kbA", sub.TargetKnowledgeBaseID)
	_, req, kerr = parseAgent24CandidateSubmit(agent24SubmitDoc(func(m map[string]any) {
		m["approved"] = true
		delete(m, "idempotency_key")
	}))
	assert.Equal(t, "invalid_request/idempotency_key_missing", agent24Outcome(kerr))
	assert.Equal(t, []string{"approved"}, req.stripped, "a refusal still reports what it stripped")
	// Codex S2 r1 C5: what a stripped field holds never fails the request (W1.6)
	_, req, kerr = parseAgent24CandidateSubmit([]byte(strings.Replace(string(agent24SubmitDoc(nil)),
		`"kind":`, `"author":"\ud800","kind":`, 1)))
	assert.Nil(t, kerr)
	assert.Equal(t, []string{"author"}, req.stripped)
}

func TestAgent24CandidateRequestEncoding(t *testing.T) {
	valid := string(agent24SubmitDoc(nil))
	for _, tc := range []struct {
		name, body, want string
	}{
		{"lone high surrogate", strings.Replace(valid, `"title":"`, `"title":"\ud800`, 1), "invalid_request"},
		{"lone low surrogate", strings.Replace(valid, `"title":"`, `"title":"\udc00`, 1), "invalid_request"},
		{"reversed pair", strings.Replace(valid, `"title":"`, `"title":"\udc00\ud800`, 1), "invalid_request"},
		{"high then letter", strings.Replace(valid, `"title":"`, `"title":"\ud800x`, 1), "invalid_request"},
		{"high then escape", strings.Replace(valid, `"title":"`, `"title":"\ud800\n`, 1), "invalid_request"},
		{"high then other \\u", strings.Replace(valid, `"title":"`, `"title":"\ud800\u0041`, 1), "invalid_request"},
		{"high at string end", strings.Replace(valid, `"title":"请假流程 🌿"`, `"title":"\ud800"`, 1), "invalid_request"},
		{"escaped backslash before u", strings.Replace(valid, `"title":"`, `"title":"\\ud800 `, 1), "ok-shape"},
		{"invalid utf-8", strings.Replace(valid, `"title":"`, "\"title\":\"\xff", 1), "invalid_request"},
		{"trailing data", valid + `{}`, "invalid_request"},
		{"not an object", `[]`, "invalid_request"},
		{"null", `null`, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub, _, kerr := parseAgent24CandidateSubmit([]byte(tc.body))
			if tc.want == "ok-shape" {
				require.Nil(t, kerr)
				assert.Equal(t, `\ud800 请假流程 🌿`, sub.Title)
				return
			}
			assert.Equal(t, tc.want, agent24Outcome(kerr))
		})
	}
	// a valid pair passes and decodes to its scalar value
	pair := strings.Replace(valid, `"title":"请假流程 🌿"`, `"title":"\ud83c\udf3f"`, 1)
	sub, _, kerr := parseAgent24CandidateSubmit([]byte(pair))
	require.Nil(t, kerr)
	assert.Equal(t, "🌿", sub.Title)
}

// PR-Daemon #157 B2: U+0000 in any field is invalid at ② (no PostgreSQL text column stores it), before request_id
// is read, so it is never echoed; a stripped field may still hold it, and an escaped backslash before u0000 is text.
func TestAgent24CandidateRequestNUL(t *testing.T) {
	revise := func(m map[string]any) {
		m["mode"], m["knowledge_id"], m["expected_source_revision"] = "revise", "kn-1", "rev-1"
	}
	str := func(base func(map[string]any), name string) func(map[string]any) {
		return func(m map[string]any) {
			if base != nil {
				base(m)
			}
			m[name] = m[name].(string) + "\x00"
		}
	}
	prov := func(name string) func(map[string]any) {
		return func(m map[string]any) {
			p := m["provenance"].(map[string]any)
			p[name] = p[name].(string) + "\x00"
		}
	}
	for name, mut := range map[string]func(map[string]any){
		"request_id": str(nil, "request_id"), "idempotency_key": str(nil, "idempotency_key"),
		"target_knowledge_base_id": str(nil, "target_knowledge_base_id"), "mode": str(nil, "mode"),
		"kind": str(nil, "kind"), "title": str(nil, "title"), "body": str(nil, "body"),
		"content_sha256": str(nil, "content_sha256"), "knowledge_id": str(revise, "knowledge_id"),
		"expected_source_revision": str(revise, "expected_source_revision"), "provenance.source": prov("source"),
		"provenance.session_ref": prov("session_ref"), "provenance.skill_version": prov("skill_version"),
	} {
		raw := agent24SubmitDoc(mut)
		require.Contains(t, string(raw), `\u0000`, name)
		_, req, kerr := parseAgent24CandidateSubmit(raw)
		assert.Equal(t, "invalid_request", agent24Outcome(kerr), name)
		if name == "request_id" {
			assert.Nil(t, req.echo.RequestID, "a request_id with U+0000 is never echoed")
		}
	}
	_, req, kerr := parseAgent24CandidateSubmit(agent24SubmitDoc(func(m map[string]any) { m["author"] = "a\x00" }))
	assert.Nil(t, kerr, "a stripped field may hold U+0000 (W1.6)")
	assert.Equal(t, []string{"author"}, req.stripped)
	sub, _, kerr := parseAgent24CandidateSubmit(agent24SubmitDoc(func(m map[string]any) { m["title"] = `a\u0000` }))
	require.Nil(t, kerr, "an escaped backslash before u0000 is text")
	assert.Equal(t, `a\u0000`, sub.Title)
	for _, body := range []string{
		`{"request_id":"r","generation":0,"candidate_id":"c1\u0000"}`,
		`{"request_id":"r\u0000","generation":0,"candidate_id":"c1"}`,
	} {
		_, _, kerr := parseAgent24CandidateRef([]byte(body))
		assert.Equal(t, "invalid_request", agent24Outcome(kerr), body)
	}
	_, _, kerr = parseAgent24CandidateRef([]byte("{\"request_id\":\"r\",\"generation\":0,\"candidate_id\":\"c\x001\"}"))
	assert.Equal(t, "invalid_request", agent24Outcome(kerr), "a raw NUL is no JSON")
}

func TestAgent24CandidateRef(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"request_id":"r","generation":0,"candidate_id":"c1"}`, "ok"},
		{`{"request_id":"r","generation":0,"candidate_id":"c1","approved":true}`, "ok"},
		{`{"request_id":"r","generation":0,"candidate_id":"c1","author":"\ud800"}`, "ok"},
		{`{"request_id":"r","generation":0,"candidate_id":"c1\ud800"}`, "invalid_request"},
		{`{"request_id":"r","generation":0,"candidate_id":null}`, "invalid_request"},
		{`{"request_id":"r","generation":0}`, "invalid_request"},
		{`{"request_id":"r","generation":0,"candidate_id":""}`, "invalid_request"},
		{`{"request_id":"r","candidate_id":"c1"}`, "invalid_request"},
		{`{"generation":0,"candidate_id":"c1"}`, "invalid_request"},
		{`{"request_id":"r","generation":0,"candidate_id":"c1","x":1}`, "invalid_request"},
		{
			`{"request_id":"r","generation":0,"candidate_id":"c1","idempotency_key":"k7Qx2mN4pR8sT1vW3yZ5bA"}`,
			"invalid_request",
		},
	} {
		id, _, kerr := parseAgent24CandidateRef([]byte(tc.body))
		assert.Equal(t, tc.want, agent24Outcome(kerr), tc.body)
		if kerr == nil {
			assert.Equal(t, "c1", id)
		}
	}
}

func TestAgent24RestrictedMarkdown(t *testing.T) {
	for _, tc := range []struct {
		body      string
		violation bool
	}{
		{"", false},
		{"# 标题\n\n## 二\n\n### 三\n\n段落 *强调* **粗** _x_", false},
		{"1. a\n2. b\n\n- c\n- d\n\n> 引用", false},
		{"见 [文档](https://example.org/a?b=1) 与 `inline <b>`", false},
		{"[ref]: https://example.org/x", false},
		{"a < b and 1<2 and x <= y and <3", false},
		{"&amp; &lt;b&gt; &#35;", false},
		{"C:\\Users\\name and \\d and a \\ b", false},
		{"```\n<script>alert(1)</script>\n![x](y)\n```\nafter", false},
		{"~~~~ go\n<b>\n~~~\n~~~~\nok", false},
		{"   ```\n<div>\n   ```", true}, // an indented fence is judged as prose
		{"```\n<b>\n   ```\nok", false}, // a closing fence may be indented
		{"`` a ` <b> ``", false},
		{"unclosed ``` fence still prose?", true}, // an unmatched run could pair on a later line
		{"```\nnever closed <div>", false},
		{"<script>x</script>", true},
		{"</p>", true},
		{"<!-- c -->", true},
		{"<?php ?>", true},
		{"<Div>", true},
		{"<https://example.org>", true},
		{"![img](https://example.org/a.png)", true},
		{"text[^1]\n\n[^1]: note", true},
		{"text[^1] alone", true},
		{"\\*not emphasis\\*", true},
		{"hard break\\", true},
		{"[x](http://example.org)", true},
		{"[x](javascript:alert(1))", true},
		{"[x]( https://example.org)", false},
		{"[x](HTTPS://EXAMPLE.ORG)", false},
		{"[x](https://)", true},
		// Codex S2 r1 C1 and its relatives: what CommonMark parses before a code span is never skipped as code
		{"[x](https://example.org/`)<img src=\"https://attacker.example/p\">`", true},
		{"[x](https://example.org \"t`\")<img src=x>`", true},
		{"[x](https://example.org \"title\")", true},
		{"[x](https://example.org/(a))", true},
		{"[x](<https://example.org>)", true},
		{"[x](https://example.org/`a`)", true},      // a backtick in a destination, even paired
		{"[x](https://a.example/b_%28c%29)", false}, // parentheses percent-encoded are kept
		{"    <b> in indented code", true},          // indented code is judged as prose
		{"`x\n` <img src=x> `y`", true},
		{"- item\n  ```\n<img src=x>\n  ```", true},
		{"[ref]: https://example.org \"t`\"", true},
		// Codex S2 r2: a reference definition inside a container still defines the link for the whole document
		{"[x]\n\n> [x]: javascript:alert(1)", true},
		{"[x]\n\n- [x]: http://attacker.example/", true},
		{"[x]\n\n1. > [x]: http://attacker.example/", true},
		{"[x\ny]: http://attacker.example/", true},
		{"[x]:\n  javascript:alert(1)", true},
		{"> [x]: https://example.org/ok", false},
		{"注意 [x]: 不是链接", true},                                     // any "]:" outside code needs a https destination
		{"a [x]: https://e.org![y]\n\n[y]: https://e.org/i", true}, // the scan goes on after a "]:" destination
		{"[x]: https://e.org[^1]", true},
		{"[x]: https://e.org/a [y]: javascript:x", true},
		// PR-Daemon #157 B1: a "](" with no opener is prose, so what follows it is scanned too
		{"](https://![x][y])\n\n[y]: https://evil.example/p.png", true},
		{"](https://![y])\n\n[y]: https://evil.example/p.png", true},
		{"[a](https://![x][y])", true}, // brackets in a destination
		{"[a](https://e.org/[x])", true},
		{"[a](https://e.org/[^1])", true},
		{"[a](https://e.org/x]) ![y]", true},
		{"](https://e.org/x) t", false},
		// Codex S2 r3: CommonMark reads a definition and a full reference's label before code spans
		{"[x`]: http://attacker.example/ \"`\"\n\n[go][x`]<img src=x>`", true},
		{"> [x`]: http://a.example/ \"`\"", true},
		{"[x`]: https://e.org/ok \"`\"", true},
		{"`[x]: http://a.example/`", true}, // "]:" even inside inline code: refusing is the safe side
		{"[go][x`]<img src=x>`", true},
		{"[go][`x`] text", true},
		{"[go][x\n`]<img src=x>`", true}, // a label left open on its line
		{"[go][x] and [y][] and [z]", false},
		{"[`code`](https://example.org/a) and [`x`]", false},
		{"`a` and `b` and ``c`d``", false},
		{"[ref]: http://example.org", true},
		{"`code` then <b>", true},
		{"unmatched ` then <b>", true},
		{"unmatched ` alone", true},
		{"    ```\n<b>\n    ```", true}, // indented four: not a fence
		{"```js`\n<b>\n```", true},      // a backtick in the info string: not a fence
		{"~~~\n<b>\n```\n<i>", false},   // a tilde fence is not closed by backticks
		{"```\na\n``\n<b>", false},      // a shorter run does not close
		{"```\na\n```x\n<b>", false},    // a closing run takes no info string
		{"> ```\n> <b>\n> ```", true},   // a fence in a quote is judged as prose
	} {
		assert.Equal(t, tc.violation, agent24RestrictedMarkdownViolation(tc.body), "%q", tc.body)
	}
}

func TestAgent24EntryTitleValid(t *testing.T) {
	for _, tc := range []struct {
		title string
		ok    bool
	}{
		{"a", true},
		{"请假流程 🌿", true},
		{strings.Repeat("字", 200), true},
		{strings.Repeat("字", 201), false},
		{"", false},
		{" a", false},
		{"a ", false},
		{"\ta", false},
		{"a\n", false},
		{"\u00a0a", false},
		{"\ufeffa", false},
		{"a\u3000", false},
		{"a\u2009", false},
		{"\u0085a", true}, // not ECMAScript whitespace
		{"a b", true},
	} {
		assert.Equal(t, tc.ok, agent24EntryTitleValid(tc.title), "%q", tc.title)
	}
}

// A closing fence may be indented up to three spaces, not four (CommonMark); a longer run closes, a shorter does not.
func TestAgent24MarkdownClosesFence(t *testing.T) {
	for _, tc := range []struct {
		line, fence string
		closes      bool
	}{
		{"```", "```", true},
		{"   ```", "```", true},
		{"    ```", "```", false},
		{"````", "```", true},
		{"``", "```", false},
		{"``` x", "```", false},
		{"```  \t", "```", true},
		{"~~~", "```", false},
	} {
		assert.Equal(t, tc.closes, agent24MarkdownClosesFence(tc.line, tc.fence), "%q", tc.line)
	}
	// so after a four-space "closing" line the block goes on, and what follows is still code
	assert.False(t, agent24RestrictedMarkdownViolation("```\na\n    ```\n<b>"))
	assert.True(t, agent24RestrictedMarkdownViolation("```\na\n   ```\n<b>"))
}

// The get and withdraw requests of the Agent24 fixtures get the schema's verdict from the server's parser, except the
// host-approval flag, which W1.6 strips; every get_response case keeps its verdict against the copied schema.
func TestAgent24CandidateRefSchemaCases(t *testing.T) {
	var f struct {
		Cases []struct {
			Name   string          `json:"name"`
			Schema string          `json:"schema"`
			Valid  bool            `json:"valid"`
			Doc    json.RawMessage `json:"doc"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(agent24OpsV2Fixture(t, "schema-cases.json"), &f))
	schemas := map[string]*jsonschema.Schema{}
	n := map[string]int{}
	for _, c := range f.Cases {
		def, ok := map[string]string{
			"candidate.get_request": "get_request", "candidate.withdraw_request": "withdraw_request",
			"candidate.get_response": "get_response",
		}[c.Schema]
		if !ok {
			continue
		}
		n[c.Schema]++
		if schemas[def] == nil {
			schemas[def] = agent24CandidateSchema(t, def)
		}
		require.Equal(t, c.Valid, agent24Validate(schemas[def], c.Doc) == nil, "fixture verdict %s", c.Name)
		if def == "get_response" {
			continue
		}
		_, req, kerr := parseAgent24CandidateRef(c.Doc)
		if len(req.stripped) > 0 {
			assert.Nil(t, kerr, "%s: a stripped field never fails the request", c.Name)
			continue
		}
		assert.Equal(t, c.Valid, kerr == nil, "%s: %v", c.Name, kerr)
	}
	assert.GreaterOrEqual(t, n["candidate.get_request"], 3)
	assert.GreaterOrEqual(t, n["candidate.get_response"], 4)
	assert.GreaterOrEqual(t, n["candidate.withdraw_request"], 2)
}
