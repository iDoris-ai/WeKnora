// Agent24 fork product patch A24-KB-06 (KB-C1-01c ③, CONTRACT-READONLY-v1 r9 R5.1):
// content-addressed revision identifiers. Every identifier is an HMAC-SHA256 of
// a domain prefix and its parts, each separated by a NUL byte, text as UTF-8,
// output as lowercase hex. Pure functions only; wiring into search, read and
// readiness comes in later slices.

package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// Agent24KnowledgeTagKeyEnv names the variable holding the HMAC key, standard
// base64, at least agent24TagKeyMinBytes bytes once decoded. Integration mode
// refuses to start without it (R5.1).
const Agent24KnowledgeTagKeyEnv = "AGENT24_KNOWLEDGE_TAG_KEY"

const agent24TagKeyMinBytes = 32

var errAgent24TagKeyMissing = errors.New("agent24 integration mode requires " +
	Agent24KnowledgeTagKeyEnv + ": a standard-base64 key of at least 32 bytes")

// Agent24KnowledgeTagKeyFromEnv decodes the key; unset, invalid base64 or a key
// shorter than agent24TagKeyMinBytes is an error (surrounding whitespace is
// tolerated).
func Agent24KnowledgeTagKeyFromEnv() ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(Agent24KnowledgeTagKeyEnv))
	if raw == "" {
		return nil, errAgent24TagKeyMissing
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) < agent24TagKeyMinBytes {
		return nil, errAgent24TagKeyMissing
	}
	return key, nil
}

// agent24MAC is HMAC-SHA256 over the parts joined by single NUL bytes, as
// lowercase hex (R5.1).
func agent24MAC(key []byte, parts ...string) string {
	mac := hmac.New(sha256.New, key)
	for i, part := range parts {
		if i > 0 {
			mac.Write([]byte{0x00})
		}
		mac.Write([]byte(part))
	}
	sum := mac.Sum(nil)
	return hex.EncodeToString(sum)
}

// agent24SourceFingerprint is the upstream knowledge row's source identity (R5.1).
type agent24SourceFingerprint struct {
	FileHash      string
	FilePath      string
	Source        string
	ManualVersion int // 0 for non-manual knowledge
}

// agent24ContentTag: HMAC(key, "chunk", chunkID, content).
func agent24ContentTag(key []byte, chunkID, content string) string {
	return agent24MAC(key, "chunk", chunkID, content)
}

// agent24SourceRevision: HMAC(key, "source", knowledgeID, FileHash, FilePath,
// Source, decimal ManualVersion).
func agent24SourceRevision(key []byte, knowledgeID string, fp agent24SourceFingerprint) string {
	return agent24MAC(key, "source", knowledgeID, fp.FileHash, fp.FilePath, fp.Source, strconv.Itoa(fp.ManualVersion))
}

// agent24IndexBuildID: HMAC(key, "build", knowledgeID, servable chunk IDs in
// ascending byte order). The empty set is valid; the input slice is not modified.
func agent24IndexBuildID(key []byte, knowledgeID string, servableChunkIDs []string) string {
	sorted := append([]string(nil), servableChunkIDs...)
	sort.Strings(sorted)
	return agent24MAC(key, append([]string{"build", knowledgeID}, sorted...)...)
}

// agent24ServableChunk: a text, enabled, not soft-deleted chunk (R5.1). Whether
// its document has a build is a separate check (agent24HasBuild).
func agent24ServableChunk(c *types.Chunk) bool {
	return c.ChunkType == types.ChunkTypeText && c.IsEnabled && !c.DeletedAt.Valid
}

// agent24HasBuild: parse_status completed or finalizing (R5.1).
func agent24HasBuild(parseStatus string) bool {
	return parseStatus == types.ParseStatusCompleted || parseStatus == types.ParseStatusFinalizing
}

// agent24DocClass is a document's state for read and readiness (contract §5 table, r9).
type agent24DocClass int

const (
	agent24DocExcluded agent24DocClass = iota // deleting, draft or unknown: not counted, read is not_found
	agent24DocBuilding                        // pending, processing: index_pending / index_building
	agent24DocFailed                          // failed, cancelled: index_failed
	agent24DocServable                        // has a build with at least one servable chunk
	agent24DocEmpty                           // has a build, no servable chunk: 200 segments [] / unsupported_kb_type
)

// agent24ClassifyDoc maps a parse_status and the document's servable chunk count
// to its class.
func agent24ClassifyDoc(parseStatus string, servable int) agent24DocClass {
	switch parseStatus {
	case types.ParseStatusPending, types.ParseStatusProcessing:
		return agent24DocBuilding
	case types.ParseStatusFailed, types.ParseStatusCancelled:
		return agent24DocFailed
	case types.ParseStatusCompleted, types.ParseStatusFinalizing:
		if servable > 0 {
			return agent24DocServable
		}
		return agent24DocEmpty
	default:
		return agent24DocExcluded
	}
}
