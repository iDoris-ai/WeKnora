// Agent24 fork product patch A24-KB-43 (KB-SOP-02 S2, WeKnora#146, CONTRACT-OPS-v2 W1.4, W1.6, W1.9 ②, W3.1,
// W8.2, W8.8, W8.13; schema protocol/knowledge/ops-v2/candidate.schema.json at Agent24 ab/weknora f7291afd).
//
// The request bodies of the candidate.propose routes the Provider sends with C. Unlike search and read they have
// no protocol_version (the ops-v2 schema does not list it). W1.9 ② (shape) is checked here; the W8.2 ⑤ checks
// (kind, lengths, the hash) run after authorization, in agent24_knowledge_candidates.go.

package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// agent24HostApprovalFields are the fields W1.6 strips before the shape check: a host-relayed approval is never
// believed, and its presence neither fails the request nor takes effect. The stripping is audited.
var agent24HostApprovalFields = []string{"approved", "author", "approver"}

var (
	agent24IdempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{22,128}$`)
	agent24SHA256Pattern         = regexp.MustCompile(`^[0-9a-f]{64}$`)
	agent24SkillVersionPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,63}$`)
)

// agent24WireIDMaxRunes is maxLength of the ops-v2 id (candidate_id, knowledge base and knowledge ids, revisions).
const agent24WireIDMaxRunes = 256

// The W8.2 provenance bounds.
const (
	agent24MessageRangeMax         = 100000
	agent24RedactionRulesMax       = 2147483647
	agent24ProvenanceSourceAgent24 = "agent24_conversation"
)

// agent24WriteRequest is a decoded write request body: its fields, the echo, and which W1.6 fields were stripped.
type agent24WriteRequest struct {
	fields   map[string]json.RawMessage
	echo     agent24KnowledgeEcho
	stripped []string
}

// parseAgent24WriteCommon decodes a write request body (one JSON object, nothing after it), strips the W1.6 host
// approval fields, refuses any field not in allowed (and not stripped), and reads request_id and, for a Provider
// request, generation. A body carrying a lone surrogate escape is invalid (W8.13): Go would decode it to U+FFFD,
// and the hash the client computed could not be reproduced. Invalid UTF-8 is refused for the same reason (it is not
// JSON, RFC 8259, and the decoder would replace it). The stripped fields are reported even when the request is then
// refused, so the refusal's audit row records them too (F2).
func parseAgent24WriteCommon(
	body []byte, provider bool, allowed ...string,
) (agent24WriteRequest, *agent24KnowledgeError) {
	var req agent24WriteRequest
	if !utf8.Valid(body) || agent24JSONHasLoneSurrogate(body) {
		return req, agent24InvalidRequest()
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&req.fields); err != nil || req.fields == nil {
		return req, agent24InvalidRequest()
	}
	if _, err := dec.Token(); err != io.EOF {
		return req, agent24InvalidRequest()
	}
	if raw, ok := req.fields["request_id"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" && utf8.RuneCountInString(s) <= agent24RequestIDMaxRunes {
			req.echo.RequestID = &s
		}
	}
	if provider {
		if raw, ok := req.fields["generation"]; ok {
			if g, ok := agent24JSONInt64(raw); ok && g >= 0 {
				req.echo.Generation = &g
			}
		}
	}
	for _, name := range agent24HostApprovalFields {
		if _, ok := req.fields[name]; ok {
			delete(req.fields, name)
			req.stripped = append(req.stripped, name)
		}
	}
	known := map[string]bool{"request_id": true}
	if provider {
		known["generation"] = true
	}
	for _, name := range allowed {
		known[name] = true
	}
	for name := range req.fields {
		if !known[name] {
			return req, agent24InvalidRequest()
		}
	}
	if req.echo.RequestID == nil || (provider && req.echo.Generation == nil) {
		return req, agent24InvalidRequest()
	}
	return req, nil
}

