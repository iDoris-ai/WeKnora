// Agent24 fork product patch A24-KB-12 (CONTRACT-READONLY-v1 r10 §4 R4.1, R4.3, R4.5; §6 R6.4).
//
// Read request parsing and the read cursor. Pure functions; the snapshot, the R4.4 judgement and the
// endpoint wiring land in later slices (WeKnora#38).

package handler

import (
	"bytes"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// agent24ReadCursorMaxBytes is maxLength of page.cursor and next_cursor (read.schema.json).
const agent24ReadCursorMaxBytes = 1024

// agent24ReadRequest is a parsed read request; PageLimit, MaxBytes and DeadlineMS hold the applied values
// (absent: the server limit; larger: clamped, R4.5).
type agent24ReadRequest struct {
	KBIDs       []string // Q, deduped, request order
	KnowledgeID string
	ChunkID     string               // "": a document-level read
	Expected    *agent24ReadExpected // nil: no expected_revision
	Cursor      *agent24ReadCursor   // nil: the first page, or no key to check the cursor with
	PageLimit   int
	MaxBytes    int
	DeadlineMS  int
}

// agent24ReadExpected holds the compared parts of expected_revision (R4.3): ContentTag for a single-chunk
// read (the other parts are validated, then dropped); SourceRevision and IndexBuildID for a document-level
// read.
type agent24ReadExpected struct {
	ContentTag     string
	SourceRevision string
	IndexBuildID   string
}

// agent24ReadCursor is the position a cursor resumes at and the revision it is bound to (R4.5).
//   - Single-chunk read: ContentTag of the chunk; Offset bytes into its text.
//   - Document-level read: SourceRevision and IndexBuildID of the first page; Position is the index, in
//     document order, of the next segment; Offset bytes into that segment's text, and when Offset > 0,
//     ContentTag is that segment's content_tag.
type agent24ReadCursor struct {
	SourceRevision string `json:"s,omitempty"`
	IndexBuildID   string `json:"b,omitempty"`
	ContentTag     string `json:"t,omitempty"`
	Position       int    `json:"p,omitempty"`
	Offset         int    `json:"o,omitempty"`
}

// agent24EncodeReadCursor returns base64url(JSON of c) "." HMAC, the HMAC (agent24MAC, domain "cursor")
// taken over the JSON array [knowledgeID, chunkID, JSON of c]. At most agent24ReadCursorMaxBytes.
func agent24EncodeReadCursor(key []byte, knowledgeID, chunkID string, c agent24ReadCursor) string {
	payload, _ := json.Marshal(c)
	bind, _ := json.Marshal([]string{knowledgeID, chunkID, string(payload)})
	mac := agent24MAC(key, "cursor", string(bind))
	return base64.RawURLEncoding.EncodeToString(payload) + "." + mac
}

// agent24DecodeReadCursor reverses agent24EncodeReadCursor; false when raw is malformed, its HMAC does not
// match knowledgeID and chunkID under key, a field is negative, or the fields do not fit the read kind
// (single-chunk: ContentTag set, SourceRevision, IndexBuildID and Position empty; document-level:
// SourceRevision and IndexBuildID set, ContentTag set exactly when Offset > 0).
func agent24DecodeReadCursor(key []byte, knowledgeID, chunkID, raw string) (agent24ReadCursor, bool) {
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return agent24ReadCursor{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return agent24ReadCursor{}, false
	}
	bind, _ := json.Marshal([]string{knowledgeID, chunkID, string(payload)})
	want := agent24MAC(key, "cursor", string(bind))
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	var c agent24ReadCursor
	if !hmac.Equal([]byte(want), []byte(parts[1])) || dec.Decode(&c) != nil {
		return agent24ReadCursor{}, false
	}
	if c.Position < 0 || c.Offset < 0 { // the HMAC already proves this server marshaled the payload
		return agent24ReadCursor{}, false
	}
	if chunkID != "" {
		return c, c.ContentTag != "" && c.SourceRevision == "" && c.IndexBuildID == "" && c.Position == 0
	}
	return c, c.SourceRevision != "" && c.IndexBuildID != "" && (c.Offset > 0) == (c.ContentTag != "")
}

// parseAgent24ReadRequest validates the body after the common fields (R4.1, R4.3, R4.5, R2.5). With a nil
// key the cursor is checked only as a string (1..agent24ReadCursorMaxBytes) and Cursor stays nil, so the
// disabled-operation check that follows the shape check still decides (R6.4); with a key, a cursor that
// agent24DecodeReadCursor rejects is invalid_request.
func parseAgent24ReadRequest(key []byte, body []byte) (agent24ReadRequest, agent24KnowledgeEcho,
	*agent24KnowledgeError,
) {
	fields, echo, kerr := parseAgent24KnowledgeCommon(body)
	if kerr != nil {
		return agent24ReadRequest{}, echo, kerr
	}
	ids, scopeOK := agent24ParseScope(fields["scope"])
	knowledgeID, idOK := agent24ReadJSONString(fields["knowledge_id"], agent24IDMaxRunes)
	chunkID, chunkOK := "", true
	if raw, ok := fields["chunk_id"]; ok {
		chunkID, chunkOK = agent24ReadJSONString(raw, agent24IDMaxRunes)
	}
	var expected *agent24ReadExpected
	expOK := true
	if raw, ok := fields["expected_revision"]; ok {
		expected, expOK = agent24ParseReadExpected(raw, chunkID != "")
	}
	cursor, limit, pageOK := agent24ReadOptPage(fields)
	maxBytes, maxOK := agent24KnowledgeMaxReadBytes, true
	if raw, ok := fields["max_bytes"]; ok {
		maxBytes, maxOK = agent24JSONPositiveInt(raw, maxBytes)
	}
	deadline, dlOK := agent24KnowledgeMaxDeadlineMS, true
	if raw, ok := fields["deadline_ms"]; ok {
		deadline, dlOK = agent24JSONPositiveInt(raw, deadline)
	}
	if !agent24ReadFieldsAllowed(fields) || !scopeOK || !idOK || !chunkOK || !expOK || !pageOK || !maxOK ||
		!dlOK {
		return agent24ReadRequest{}, echo, agent24InvalidRequest()
	}
	req := agent24ReadRequest{
		KBIDs: ids, KnowledgeID: knowledgeID, ChunkID: chunkID, Expected: expected,
		PageLimit: limit, MaxBytes: maxBytes, DeadlineMS: deadline,
	}
	if cursor != "" && key != nil {
		c, good := agent24DecodeReadCursor(key, knowledgeID, chunkID, cursor)
		if !good {
			return agent24ReadRequest{}, echo, agent24InvalidRequest()
		}
		req.Cursor = &c
	}
	return req, echo, nil
}

// agent24ReadFieldsAllowed reports whether fields has only read keys (scope and knowledge_id, required, fail
// their own parse when absent).
func agent24ReadFieldsAllowed(fields map[string]json.RawMessage) bool {
	for k := range fields {
		switch k {
		case "protocol_version", "request_id", "generation", "scope", "knowledge_id", "chunk_id",
			"expected_revision", "page", "max_bytes", "deadline_ms":
		default:
			return false
		}
	}
	return true
}

// agent24ReadJSONString reads a JSON string of 1..maxRunes runes.
func agent24ReadJSONString(raw json.RawMessage, maxRunes int) (string, bool) {
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	n := utf8.RuneCountInString(s)
	return s, n >= 1 && n <= maxRunes
}

// agent24ParseReadExpected parses expected_revision; chunked selects the single-chunk shape.
func agent24ParseReadExpected(raw json.RawMessage, chunked bool) (*agent24ReadExpected, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, false
	}
	if !chunked {
		src, a := agent24ReadJSONString(obj["source_revision"], agent24IDMaxRunes)
		build, b := agent24ReadJSONString(obj["index_build_id"], agent24IDMaxRunes)
		if len(obj) == 2 && a && b {
			return &agent24ReadExpected{SourceRevision: src, IndexBuildID: build}, true
		}
		return nil, false
	}
	tag, ok := agent24ReadJSONString(obj["content_tag"], agent24IDMaxRunes)
	for k, v := range obj {
		switch k {
		case "content_tag":
		case "source_revision", "index_build_id":
			_, good := agent24ReadJSONString(v, agent24IDMaxRunes)
			ok = ok && good
		case "chunk_revision": // null or an integer >= 0, as JSON Schema reads integers (0.0, 3.0, 1e2)
			_, positive := agent24JSONPositiveInt(v, 1)
			ok = ok && (string(v) == "null" || positive || agent24JSONZero(v))
		default:
			ok = false
		}
	}
	if !ok {
		return nil, false
	}
	return &agent24ReadExpected{ContentTag: tag}, true
}

