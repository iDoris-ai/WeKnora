// Agent24 fork product patch A24-KB-36 (KB-C2-01c3, WeKnora#133, READONLY-v1 R2.6, R2.8).

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
)

// agent24UIWikiInLinkMax is the most backlink rows wiki.get returns.
const agent24UIWikiInLinkMax = 200

// agent24UIWikiBatch is the slug lookup batch. GetPage uses the same bound.
const agent24UIWikiBatch = 1000

// agent24UIWikiJSON scans a JSON text or jsonb column. SQLite TEXT arrives as a string;
// postgres jsonb arrives as []byte. StringArray.Scan only accepts []byte, so these columns
// are not scanned through StringArray.
type agent24UIWikiJSON []byte

func (b *agent24UIWikiJSON) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		*b = nil
		return nil
	case []byte:
		*b = append(agent24UIWikiJSON(nil), v...)
		return nil
	case string:
		*b = agent24UIWikiJSON(v)
		return nil
	default:
		return fmt.Errorf("wiki json column: %T", value)
	}
}

// agent24UIWikiListItem is one wiki.list row. content is not a field.
type agent24UIWikiListItem struct {
	Slug      string    `json:"slug"`
	Title     string    `json:"title"`
	PageType  string    `json:"page_type"`
	Summary   string    `json:"summary"`
	UpdatedAt time.Time `json:"updated_at"`
}

type agent24UIWikiList struct {
	Pages    []agent24UIWikiListItem `json:"pages"`
	Total    int64                   `json:"total"`
	Page     int                     `json:"page"`
	PageSize int                     `json:"page_size"`
}

type agent24UIWikiLink struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

type agent24UIWikiSource struct {
	KnowledgeID string `json:"knowledge_id"`
	Title       string `json:"title"`
}

// agent24UIWikiPage is the wiki.get whitelist. Unlisted stored columns are not fields.
type agent24UIWikiPage struct {
	Slug            string                `json:"slug"`
	Title           string                `json:"title"`
	PageType        string                `json:"page_type"`
	Summary         string                `json:"summary"`
	Content         string                `json:"content"`
	Aliases         []string              `json:"aliases"`
	Version         int                   `json:"version"`
	UpdatedAt       time.Time             `json:"updated_at"`
	OutLinks        []string              `json:"out_links"`
	InLinks         []agent24UIWikiLink   `json:"in_links"`
	SourceDocuments []agent24UIWikiSource `json:"source_documents"`
}

type agent24UIWikiOne struct {
	Page agent24UIWikiPage `json:"page"`
}

type agent24UIWikiListRow struct {
	Slug      string
	Title     string
	PageType  string `gorm:"column:page_type"`
	Summary   string
	UpdatedAt time.Time
}

type agent24UIWikiGetRow struct {
	Slug       string
	Title      string
	PageType   string `gorm:"column:page_type"`
	Summary    string
	Content    string
	Aliases    agent24UIWikiJSON
	Version    int
	UpdatedAt  time.Time
	OutLinks   agent24UIWikiJSON `gorm:"column:out_links"`
	InLinks    agent24UIWikiJSON `gorm:"column:in_links"`
	SourceRefs agent24UIWikiJSON `gorm:"column:source_refs"`
}

type agent24UIWikiSlugTitle struct {
	Slug  string
	Title string
}

// agent24UIWikiSlugParam is c.Param("slug") with one leading "/" removed. It does not trim space or normalize.
func agent24UIWikiSlugParam(c *gin.Context) string { // A24-KB-36
	return strings.TrimPrefix(c.Param("slug"), "/")
}

// agent24UIWikiReady reports that id is in E and the knowledge base has wiki enabled.
// A missing base, one outside E, and one with wiki disabled are the same not_found.
func agent24UIWikiReady(st *agent24UIReadState, id string) bool {
	kb := agent24UIKBByID(st.kbs)[id]
	return kb != nil && slices.Contains(st.e, id) && kb.IsWikiEnabled()
}

