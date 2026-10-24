package handler

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

func agent24ReadTestSnapshot() agent24ReadSnapshot {
	return agent24ReadSnapshot{
		Found: true, ParseStatus: types.ParseStatusCompleted, Title: "T", FileName: "f.pdf",
		Fingerprint: agent24SourceFingerprint{FileHash: "h", FilePath: "p", Source: "s"},
		Chunks: []agent24ReadChunk{
			{ID: "c1", ContentRevision: 2, Content: "one", Loaded: true},
			{ID: "c2", Content: "twotwo", Loaded: true},
			{ID: "c3", ContentRevision: 5, Content: "三四五", Loaded: true},
		},
	}
}

func TestAgent24JudgeReadChunk(t *testing.T) {
	key := agent24ReadTestKey
	snap := agent24ReadTestSnapshot()
	source := agent24SourceRevision(key, "doc", snap.Fingerprint)
	build := agent24IndexBuildID(key, "doc", []string{"c3", "c1", "c2"})
	tag1, tag3 := agent24ContentTag(key, "c1", "one"), agent24ContentTag(key, "c3", "三四五")
	req := func(chunk string, exp *agent24ReadExpected, cur *agent24ReadCursor, maxBytes int) agent24ReadRequest {
		return agent24ReadRequest{
			KnowledgeID: "doc", ChunkID: chunk, Expected: exp, Cursor: cur, PageLimit: 100, MaxBytes: maxBytes,
		}
	}

	page, kerr := agent24JudgeRead(key, req("c1", &agent24ReadExpected{ContentTag: tag1}, nil, 100), snap)
	require.Nil(t, kerr)
	assert.Equal(t, agent24ReadPage{
		Revision: agent24DocRevision{SourceRevision: source, IndexBuildID: build},
		Display:  &agent24HitDisplay{Title: "T", FileName: "f.pdf"},
		Segments: []agent24ReadSegment{{
			ChunkID: "c1", ChunkType: "text", ChunkRevision: 2, ContentTag: tag1, Text: "one",
		}},
	}, page)

	// The document check comes first, with or without chunk_id (R2.6, R6.4).
	_, kerr = agent24JudgeRead(key, req("c1", &agent24ReadExpected{ContentTag: "x"}, nil, 100),
		agent24ReadSnapshot{ParseStatus: types.ParseStatusCompleted, Chunks: snap.Chunks})
	assert.Equal(t, agent24NotFound(), kerr)

	// (1) Not servable: stale/anchor_missing with the current pair, before any content_tag check.
	_, kerr = agent24JudgeRead(key, req("c9", &agent24ReadExpected{ContentTag: "x"}, nil, 100), snap)
	assert.Equal(t, agent24Stale(source, &build, nil, "", true), kerr)
	noBuild := agent24ReadSnapshot{Found: true, ParseStatus: types.ParseStatusFailed, Fingerprint: snap.Fingerprint}
	_, kerr = agent24JudgeRead(key, req("c1", nil, nil, 100), noBuild)
	assert.Equal(t, agent24Stale(source, nil, nil, "", true), kerr)

	// (2) Another content_tag, from expected_revision or from the cursor: stale with the chunk's current pair.
	wantStale := agent24Stale(source, &build, &snap.Chunks[0], tag1, false)
	_, kerr = agent24JudgeRead(key, req("c1", &agent24ReadExpected{ContentTag: "old"}, nil, 100), snap)
	assert.Equal(t, wantStale, kerr)
	_, kerr = agent24JudgeRead(key, req("c1", nil, &agent24ReadCursor{ContentTag: "old", Offset: 1}, 100), snap)
	assert.Equal(t, wantStale, kerr)
	_, kerr = agent24JudgeRead(key, req("c1", &agent24ReadExpected{ContentTag: tag1},
		&agent24ReadCursor{ContentTag: "old", Offset: 1}, 100), snap)
	assert.Equal(t, wantStale, kerr)

	// (3) Cut by max_bytes on a rune boundary; the cursor continues the same chunk, bound to its content_tag.
	var texts []string
	var cur *agent24ReadCursor
	for range 4 {
		page, kerr = agent24JudgeRead(key, req("c3", nil, cur, 4), snap)
		require.Nil(t, kerr)
		require.Len(t, page.Segments, 1)
		assert.Equal(t, agent24ReadSegment{
			ChunkID: "c3", ChunkType: "text", ChunkRevision: 5, ContentTag: tag3, Text: page.Segments[0].Text,
		}, page.Segments[0])
		texts = append(texts, page.Segments[0].Text)
		assert.Equal(t, page.NextCursor != nil, page.Truncated)
		if page.NextCursor == nil {
			break
		}
		c, ok := agent24DecodeReadCursor(key, "doc", "c3", *page.NextCursor)
		require.True(t, ok)
		cur = &c
	}
	assert.Equal(t, []string{"三", "四", "五"}, texts)
	assert.Equal(t, &agent24ReadCursor{ContentTag: tag3, Offset: 6}, cur)

	// A page always advances by at least one rune, and the page that ends the text is not truncated even when
	// that rune is wider than max_bytes.
	texts, cur = nil, nil
	for range 4 {
		page, kerr = agent24JudgeRead(key, req("c3", nil, cur, 1), snap)
		require.Nil(t, kerr)
		texts = append(texts, page.Segments[0].Text)
		if page.NextCursor == nil {
			assert.False(t, page.Truncated)
			break
		}
		c, ok := agent24DecodeReadCursor(key, "doc", "c3", *page.NextCursor)
		require.True(t, ok)
		cur = &c
	}
	assert.Equal(t, []string{"三", "四", "五"}, texts)

	// An offset that does not fit the text is a server fault, not a guess.
	for _, off := range []int{1, 9, 10} {
		_, kerr = agent24JudgeRead(key, req("c3", nil, &agent24ReadCursor{ContentTag: tag3, Offset: off}, 4), snap)
		assert.Equal(t, agent24Internal(), kerr, off)
	}
	unloaded := agent24ReadTestSnapshot()
	unloaded.Chunks[0].Loaded = false
	_, kerr = agent24JudgeRead(key, req("c1", nil, nil, 100), unloaded)
	assert.Equal(t, agent24Internal(), kerr)
}

