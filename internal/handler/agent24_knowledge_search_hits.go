// Agent24 fork product patch A24-KB-08 (CONTRACT-READONLY-v1 r10 §3 R3.8–R3.10, §5 R5.1–R5.3).
//
// Search hits: the upstream matches are looked up again in one database snapshot, kept only while their
// chunk is servable, and assembled with identifiers and a snippet taken from the same row. The endpoint
// wiring lands in a later slice (WeKnora#38).

package handler

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// agent24DisplayMaxRunes is the schema bound of display.title and display.file_name.
const agent24DisplayMaxRunes = 1024

// agent24SearchCandidate is one upstream retrieval match, in the order the retrieval returned it (R3.6).
type agent24SearchCandidate struct {
	ChunkID string
	Score   float64
}

// agent24HitChunk is a servable chunk read in the snapshot.
type agent24HitChunk struct {
	ID              string
	KnowledgeID     string
	KnowledgeBaseID string
	Content         string
	ContentRevision int
}

// agent24HitDoc is a document with a build read in the snapshot.
type agent24HitDoc struct {
	ID               string
	KnowledgeBaseID  string
	Title            string
	FileName         string
	Fingerprint      agent24SourceFingerprint
	ServableChunkIDs []string // the document's whole servable set (R5.1), any order
}

// agent24HitSnapshot holds the rows of one database snapshot (R3.9). Chunks has only servable chunks
// whose document has a build and lies in the searched KBs, minus those whose index sync is pending or failed
// (R5.3); Docs has their documents, by ID.
type agent24HitSnapshot struct {
	Chunks map[string]agent24HitChunk
	Docs   map[string]agent24HitDoc
}

type agent24HitRevision struct {
	SourceRevision string `json:"source_revision"`
	IndexBuildID   string `json:"index_build_id"`
	ChunkRevision  int    `json:"chunk_revision"`
	ContentTag     string `json:"content_tag"`
}

type agent24HitLocator struct {
	Valid bool `json:"valid"`
}

type agent24HitSnippet struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

type agent24HitDisplay struct {
	Title    string `json:"title,omitempty"`
	FileName string `json:"file_name,omitempty"`
}

// agent24SearchHit is one R3.8 hit.
type agent24SearchHit struct {
	Rank            int                `json:"rank"`
	KnowledgeBaseID string             `json:"knowledge_base_id"`
	KnowledgeID     string             `json:"knowledge_id"`
	ChunkID         string             `json:"chunk_id"`
	ChunkType       string             `json:"chunk_type"`
	Revision        agent24HitRevision `json:"revision"`
	Locator         agent24HitLocator  `json:"locator"`
	IndexState      string             `json:"index_state"`
	Score           float64            `json:"score"`
	ScoreSpace      string             `json:"score_space"`
	Snippet         agent24HitSnippet  `json:"snippet"`
	Display         *agent24HitDisplay `json:"display,omitempty"`
}

// agent24HitReader reads the snapshot for a search's candidates.
type agent24HitReader interface {
	Snapshot(ctx context.Context, tenantID uint64, kbIDs, chunkIDs []string) (agent24HitSnapshot, error)
}

type agent24DBHitReader struct{ db *gorm.DB }

var _ agent24HitReader = agent24DBHitReader{}

// agent24UnservedIndexStatuses: chunk index_status values upstream never serves as a hit (R5.3,
// knowledgebase_search_results.go isSearchableChunk). They stay in the servable set of index_build_id (R5.1).
var agent24UnservedIndexStatuses = []string{"processing", "failed"}

// agent24HitSnapshotTestHook, when set by a test, runs inside the snapshot between its first and second query.
var agent24HitSnapshotTestHook func()

