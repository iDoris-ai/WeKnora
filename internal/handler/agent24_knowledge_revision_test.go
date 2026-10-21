package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
)

var agent24HexID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// wantMAC is the contract's encoding (R5.1, r9), written out independently.
func wantMAC(key []byte, parts ...string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(m.Sum(nil))
}

func TestAgent24KnowledgeTagKeyFromEnv(t *testing.T) {
	k32 := base64.StdEncoding.EncodeToString(make([]byte, 32))
	k31 := base64.StdEncoding.EncodeToString(make([]byte, 31))
	cases := []struct {
		name, val string
		ok        bool
	}{
		{"unset", "", false},
		{"not base64", "!!!", false},
		{"31 bytes", k31, false},
		{"32 bytes", k32, true},
		{"32 bytes with spaces", "  " + k32 + "\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(Agent24KnowledgeTagKeyEnv, tc.val)
			key, err := Agent24KnowledgeTagKeyFromEnv()
			if !tc.ok {
				require.Error(t, err)
				if tc.val != "" {
					assert.NotContains(t, err.Error(), tc.val, "the error must not echo the key")
				}
				return
			}
			require.NoError(t, err)
			assert.Len(t, key, 32)
		})
	}
}

func TestValidateAgent24IntegrationConfigNeedsTagKey(t *testing.T) {
	tr := true
	cfg := &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &tr}}
	t.Setenv(Agent24KnowledgeTagKeyEnv, "")
	require.Error(t, ValidateAgent24IntegrationConfig(cfg))
	t.Setenv(Agent24KnowledgeTagKeyEnv, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	require.NoError(t, ValidateAgent24IntegrationConfig(cfg))
}

func TestAgent24RevisionIdentifiers(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	other := []byte("fedcba9876543210fedcba9876543210")

	tag := agent24ContentTag(key, "chk-1", "正文")
	assert.Regexp(t, agent24HexID, tag)
	assert.Equal(t, wantMAC(key, "chunk", "chk-1", "正文"), tag)
	assert.NotEqual(t, tag, agent24ContentTag(key, "chk-1", "正文 "), "content")
	assert.NotEqual(t, tag, agent24ContentTag(key, "chk-2", "正文"), "chunk id")
	assert.NotEqual(t, tag, agent24ContentTag(other, "chk-1", "正文"), "key")

	fp := agent24SourceFingerprint{FileHash: "h", FilePath: "p", Source: "s", ManualVersion: 3}
	src := agent24SourceRevision(key, "doc-1", fp)
	assert.Equal(t, wantMAC(key, "source", "doc-1", "h", "p", "s", "3"), src)
	for name, f := range map[string]agent24SourceFingerprint{
		"file_hash": {FileHash: "h2", FilePath: "p", Source: "s", ManualVersion: 3},
		"file_path": {FileHash: "h", FilePath: "p2", Source: "s", ManualVersion: 3},
		"source":    {FileHash: "h", FilePath: "p", Source: "s2", ManualVersion: 3},
		"version":   {FileHash: "h", FilePath: "p", Source: "s", ManualVersion: 4},
	} {
		assert.NotEqual(t, src, agent24SourceRevision(key, "doc-1", f), name)
	}
	assert.NotEqual(t, src, agent24SourceRevision(key, "doc-2", fp), "knowledge id")

	ids := []string{"chk-b", "chk-a", "chk-c"}
	build := agent24IndexBuildID(key, "doc-1", ids)
	assert.Equal(t, wantMAC(key, "build", "doc-1", "chk-a", "chk-b", "chk-c"), build)
	assert.Equal(t, []string{"chk-b", "chk-a", "chk-c"}, ids, "input not modified")
	assert.Equal(t, build, agent24IndexBuildID(key, "doc-1", []string{"chk-c", "chk-b", "chk-a"}), "order")
	assert.NotEqual(t, build, agent24IndexBuildID(key, "doc-1", []string{"chk-a", "chk-b"}), "set")
	assert.NotEqual(t, build, agent24IndexBuildID(key, "doc-2", ids), "knowledge id")
	empty := agent24IndexBuildID(key, "doc-1", nil)
	assert.Equal(t, wantMAC(key, "build", "doc-1"), empty)
	assert.NotEqual(t, empty, agent24IndexBuildID(key, "doc-2", nil), "empty sets differ by knowledge id")

	// Domain separation: the same parts under different prefixes never collide.
	assert.NotEqual(t, agent24ContentTag(key, "doc-1", "x"), agent24IndexBuildID(key, "doc-1", []string{"x"}))
}

func TestAgent24ServableChunk(t *testing.T) {
	ok := types.Chunk{ChunkType: types.ChunkTypeText, IsEnabled: true}
	assert.True(t, agent24ServableChunk(&ok))
	faq := ok
	faq.ChunkType = types.ChunkTypeFAQ
	assert.False(t, agent24ServableChunk(&faq))
	parent := ok
	parent.ChunkType = types.ChunkTypeParentText
	assert.False(t, agent24ServableChunk(&parent))
	off := ok
	off.IsEnabled = false
	assert.False(t, agent24ServableChunk(&off))
	gone := ok
	gone.DeletedAt = gorm.DeletedAt{Time: time.Now(), Valid: true}
	assert.False(t, agent24ServableChunk(&gone))
}

func TestAgent24ClassifyDoc(t *testing.T) {
	cases := []struct {
		status   string
		servable int
		want     agent24DocClass
		build    bool
	}{
		{types.ParseStatusPending, 0, agent24DocBuilding, false},
		{types.ParseStatusProcessing, 5, agent24DocBuilding, false},
		{types.ParseStatusFailed, 0, agent24DocFailed, false},
		{types.ParseStatusCancelled, 3, agent24DocFailed, false},
		{types.ParseStatusCompleted, 2, agent24DocServable, true},
		{types.ParseStatusFinalizing, 1, agent24DocServable, true},
		{types.ParseStatusCompleted, 0, agent24DocEmpty, true},
		{types.ParseStatusFinalizing, 0, agent24DocEmpty, true},
		{types.ParseStatusDeleting, 4, agent24DocExcluded, false},
		{types.ManualKnowledgeStatusDraft, 0, agent24DocExcluded, false},
		{"something-new", 1, agent24DocExcluded, false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, agent24ClassifyDoc(tc.status, tc.servable), "%s/%d", tc.status, tc.servable)
		assert.Equal(t, tc.build, agent24HasBuild(tc.status), "%s has build", tc.status)
	}
}
