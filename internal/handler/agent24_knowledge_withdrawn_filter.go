// Agent24 fork product patch A24-KB-47 (KB-SOP-02 S4b, WeKnora 原 #146, CONTRACT-OPS-v2 W8.3, W8.10): the read
// surfaces leave out withdrawn entry documents. The filter is the withdrawal table, not the document's
// parse_status: a withdrawn document can still read completed or finalizing (S4a's known limitations: a worker's
// final whole-row save after its last abort check, and a redelivered parse overlapping post-process), and it must
// not appear either way.
//
// The filtered reads are search's snapshot (A24-KB-08), read and the UI doc.get and chunk.list that share its
// snapshot (A24-KB-13), the UI doc.list and its total, the knowledge-base and tag document counts (A24-KB-35), and
// the wiki page's source list (A24-KB-36), and the index facts ready and search share (agent24DBIndexReader.Facts).
// A withdrawn document is absent from each of them exactly as a document that does not exist: read answers
// not_found and a count does not include it. A wiki page citing one is hidden whole (agent24UIWikiHiddenSlugs).

package handler

import (
	"context"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

// agent24NotWithdrawn scopes a query over knowledges to the rows with no agent24_entry_withdrawals row. table is
// the name or alias the query uses for knowledges ("knowledges", or "k" in a join). The table always exists where
// these reads run (the container ensures it before the gate is installed), so a missing table is a storage error,
// not an unfiltered read.
func agent24NotWithdrawn(table string) func(*gorm.DB) *gorm.DB {
	cond := "NOT EXISTS (SELECT 1 FROM " + types.Agent24EntryWithdrawal{}.TableName() + " AS w WHERE w.tenant_id = " +
		table + ".tenant_id AND w.knowledge_id = " + table + ".id)"
	return func(db *gorm.DB) *gorm.DB { return db.Where(cond) }
}

// agent24UIWikiHiddenSlugs is the set of published wiki pages in this tenant and knowledge base that cite a
// withdrawn document in source_refs (A24-KB-47, review C1). Such a page's summary and content may be built from
// the withdrawn document, so wiki.list, wiki.get and the page links treat it as a page that does not exist; a page
// built from several sources is hidden whole. The refs are parsed exactly as the source list parses them
// (types.WikiSourceKnowledgeID). The pages are scanned only when the knowledge base has a withdrawn document.
func (h *Agent24KnowledgeUIReadHandler) agent24UIWikiHiddenSlugs(
	ctx context.Context, tenantID uint64, kbID string,
) ([]string, error) {
	gone, err := h.agent24UIWikiWithdrawnIDs(ctx, tenantID, kbID)
	if err != nil || len(gone) == 0 {
		return nil, err
	}
	var rows []agent24UIWikiRefsRow
	err = h.db.WithContext(ctx).Table("wiki_pages").
		Select("slug, source_refs").
		Where("tenant_id = ? AND knowledge_base_id = ? AND status = ? AND deleted_at IS NULL",
			tenantID, kbID, types.WikiPageStatusPublished).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	hidden := make([]string, 0)
	for _, row := range rows {
		refs, err := agent24UIWikiStrings(row.SourceRefs)
		if err != nil {
			return nil, err
		}
		if agent24UIWikiCitesAny(refs, gone) {
			hidden = append(hidden, row.Slug)
		}
	}
	return hidden, nil
}

// agent24UIWikiWithdrawnIDs is the set of withdrawn documents of this tenant in kbID (a soft-deleted document row
// still counts, so the set only ever hides more).
func (h *Agent24KnowledgeUIReadHandler) agent24UIWikiWithdrawnIDs(
	ctx context.Context, tenantID uint64, kbID string,
) (map[string]struct{}, error) {
	var withdrawn []string
	err := h.db.WithContext(ctx).Table(types.Agent24EntryWithdrawal{}.TableName()+" AS w").
		Select("w.knowledge_id").
		Where("w.tenant_id = ? AND w.knowledge_id IN (?)", tenantID,
			h.db.WithContext(ctx).Table("knowledges").Select("id").
				Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID)).
		Pluck("w.knowledge_id", &withdrawn).Error
	if err != nil {
		return nil, err
	}
	gone := make(map[string]struct{}, len(withdrawn))
	for _, id := range withdrawn {
		gone[id] = struct{}{}
	}
	return gone, nil
}

type agent24UIWikiRefsRow struct {
	Slug       string
	SourceRefs agent24UIWikiJSON `gorm:"column:source_refs"`
}

// agent24UIWikiCitesAny reports whether a source ref names a document in gone.
func agent24UIWikiCitesAny(refs []string, gone map[string]struct{}) bool {
	for _, ref := range refs {
		if _, ok := gone[types.WikiSourceKnowledgeID(ref)]; ok {
			return true
		}
	}
	return false
}
