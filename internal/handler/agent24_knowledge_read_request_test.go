package handler

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

var agent24ReadTestKey = []byte("0123456789abcdef0123456789abcdef")

func agent24ReadBodyOf(fields ...string) string {
	return `{"protocol_version":1,"request_id":"r1","generation":3,` + strings.Join(fields, ",") + `}`
}

func TestAgent24ParseReadRequest(t *testing.T) {
	const scope = `"scope":{"knowledge_base_ids":["a","b","a"]}`
	const kid = `"knowledge_id":"doc-7"`
	rid, gen := "r1", int64(3)
	echo := agent24KnowledgeEcho{RequestID: &rid, Generation: &gen}
	parse := func(fields ...string) (agent24ReadRequest, agent24KnowledgeEcho, *agent24KnowledgeError) {
		return parseAgent24ReadRequest(agent24ReadTestKey, []byte(agent24ReadBodyOf(fields...)))
	}

	// Absent page.limit, max_bytes and deadline_ms take the server limits.
	req, gotEcho, kerr := parse(scope, kid)
	assert.Nil(t, kerr)
	assert.Equal(t, echo, gotEcho)
	assert.Equal(t, agent24ReadRequest{
		KBIDs: []string{"a", "b"}, KnowledgeID: "doc-7",
		PageLimit: 100, MaxBytes: 262144, DeadlineMS: 30000,
	}, req)

	// Single chunk: only content_tag is kept; the informational parts are validated and dropped (R4.3).
	req, _, kerr = parse(scope, kid, `"chunk_id":"chk-71"`,
		`"expected_revision":{"content_tag":"ct","chunk_revision":3,"index_build_id":"ib","source_revision":"s"}`,
		`"max_bytes":4096`, `"deadline_ms":5000`)
	assert.Nil(t, kerr)
	assert.Equal(t, agent24ReadRequest{
		KBIDs: []string{"a", "b"}, KnowledgeID: "doc-7", ChunkID: "chk-71",
		Expected:  &agent24ReadExpected{ContentTag: "ct"},
		PageLimit: 100, MaxBytes: 4096, DeadlineMS: 5000,
	}, req)
	for _, rev := range []string{"0", "-0", "0.0", "0e0", "0E-3", "3.0", "1e2", "9223372036854775808"} {
		_, _, kerr = parse(scope, kid, `"chunk_id":"chk-71"`,
			`"expected_revision":{"content_tag":"ct","chunk_revision":`+rev+`}`)
		assert.Nil(t, kerr, rev)
	}
	req, _, kerr = parse(scope, kid, `"chunk_id":"chk-71"`,
		`"expected_revision":{"content_tag":"ct","chunk_revision":null}`, `"page":{"cursor":null,"limit":7}`)
	assert.Nil(t, kerr)
	assert.Equal(t, &agent24ReadExpected{ContentTag: "ct"}, req.Expected)
	assert.Nil(t, req.Cursor)
	assert.Equal(t, 7, req.PageLimit)

	// Document level: both parts kept; values above the limits are clamped, integers as in JSON Schema.
	req, _, kerr = parse(scope, kid, `"expected_revision":{"source_revision":"s","index_build_id":"ib"}`,
		`"page":{"cursor":null,"limit":1000}`, `"max_bytes":1e9`, `"deadline_ms":999999.0`)
	assert.Nil(t, kerr)
	assert.Equal(t, agent24ReadRequest{
		KBIDs: []string{"a", "b"}, KnowledgeID: "doc-7",
		Expected:  &agent24ReadExpected{SourceRevision: "s", IndexBuildID: "ib"},
		PageLimit: 100, MaxBytes: 262144, DeadlineMS: 30000,
	}, req)

	long := strings.Repeat("é", agent24IDMaxRunes)
	req, _, kerr = parse(scope, `"knowledge_id":"`+long+`"`, `"chunk_id":"`+long+`"`)
	assert.Nil(t, kerr)
	assert.Equal(t, long, req.KnowledgeID)
	assert.Equal(t, long, req.ChunkID)

	tooLong := `"` + strings.Repeat("é", agent24IDMaxRunes+1) + `"`
	chunk := `"chunk_id":"c"`
	cexp := func(extra string) string { return `"expected_revision":{"content_tag":"t",` + extra + `}` }
	for name, fields := range map[string][]string{
		"unknown field":              {scope, kid, `"query":"q"`},
		"keys joined":                {scope, kid, `"scope|knowledge_id":1`},
		"scope missing":              {kid},
		"scope empty":                {`"scope":{"knowledge_base_ids":[]}`, kid},
		"knowledge_id missing":       {scope},
		"knowledge_id empty":         {scope, `"knowledge_id":""`},
		"knowledge_id null":          {scope, `"knowledge_id":null`},
		"knowledge_id number":        {scope, `"knowledge_id":7`},
		"knowledge_id too long":      {scope, `"knowledge_id":` + tooLong},
		"chunk_id empty":             {scope, kid, `"chunk_id":""`},
		"chunk_id null":              {scope, kid, `"chunk_id":null`},
		"chunk_id too long":          {scope, kid, `"chunk_id":` + tooLong},
		"expected null":              {scope, kid, `"expected_revision":null`},
		"expected not object":        {scope, kid, `"expected_revision":"s"`},
		"chunk expected no tag":      {scope, kid, chunk, `"expected_revision":{"chunk_revision":1}`},
		"chunk expected tag empty":   {scope, kid, chunk, `"expected_revision":{"content_tag":""}`},
		"chunk expected tag number":  {scope, kid, chunk, `"expected_revision":{"content_tag":1}`},
		"chunk expected tag long":    {scope, kid, chunk, `"expected_revision":{"content_tag":` + tooLong + `}`},
		"chunk expected extra key":   {scope, kid, chunk, cexp(`"x":1`)},
		"chunk expected rev -1":      {scope, kid, chunk, cexp(`"chunk_revision":-1`)},
		"chunk expected rev 1.5":     {scope, kid, chunk, cexp(`"chunk_revision":1.5`)},
		"chunk expected rev 0.5":     {scope, kid, chunk, cexp(`"chunk_revision":0.5`)},
		"chunk expected rev -1e0":    {scope, kid, chunk, cexp(`"chunk_revision":-1e0`)},
		"chunk expected rev string":  {scope, kid, chunk, cexp(`"chunk_revision":"1"`)},
		"chunk expected source null": {scope, kid, chunk, cexp(`"source_revision":null`)},
		"chunk expected build empty": {scope, kid, chunk, cexp(`"index_build_id":""`)},
		"doc expected one part":      {scope, kid, `"expected_revision":{"source_revision":"s"}`},
		"doc expected build null":    {scope, kid, `"expected_revision":{"source_revision":"s","index_build_id":null}`},
		"doc expected source empty":  {scope, kid, `"expected_revision":{"source_revision":"","index_build_id":"b"}`},
		"doc expected chunk_revision": {
			scope, kid,
			`"expected_revision":{"source_revision":"s","index_build_id":"b","chunk_revision":0}`,
		},
		"doc expected content_tag": {
			scope, kid,
			`"expected_revision":{"source_revision":"s","index_build_id":"b","content_tag":"t"}`,
		},
		"page null":            {scope, kid, `"page":null`},
		"page no cursor":       {scope, kid, `"page":{"limit":2}`},
		"page extra key":       {scope, kid, `"page":{"cursor":null,"x":1}`},
		"page cursor empty":    {scope, kid, `"page":{"cursor":""}`},
		"page cursor number":   {scope, kid, `"page":{"cursor":1}`},
		"page cursor too long": {scope, kid, `"page":{"cursor":"` + strings.Repeat("a", 1025) + `"}`},
		"page limit zero":      {scope, kid, `"page":{"cursor":null,"limit":0}`},
		"page limit string":    {scope, kid, `"page":{"cursor":null,"limit":"2"}`},
		"page limit null":      {scope, kid, `"page":{"cursor":null,"limit":null}`},
		"max_bytes zero":       {scope, kid, `"max_bytes":0`},
		"max_bytes fraction":   {scope, kid, `"max_bytes":1.5`},
		"max_bytes null":       {scope, kid, `"max_bytes":null`},
		"deadline_ms negative": {scope, kid, `"deadline_ms":-1`},
		"deadline_ms zero":     {scope, kid, `"deadline_ms":0`},
	} {
		_, gotEcho, kerr := parse(fields...)
		assert.Equal(t, agent24InvalidRequest(), kerr, name)
		assert.Equal(t, echo, gotEcho, name)
	}

	// The common fields are checked first, by parseAgent24KnowledgeCommon.
	_, _, kerr = parseAgent24ReadRequest(agent24ReadTestKey, []byte(`{"protocol_version":2,"request_id":"r1"}`))
	assert.Equal(t, agent24UnsupportedProtocolVersion(agent24KnowledgeSupportedProtocolVersions), kerr)
}

