// Agent24 fork product patch A24-KB-03 (CONTRACT-READONLY-v1 R6.1).
//
// Common request fields of the Agent24 knowledge integration endpoints.

package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"

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

const agent24RequestIDMaxBytes = 128

// agent24JSONInt64 reports whether raw is a JSON integer that fits in int64.
func agent24JSONInt64(raw json.RawMessage) (int64, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return 0, false
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return i, err == nil
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
		if json.Unmarshal(raw, &s) == nil && len(s) >= 1 && len(s) <= agent24RequestIDMaxBytes {
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
