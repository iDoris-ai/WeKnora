// Agent24 fork product patch A24-KB-13 (CONTRACT-READONLY-v1 r10 §4 R4.4–R4.5, R2.6, §5 R5.1, §13).
//
// Read snapshot: the document, its servable set and the page's text, read in one database snapshot. The
// judgement (agent24_knowledge_read_judge.go) and the endpoint wiring land in later slices (WeKnora#38).

package handler

import (
	"context"
	"slices"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

// agent24ReadChunk is one servable chunk of the read document (R5.1).
type agent24ReadChunk struct {
	ID              string
	ContentRevision int
	Content         string
	Loaded          bool // Content was read: only for the chunks the page asked for
}

// agent24ReadSnapshot holds the rows of one database snapshot (R4.4, R4.5).
type agent24ReadSnapshot struct {
	Found       bool // the document is in the tenant, lies in E, and is not deleting, draft or unknown (R2.6)
	ParseStatus string
	Title       string
	FileName    string
	Fingerprint agent24SourceFingerprint
	// Chunks is the document's whole servable set in document order (chunk_index, then id): nil without a
	// build, non-nil (possibly empty) with one. The same set as agent24DBHitReader's, so index_build_id
	// agrees between search and read.
	Chunks []agent24ReadChunk
}

// agent24ReadWindow names the chunks whose text the snapshot loads: ChunkID for a single-chunk read,
// otherwise Limit chunks from Position in document order.
type agent24ReadWindow struct {
	ChunkID  string
	Position int
	Limit    int
}

// agent24ReadReader reads the snapshot for one read.
type agent24ReadReader interface {
	Snapshot(ctx context.Context, tenantID uint64, kbIDs []string, knowledgeID string, w agent24ReadWindow) (
		agent24ReadSnapshot, error)
}

type agent24DBReadReader struct{ db *gorm.DB }

var _ agent24ReadReader = agent24DBReadReader{}

// agent24ReadSnapshotTestHook, when set by a test, runs inside the snapshot after the document query and
// again after the servable-set query.
var agent24ReadSnapshotTestHook func()

// Snapshot reads, in one transaction (agent24SnapshotTxOptions), the document row, its servable set and the
// window's text. gorm adds deleted_at IS NULL for both models. A document outside the tenant or kbIDs, or
// whose parse_status agent24ClassifyDoc calls excluded, is Found false and nothing else is read.
func (r agent24DBReadReader) Snapshot(ctx context.Context, tenantID uint64, kbIDs []string, knowledgeID string,
	w agent24ReadWindow,
) (agent24ReadSnapshot, error) {
	var snap agent24ReadSnapshot
	fn := func(tx *gorm.DB) error {
		var rows []types.Knowledge
		if err := tx.Select(
			"id, knowledge_base_id, parse_status, title, file_name, file_hash, file_path, source, type, metadata",
		).Where("tenant_id = ? AND id = ?", tenantID, knowledgeID).Limit(1).Find(&rows).Error; err != nil {
			return err
		}
		if agent24ReadSnapshotTestHook != nil {
			agent24ReadSnapshotTestHook()
		}
		if len(rows) == 0 || !slices.Contains(kbIDs, rows[0].KnowledgeBaseID) ||
			agent24ClassifyDoc(rows[0].ParseStatus, 0) == agent24DocExcluded {
			return nil
		}
		row := &rows[0]
		version, err := agent24ManualVersion(row)
		if err != nil {
			return err
		}
		snap = agent24ReadSnapshot{
			Found: true, ParseStatus: row.ParseStatus, Title: row.Title, FileName: row.FileName,
			Fingerprint: agent24SourceFingerprint{
				FileHash: row.FileHash, FilePath: row.FilePath, Source: row.Source, ManualVersion: version,
			},
		}
		if !agent24HasBuild(row.ParseStatus) {
			return nil
		}
		var found []types.Chunk
		if err := tx.Select("id, content_revision").
			Where("tenant_id = ? AND knowledge_id = ? AND chunk_type = ? AND is_enabled = ?",
				tenantID, knowledgeID, string(types.ChunkTypeText), true).
			Order("chunk_index ASC, id ASC").Find(&found).Error; err != nil {
			return err
		}
		snap.Chunks = make([]agent24ReadChunk, 0, len(found))
		for _, c := range found {
			snap.Chunks = append(snap.Chunks, agent24ReadChunk{ID: c.ID, ContentRevision: c.ContentRevision})
		}
		if agent24ReadSnapshotTestHook != nil {
			agent24ReadSnapshotTestHook()
		}
		var ids []string
		if w.ChunkID != "" {
			for _, c := range snap.Chunks {
				if c.ID == w.ChunkID {
					ids = []string{c.ID}
					break
				}
			}
		} else if w.Position >= 0 && w.Position < len(snap.Chunks) {
			for _, c := range snap.Chunks[w.Position:min(w.Position+max(w.Limit, 0), len(snap.Chunks))] {
				ids = append(ids, c.ID)
			}
		}
		if len(ids) == 0 {
			return nil
		}
		var texts []types.Chunk
		if err := tx.Select("id, content").Where("tenant_id = ? AND id IN ?", tenantID, ids).
			Find(&texts).Error; err != nil {
			return err
		}
		byID := make(map[string]string, len(texts))
		for _, c := range texts {
			byID[c.ID] = c.Content
		}
		for i := range snap.Chunks {
			if content, ok := byID[snap.Chunks[i].ID]; ok {
				snap.Chunks[i].Content, snap.Chunks[i].Loaded = content, true
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
		return agent24ReadSnapshot{}, err
	}
	return snap, nil
}
