package handler

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

func agent24ReadTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "read.db") + "?_journal_mode=WAL&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Knowledge{}, &types.Chunk{}))
	text := string(types.ChunkTypeText)
	doc := func(id string, tenant uint64, kb, status string) *types.Knowledge {
		return &types.Knowledge{ID: id, TenantID: tenant, KnowledgeBaseID: kb, ParseStatus: status, Type: "file"}
	}
	chunk := func(id, docID string, index int, content string) *types.Chunk {
		return &types.Chunk{
			ID: id, TenantID: 1, KnowledgeBaseID: "a", KnowledgeID: docID, ChunkType: text, IsEnabled: true,
			ChunkIndex: index, Content: content,
		}
	}
	d1 := doc("d1", 1, "a", types.ParseStatusCompleted)
	d1.Title, d1.FileName, d1.FileHash, d1.FilePath, d1.Source = "T1", "f1.pdf", "h1", "p1", "s1"
	m1 := doc("m1", 1, "b", types.ParseStatusFinalizing)
	m1.Type, m1.Metadata = types.KnowledgeTypeManual, types.JSON(`{"content":"x","status":"publish","version":3}`)
	m2 := doc("m2", 1, "b", types.ParseStatusCompleted)
	m2.Type = types.KnowledgeTypeManual
	c1 := chunk("c1", "d1", 2, "two")
	c1.ContentRevision = 3
	c4 := chunk("c4", "d1", 1, "summary")
	c4.ChunkType = types.ChunkTypeSummary
	c7 := chunk("c7", "d1", 3, "syncing")
	c7.IndexStatus = "processing" // servable: index_status only filters search hits (R5.3)
	c8 := chunk("c8", "d1", 4, "other KB row")
	c8.KnowledgeBaseID = "z" // still the document's chunk, as in agent24DBHitReader's servable set
	c9 := chunk("c9", "d1", 5, "other tenant")
	c9.TenantID = 2
	rows := []any{
		d1, c1,
		chunk("c2", "d1", 0, "zero"),
		chunk("c3", "d1", 1, "disabled"),
		c4,
		chunk("c5", "d1", 1, "soft-deleted"),
		chunk("c6", "d1", 1, "one"), // same chunk_index as c3 and c5: id breaks the tie
		chunk("c10", "d1", 1, "one-b"),
		c7, c8, c9,
		doc("d2", 1, "a", types.ParseStatusProcessing), chunk("c20", "d2", 0, "no build"),
		doc("d3", 1, "a", types.ParseStatusDeleting), chunk("c30", "d3", 0, "deleting"),
		doc("d4", 1, "a", types.ManualKnowledgeStatusDraft), chunk("c40", "d4", 0, "draft"),
		doc("d5", 1, "a", "weird"), chunk("c50", "d5", 0, "unknown status"),
		doc("d6", 1, "z", types.ParseStatusCompleted), chunk("c60", "d6", 0, "out of scope"),
		doc("d7", 2, "a", types.ParseStatusCompleted),
		doc("d8", 1, "a", types.ParseStatusCompleted), chunk("c80", "d8", 0, "document soft-deleted"),
		doc("d9", 1, "a", types.ParseStatusCompleted),
		m1, chunk("c90", "m1", 0, "manual"),
		m2,
	}
	for _, row := range rows {
		require.NoError(t, db.Create(row).Error)
	}
	require.NoError(t, db.Model(&types.Chunk{}).Where("id = ?", "c3").Update("is_enabled", false).Error)
	require.NoError(t, db.Delete(&types.Chunk{}, "id = ?", "c5").Error)
	require.NoError(t, db.Delete(&types.Knowledge{}, "id = ?", "d8").Error)
	return db
}