// agent24SnapshotTxOptions: Postgres reads in a read-only REPEATABLE READ transaction, one snapshot for
// every statement; SQLite (WAL) keeps the snapshot of its first read for the whole transaction.
func agent24SnapshotTxOptions(db *gorm.DB) *sql.TxOptions {
	if db.Name() == "postgres" {
		return &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	return nil
}

// Snapshot reads, in one transaction (agent24SnapshotTxOptions), the servable chunks among chunkIDs, their
// documents and each document's whole servable set (R3.9, R5.1). gorm adds deleted_at IS NULL for both models.
// kbIDs may be any size and is applied in Go; more than agent24ScopeBatch chunkIDs is an error (bind limits).
func (r agent24DBHitReader) Snapshot(ctx context.Context, tenantID uint64, kbIDs, chunkIDs []string) (
	agent24HitSnapshot, error,
) {
	if len(chunkIDs) > agent24ScopeBatch {
		return agent24HitSnapshot{}, fmt.Errorf("too many chunk ids: %d > %d", len(chunkIDs), agent24ScopeBatch)
	}
	if len(chunkIDs) == 0 || len(kbIDs) == 0 {
		return agent24HitSnapshot{}, nil
	}
	inKB := map[string]bool{}
	for _, id := range kbIDs {
		inKB[id] = true
	}
	var snap agent24HitSnapshot
	fn := func(tx *gorm.DB) error {
		var kept []types.Chunk
		if err := tx.Select("id, knowledge_id, knowledge_base_id, content, content_revision").
			Where("tenant_id = ? AND id IN ? AND chunk_type = ? AND is_enabled = ? AND index_status NOT IN ?",
				tenantID, chunkIDs, string(types.ChunkTypeText), true, agent24UnservedIndexStatuses).
			Find(&kept).Error; err != nil {
			return err
		}
		kept = slices.DeleteFunc(kept, func(c types.Chunk) bool { return !inKB[c.KnowledgeBaseID] })
		if agent24HitSnapshotTestHook != nil {
			agent24HitSnapshotTestHook()
		}
		if len(kept) == 0 {
			return nil
		}
		var docIDs []string
		for _, c := range kept {
			docIDs = append(docIDs, c.KnowledgeID)
		}
		slices.Sort(docIDs)
		docIDs = slices.Compact(docIDs)
		var knowledges []types.Knowledge
		if err := tx.Select("id, knowledge_base_id, title, file_name, file_hash, file_path, source, type, metadata").
			Where("tenant_id = ? AND id IN ? AND parse_status IN ?", tenantID, docIDs, agent24ActiveParseStatuses).
			Find(&knowledges).Error; err != nil {
			return err
		}
		knowledges = slices.DeleteFunc(knowledges, func(k types.Knowledge) bool { return !inKB[k.KnowledgeBaseID] })
		ids := make([]string, len(knowledges))
		for i := range knowledges {
			ids[i] = knowledges[i].ID
		}
		byDoc := map[string][]string{}
		if len(ids) > 0 {
			var servable []types.Chunk
			if err := tx.Select("id, knowledge_id").
				Where("tenant_id = ? AND knowledge_id IN ? AND chunk_type = ? AND is_enabled = ?",
					tenantID, ids, string(types.ChunkTypeText), true).
				Find(&servable).Error; err != nil {
				return err
			}
			for _, c := range servable {
				byDoc[c.KnowledgeID] = append(byDoc[c.KnowledgeID], c.ID)
			}
		}
		snap.Docs, snap.Chunks = map[string]agent24HitDoc{}, map[string]agent24HitChunk{}
		for i := range knowledges {
			k, v := &knowledges[i], 0
			if k.IsManual() {
				meta, err := k.ManualMetadata()
				if err != nil {
					return err
				}
				v = 1
				if meta != nil {
					v = meta.Version
				}
			}
			snap.Docs[k.ID] = agent24HitDoc{
				ID: k.ID, KnowledgeBaseID: k.KnowledgeBaseID, Title: k.Title, FileName: k.FileName,
				Fingerprint: agent24SourceFingerprint{
					FileHash: k.FileHash, FilePath: k.FilePath, Source: k.Source, ManualVersion: v,
				},
				ServableChunkIDs: byDoc[k.ID],
			}
		}
		for _, c := range kept {
			if _, ok := snap.Docs[c.KnowledgeID]; !ok {
				continue
			}
			snap.Chunks[c.ID] = agent24HitChunk{
				ID: c.ID, KnowledgeID: c.KnowledgeID, KnowledgeBaseID: c.KnowledgeBaseID,
				Content: c.Content, ContentRevision: c.ContentRevision,
			}
		}
		return nil
	}
	var err error
	if opts := agent24SnapshotTxOptions(r.db); opts != nil {
		err = r.db.WithContext(ctx).Transaction(fn, opts)
	} else {
		err = r.db.WithContext(ctx).Transaction(fn)
	}
	if err != nil {
		return agent24HitSnapshot{}, err
	}
	return snap, nil
}

// agent24AssembleHits turns candidates into hits in candidate order (R3.6, R3.8–R3.10, R5.3). A candidate
// is dropped when its chunk is not in snap.Chunks, its document is not in snap.Docs, the two disagree on the
// KB, or its chunk was already taken. At most maxResults hits; rank counts from 1.
func agent24AssembleHits(key []byte, scoreSpace string, cands []agent24SearchCandidate, snap agent24HitSnapshot,
	maxResults int,
) []agent24SearchHit {
	var hits []agent24SearchHit
	seen := map[string]bool{}
	builds := map[string]string{} // index_build_id by document, computed once
	for _, c := range cands {
		if len(hits) >= maxResults {
			break
		}
		ch, ok := snap.Chunks[c.ChunkID]
		if !ok || seen[ch.ID] {
			continue
		}
		doc, ok := snap.Docs[ch.KnowledgeID]
		if !ok || doc.KnowledgeBaseID != ch.KnowledgeBaseID {
			continue
		}
		seen[ch.ID] = true
		build, ok := builds[doc.ID]
		if !ok {
			build = agent24IndexBuildID(key, doc.ID, doc.ServableChunkIDs)
			builds[doc.ID] = build
		}
		text, truncated := agent24Snippet(ch.Content, agent24KnowledgeMaxSnippetBytes)
		hits = append(hits, agent24SearchHit{
			Rank: len(hits) + 1, KnowledgeBaseID: ch.KnowledgeBaseID, KnowledgeID: doc.ID, ChunkID: ch.ID,
			ChunkType: string(types.ChunkTypeText),
			Revision: agent24HitRevision{
				SourceRevision: agent24SourceRevision(key, doc.ID, doc.Fingerprint), IndexBuildID: build,
				ChunkRevision: ch.ContentRevision, ContentTag: agent24ContentTag(key, ch.ID, ch.Content),
			},
			IndexState: "fresh", Score: c.Score, ScoreSpace: scoreSpace,
			Snippet: agent24HitSnippet{Text: text, Truncated: truncated},
			Display: agent24Display(doc.Title, doc.FileName),
		})
	}
	return hits
}

// agent24Snippet cuts content to at most maxBytes bytes of UTF-8 on a rune boundary; invalid bytes become
// U+FFFD first, so the JSON-encoded text cannot outgrow the bound.
func agent24Snippet(content string, maxBytes int) (string, bool) {
	s := strings.ToValidUTF8(content, "�")
	if len(s) <= maxBytes {
		return s, false
	}
	i := maxBytes
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i], true
}

// agent24Display is the optional display metadata, each field cut to agent24DisplayMaxRunes; nil when both
// are empty.
func agent24Display(title, fileName string) *agent24HitDisplay {
	if title == "" && fileName == "" {
		return nil
	}
	return &agent24HitDisplay{
		Title:    agent24TruncateRunes(title, agent24DisplayMaxRunes),
		FileName: agent24TruncateRunes(fileName, agent24DisplayMaxRunes),
	}
}

func agent24TruncateRunes(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}
