// Agent24 fork product patch A24-KB-47 (KB-SOP-02 S4b, WeKnora 原 #146, CONTRACT-OPS-v2 W8.3, W8.10): the read
// surfaces leave out withdrawn entry documents. The filter is the withdrawal table, not the document's
// parse_status: a withdrawn document can still read completed or finalizing (S4a's known limitations: a worker's
// final whole-row save after its last abort check, and a redelivered parse overlapping post-process), and it must
// not appear either way.
//
// The filtered reads are search's snapshot (A24-KB-08), read and the UI doc.get and chunk.list that share its
// snapshot (A24-KB-13), the UI doc.list and its total, the knowledge-base and tag document counts (A24-KB-35), and
// the wiki page's source list (A24-KB-36). A withdrawn document is absent from each of them exactly as a document
// that does not exist: read answers not_found and a count does not include it.

package handler

import (
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