// ListWikiPages is GET …/ui/knowledge-bases/:kb_id/wiki/pages (wiki.list).
// Rows are this tenant, this knowledge base, status published, not soft-deleted.
// Order is updated_at DESC, slug ASC. total counts that set. summary has citation marks removed.
func (h *Agent24KnowledgeUIReadHandler) ListWikiPages(c *gin.Context) {
	st, ok := agent24UIReadFrom(c)
	if !ok || h.db == nil {
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	id := c.Param("kb_id")
	if !agent24UIWikiReady(st, id) {
		writeAgent24KnowledgeError(c, agent24NotFound(), agent24KnowledgeEcho{})
		return
	}
	rows, total, err := h.agent24UIListWikiPages(c.Request.Context(), st.tenantID, id, st.page, st.pageSize)
	if agent24UIReadFail(c, err) {
		return
	}
	out := make([]agent24UIWikiListItem, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		out = append(out, agent24UIWikiListItem{
			Slug: row.Slug, Title: row.Title, PageType: row.PageType,
			Summary:   service.Agent24StripWikiInlineChunkCitations(row.Summary),
			UpdatedAt: row.UpdatedAt,
		})
	}
	agent24UIReadFinish(c, st)
	c.JSON(http.StatusOK, agent24UIWikiList{
		Pages: out, Total: total, Page: st.page, PageSize: st.pageSize,
	})
}

func (h *Agent24KnowledgeUIReadHandler) agent24UIListWikiPages(
	ctx context.Context, tenantID uint64, kbID string, page, pageSize int,
) ([]agent24UIWikiListRow, int64, error) {
	base := func() *gorm.DB {
		return h.db.WithContext(ctx).Model(&types.WikiPage{}).
			Where("tenant_id = ? AND knowledge_base_id = ? AND status = ?",
				tenantID, kbID, types.WikiPageStatusPublished)
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []agent24UIWikiListRow
	err := base().
		Select("slug, title, page_type, summary, updated_at").
		Order("updated_at DESC, slug ASC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&rows).Error
	return rows, total, err
}

// GetWikiPage is GET …/ui/knowledge-bases/:kb_id/wiki/pages/*slug (wiki.get).
// The slug is an exact match after one leading "/" is removed. Empty is not_found.
// A page that is not published, or not in this tenant and knowledge base, is the same not_found.
func (h *Agent24KnowledgeUIReadHandler) GetWikiPage(c *gin.Context) {
	st, ok := agent24UIReadFrom(c)
	if !ok || h.db == nil {
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	id := c.Param("kb_id")
	if !agent24UIWikiReady(st, id) {
		writeAgent24KnowledgeError(c, agent24NotFound(), agent24KnowledgeEcho{})
		return
	}
	slug := agent24UIWikiSlugParam(c)
	if slug == "" || !agent24UIPathTextUsable(slug) {
		writeAgent24KnowledgeError(c, agent24NotFound(), agent24KnowledgeEcho{})
		return
	}
	row, err := h.agent24UIGetWikiPage(c.Request.Context(), st.tenantID, id, slug)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeAgent24KnowledgeError(c, agent24NotFound(), agent24KnowledgeEcho{})
		return
	}
	if agent24UIReadFail(c, err) {
		return
	}
	page, err := h.agent24UIWikiPageView(c.Request.Context(), st.tenantID, id, row)
	if agent24UIReadFail(c, err) {
		return
	}
	agent24UIReadFinish(c, st)
	c.JSON(http.StatusOK, agent24UIWikiOne{Page: page})
}

func (h *Agent24KnowledgeUIReadHandler) agent24UIGetWikiPage(
	ctx context.Context, tenantID uint64, kbID, slug string,
) (agent24UIWikiGetRow, error) {
	var rows []agent24UIWikiGetRow
	err := h.db.WithContext(ctx).Table("wiki_pages").
		Select("slug, title, page_type, summary, content, aliases, version, updated_at, "+
			"out_links, in_links, source_refs").
		Where("tenant_id = ? AND knowledge_base_id = ? AND slug = ? AND status = ? AND deleted_at IS NULL",
			tenantID, kbID, slug, types.WikiPageStatusPublished).
		Limit(1).
		Find(&rows).Error
	if err != nil {
		return agent24UIWikiGetRow{}, err
	}
	if len(rows) == 0 {
		return agent24UIWikiGetRow{}, gorm.ErrRecordNotFound
	}
	return rows[0], nil
}

func (h *Agent24KnowledgeUIReadHandler) agent24UIWikiPageView(
	ctx context.Context, tenantID uint64, kbID string, row agent24UIWikiGetRow,
) (agent24UIWikiPage, error) {
	aliases, err := agent24UIWikiStrings(row.Aliases)
	if err != nil {
		return agent24UIWikiPage{}, err
	}
	outStored, err := agent24UIWikiStrings(row.OutLinks)
	if err != nil {
		return agent24UIWikiPage{}, err
	}
	inStored, err := agent24UIWikiStrings(row.InLinks)
	if err != nil {
		return agent24UIWikiPage{}, err
	}
	refs, err := agent24UIWikiStrings(row.SourceRefs)
	if err != nil {
		return agent24UIWikiPage{}, err
	}
	live, err := h.agent24UIWikiPublished(ctx, tenantID, kbID, append(append([]string{}, outStored...), inStored...))
	if err != nil {
		return agent24UIWikiPage{}, err
	}
	sources, err := h.agent24UIWikiSources(ctx, tenantID, kbID, refs)
	if err != nil {
		return agent24UIWikiPage{}, err
	}
	return agent24UIWikiPage{
		Slug:            row.Slug,
		Title:           row.Title,
		PageType:        row.PageType,
		Summary:         service.Agent24StripWikiInlineChunkCitations(row.Summary),
		Content:         service.Agent24StripWikiInlineChunkCitations(row.Content),
		Aliases:         aliases,
		Version:         row.Version,
		UpdatedAt:       row.UpdatedAt,
		OutLinks:        agent24UIWikiFilterOut(outStored, live),
		InLinks:         agent24UIWikiFilterIn(inStored, live),
		SourceDocuments: sources,
	}, nil
}

func agent24UIWikiStrings(raw agent24UIWikiJSON) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return []string{}, nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return []string{}, nil
	}
	return out, nil
}

// agent24UIWikiPublished resolves slugs the way GetPage resolves backlinks (batches of 1000),
// and keeps a slug only when that page is published in this tenant and knowledge base and not soft-deleted.
func (h *Agent24KnowledgeUIReadHandler) agent24UIWikiPublished(
	ctx context.Context, tenantID uint64, kbID string, slugs []string,
) (map[string]string, error) {
	out := map[string]string{}
	if len(slugs) == 0 {
		return out, nil
	}
	for start := 0; start < len(slugs); start += agent24UIWikiBatch {
		end := min(start+agent24UIWikiBatch, len(slugs))
		var rows []agent24UIWikiSlugTitle
		err := h.db.WithContext(ctx).Table("wiki_pages").
			Select("slug, title").
			Where("tenant_id = ? AND knowledge_base_id = ? AND status = ? AND deleted_at IS NULL AND slug IN ?",
				tenantID, kbID, types.WikiPageStatusPublished, slugs[start:end]).
			Find(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			out[row.Slug] = row.Title
		}
	}
	return out, nil
}

// agent24UIWikiFilterOut keeps stored out_links that are published in this knowledge base.
// Order is the stored order. Duplicates and empty slugs are dropped.
func agent24UIWikiFilterOut(stored []string, live map[string]string) []string {
	out := make([]string, 0)
	seen := map[string]struct{}{}
	for _, slug := range stored {
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		if _, ok := live[slug]; !ok {
			continue
		}
		out = append(out, slug)
	}
	return out
}

// agent24UIWikiFilterIn is GetPage's backlink set (the stored in_links that resolved), sorted by slug,
// at most agent24UIWikiInLinkMax. live already applied tenant, knowledge base, and published.
func agent24UIWikiFilterIn(stored []string, live map[string]string) []agent24UIWikiLink {
	seen := map[string]struct{}{}
	out := make([]agent24UIWikiLink, 0)
	for _, slug := range stored {
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		title, ok := live[slug]
		if !ok {
			continue
		}
		out = append(out, agent24UIWikiLink{Slug: slug, Title: title})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	if len(out) > agent24UIWikiInLinkMax {
		out = out[:agent24UIWikiInLinkMax]
	}
	return out
}

// agent24UIWikiSources keeps source_refs whose document is R2.6-visible in this knowledge base.
// Order follows source_refs. knowledge_id is deduped. title is the document row's current title.
func (h *Agent24KnowledgeUIReadHandler) agent24UIWikiSources(
	ctx context.Context, tenantID uint64, kbID string, refs []string,
) ([]agent24UIWikiSource, error) {
	ids := make([]string, 0)
	seen := map[string]struct{}{}
	for _, ref := range refs {
		id := types.WikiSourceKnowledgeID(ref)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return []agent24UIWikiSource{}, nil
	}
	found := map[string]string{}
	for start := 0; start < len(ids); start += agent24UIWikiBatch {
		end := min(start+agent24UIWikiBatch, len(ids))
		var rows []types.Knowledge
		err := h.db.WithContext(ctx).Model(&types.Knowledge{}).
			Select("id, title").
			Where("tenant_id = ? AND knowledge_base_id = ? AND id IN ? AND parse_status IN ?",
				tenantID, kbID, ids[start:end], agent24CountedParseStatuses).
			Find(&rows).Error
		if err != nil {
			return nil, err
		}
		for i := range rows {
			found[rows[i].ID] = rows[i].Title
		}
	}
	out := make([]agent24UIWikiSource, 0)
	for _, id := range ids {
		title, ok := found[id]
		if !ok {
			continue
		}
		out = append(out, agent24UIWikiSource{KnowledgeID: id, Title: title})
	}
	return out, nil
}
