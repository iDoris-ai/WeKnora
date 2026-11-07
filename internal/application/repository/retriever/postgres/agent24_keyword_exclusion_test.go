// Agent24 fork patch A24-PATCH-02 (WeKnora#127; upstream Tencent/WeKnora#4899).

package postgres

import (
	"strings"
	"testing"

	"gorm.io/gorm/clause"
)

// ParadeDB v0.25.11 rejects a BM25 query whose filter compares two columns
// ("Unsupported query shape"), so the keyword exclusion must test the prefix
// with starts_with only.
func TestGeneratedQuestionExclusionClauseIsBM25Safe(t *testing.T) {
	if generatedQuestionExclusionClause(nil) != nil {
		t.Fatal("no knowledge base ids must leave the filter off")
	}
	expr, ok := generatedQuestionExclusionClause([]string{"kb-1", "kb-2"}).(clause.Expr)
	if !ok {
		t.Fatal("want a clause.Expr")
	}
	want := "NOT (knowledge_base_id IN (?, ?) AND source_type = 0 AND chunk_id <> '' " +
		"AND starts_with(source_id, chunk_id || '-'))"
	if expr.SQL != want {
		t.Fatalf("SQL = %q\nwant  %q", expr.SQL, want)
	}
	for _, banned := range []string{"source_id <>", "substr("} {
		if strings.Contains(expr.SQL, banned) {
			t.Fatalf("SQL compares columns (%q): %s", banned, expr.SQL)
		}
	}
	if len(expr.Vars) != 2 || expr.Vars[0] != "kb-1" || expr.Vars[1] != "kb-2" {
		t.Fatalf("Vars = %v", expr.Vars)
	}
}