// agent24JSONHasLoneSurrogate reports whether a JSON text has a \u escape of a UTF-16 surrogate that is not one
// half of a high-low pair. Escapes are only read inside strings; a malformed escape is left to the decoder.
func agent24JSONHasLoneSurrogate(body []byte) bool {
	inString := false
	pendingHigh := false
	for i := 0; i < len(body); i++ {
		b := body[i]
		if !inString {
			if b == '"' {
				inString = true
			}
			continue
		}
		switch {
		case b == '"':
			if pendingHigh {
				return true
			}
			inString = false
		case b == '\\' && i+1 < len(body):
			if body[i+1] != 'u' || i+5 >= len(body) {
				if pendingHigh {
					return true
				}
				i++
				continue
			}
			u, ok := agent24Hex4(body[i+2 : i+6])
			i += 5
			if !ok {
				continue
			}
			switch {
			case u >= 0xD800 && u <= 0xDBFF:
				if pendingHigh {
					return true
				}
				pendingHigh = true
			case u >= 0xDC00 && u <= 0xDFFF:
				if !pendingHigh {
					return true
				}
				pendingHigh = false
			default:
				if pendingHigh {
					return true
				}
			}
		default:
			if pendingHigh {
				return true
			}
		}
	}
	return pendingHigh
}

func agent24Hex4(b []byte) (rune, bool) {
	var v rune
	for _, c := range b {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			v |= rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= rune(c-'A') + 10
		default:
			return 0, false
		}
	}
	return v, true
}