// d1's servable set in document order: chunk_index, then id (c10 < c6 bytewise).
func agent24ReadTestD1(loaded ...string) agent24ReadSnapshot {
	chunks := []agent24ReadChunk{
		{ID: "c2", Content: "zero"},
		{ID: "c10", Content: "one-b"},
		{ID: "c6", Content: "one"},
		{ID: "c1", ContentRevision: 3, Content: "two"},
		{ID: "c7", Content: "syncing"},
		{ID: "c8", Content: "other KB row"},
	}
	for i := range chunks {
		if chunks[i].Loaded = slices.Contains(loaded, chunks[i].ID); !chunks[i].Loaded {
			chunks[i].Content = ""
		}
	}
	return agent24ReadSnapshot{
		Found: true, KnowledgeBaseID: "a", ParseStatus: types.ParseStatusCompleted, Title: "T1", FileName: "f1.pdf",
		Fingerprint: agent24SourceFingerprint{FileHash: "h1", FilePath: "p1", Source: "s1"}, Chunks: chunks,
	}
}

func TestAgent24DBReadSnapshot(t *testing.T) {
	db := agent24ReadTestDB(t)
	r := agent24DBReadReader{db}
	ctx := context.Background()
	kbs := []string{"a", "b"}
	snap := func(id string, w agent24ReadWindow) agent24ReadSnapshot {
		s, err := r.Snapshot(ctx, 1, kbs, id, w)
		require.NoError(t, err, id)
		return s
	}

	assert.Equal(t, agent24ReadTestD1("c10", "c6"), snap("d1", agent24ReadWindow{Position: 1, Limit: 2}))
	all := agent24ReadTestD1("c2", "c10", "c6", "c1", "c7", "c8")
	assert.Equal(t, all, snap("d1", agent24ReadWindow{Limit: 100}))
	assert.Equal(t, agent24ReadTestD1(), snap("d1", agent24ReadWindow{Position: 6, Limit: 100}))
	assert.Equal(t, agent24ReadTestD1("c1"), snap("d1", agent24ReadWindow{ChunkID: "c1"}))
	// A chunk outside the servable set loads nothing; the set is still returned.
	for _, id := range []string{"c3", "c4", "c5", "c9", "c20", "nope"} {
		assert.Equal(t, agent24ReadTestD1(), snap("d1", agent24ReadWindow{ChunkID: id}), id)
	}

	// No build: found, no servable set; a build with no servable chunk: an empty, non-nil set.
	assert.Equal(t, agent24ReadSnapshot{
		Found: true, KnowledgeBaseID: "a", ParseStatus: types.ParseStatusProcessing,
	}, snap("d2", agent24ReadWindow{ChunkID: "c20"}))
	d9 := snap("d9", agent24ReadWindow{Limit: 100})
	assert.Equal(t, agent24ReadSnapshot{
		Found: true, KnowledgeBaseID: "a", ParseStatus: types.ParseStatusCompleted, Chunks: []agent24ReadChunk{},
	}, d9)
	assert.NotNil(t, d9.Chunks)

	// Deleting, draft, unknown status, outside E, another tenant, soft-deleted, absent: not found (R2.6).
	for _, id := range []string{"d3", "d4", "d5", "d6", "d7", "d8", "nope"} {
		assert.Equal(t, agent24ReadSnapshot{}, snap(id, agent24ReadWindow{Limit: 100}), id)
	}
	s, err := r.Snapshot(ctx, 1, []string{"b"}, "d1", agent24ReadWindow{Limit: 100})
	require.NoError(t, err)
	assert.False(t, s.Found)
	s, err = r.Snapshot(ctx, 2, kbs, "d7", agent24ReadWindow{Limit: 100})
	require.NoError(t, err)
	assert.Equal(t, agent24ReadSnapshot{
		Found: true, KnowledgeBaseID: "a", ParseStatus: types.ParseStatusCompleted, Chunks: []agent24ReadChunk{},
	}, s)

	// Manual knowledge: the version from metadata, 1 without metadata; unreadable metadata is an error.
	assert.Equal(t, agent24SourceFingerprint{ManualVersion: 3}, snap("m1", agent24ReadWindow{Limit: 1}).Fingerprint)
	assert.Equal(t, agent24SourceFingerprint{ManualVersion: 1}, snap("m2", agent24ReadWindow{Limit: 1}).Fingerprint)
	require.NoError(t, db.Model(&types.Knowledge{}).Where("id = ?", "m1").Update("metadata", "{").Error)
	_, err = r.Snapshot(ctx, 1, kbs, "m1", agent24ReadWindow{Limit: 1})
	assert.Error(t, err)
	// Out of E or excluded, an unreadable metadata value is not read: such documents stay indistinguishable
	// from absent ones (R2.6).
	require.NoError(t, db.Model(&types.Knowledge{}).Where("id IN ?", []string{"d3", "d6"}).
		Updates(map[string]any{"metadata": "{", "type": types.KnowledgeTypeManual}).Error)
	_, err = r.Snapshot(ctx, 1, []string{"b"}, "m1", agent24ReadWindow{Limit: 1})
	assert.Error(t, err) // in E: still an error
	for _, id := range []string{"d3", "d6", "m1"} {
		s, err := r.Snapshot(ctx, 1, []string{"a"}, id, agent24ReadWindow{Limit: 1})
		require.NoError(t, err, id)
		assert.False(t, s.Found, id)
	}
}

