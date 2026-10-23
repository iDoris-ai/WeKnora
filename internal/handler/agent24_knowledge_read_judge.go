// Agent24 fork product patch A24-KB-14 (CONTRACT-READONLY-v1 r10 §4 R4.2–R4.5, R2.6, §6 R6.4).
//
// Read judgement: one snapshot (agent24_knowledge_read.go) is judged in the R6.4 order (document, anchor and
// revision) and assembled into segments. The endpoint wiring lands in a later slice (WeKnora#38).

package handler

import (
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

type agent24DocRevision struct {
	SourceRevision string `json:"source_revision"`
	IndexBuildID   string `json:"index_build_id"`
}

// agent24ReadSegment is one R4.2 segment.
type agent24ReadSegment struct {
	ChunkID       string `json:"chunk_id"`
	ChunkType     string `json:"chunk_type"`
	ChunkRevision int    `json:"chunk_revision"`
	ContentTag    string `json:"content_tag"`
	Text          string `json:"text"`
}

// agent24ReadPage is a read's success result, without the echoed fields and effective_scope.
type agent24ReadPage struct {
	Revision   agent24DocRevision
	Display    *agent24HitDisplay
	Segments   []agent24ReadSegment // never nil
	Truncated  bool
	NextCursor *string
}

// agent24Stale is the stale error (R4.4): current_revision always, index_build_id null without a build;
// current_chunk_revision and current_content_tag only when chunk is set; reason anchor_missing when anchor.
func agent24Stale(source string, build *string, chunk *agent24ReadChunk, tag string, anchor bool,
) *agent24KnowledgeError {
	e := &agent24KnowledgeError{Code: agent24CodeStale, Extra: map[string]any{
		"current_revision": map[string]any{"source_revision": source, "index_build_id": build},
	}}
	if anchor {
		e.Reason = "anchor_missing"
	}
	if chunk != nil {
		e.Extra["current_chunk_revision"] = chunk.ContentRevision
		e.Extra["current_content_tag"] = tag
	}
	return e
}

// agent24ReadCut returns s[off:] cut to at most maxBytes on a rune boundary, and whether it was cut. It keeps
// at least one rune, so every page advances even when maxBytes is smaller than the first rune.
func agent24ReadCut(s string, off, maxBytes int) (string, bool) {
	rest := s[off:]
	if len(rest) <= maxBytes {
		return rest, false
	}
	i := maxBytes
	for i > 0 && !utf8.RuneStart(rest[i]) {
		i--
	}
	if i == 0 {
		_, i = utf8.DecodeRuneInString(rest)
	}
	return rest[:i], i < len(rest)
}

// agent24JudgeRead applies the R6.4 order after E to one snapshot: document (not_found, R2.6), then anchor
// and revision (stale, R4.4, R4.5), then the page. Text is valid UTF-8 (invalid bytes become U+FFFD) and is
// cut and counted in bytes of that form; content_tag is over the stored text, as in search.
func agent24JudgeRead(key []byte, req agent24ReadRequest, snap agent24ReadSnapshot) (
	agent24ReadPage, *agent24KnowledgeError,
) {
	if !snap.Found {
		return agent24ReadPage{}, agent24NotFound()
	}
	source := agent24SourceRevision(key, req.KnowledgeID, snap.Fingerprint)
	var build *string
	if snap.Chunks != nil {
		ids := make([]string, len(snap.Chunks))
		for i, c := range snap.Chunks {
			ids[i] = c.ID
		}
		b := agent24IndexBuildID(key, req.KnowledgeID, ids)
		build = &b
	}
	if req.ChunkID != "" {
		return agent24JudgeChunkRead(key, req, snap, source, build)
	}
	return agent24JudgeDocRead(key, req, snap, source, build)
}

// agent24JudgeChunkRead: (1) not servable → stale/anchor_missing; (2) expected_revision or the cursor bound to
// another content_tag → stale with the chunk's current pair, no text; (3) the chunk's text from the cursor's
// offset, at most MaxBytes.
func agent24JudgeChunkRead(key []byte, req agent24ReadRequest, snap agent24ReadSnapshot, source string,
	build *string,
) (agent24ReadPage, *agent24KnowledgeError) {
	var ch *agent24ReadChunk
	for i := range snap.Chunks {
		if snap.Chunks[i].ID == req.ChunkID {
			ch = &snap.Chunks[i]
		}
	}
	if ch == nil {
		return agent24ReadPage{}, agent24Stale(source, build, nil, "", true)
	}
	if !ch.Loaded {
		return agent24ReadPage{}, agent24Internal()
	}
	tag := agent24ContentTag(key, ch.ID, ch.Content)
	if (req.Expected != nil && req.Expected.ContentTag != tag) || (req.Cursor != nil && req.Cursor.ContentTag != tag) {
		return agent24ReadPage{}, agent24Stale(source, build, ch, tag, false)
	}
	text, off := strings.ToValidUTF8(ch.Content, "�"), 0
	if req.Cursor != nil {
		off = req.Cursor.Offset
	}
	if off > 0 && (off >= len(text) || !utf8.RuneStart(text[off])) {
		return agent24ReadPage{}, agent24Internal() // a cursor this server issued for the same text cannot do this
	}
	piece, cut := agent24ReadCut(text, off, req.MaxBytes)
	page := agent24ReadPage{
		Revision: agent24DocRevision{SourceRevision: source, IndexBuildID: *build},
		Display:  agent24Display(snap.Title, snap.FileName),
		Segments: []agent24ReadSegment{{
			ChunkID: ch.ID, ChunkType: string(types.ChunkTypeText), ChunkRevision: ch.ContentRevision,
			ContentTag: tag, Text: piece,
		}},
		Truncated: cut,
	}
	if cut {
		next := agent24EncodeReadCursor(key, req.KnowledgeID, req.ChunkID,
			agent24ReadCursor{ContentTag: tag, Offset: off + len(piece)})
		page.NextCursor = &next
	}
	return page, nil
}

// agent24JudgeDocRead: expected_revision, then the cursor's binding, against the current pair (stale); without
// a build, index_pending or index_failed; otherwise whole segments from the cursor's position while they fit
// in MaxBytes, at most PageLimit. Only a page's first segment is ever cut (when it alone exceeds MaxBytes);
// a cursor inside a chunk whose text has since changed restarts that chunk.
func agent24JudgeDocRead(key []byte, req agent24ReadRequest, snap agent24ReadSnapshot, source string,
	build *string,
) (agent24ReadPage, *agent24KnowledgeError) {
	differs := func(s, b string) bool { return s != source || build == nil || b != *build }
	if req.Expected != nil && differs(req.Expected.SourceRevision, req.Expected.IndexBuildID) ||
		req.Cursor != nil && differs(req.Cursor.SourceRevision, req.Cursor.IndexBuildID) {
		return agent24ReadPage{}, agent24Stale(source, build, nil, "", false)
	}
	if build == nil {
		if agent24ClassifyDoc(snap.ParseStatus, 0) == agent24DocBuilding {
			return agent24ReadPage{}, agent24Unavailable("index_pending")
		}
		return agent24ReadPage{}, agent24Unavailable("index_failed")
	}
	pos, off := 0, 0
	if req.Cursor != nil {
		pos, off = req.Cursor.Position, req.Cursor.Offset
	}
	if pos > len(snap.Chunks) {
		return agent24ReadPage{}, agent24Internal() // the set is bound by index_build_id, so the cursor fits it
	}
	page := agent24ReadPage{
		Revision: agent24DocRevision{SourceRevision: source, IndexBuildID: *build},
		Display:  agent24Display(snap.Title, snap.FileName),
		Segments: []agent24ReadSegment{},
	}
	var next *agent24ReadCursor
	budget := req.MaxBytes
	for i := pos; i < len(snap.Chunks); i++ {
		if len(page.Segments) == req.PageLimit {
			next = &agent24ReadCursor{Position: i}
			break
		}
		ch := snap.Chunks[i]
		if !ch.Loaded {
			return agent24ReadPage{}, agent24Internal()
		}
		tag := agent24ContentTag(key, ch.ID, ch.Content)
		text := strings.ToValidUTF8(ch.Content, "�")
		start := 0
		if i == pos && off > 0 && req.Cursor.ContentTag == tag && off < len(text) && utf8.RuneStart(text[off]) {
			start = off
		}
		if len(text)-start > budget && len(page.Segments) > 0 {
			next = &agent24ReadCursor{Position: i}
			break
		}
		piece, cut := agent24ReadCut(text, start, budget)
		page.Segments = append(page.Segments, agent24ReadSegment{
			ChunkID: ch.ID, ChunkType: string(types.ChunkTypeText), ChunkRevision: ch.ContentRevision,
			ContentTag: tag, Text: piece,
		})
		budget -= len(piece)
		if cut {
			page.Truncated = true
			next = &agent24ReadCursor{Position: i, Offset: start + len(piece), ContentTag: tag}
			break
		}
	}
	if next != nil {
		next.SourceRevision, next.IndexBuildID = source, *build
		raw := agent24EncodeReadCursor(key, req.KnowledgeID, "", *next)
		page.NextCursor = &raw
	}
	return page, nil
}