// agent24JSONZero reports whether raw is a JSON number equal to zero in any spelling (0, -0, 0.0, 0e5).
func agent24JSONZero(raw json.RawMessage) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return false
	}
	n, ok := v.(json.Number)
	mant, _, _ := strings.Cut(strings.ToLower(string(n)), "e")
	return ok && strings.Trim(strings.TrimPrefix(mant, "-"), "0.") == ""
}

// agent24ReadOptPage reads the optional page: the cursor ("" for null or no page), the applied limit, and
// whether page is well formed.
func agent24ReadOptPage(fields map[string]json.RawMessage) (string, int, bool) {
	raw, present := fields["page"]
	if !present {
		return "", agent24KnowledgeMaxPageLimit, true
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return "", 0, false
	}
	craw, hasCursor := obj["cursor"]
	limitRaw, hasLimit := obj["limit"]
	if !hasCursor || len(obj) > 2 || len(obj) == 2 && !hasLimit {
		return "", 0, false
	}
	cursor := ""
	if string(craw) != "null" {
		var ok bool
		if cursor, ok = agent24ReadJSONString(craw, agent24ReadCursorMaxBytes); !ok {
			return "", 0, false
		}
	}
	if !hasLimit {
		return cursor, agent24KnowledgeMaxPageLimit, true
	}
	limit, ok := agent24JSONPositiveInt(limitRaw, agent24KnowledgeMaxPageLimit)
	return cursor, limit, ok
}
