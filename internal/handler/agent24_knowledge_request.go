// Agent24 fork product patch A24-KB-03, A24-KB-20 (CONTRACT-READONLY-v1 R6.1, WeKnora#56).
//
// Common request fields of the Agent24 knowledge integration endpoints.

package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

var agent24KnowledgeSupportedProtocolVersions = []int{agent24KnowledgeProtocolVersion}

// agent24KnowledgeMaxBodyBytes caps every agent24 knowledge request body; a larger body is invalid_request.
const agent24KnowledgeMaxBodyBytes = 1 << 20

// agent24ReadBody reads the request body, at most agent24KnowledgeMaxBodyBytes; false on a read error or
// a larger body.
func agent24ReadBody(c *gin.Context) ([]byte, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, agent24KnowledgeMaxBodyBytes)
	body, err := io.ReadAll(c.Request.Body)
	return body, err == nil
}

// agent24RequestIDMaxRunes is maxLength of request_id (common.schema.json), in characters.
const agent24RequestIDMaxRunes = 128

// agent24JSONDecimal reads a JSON number exactly, as |value| = digits × 10^exp with digits free of leading
// and trailing zeros ("" for zero). The literal is read digit by digit, in time linear in its length, with
// no rounding. false if raw is not a JSON number.
func agent24JSONDecimal(raw json.RawMessage) (neg bool, digits string, exp int64, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return false, "", 0, false
	}
	n, isNum := v.(json.Number)
	if !isNum {
		return false, "", 0, false
	}
	lit, neg := strings.CutPrefix(strings.ToLower(string(n)), "-")
	mant, expPart, _ := strings.Cut(lit, "e")
	intPart, frac, _ := strings.Cut(mant, ".")
	if expPart != "" {
		var err error
		if exp, err = strconv.ParseInt(expPart, 10, 32); err != nil { // beyond int32: only the sign matters
			exp = 1 << 31
			if strings.HasPrefix(expPart, "-") {
				exp = -exp
			}
		}
	}
	all := strings.TrimLeft(intPart+frac, "0")
	digits = strings.TrimRight(all, "0")
	exp += int64(len(all)-len(digits)) - int64(len(frac))
	return neg, digits, exp, true
}

// agent24JSONInt64 reads a JSON number that is exactly an integer in int64. As in JSON Schema 2020-12,
// 1.0 and 1e0 are integers (and -0 is 0); a fractional part or a value outside int64 is false, never
// clamped.
func agent24JSONInt64(raw json.RawMessage) (int64, bool) {
	neg, digits, exp, ok := agent24JSONDecimal(raw)
	switch {
	case !ok:
		return 0, false
	case digits == "":
		return 0, true
	case exp < 0 || int64(len(digits))+exp > 19: // a fraction, or more digits than int64 holds
		return 0, false
	}
	u, err := strconv.ParseUint(digits+strings.Repeat("0", int(exp)), 10, 64)
	switch {
	case err != nil || u > 1<<63 || (!neg && u == 1<<63):
		return 0, false
	case neg:
		return int64(-u), true
	}
	return int64(u), true
}

// parseAgent24KnowledgeCommon decodes the body and validates protocol_version,
// request_id and generation, echoing whichever of the latter two is valid.
func parseAgent24KnowledgeCommon(
	body []byte,
) (map[string]json.RawMessage, agent24KnowledgeEcho, *agent24KnowledgeError) {
	var echo agent24KnowledgeEcho
	var fields map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&fields); err != nil || fields == nil {
		return nil, echo, agent24InvalidRequest()
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, echo, agent24InvalidRequest()
	}

	if raw, ok := fields["request_id"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" && utf8.RuneCountInString(s) <= agent24RequestIDMaxRunes {
			echo.RequestID = &s
		}
	}
	if raw, ok := fields["generation"]; ok {
		if g, ok := agent24JSONInt64(raw); ok && g >= 0 {
			echo.Generation = &g
		}
	}

	raw, ok := fields["protocol_version"]
	if !ok {
		return nil, echo, agent24InvalidRequest()
	}
	pv, ok := agent24JSONInt64(raw)
	if !ok {
		return nil, echo, agent24InvalidRequest()
	}
	if !slices.Contains(agent24KnowledgeSupportedProtocolVersions, int(pv)) || int64(int(pv)) != pv {
		return nil, echo, agent24UnsupportedProtocolVersion(agent24KnowledgeSupportedProtocolVersions)
	}
	if echo.RequestID == nil || echo.Generation == nil {
		return nil, echo, agent24InvalidRequest()
	}
	return fields, echo, nil
}