func TestAgent24JudgeReadDocument(t *testing.T) {
	key := agent24ReadTestKey
	snap := agent24ReadTestSnapshot()
	source := agent24SourceRevision(key, "doc", snap.Fingerprint)
	build := agent24IndexBuildID(key, "doc", []string{"c1", "c2", "c3"})
	tags := []string{
		agent24ContentTag(key, "c1", "one"), agent24ContentTag(key, "c2", "twotwo"),
		agent24ContentTag(key, "c3", "三四五"),
	}
	seg := func(i int, text string) agent24ReadSegment {
		return agent24ReadSegment{
			ChunkID: snap.Chunks[i].ID, ChunkType: "text", ChunkRevision: snap.Chunks[i].ContentRevision,
			ContentTag: tags[i], Text: text,
		}
	}
	req := func(exp *agent24ReadExpected, cur *agent24ReadCursor, limit, maxBytes int) agent24ReadRequest {
		return agent24ReadRequest{KnowledgeID: "doc", Expected: exp, Cursor: cur, PageLimit: limit, MaxBytes: maxBytes}
	}
	decode := func(p agent24ReadPage) *agent24ReadCursor {
		if p.NextCursor == nil {
			return nil
		}
		c, ok := agent24DecodeReadCursor(key, "doc", "", *p.NextCursor)
		require.True(t, ok)
		return &c
	}
	at := func(pos, off int, tag string) *agent24ReadCursor {
		return &agent24ReadCursor{
			SourceRevision: source, IndexBuildID: build, Position: pos, Offset: off, ContentTag: tag,
		}
	}

	// page.limit pages in document order; next_cursor without truncation (R4.5).
	page, kerr := agent24JudgeRead(key, req(&agent24ReadExpected{SourceRevision: source, IndexBuildID: build},
		nil, 2, 1000), snap)
	require.Nil(t, kerr)
	assert.Equal(t, agent24DocRevision{SourceRevision: source, IndexBuildID: build}, page.Revision)
	assert.Equal(t, &agent24HitDisplay{Title: "T", FileName: "f.pdf"}, page.Display)
	assert.Equal(t, []agent24ReadSegment{seg(0, "one"), seg(1, "twotwo")}, page.Segments)
	assert.False(t, page.Truncated)
	assert.Equal(t, at(2, 0, ""), decode(page))
	page, kerr = agent24JudgeRead(key, req(nil, decode(page), 2, 1000), snap)
	require.Nil(t, kerr)
	assert.Equal(t, []agent24ReadSegment{seg(2, "三四五")}, page.Segments)
	assert.Nil(t, page.NextCursor)

	// Whole segments while they fit together: one (3) and twotwo (6) each fit 8 bytes, not both.
	page, kerr = agent24JudgeRead(key, req(nil, nil, 100, 8), snap)
	require.Nil(t, kerr)
	assert.Equal(t, []agent24ReadSegment{seg(0, "one")}, page.Segments)
	assert.Equal(t, at(1, 0, ""), decode(page))
	page, kerr = agent24JudgeRead(key, req(nil, nil, 100, 9), snap)
	require.Nil(t, kerr)
	assert.Equal(t, []agent24ReadSegment{seg(0, "one"), seg(1, "twotwo")}, page.Segments)

	// Only a page's first segment is cut.
	page, kerr = agent24JudgeRead(key, req(nil, nil, 100, 5), snap)
	require.Nil(t, kerr)
	assert.Equal(t, []agent24ReadSegment{seg(0, "one")}, page.Segments)
	assert.False(t, page.Truncated)
	assert.Equal(t, at(1, 0, ""), decode(page))
	page, kerr = agent24JudgeRead(key, req(nil, at(1, 0, ""), 100, 4), snap)
	require.Nil(t, kerr)
	assert.Equal(t, []agent24ReadSegment{seg(1, "twot")}, page.Segments)
	assert.True(t, page.Truncated)
	assert.Equal(t, at(1, 4, tags[1]), decode(page))
	page, kerr = agent24JudgeRead(key, req(nil, at(1, 4, tags[1]), 100, 4), snap)
	require.Nil(t, kerr)
	assert.Equal(t, []agent24ReadSegment{seg(1, "wo")}, page.Segments) // 三四五 (9 bytes) does not fit the rest
	assert.False(t, page.Truncated)
	assert.Equal(t, at(2, 0, ""), decode(page))
	// A chunk edited since the cursor was issued restarts from its beginning; the edit is not stale (R4.5).
	page, kerr = agent24JudgeRead(key, req(nil, at(1, 4, "old"), 100, 4), snap)
	require.Nil(t, kerr)
	assert.Equal(t, []agent24ReadSegment{seg(1, "twot")}, page.Segments)
	page, kerr = agent24JudgeRead(key, req(nil, at(2, 0, ""), 100, 1), snap)
	require.Nil(t, kerr)
	assert.Equal(t, []agent24ReadSegment{seg(2, "三")}, page.Segments) // at least one rune
	assert.Equal(t, at(2, 3, tags[2]), decode(page))
	page, kerr = agent24JudgeRead(key, req(nil, at(2, 6, tags[2]), 100, 1), snap)
	require.Nil(t, kerr)
	assert.Equal(t, []agent24ReadSegment{seg(2, "五")}, page.Segments) // the last rune ends the document
	assert.False(t, page.Truncated)
	assert.Nil(t, page.NextCursor)

	// expected_revision, then the cursor's binding: either part differing is stale (R4.4, R4.5).
	wantStale := agent24Stale(source, &build, nil, "", false)
	for name, r := range map[string]agent24ReadRequest{
		"expected source": req(&agent24ReadExpected{SourceRevision: "x", IndexBuildID: build}, nil, 100, 100),
		"expected build":  req(&agent24ReadExpected{SourceRevision: source, IndexBuildID: "x"}, nil, 100, 100),
		"cursor source":   req(nil, &agent24ReadCursor{SourceRevision: "x", IndexBuildID: build}, 100, 100),
		"cursor build":    req(nil, &agent24ReadCursor{SourceRevision: source, IndexBuildID: "x"}, 100, 100),
		"both, cursor stale": req(&agent24ReadExpected{SourceRevision: source, IndexBuildID: build},
			&agent24ReadCursor{SourceRevision: source, IndexBuildID: "x"}, 100, 100),
	} {
		_, kerr = agent24JudgeRead(key, r, snap)
		assert.Equal(t, wantStale, kerr, name)
	}

	// Without a build: stale when a revision was expected, else index_pending or index_failed (R4.4).
	for status, want := range map[string]*agent24KnowledgeError{
		types.ParseStatusPending:    agent24Unavailable("index_pending"),
		types.ParseStatusProcessing: agent24Unavailable("index_pending"),
		types.ParseStatusFailed:     agent24Unavailable("index_failed"),
		types.ParseStatusCancelled:  agent24Unavailable("index_failed"),
	} {
		s := agent24ReadSnapshot{Found: true, ParseStatus: status, Fingerprint: snap.Fingerprint}
		_, kerr = agent24JudgeRead(key, req(nil, nil, 100, 100), s)
		assert.Equal(t, want, kerr, status)
		_, kerr = agent24JudgeRead(key, req(&agent24ReadExpected{SourceRevision: source, IndexBuildID: build},
			nil, 100, 100), s)
		assert.Equal(t, agent24Stale(source, nil, nil, "", false), kerr, status)
	}

	// A build with an empty servable set: 200 with no segments.
	empty := agent24ReadSnapshot{Found: true, ParseStatus: types.ParseStatusFinalizing, Chunks: []agent24ReadChunk{}}
	page, kerr = agent24JudgeRead(key, req(nil, nil, 100, 100), empty)
	require.Nil(t, kerr)
	assert.Equal(t, agent24ReadPage{
		Revision: agent24DocRevision{
			SourceRevision: agent24SourceRevision(key, "doc", agent24SourceFingerprint{}),
			IndexBuildID:   agent24IndexBuildID(key, "doc", nil),
		},
		Segments: []agent24ReadSegment{},
	}, page)

	_, kerr = agent24JudgeRead(key, req(nil, nil, 100, 100), agent24ReadSnapshot{ParseStatus: "completed"})
	assert.Equal(t, agent24NotFound(), kerr)
	_, kerr = agent24JudgeRead(key, req(nil, at(4, 0, ""), 100, 100), snap)
	assert.Equal(t, agent24Internal(), kerr)
	unloaded := agent24ReadTestSnapshot()
	unloaded.Chunks[1].Loaded = false
	_, kerr = agent24JudgeRead(key, req(nil, nil, 100, 100), unloaded)
	assert.Equal(t, agent24Internal(), kerr)
	// Only the window is touched: chunks beyond page.limit need not be loaded.
	page, kerr = agent24JudgeRead(key, req(nil, nil, 1, 100), unloaded)
	require.Nil(t, kerr)
	assert.Equal(t, []agent24ReadSegment{seg(0, "one")}, page.Segments)
}

