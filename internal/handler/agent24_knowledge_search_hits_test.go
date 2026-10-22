package handler

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAgent24AssembleHits(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	long := strings.Repeat("a", agent24KnowledgeMaxSnippetBytes-1) + "é" + "tail"
	fp1 := agent24SourceFingerprint{FileHash: "h1", FilePath: "p1", Source: "s1"}
	snap := agent24HitSnapshot{
		Chunks: map[string]agent24HitChunk{
			"c1": {ID: "c1", KnowledgeID: "d1", KnowledgeBaseID: "a", Content: "one", ContentRevision: 2},
			"c2": {ID: "c2", KnowledgeID: "d1", KnowledgeBaseID: "a", Content: "two"},
			"c3": {ID: "c3", KnowledgeID: "d2", KnowledgeBaseID: "a", Content: long},
			"c4": {ID: "c4", KnowledgeID: "gone", KnowledgeBaseID: "a", Content: "orphan"},
			"c5": {ID: "c5", KnowledgeID: "d3", KnowledgeBaseID: "a", Content: "moved"},
		},
		Docs: map[string]agent24HitDoc{
			"d1": {
				ID: "d1", KnowledgeBaseID: "a", Title: "T", FileName: "f.pdf", Fingerprint: fp1,
				ServableChunkIDs: []string{"c9", "c2", "c1"},
			},
			"d2": {ID: "d2", KnowledgeBaseID: "a", ServableChunkIDs: []string{"c3"}},
			"d3": {ID: "d3", KnowledgeBaseID: "b", ServableChunkIDs: []string{"c5"}},
		},
	}
	cands := []agent24SearchCandidate{
		{ChunkID: "c3", Score: 0.9},
		{ChunkID: "nope", Score: 0.8},
		{ChunkID: "c1", Score: 0.7},
		{ChunkID: "c3", Score: 0.6},
		{ChunkID: "c4", Score: 0.5},
		{ChunkID: "c5", Score: 0.4},
		{ChunkID: "c2", Score: 0.3},
	}
	hits := agent24AssembleHits(key, "emb:e1", cands, snap, 50)
	ids := func(hs []agent24SearchHit) (out []string) {
		for _, h := range hs {
			out = append(out, h.ChunkID)
		}
		return out
	}
	// Candidate order; unknown, orphaned, KB-mismatched and repeated chunks are dropped (R3.6, R3.9).
	require.Equal(t, []string{"c3", "c1", "c2"}, ids(hits))
	assert.Equal(t, agent24SearchHit{
		Rank: 2, KnowledgeBaseID: "a", KnowledgeID: "d1", ChunkID: "c1", ChunkType: "text",
		Revision: agent24HitRevision{
			SourceRevision: agent24SourceRevision(key, "d1", fp1),
			IndexBuildID:   agent24IndexBuildID(key, "d1", []string{"c1", "c2", "c9"}),
			ChunkRevision:  2, ContentTag: agent24ContentTag(key, "c1", "one"),
		},
		Locator: agent24HitLocator{Valid: false}, IndexState: "fresh", Score: 0.7, ScoreSpace: "emb:e1",
		Snippet: agent24HitSnippet{Text: "one"}, Display: &agent24HitDisplay{Title: "T", FileName: "f.pdf"},
	}, hits[1])
	// The snippet comes from the same text as content_tag, cut on a rune boundary (R3.8).
	assert.Equal(t, agent24ContentTag(key, "c3", long), hits[0].Revision.ContentTag)
	assert.Equal(t, agent24HitSnippet{Text: long[:agent24KnowledgeMaxSnippetBytes-1], Truncated: true},
		hits[0].Snippet)
	assert.Nil(t, hits[0].Display)
	assert.Equal(t, []int{1, 2, 3}, []int{hits[0].Rank, hits[1].Rank, hits[2].Rank})
	assert.Equal(t, hits[1].Revision.IndexBuildID, hits[2].Revision.IndexBuildID) // same document, same build

	// max_results counts kept hits, not candidates.
	assert.Equal(t, []string{"c3", "c1"}, ids(agent24AssembleHits(key, "emb:e1", cands, snap, 2)))
	assert.Nil(t, agent24AssembleHits(key, "emb:e1", nil, snap, 50))

	// Wire form: locator is exactly {"valid":false}, display is omitted when absent.
	raw, err := json.Marshal(hits[0])
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"locator":{"valid":false}`)
	assert.NotContains(t, string(raw), `"display"`)
}

func TestAgent24Snippet(t *testing.T) {
	cases := []struct {
		in, want  string
		truncated bool
	}{
		{"", "", false},
		{"abc", "abc", false},
		{"abcd", "abcd", false},
		{"abcde", "abcd", true},
		{"abé", "abé", false},  // 4 bytes
		{"abcé", "abc", true},  // é would end at byte 5
		{"a\xffb", "a�", true}, // invalid byte becomes 3 bytes first
		{"\xff", "�", false},
	}
	for _, c := range cases {
		got, truncated := agent24Snippet(c.in, 4)
		assert.Equal(t, []any{c.want, c.truncated}, []any{got, truncated}, "%q", c.in)
		assert.True(t, utf8.ValidString(got))
	}
}

func TestAgent24Display(t *testing.T) {
	assert.Nil(t, agent24Display("", ""))
	assert.Equal(t, &agent24HitDisplay{FileName: "f"}, agent24Display("", "f"))
	long := strings.Repeat("字", agent24DisplayMaxRunes+1)
	d := agent24Display(long, "x")
	assert.Equal(t, agent24DisplayMaxRunes, utf8.RuneCountInString(d.Title))
	assert.Equal(t, long[:len(long)-len("字")], d.Title)
	assert.Equal(t, "ab", agent24TruncateRunes("ab", 2))
	assert.Equal(t, "", agent24TruncateRunes("ab", 0))
}

func agent24HitTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "hits.db") + "?_journal_mode=WAL&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Knowledge{}, &types.Chunk{}))
	text := string(types.ChunkTypeText)
	doc := func(id string, tenant uint64, kb, status string) *types.Knowledge {
		return &types.Knowledge{ID: id, TenantID: tenant, KnowledgeBaseID: kb, ParseStatus: status, Type: "file"}
	}
	chunk := func(id string, tenant uint64, kb, doc, typ, content string) *types.Chunk {
		return &types.Chunk{
			ID: id, TenantID: tenant, KnowledgeBaseID: kb, KnowledgeID: doc, ChunkType: typ,
			IsEnabled: true, Content: content,
		}
	}
	k1 := doc("k1", 1, "a", types.ParseStatusCompleted)
	k1.Title, k1.FileName, k1.FileHash, k1.FilePath, k1.Source = "T1", "f1.pdf", "h1", "p1", "s1"
	k5 := doc("k5", 1, "b", types.ParseStatusFinalizing)
	k5.Type = types.KnowledgeTypeManual
	k5.Metadata = types.JSON(`{"content":"x","status":"publish","version":3}`)
	k7 := doc("k7", 1, "b", types.ParseStatusCompleted)
	k7.Type = types.KnowledgeTypeManual
	rows := []any{
		k1,
		chunk("c1", 1, "a", "k1", text, "one"),
		chunk("c2", 1, "a", "k1", text, "two"),
		chunk("c3", 1, "a", "k1", text, "disabled"),
		chunk("c4", 1, "a", "k1", types.ChunkTypeSummary, "summary"),
		chunk("c5", 1, "a", "k1", text, "soft-deleted"),
		&types.Chunk{
			ID: "c13", TenantID: 1, KnowledgeBaseID: "a", KnowledgeID: "k1", ChunkType: text, IsEnabled: true,
			IndexStatus: "processing",
		},
		doc("k2", 1, "a", types.ParseStatusProcessing),
		chunk("c6", 1, "a", "k2", text, "no build"),
		doc("k3", 1, "z", types.ParseStatusCompleted),
		chunk("c7", 1, "z", "k3", text, "out of scope"),
		doc("k4", 2, "a", types.ParseStatusCompleted),
		chunk("c8", 2, "a", "k4", text, "other tenant"),
		k5,
		chunk("c9", 1, "b", "k5", text, "manual"),
		doc("k6", 1, "a", types.ParseStatusCompleted),
		chunk("c10", 1, "a", "k6", text, "document soft-deleted"),
		k7,
		chunk("c11", 1, "b", "k7", text, "manual, no metadata"),
	}
	for _, row := range rows {
		require.NoError(t, db.Create(row).Error)
	}
	require.NoError(t, db.Model(&types.Chunk{}).Where("id = ?", "c3").Update("is_enabled", false).Error)
	require.NoError(t, db.Delete(&types.Chunk{}, "id = ?", "c5").Error)
	require.NoError(t, db.Delete(&types.Knowledge{}, "id = ?", "k6").Error)
	return db
}

func agent24SortedSnapshot(s agent24HitSnapshot) agent24HitSnapshot {
	for id, d := range s.Docs {
		d.ServableChunkIDs = slices.Sorted(slices.Values(d.ServableChunkIDs))
		s.Docs[id] = d
	}
	return s
}

var agent24HitTestChunkIDs = []string{
	"c1", "c3", "c4", "c5", "c6", "c7", "c8", "c9", "c10", "c11", "c13", "nope",
}

var agent24HitTestWant = agent24HitSnapshot{
	Chunks: map[string]agent24HitChunk{
		"c1":  {ID: "c1", KnowledgeID: "k1", KnowledgeBaseID: "a", Content: "one"},
		"c9":  {ID: "c9", KnowledgeID: "k5", KnowledgeBaseID: "b", Content: "manual"},
		"c11": {ID: "c11", KnowledgeID: "k7", KnowledgeBaseID: "b", Content: "manual, no metadata"},
	},
	Docs: map[string]agent24HitDoc{
		"k1": {
			ID: "k1", KnowledgeBaseID: "a", Title: "T1", FileName: "f1.pdf",
			Fingerprint:      agent24SourceFingerprint{FileHash: "h1", FilePath: "p1", Source: "s1"},
			ServableChunkIDs: []string{"c1", "c13", "c2"}, // c13 is servable though its index sync is pending
		},
		"k5": {
			ID: "k5", KnowledgeBaseID: "b", Fingerprint: agent24SourceFingerprint{ManualVersion: 3},
			ServableChunkIDs: []string{"c9"},
		},
		"k7": {
			ID: "k7", KnowledgeBaseID: "b", Fingerprint: agent24SourceFingerprint{ManualVersion: 1},
			ServableChunkIDs: []string{"c11"},
		},
	},
}

// Only servable chunks of documents with a build, in the tenant and the searched KBs, and not pending or failed
// index sync (R3.9, R5.1, R5.3).
func TestAgent24DBHitSnapshot(t *testing.T) {
	db := agent24HitTestDB(t)
	snap, err := agent24DBHitReader{db}.Snapshot(context.Background(), 1, []string{"a", "b"}, agent24HitTestChunkIDs)
	require.NoError(t, err)
	assert.Equal(t, agent24HitTestWant, agent24SortedSnapshot(snap))

	snap, err = agent24DBHitReader{db}.Snapshot(context.Background(), 1, []string{"a", "b"}, nil)
	require.NoError(t, err)
	assert.Empty(t, snap.Chunks)
	assert.Empty(t, snap.Docs)

	// At most agent24ScopeBatch chunk ids per call.
	_, err = agent24DBHitReader{db}.Snapshot(context.Background(), 1, []string{"a"}, make([]string, agent24ScopeBatch))
	assert.NoError(t, err)
	_, err = agent24DBHitReader{db}.Snapshot(context.Background(), 1, []string{"a"},
		make([]string, agent24ScopeBatch+1))
	assert.Error(t, err)

	// Unreadable manual metadata is an error, not a guessed version.
	require.NoError(t, db.Model(&types.Knowledge{}).Where("id = ?", "k5").Update("metadata", "{").Error)
	_, err = agent24DBHitReader{db}.Snapshot(context.Background(), 1, []string{"b"}, []string{"c9"})
	assert.Error(t, err)
}

// A concurrent writer between the queries does not leak into the result: all reads share one snapshot (R3.9).
func TestAgent24DBHitSnapshotIsOneSnapshot(t *testing.T) {
	db := agent24HitTestDB(t)
	agent24HitSnapshotTestHook = func() {
		require.NoError(t, db.Model(&types.Knowledge{}).Where("id = ?", "k1").
			Update("parse_status", types.ParseStatusDeleting).Error)
		require.NoError(t, db.Model(&types.Knowledge{}).Where("id = ?", "k5").Update("file_hash", "new").Error)
		require.NoError(t, db.Create(&types.Chunk{
			ID: "c12", TenantID: 1, KnowledgeBaseID: "b", KnowledgeID: "k7", ChunkType: "text", IsEnabled: true,
		}).Error)
	}
	t.Cleanup(func() { agent24HitSnapshotTestHook = nil })
	snap, err := agent24DBHitReader{db}.Snapshot(context.Background(), 1, []string{"a", "b"}, agent24HitTestChunkIDs)
	require.NoError(t, err)
	assert.Equal(t, agent24HitTestWant, agent24SortedSnapshot(snap))
}

func TestAgent24SnapshotTxOptions(t *testing.T) {
	db := agent24HitTestDB(t)
	assert.Nil(t, agent24SnapshotTxOptions(db))
}