func TestAgent24ParseReadRequestCursor(t *testing.T) {
	const scope = `"scope":{"knowledge_base_ids":["a"]}`
	chunkCur := agent24ReadCursor{ContentTag: "ct", Offset: 4096}
	docCur := agent24ReadCursor{SourceRevision: "s", IndexBuildID: "ib", Position: 2}
	page := func(c string) string { return `"page":{"cursor":"` + c + `"}` }
	parse := func(key []byte, fields ...string) (agent24ReadRequest, *agent24KnowledgeError) {
		req, _, kerr := parseAgent24ReadRequest(key, []byte(agent24ReadBodyOf(fields...)))
		return req, kerr
	}

	cc := agent24EncodeReadCursor(agent24ReadTestKey, "doc-7", "chk-71", chunkCur)
	req, kerr := parse(agent24ReadTestKey, scope, `"knowledge_id":"doc-7"`, `"chunk_id":"chk-71"`, page(cc))
	assert.Nil(t, kerr)
	assert.Equal(t, &chunkCur, req.Cursor)
	dc := agent24EncodeReadCursor(agent24ReadTestKey, "doc-7", "", docCur)
	req, kerr = parse(agent24ReadTestKey, scope, `"knowledge_id":"doc-7"`, page(dc))
	assert.Nil(t, kerr)
	assert.Equal(t, &docCur, req.Cursor)

	// Without a key the cursor is only a string: the disabled check decides next (R6.4).
	req, kerr = parse(nil, scope, `"knowledge_id":"doc-7"`, page("garbage"))
	assert.Nil(t, kerr)
	assert.Nil(t, req.Cursor)

	otherKey := []byte("fedcba9876543210fedcba9876543210")
	flipLast := func(s string) string { // another hex digit in the last place
		if s[len(s)-1] == '0' {
			return s[:len(s)-1] + "1"
		}
		return s[:len(s)-1] + "0"
	}
	for name, fields := range map[string][]string{
		"garbage":                {`"knowledge_id":"doc-7"`, page("garbage")},
		"other document":         {`"knowledge_id":"doc-8"`, page(dc)},
		"other chunk":            {`"knowledge_id":"doc-7"`, `"chunk_id":"chk-72"`, page(cc)},
		"chunk cursor, doc read": {`"knowledge_id":"doc-7"`, page(cc)},
		"doc cursor, chunk read": {`"knowledge_id":"doc-7"`, `"chunk_id":"chk-71"`, page(dc)},
		"other key": {
			`"knowledge_id":"doc-7"`,
			page(agent24EncodeReadCursor(otherKey, "doc-7", "", docCur)),
		},
		"mac flipped": {`"knowledge_id":"doc-7"`, page(flipLast(dc))},
	} {
		_, kerr := parse(agent24ReadTestKey, append([]string{scope}, fields...)...)
		assert.Equal(t, agent24InvalidRequest(), kerr, name)
	}
}