// agent24WireString reads a required JSON string field; false if absent or not a string.
func agent24WireString(fields map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := fields[name]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// agent24WireID reads a required ops-v2 id field (1–256 characters).
func agent24WireID(fields map[string]json.RawMessage, name string) (string, bool) {
	s, ok := agent24WireString(fields, name)
	if !ok || s == "" || utf8.RuneCountInString(s) > agent24WireIDMaxRunes {
		return "", false
	}
	return s, true
}

// agent24CandidateSubmit is a parsed candidate.propose submission. Kind, Title and Body are only shape-checked here
// (strings, no CR, restricted Markdown); their W8.2 ⑤1 checks run after authorization.
type agent24CandidateSubmit struct {
	IdempotencyKey        string
	TargetKnowledgeBaseID string
	Mode                  string
	KnowledgeID           string
	ExpectedRevision      string
	Kind                  string
	Title                 string
	Body                  string
	ContentSHA256         string
	SessionRef            string
	MessageFrom           int
	MessageTo             int
	SkillVersion          string
	RedactionRulesVersion int64
}

var agent24CandidateSubmitFields = []string{
	"idempotency_key", "target_knowledge_base_id", "mode", "knowledge_id", "expected_source_revision", "kind",
	"title", "body", "content_sha256", "provenance",
}

// parseAgent24CandidateSubmit is W1.9 ② for a submission: every field present and of its type, the mode's own
// fields, no CR in kind, title or body (W8.13), the restricted Markdown of W8.2 (unsupported_format), and the
// provenance bounds. A missing idempotency key is invalid_request/idempotency_key_missing (W3.1).
func parseAgent24CandidateSubmit(body []byte) (agent24CandidateSubmit, agent24WriteRequest, *agent24KnowledgeError) {
	var s agent24CandidateSubmit
	req, kerr := parseAgent24WriteCommon(body, true, agent24CandidateSubmitFields...)
	if kerr != nil {
		return s, req, kerr
	}
	f := req.fields
	raw, ok := f["idempotency_key"]
	if !ok {
		return s, req, agent24InvalidWrite("idempotency_key_missing")
	}
	if json.Unmarshal(raw, &s.IdempotencyKey) != nil || !agent24IdempotencyKeyPattern.MatchString(s.IdempotencyKey) {
		return s, req, agent24InvalidRequest()
	}
	var okAll bool
	if s.TargetKnowledgeBaseID, okAll = agent24WireID(f, "target_knowledge_base_id"); !okAll {
		return s, req, agent24InvalidRequest()
	}
	s.Mode, ok = agent24WireString(f, "mode")
	_, hasKnowledge := f["knowledge_id"]
	_, hasRevision := f["expected_source_revision"]
	switch {
	case !ok:
		return s, req, agent24InvalidRequest()
	case s.Mode == "create":
		if hasKnowledge || hasRevision {
			return s, req, agent24InvalidRequest()
		}
	case s.Mode == "revise":
		if s.KnowledgeID, ok = agent24WireID(f, "knowledge_id"); !ok {
			return s, req, agent24InvalidRequest()
		}
		if s.ExpectedRevision, ok = agent24WireID(f, "expected_source_revision"); !ok {
			return s, req, agent24InvalidRequest()
		}
	default:
		return s, req, agent24InvalidRequest()
	}
	for name, dst := range map[string]*string{"kind": &s.Kind, "title": &s.Title, "body": &s.Body} {
		v, ok := agent24WireString(f, name)
		if !ok || strings.ContainsRune(v, '\r') {
			return s, req, agent24InvalidRequest()
		}
		*dst = v
	}
	s.ContentSHA256, ok = agent24WireString(f, "content_sha256")
	if !ok || !agent24SHA256Pattern.MatchString(s.ContentSHA256) {
		return s, req, agent24InvalidRequest()
	}
	if kerr := parseAgent24Provenance(f["provenance"], &s); kerr != nil {
		return s, req, kerr
	}
	if agent24RestrictedMarkdownViolation(s.Body) {
		return s, req, agent24InvalidWrite("unsupported_format")
	}
	return s, req, nil
}

// parseAgent24Provenance reads the W8.2 provenance object: exactly its five fields, the source constant, a 64-hex
// session_ref, an ordered message_range in 0–100000, the skill_version pattern, and a positive rules version.
func parseAgent24Provenance(raw json.RawMessage, s *agent24CandidateSubmit) *agent24KnowledgeError {
	if raw == nil {
		return agent24InvalidRequest()
	}
	var p map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&p) != nil || p == nil || len(p) != 5 {
		return agent24InvalidRequest()
	}
	if src, ok := agent24WireString(p, "source"); !ok || src != agent24ProvenanceSourceAgent24 {
		return agent24InvalidRequest()
	}
	var ok bool
	if s.SessionRef, ok = agent24WireString(p, "session_ref"); !ok || !agent24SHA256Pattern.MatchString(s.SessionRef) {
		return agent24InvalidRequest()
	}
	if s.SkillVersion, ok = agent24WireString(p, "skill_version"); !ok ||
		!agent24SkillVersionPattern.MatchString(s.SkillVersion) {
		return agent24InvalidRequest()
	}
	var rng []json.RawMessage
	if json.Unmarshal(p["message_range"], &rng) != nil || len(rng) != 2 {
		return agent24InvalidRequest()
	}
	from, okFrom := agent24JSONInt64(rng[0])
	to, okTo := agent24JSONInt64(rng[1])
	if !okFrom || !okTo || from < 0 || to > agent24MessageRangeMax || from > to {
		return agent24InvalidRequest()
	}
	s.MessageFrom, s.MessageTo = int(from), int(to)
	v, ok := agent24JSONInt64(p["redaction_rules_version"])
	if !ok || v < 1 || v > agent24RedactionRulesMax {
		return agent24InvalidRequest()
	}
	s.RedactionRulesVersion = v
	return nil
}

// parseAgent24CandidateRef reads the body of candidate.propose withdraw and get: request_id, generation and
// candidate_id only.
func parseAgent24CandidateRef(body []byte) (string, agent24WriteRequest, *agent24KnowledgeError) {
	req, kerr := parseAgent24WriteCommon(body, true, "candidate_id")
	if kerr != nil {
		return "", req, kerr
	}
	id, ok := agent24WireID(req.fields, "candidate_id")
	if !ok {
		return "", req, agent24InvalidRequest()
	}
	return id, req, nil
}