// Invalid UTF-8 becomes U+FFFD in text and in the byte count; content_tag stays over the stored bytes.
func TestAgent24JudgeReadInvalidUTF8(t *testing.T) {
	key := agent24ReadTestKey
	snap := agent24ReadSnapshot{Found: true, ParseStatus: types.ParseStatusCompleted, Chunks: []agent24ReadChunk{
		{ID: "c1", Content: "a\xffb", Loaded: true},
	}}
	page, kerr := agent24JudgeRead(key, agent24ReadRequest{KnowledgeID: "d", PageLimit: 1, MaxBytes: 4}, snap)
	require.Nil(t, kerr)
	assert.Equal(t, "a�", page.Segments[0].Text)
	assert.True(t, page.Truncated)
	assert.Equal(t, agent24ContentTag(key, "c1", "a\xffb"), page.Segments[0].ContentTag)
	page, kerr = agent24JudgeRead(key, agent24ReadRequest{KnowledgeID: "d", ChunkID: "c1", MaxBytes: 4}, snap)
	require.Nil(t, kerr)
	assert.Equal(t, []any{"a\uFFFD", true}, []any{page.Segments[0].Text, page.Truncated})
}

// Wire form of the stale details (error.schema.json $defs/stale).
func TestAgent24StaleDetails(t *testing.T) {
	b := "ib"
	raw, err := json.Marshal(agent24Stale("src", nil, nil, "", true).Extra)
	require.NoError(t, err)
	assert.JSONEq(t, `{"current_revision":{"source_revision":"src","index_build_id":null}}`, string(raw))
	e := agent24Stale("src", &b, &agent24ReadChunk{ContentRevision: 4}, "ct", false)
	assert.Empty(t, e.Reason)
	raw, err = json.Marshal(e.Extra)
	require.NoError(t, err)
	assert.JSONEq(t, `{"current_revision":{"source_revision":"src","index_build_id":"ib"},`+
		`"current_chunk_revision":4,"current_content_tag":"ct"}`, string(raw))
	assert.Equal(t, "anchor_missing", agent24Stale("src", &b, nil, "", true).Reason)
}

