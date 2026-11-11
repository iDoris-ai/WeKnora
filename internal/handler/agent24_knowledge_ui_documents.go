// Agent24 fork product patch A24-KB-35 (KB-C2-01c2, WeKnora#133, READONLY-v1 R2.6, R2.8, R5.1).

package handler

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

// agent24UIDocumentItem is one doc.list row. Every field is always present.
type agent24UIDocumentItem struct {
	KnowledgeID     string    `json:"knowledge_id"`
	KnowledgeBaseID string    `json:"knowledge_base_id"`
	Title           string    `json:"title"`
	FileName        string    `json:"file_name"`
	ParseStatus     string    `json:"parse_status"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type agent24UIDocumentList struct {
	Documents []agent24UIDocumentItem `json:"documents"`
	Total     int64                   `json:"total"`
	Page      int                     `json:"page"`
	PageSize  int                     `json:"page_size"`
}

// agent24UIRevision is the document revision pair (R5.1). index_build_id is JSON null without a build.
type agent24UIRevision struct {
	SourceRevision string  `json:"source_revision"`
	IndexBuildID   *string `json:"index_build_id"`
}

type agent24UIDocumentDetail struct {
	KnowledgeID     string            `json:"knowledge_id"`
	KnowledgeBaseID string            `json:"knowledge_base_id"`
	Title           string            `json:"title"`
	FileName        string            `json:"file_name"`
	ParseStatus     string            `json:"parse_status"`
	Revision        agent24UIRevision `json:"revision"`
	ChunkCount      int               `json:"chunk_count"`
}

type agent24UIDocumentOne struct {
	Document agent24UIDocumentDetail `json:"document"`
}

type agent24UIChunkItem struct {
	ChunkID       string `json:"chunk_id"`
	Position      int    `json:"position"`
	ChunkRevision int    `json:"chunk_revision"`
	ContentTag    string `json:"content_tag"`
	Text          string `json:"text"`
}

type agent24UIChunkList struct {
	KnowledgeID     string               `json:"knowledge_id"`
	KnowledgeBaseID string               `json:"knowledge_base_id"`
	Revision        agent24UIRevision    `json:"revision"`
	Chunks          []agent24UIChunkItem `json:"chunks"`
	Total           int                  `json:"total"`
	Page            int                  `json:"page"`
	PageSize        int                  `json:"page_size"`
}

// ListDocuments is GET …/ui/knowledge-bases/:kb_id/documents (doc.list).
// A knowledge base outside E is the same not_found as kb.get. total counts visible documents only
// (R2.6: counted parse statuses; gorm omits soft-deleted rows). Order is created_at DESC, id DESC.
func (h *Agent24KnowledgeUIReadHandler) ListDocuments(c *gin.Context) {
	st, ok := agent24UIReadFrom(c)
	if !ok || h.db == nil {
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	id := c.Param("kb_id")
	if agent24UIKBByID(st.kbs)[id] == nil || !slices.Contains(st.e, id) {
		writeAgent24KnowledgeError(c, agent24NotFound(), agent24KnowledgeEcho{})
		return
	}
	rows, total, err := h.agent24UIListDocuments(c.Request.Context(), st.tenantID, id, st.page, st.pageSize)
	if agent24UIReadFail(c, err) {
		return
	}
	out := make([]agent24UIDocumentItem, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		out = append(out, agent24UIDocumentItem{
			KnowledgeID: row.ID, KnowledgeBaseID: row.KnowledgeBaseID,
			Title: row.Title, FileName: row.FileName, ParseStatus: row.ParseStatus,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	agent24UIReadFinish(c, st)
	c.JSON(http.StatusOK, agent24UIDocumentList{Documents: out, Total: total, Page: st.page, PageSize: st.pageSize})
}

// agent24UIListDocuments lists one page of documents visible under R2.6.
// The status filter is agent24CountedParseStatuses, the same set ClassifyDoc does not call excluded.
func (h *Agent24KnowledgeUIReadHandler) agent24UIListDocuments(
	ctx context.Context, tenantID uint64, kbID string, page, pageSize int,
) ([]types.Knowledge, int64, error) {
	base := func() *gorm.DB {
		return h.db.WithContext(ctx).Model(&types.Knowledge{}).
			Where("tenant_id = ? AND knowledge_base_id = ? AND parse_status IN ?",
				tenantID, kbID, agent24CountedParseStatuses).
			// A24-KB-47
			Scopes(agent24NotWithdrawn("knowledges"))
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []types.Knowledge
	err := base().
		Select("id, knowledge_base_id, title, file_name, parse_status, created_at, updated_at").
		Order("created_at DESC, id DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&rows).Error
	return rows, total, err
}

// GetDocument is GET …/ui/documents/:knowledge_id (doc.get).
// Visibility is the read snapshot (R2.6): another tenant, outside E, deleting, draft, unknown,
// soft-deleted and absent are one not_found. The window position is -1, so no chunk text is loaded.
// revision is agent24RevisionPair, the same pair agent24JudgeRead returns. chunk_count is the
// servable set (0 without a build).
func (h *Agent24KnowledgeUIReadHandler) GetDocument(c *gin.Context) {
	st, ok := agent24UIReadFrom(c)
	if !ok || h.db == nil {
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	id := c.Param("knowledge_id")
	if !agent24UIPathTextUsable(id) { // A24-KB-36
		writeAgent24KnowledgeError(c, agent24NotFound(), agent24KnowledgeEcho{})
		return
	}
	snap, err := (agent24DBReadReader{db: h.db}).Snapshot(c.Request.Context(), st.tenantID, st.e, id,
		agent24ReadWindow{Position: -1})
	if agent24UIReadFail(c, err) {
		return
	}
	if !snap.Found {
		writeAgent24KnowledgeError(c, agent24NotFound(), agent24KnowledgeEcho{})
		return
	}
	source, build := agent24RevisionPair(h.key, id, snap)
	n := 0
	if snap.Chunks != nil {
		n = len(snap.Chunks)
	}
	agent24UIReadFinish(c, st)
	c.JSON(http.StatusOK, agent24UIDocumentOne{Document: agent24UIDocumentDetail{
		KnowledgeID: id, KnowledgeBaseID: snap.KnowledgeBaseID,
		Title: snap.Title, FileName: snap.FileName, ParseStatus: snap.ParseStatus,
		Revision:   agent24UIRevision{SourceRevision: source, IndexBuildID: build},
		ChunkCount: n,
	}})
}

// ListChunks is GET …/ui/documents/:knowledge_id/chunks (chunk.list).
// The document check is the same snapshot as doc.get, with the page window. chunks are that snapshot's
// servable set (text, enabled, not deleted), in its order. position is the 0-based index in that set.
// content_tag is agent24ContentTag over the stored text. text is valid UTF-8 and is not cut.
// A page past the end is an empty list with the real total. Without a build, chunks is empty,
// total is 0 and index_build_id is null.
func (h *Agent24KnowledgeUIReadHandler) ListChunks(c *gin.Context) {
	st, ok := agent24UIReadFrom(c)
	if !ok || h.db == nil {
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	id := c.Param("knowledge_id")
	if !agent24UIPathTextUsable(id) { // A24-KB-36
		writeAgent24KnowledgeError(c, agent24NotFound(), agent24KnowledgeEcho{})
		return
	}
	start := (st.page - 1) * st.pageSize
	snap, err := (agent24DBReadReader{db: h.db}).Snapshot(c.Request.Context(), st.tenantID, st.e, id,
		agent24ReadWindow{Position: start, Limit: st.pageSize})
	if agent24UIReadFail(c, err) {
		return
	}
	if !snap.Found {
		writeAgent24KnowledgeError(c, agent24NotFound(), agent24KnowledgeEcho{})
		return
	}
	source, build := agent24RevisionPair(h.key, id, snap)
	total := 0
	chunks := make([]agent24UIChunkItem, 0)
	if snap.Chunks != nil {
		total = len(snap.Chunks)
		if start < len(snap.Chunks) {
			window := snap.Chunks[start:]
			if len(window) > st.pageSize {
				window = window[:st.pageSize]
			}
			for n, ch := range window {
				if !ch.Loaded {
					writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
					return
				}
				chunks = append(chunks, agent24UIChunkItem{
					ChunkID: ch.ID, Position: start + n, ChunkRevision: ch.ContentRevision,
					ContentTag: agent24ContentTag(h.key, ch.ID, ch.Content),
					Text:       strings.ToValidUTF8(ch.Content, "�"),
				})
			}
		}
	}
	agent24UIReadFinish(c, st)
	c.JSON(http.StatusOK, agent24UIChunkList{
		KnowledgeID: id, KnowledgeBaseID: snap.KnowledgeBaseID,
		Revision: agent24UIRevision{SourceRevision: source, IndexBuildID: build},
		Chunks:   chunks, Total: total, Page: st.page, PageSize: st.pageSize,
	})
}