func TestAgent24ReadCursorCodec(t *testing.T) {
	hex64 := strings.Repeat("ab", 32)
	long := strings.Repeat("é", agent24IDMaxRunes)
	for name, tc := range map[string]struct {
		chunkID string
		c       agent24ReadCursor
	}{
		"chunk":         {"chk", agent24ReadCursor{ContentTag: hex64, Offset: 1}},
		"chunk at zero": {"chk", agent24ReadCursor{ContentTag: hex64}},
		"doc":           {"", agent24ReadCursor{SourceRevision: hex64, IndexBuildID: hex64, Position: 3}},
		"doc at zero":   {"", agent24ReadCursor{SourceRevision: hex64, IndexBuildID: hex64}},
		"doc inside": {"", agent24ReadCursor{
			SourceRevision: hex64, IndexBuildID: hex64, ContentTag: hex64, Position: 9, Offset: 262143,
		}},
		"long ids, chunk": {long, agent24ReadCursor{ContentTag: hex64, Offset: 262143}},
	} {
		kid := "doc"
		if strings.HasPrefix(name, "long") {
			kid = long
		}
		raw := agent24EncodeReadCursor(agent24ReadTestKey, kid, tc.chunkID, tc.c)
		assert.LessOrEqual(t, len(raw), agent24ReadCursorMaxBytes, name)
		assert.NotEmpty(t, raw, name)
		got, ok := agent24DecodeReadCursor(agent24ReadTestKey, kid, tc.chunkID, raw)
		assert.True(t, ok, name)
		assert.Equal(t, tc.c, got, name)
	}

	// Field combinations that do not fit the read kind are rejected even under a valid HMAC.
	for name, tc := range map[string]struct {
		chunkID string
		c       agent24ReadCursor
	}{
		"chunk without tag":      {"chk", agent24ReadCursor{Offset: 1}},
		"chunk with source":      {"chk", agent24ReadCursor{ContentTag: "t", SourceRevision: "s"}},
		"chunk with build":       {"chk", agent24ReadCursor{ContentTag: "t", IndexBuildID: "b"}},
		"chunk with position":    {"chk", agent24ReadCursor{ContentTag: "t", Position: 1}},
		"chunk negative offset":  {"chk", agent24ReadCursor{ContentTag: "t", Offset: -1}},
		"doc without source":     {"", agent24ReadCursor{IndexBuildID: "b"}},
		"doc without build":      {"", agent24ReadCursor{SourceRevision: "s"}},
		"doc offset without tag": {"", agent24ReadCursor{SourceRevision: "s", IndexBuildID: "b", Offset: 5}},
		"doc tag without offset": {"", agent24ReadCursor{SourceRevision: "s", IndexBuildID: "b", ContentTag: "t"}},
		"doc negative position":  {"", agent24ReadCursor{SourceRevision: "s", IndexBuildID: "b", Position: -1}},
	} {
		raw := agent24EncodeReadCursor(agent24ReadTestKey, "doc", tc.chunkID, tc.c)
		_, ok := agent24DecodeReadCursor(agent24ReadTestKey, "doc", tc.chunkID, raw)
		assert.False(t, ok, name)
	}

	// Malformed cursors.
	good := agent24EncodeReadCursor(agent24ReadTestKey, "doc", "",
		agent24ReadCursor{SourceRevision: "s", IndexBuildID: "b"})
	payload, mac, _ := strings.Cut(good, ".")
	for name, raw := range map[string]string{
		"empty":            "",
		"no dot":           payload + mac,
		"payload not b64":  "!!!." + mac,
		"payload not json": base64.RawURLEncoding.EncodeToString([]byte("[1]")) + "." + mac,
		"mac not hex":      payload + ".zz",
		"mac truncated":    payload + "." + mac[:len(mac)-2],
		"extra part":       good + ".x",
		"payload replaced": base64.RawURLEncoding.EncodeToString([]byte(`{"s":"s","b":"c"}`)) + "." + mac,
	} {
		_, ok := agent24DecodeReadCursor(agent24ReadTestKey, "doc", "", raw)
		assert.False(t, ok, name)
	}

	// The HMAC binds the ids as a JSON array: moving a NUL between them does not keep it valid.
	a := agent24EncodeReadCursor(agent24ReadTestKey, "a\x00b", "c", agent24ReadCursor{ContentTag: "t"})
	_, ok := agent24DecodeReadCursor(agent24ReadTestKey, "a", "b\x00c", a)
	assert.False(t, ok)
}