// Paging to the end under any max_bytes ends in a finite number of pages and returns the text exactly once
// (R4.5, R4.7), also when a rune is wider than max_bytes.
func TestAgent24JudgeReadPagesToEnd(t *testing.T) {
	key := agent24ReadTestKey
	contents := []string{"三四五", "a😀b😀", "😀", "é", "x"}
	snap := agent24ReadSnapshot{Found: true, ParseStatus: types.ParseStatusCompleted}
	for i, c := range contents {
		snap.Chunks = append(snap.Chunks, agent24ReadChunk{ID: fmt.Sprintf("c%d", i), Content: c, Loaded: true})
	}
	for maxBytes := 1; maxBytes <= 5; maxBytes++ {
		for i, want := range contents { // single chunk
			var got string
			var cur *agent24ReadCursor
			for pages := 0; ; pages++ {
				require.Less(t, pages, 32, "chunk %d max_bytes %d", i, maxBytes)
				page, kerr := agent24JudgeRead(key, agent24ReadRequest{
					KnowledgeID: "d", ChunkID: snap.Chunks[i].ID, Cursor: cur, MaxBytes: maxBytes,
				}, snap)
				require.Nil(t, kerr)
				got += page.Segments[0].Text
				assert.Equal(t, page.NextCursor != nil, page.Truncated)
				if page.NextCursor == nil {
					break
				}
				c, ok := agent24DecodeReadCursor(key, "d", snap.Chunks[i].ID, *page.NextCursor)
				require.True(t, ok)
				cur = &c
			}
			assert.Equal(t, want, got, "chunk %d max_bytes %d", i, maxBytes)
		}
		got := map[string]string{} // document level
		var cur *agent24ReadCursor
		for pages := 0; ; pages++ {
			require.Less(t, pages, 64, "document max_bytes %d", maxBytes)
			page, kerr := agent24JudgeRead(key, agent24ReadRequest{
				KnowledgeID: "d", Cursor: cur, PageLimit: 2, MaxBytes: maxBytes,
			}, snap)
			require.Nil(t, kerr)
			for _, s := range page.Segments {
				got[s.ChunkID] += s.Text
			}
			if page.NextCursor == nil {
				assert.False(t, page.Truncated)
				break
			}
			c, ok := agent24DecodeReadCursor(key, "d", "", *page.NextCursor)
			require.True(t, ok)
			cur = &c
		}
		for i, want := range contents {
			assert.Equal(t, want, got[snap.Chunks[i].ID], "document chunk %d max_bytes %d", i, maxBytes)
		}
	}
}