// Search and read derive index_build_id from the same servable set (R5.1).
func TestAgent24DBReadSnapshotAgreesWithSearch(t *testing.T) {
	db := agent24ReadTestDB(t)
	hits, err := agent24DBHitReader{db}.Snapshot(context.Background(), 1, []string{"a"}, []string{"c1"})
	require.NoError(t, err)
	read, err := agent24DBReadReader{db}.Snapshot(context.Background(), 1, []string{"a"}, "d1",
		agent24ReadWindow{ChunkID: "c1"})
	require.NoError(t, err)
	var ids []string
	for _, c := range read.Chunks {
		ids = append(ids, c.ID)
	}
	assert.ElementsMatch(t, hits.Docs["d1"].ServableChunkIDs, ids)
	assert.Equal(t, hits.Docs["d1"].Fingerprint, read.Fingerprint)
}

// Writers between the queries do not leak into the result: identifiers and text share one snapshot (§13).
func TestAgent24DBReadSnapshotIsOneSnapshot(t *testing.T) {
	db := agent24ReadTestDB(t)
	calls := 0
	agent24ReadSnapshotTestHook = func() {
		calls++
		edit := func(id, content string) {
			require.NoError(t, db.Model(&types.Chunk{}).Where("id = ?", id).Update("content", content).Error)
		}
		if calls == 1 { // after the document query: the document, the set and the text change
			require.NoError(t, db.Model(&types.Knowledge{}).Where("id = ?", "d1").Update("file_hash", "new").Error)
			require.NoError(t, db.Delete(&types.Chunk{}, "id = ?", "c2").Error)
			require.NoError(t, db.Create(&types.Chunk{
				ID: "c99", TenantID: 1, KnowledgeBaseID: "a", KnowledgeID: "d1", ChunkType: "text", IsEnabled: true,
			}).Error)
			edit("c1", "edited")
			return
		}
		edit("c10", "edited") // after the servable-set query: only the text changes
		edit("c1", "edited again")
	}
	t.Cleanup(func() { agent24ReadSnapshotTestHook = nil })
	s, err := agent24DBReadReader{db}.Snapshot(context.Background(), 1, []string{"a"}, "d1",
		agent24ReadWindow{Limit: 100})
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Equal(t, agent24ReadTestD1("c2", "c10", "c6", "c1", "c7", "c8"), s)
}

// Snapshot begins its one transaction with the dialect's options, as agent24DBHitReader does.
func TestAgent24DBReadSnapshotTxOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		want []*sql.TxOptions
	}{
		{"postgres", []*sql.TxOptions{{Isolation: sql.LevelRepeatableRead, ReadOnly: true}}},
		{"sqlite", []*sql.TxOptions{nil}},
	} {
		db := agent24ReadTestDB(t)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		rec := &agent24TxRecorder{DB: sqlDB}
		db.ConnPool, db.Statement.ConnPool = rec, rec
		db.Dialector = agent24NamedDialector{db.Dialector, tc.name}
		s, err := agent24DBReadReader{db}.Snapshot(context.Background(), 1, []string{"a"}, "d1",
			agent24ReadWindow{ChunkID: "c1"})
		require.NoError(t, err)
		assert.Equal(t, agent24ReadTestD1("c1"), s, tc.name)
		assert.Equal(t, tc.want, rec.opts, tc.name)
	}
}
