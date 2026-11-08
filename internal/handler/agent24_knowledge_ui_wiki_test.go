// Agent24 fork product patch A24-KB-36 (KB-C2-01c3, WeKnora#133, READONLY-v1 R2.6, R2.8).

package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

const agent24UIWikiContentLeak = "WIKI-CONTENT-LEAK"

func agent24UIReadSeedWiki(t *testing.T, db *gorm.DB) {
	t.Helper()
	extra := func(id string, tenant uint64, kb, status, title string) *types.Knowledge {
		return &types.Knowledge{
			ID: id, TenantID: tenant, KnowledgeBaseID: kb, ParseStatus: status, Type: "file",
			Title: title, FileName: id + ".bin",
		}
	}
	for _, row := range []*types.Knowledge{
		extra("doc-wiki-draft", 9, "kb-b", types.ManualKnowledgeStatusDraft, "wiki-draft-title"),
		extra("doc-wiki-foreign", 8, "kb-b", types.ParseStatusCompleted, "wiki-foreign-title"),
		extra("doc-wiki-deleted", 9, "kb-b", types.ParseStatusCompleted, "wiki-deleted-title"),
	} {
		require.NoError(t, db.Create(row).Error)
	}
	require.NoError(t, db.Delete(&types.Knowledge{}, "id = ?", "doc-wiki-deleted").Error)

	newest := time.Date(2024, 7, 4, 1, 0, 0, 0, time.UTC)
	mid := time.Date(2024, 7, 3, 1, 0, 0, 0, time.UTC)
	oldest := time.Date(2024, 7, 1, 1, 0, 0, 0, time.UTC)
	page := func(
		id string, tenant uint64, kb, slug, title, pageType, status, summary, content string, at time.Time,
	) *types.WikiPage {
		return &types.WikiPage{
			ID: id, TenantID: tenant, KnowledgeBaseID: kb, Slug: slug, Title: title,
			PageType: pageType, Status: status, Summary: summary, Content: content,
			Aliases: types.StringArray{}, OutLinks: types.StringArray{}, InLinks: types.StringArray{},
			SourceRefs: types.StringArray{}, Version: 1, CreatedAt: at, UpdatedAt: at,
		}
	}
	acme := page("wiki-acme", 9, "kb-b", "entity/acme-corp", "Acme Corp", types.WikiPageTypeEntity,
		types.WikiPageStatusPublished, "Acme [c001] corp",
		"Body [c001, c002] end "+agent24UIWikiContentLeak, newest)
	acme.Aliases = types.StringArray{"ACME", "Acme Corp"}
	acme.Version = 3
	acme.OutLinks = types.StringArray{
		"concept/zeta", "concept/zeta", "draft-page", "concept/alpha",
		"missing-page", "archived-page", "other-kb-page", "",
	}
	acme.InLinks = types.StringArray{
		"concept/zeta", "draft-page", "concept/alpha", "archived-page", "foreign-page",
		"other-kb-page", "deleted-page", "missing-page", "concept/alpha",
	}
	acme.SourceRefs = types.StringArray{
		"doc-wiki-draft|wiki-draft-title",
		"doc-kb-b|stale-wiki-title",
		"doc-out|outside",
		"doc-wiki-foreign|wiki-foreign-title",
		"doc-wiki-deleted|wiki-deleted-title",
		"doc-kb-b|stale-wiki-title-again",
		"|no-id",
		"",
	}
	alpha := page("wiki-alpha", 9, "kb-b", "concept/alpha", "Alpha", types.WikiPageTypeConcept,
		types.WikiPageStatusPublished, "Alpha summary", "alpha body", mid)
	alpha.OutLinks = types.StringArray{"entity/acme-corp"}
	zeta := page("wiki-zeta", 9, "kb-b", "concept/zeta", "Zeta", types.WikiPageTypeConcept,
		types.WikiPageStatusPublished, "Zeta summary", "zeta body", mid)
	zeta.OutLinks = types.StringArray{"entity/acme-corp", "entity/acme-corp"}
	index := page("wiki-index", 9, "kb-b", "index", "Index", types.WikiPageTypeIndex,
		types.WikiPageStatusPublished, "Index summary", "Index body", oldest)
	pages := []*types.WikiPage{
		acme, alpha, zeta, index,
		page("wiki-draft", 9, "kb-b", "draft-page", "Draft Hidden", types.WikiPageTypeSummary,
			types.WikiPageStatusDraft, "draft summary", "hidden-wiki-draft", mid),
		page("wiki-archived", 9, "kb-b", "archived-page", "Archived Hidden", types.WikiPageTypeSummary,
			types.WikiPageStatusArchived, "archived summary", "hidden-wiki-archived", mid),
		page("wiki-foreign", 8, "kb-b", "foreign-page", "Foreign Hidden", types.WikiPageTypeSummary,
			types.WikiPageStatusPublished, "foreign summary", "hidden-wiki-foreign", mid),
		page("wiki-deleted", 9, "kb-b", "deleted-page", "Deleted Hidden", types.WikiPageTypeSummary,
			types.WikiPageStatusPublished, "deleted summary", "hidden-wiki-deleted", mid),
		page("wiki-otherkb", 9, "kb-a", "other-kb-page", "Other KB", types.WikiPageTypeSummary,
			types.WikiPageStatusPublished, "other kb summary", "hidden-wiki-otherkb", mid),
	}
	for _, row := range pages {
		require.NoError(t, db.Create(row).Error)
	}
	require.NoError(t, db.Delete(&types.WikiPage{}, "id = ?", "wiki-deleted").Error)
}

func agent24UIStringList(t *testing.T, v any) []string {
	t.Helper()
	items, ok := v.([]any)
	require.True(t, ok, "%#v", v)
	out := make([]string, len(items))
	for i, item := range items {
		out[i], ok = item.(string)
		require.True(t, ok, "%#v", item)
	}
	return out
}

func TestAgent24UIReadWiki(t *testing.T) {
	rig, _ := agent24UIReadFixture(t, nil, nil)
	// The shared fixture leaves RedirectTrailingSlash at gin's default (true), so /pages/ would
	// redirect to the list route. Integration mode turns the redirect off; this test does too.
	rig.r.RedirectTrailingSlash = false
	secrets := []string{rig.token, rig.proof, rig.raw, agent24UISentinelAgent}
	base := "/api/v1/agent24/knowledge/ui/knowledge-bases/"
	listPath := base + "kb-b/wiki/pages"
	getPath := listPath + "/entity/acme-corp"
	hidden := []string{
		"draft-page", "archived-page", "foreign-page", "deleted-page", "other-kb-page", "missing-page",
		"wiki-draft-title", "wiki-foreign-title", "wiki-deleted-title", "stale-wiki-title",
		"hidden-wiki-draft", "hidden-wiki-archived", "hidden-wiki-foreign", "hidden-wiki-deleted",
		"hidden-wiki-otherkb", "wiki-acme", "doc-wiki-draft", "doc-wiki-foreign", "doc-wiki-deleted",
		"doc-out",
	}

	t.Run("list", func(t *testing.T) {
		rig.resetCalls()
		before := len(rig.stub.entries)
		w := rig.authed(listPath)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, 1, *rig.calls)
		agent24UIRequireNoStore(t, w)
		body := agent24UIJSONMap(t, w.Body.Bytes())
		agent24UIKeySet(t, body, "pages", "total", "page", "page_size")
		agent24UIForbidKeys(t, body)
		require.Equal(t, float64(4), body["total"])
		require.Equal(t, float64(1), body["page"])
		require.Equal(t, float64(20), body["page_size"])
		items := body["pages"].([]any)
		require.Len(t, items, 4)
		want := []struct{ slug, title, pageType, summary string }{
			{"entity/acme-corp", "Acme Corp", types.WikiPageTypeEntity, "Acme corp"},
			{"concept/alpha", "Alpha", types.WikiPageTypeConcept, "Alpha summary"},
			{"concept/zeta", "Zeta", types.WikiPageTypeConcept, "Zeta summary"},
			{"index", "Index", types.WikiPageTypeIndex, "Index summary"},
		}
		for i, row := range want {
			item := items[i].(map[string]any)
			agent24UIKeySet(t, item, "slug", "title", "page_type", "summary", "updated_at")
			require.Equal(t, row.slug, item["slug"])
			require.Equal(t, row.title, item["title"])
			require.Equal(t, row.pageType, item["page_type"])
			require.Equal(t, row.summary, item["summary"])
			require.NotContains(t, item, "content")
		}
		alphaAt := agent24UIDocTime(t, items[1].(map[string]any)["updated_at"])
		zetaAt := agent24UIDocTime(t, items[2].(map[string]any)["updated_at"])
		require.True(t, alphaAt.Equal(zetaAt))
		require.True(t, agent24UIDocTime(t, items[0].(map[string]any)["updated_at"]).After(alphaAt))
		require.True(t, agent24UIDocTime(t, items[3].(map[string]any)["updated_at"]).Before(alphaAt))
		require.NotContains(t, w.Body.String(), "[c001")
		require.NotContains(t, w.Body.String(), agent24UIWikiContentLeak)
		for _, secret := range hidden {
			require.NotContains(t, w.Body.String(), secret)
		}
		op, outcome, reason, resource := agent24UIReadOp(t, rig.stub, before+1)
		require.Equal(t, before+1, len(rig.stub.entries))
		require.Equal(t, "wiki.list", op)
		require.Equal(t, "ok", outcome)
		require.Empty(t, reason)
		require.Equal(t, "kb-b", resource["kb_id"])
		_, hasSlug := resource["slug"]
		require.False(t, hasSlug)
		agent24UIAssertNoSecrets(t, w.Body.String(), secrets...)
		agent24UIAssertNoSecrets(t, agent24AuditBlob(t, rig.stub.entries[before]), append(
			secrets, agent24UIWikiContentLeak, "Acme [c001] corp")...)
	})

	t.Run("page", func(t *testing.T) {
		w := rig.authed(listPath + "?page=2&page_size=2")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		body := agent24UIJSONMap(t, w.Body.Bytes())
		require.Equal(t, float64(4), body["total"])
		require.Equal(t, float64(2), body["page"])
		require.Equal(t, float64(2), body["page_size"])
		items := body["pages"].([]any)
		require.Len(t, items, 2)
		require.Equal(t, "concept/zeta", items[0].(map[string]any)["slug"])
		require.Equal(t, "index", items[1].(map[string]any)["slug"])
		require.NotContains(t, w.Body.String(), "entity/acme-corp")
		require.NotContains(t, w.Body.String(), "concept/alpha")

		past := rig.authed(listPath + "?page=3&page_size=2")
		require.Equal(t, http.StatusOK, past.Code, past.Body.String())
		pastBody := agent24UIJSONMap(t, past.Body.Bytes())
		require.Equal(t, float64(4), pastBody["total"])
		require.Empty(t, pastBody["pages"].([]any))
	})

	t.Run("get", func(t *testing.T) {
		rig.resetCalls()
		before := len(rig.stub.entries)
		w := rig.authed(getPath)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, 1, *rig.calls)
		agent24UIRequireNoStore(t, w)
		body := agent24UIJSONMap(t, w.Body.Bytes())
		agent24UIKeySet(t, body, "page")
		agent24UIForbidKeys(t, body)
		page := body["page"].(map[string]any)
		agent24UIKeySet(t, page,
			"slug", "title", "page_type", "summary", "content", "aliases", "version", "updated_at",
			"out_links", "in_links", "source_documents")
		require.Equal(t, "entity/acme-corp", page["slug"])
		require.Equal(t, "Acme Corp", page["title"])
		require.Equal(t, types.WikiPageTypeEntity, page["page_type"])
		require.Equal(t, "Acme corp", page["summary"])
		require.Equal(t, "Body end "+agent24UIWikiContentLeak, page["content"])
		require.NotContains(t, page["content"], "[c001")
		require.Equal(t, []string{"ACME", "Acme Corp"}, agent24UIStringList(t, page["aliases"]))
		require.Equal(t, float64(3), page["version"])
		require.False(t, agent24UIDocTime(t, page["updated_at"]).IsZero())
		require.Equal(t, []string{"concept/zeta", "concept/alpha"}, agent24UIStringList(t, page["out_links"]))
		links := page["in_links"].([]any)
		require.Len(t, links, 2)
		for i, row := range []struct{ slug, title string }{{"concept/alpha", "Alpha"}, {"concept/zeta", "Zeta"}} {
			link := links[i].(map[string]any)
			agent24UIKeySet(t, link, "slug", "title")
			require.Equal(t, row.slug, link["slug"])
			require.Equal(t, row.title, link["title"])
		}
		sources := page["source_documents"].([]any)
		require.Len(t, sources, 1)
		src := sources[0].(map[string]any)
		agent24UIKeySet(t, src, "knowledge_id", "title")
		require.Equal(t, "doc-kb-b", src["knowledge_id"])
		require.Equal(t, "doc-kb-b", src["title"])
		for _, secret := range append(hidden, "tenant_id", "chunk_refs", "page_metadata", "last_editor_id",
			"last_edit_source", "folder_id", "deleted_at", "created_at") {
			require.NotContains(t, w.Body.String(), secret)
		}
		op, outcome, reason, resource := agent24UIReadOp(t, rig.stub, before+1)
		require.Equal(t, "wiki.get", op)
		require.Equal(t, "ok", outcome)
		require.Empty(t, reason)
		require.Equal(t, "kb-b", resource["kb_id"])
		require.Equal(t, "entity/acme-corp", resource["slug"])
		agent24UIAssertNoSecrets(t, w.Body.String(), secrets...)
		agent24UIAssertNoSecrets(t, agent24AuditBlob(t, rig.stub.entries[before]), append(secrets,
			agent24UIWikiContentLeak, "Body [c001, c002] end", "Acme [c001] corp")...)

		index := rig.authed(listPath + "/index")
		require.Equal(t, http.StatusOK, index.Code, index.Body.String())
		indexPage := agent24UIJSONMap(t, index.Body.Bytes())["page"].(map[string]any)
		require.Empty(t, agent24UIStringList(t, indexPage["aliases"]))
		require.Empty(t, agent24UIStringList(t, indexPage["out_links"]))
		require.Empty(t, indexPage["in_links"].([]any))
		require.Empty(t, indexPage["source_documents"].([]any))
		require.Equal(t, "Index body", indexPage["content"])

		noisy := rig.authed(getPath + "?page=0&page_size=101&agent_id=" + agent24UISentinelAgent)
		require.Equal(t, http.StatusOK, noisy.Code, noisy.Body.String())
		require.Equal(t, w.Body.Bytes(), noisy.Body.Bytes())
		agent24UIAssertNoSecrets(t, noisy.Body.String(), agent24UISentinelAgent)
	})

	t.Run("empty slug", func(t *testing.T) {
		missing := rig.authed(listPath + "/missing-page")
		require.Equal(t, http.StatusNotFound, missing.Code, missing.Body.String())
		rig.resetCalls()
		before := len(rig.stub.entries)
		w := rig.authed(listPath + "/")
		require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
		require.Equal(t, 1, *rig.calls)
		require.Equal(t, missing.Body.Bytes(), w.Body.Bytes())
		agent24UIRequireNullEcho(t, w.Body.Bytes())
		_, _, _, resource := agent24UIReadOp(t, rig.stub, before+1)
		require.Equal(t, "kb-b", resource["kb_id"])
		_, hasSlug := resource["slug"]
		require.False(t, hasSlug)
	})

	t.Run("invisible", func(t *testing.T) {
		missingList := rig.authed(base + "missing-kb/wiki/pages")
		require.Equal(t, http.StatusNotFound, missingList.Code, missingList.Body.String())
		agent24UIRequireNullEcho(t, missingList.Body.Bytes())
		for _, id := range []string{"kb-local-out", "kb-other", "kb-gone", "kb-a", "missing-kb"} {
			rig.resetCalls()
			before := len(rig.stub.entries)
			w := rig.authed(base + id + "/wiki/pages")
			require.Equal(t, http.StatusNotFound, w.Code, id+" "+w.Body.String())
			require.Equal(t, 1, *rig.calls, id)
			require.Equal(t, missingList.Body.Bytes(), w.Body.Bytes(), id)
			op, outcome, _, resource := agent24UIReadOp(t, rig.stub, before+1)
			require.Equal(t, "wiki.list", op)
			require.Equal(t, "not_found", outcome)
			require.Equal(t, id, resource["kb_id"])
		}
		missingGet := rig.authed(listPath + "/missing-page")
		require.Equal(t, http.StatusNotFound, missingGet.Code, missingGet.Body.String())
		for _, path := range []string{
			listPath + "/draft-page",
			listPath + "/archived-page",
			listPath + "/foreign-page",
			listPath + "/deleted-page",
			listPath + "/no-such-page",
			base + "kb-a/wiki/pages/other-kb-page",
			base + "kb-a/wiki/pages/missing-page",
			base + "kb-local-out/wiki/pages/missing-page",
			base + "kb-other/wiki/pages/missing-page",
			base + "kb-gone/wiki/pages/missing-page",
			base + "missing-kb/wiki/pages/entity/acme-corp",
		} {
			rig.resetCalls()
			w := rig.authed(path)
			require.Equal(t, http.StatusNotFound, w.Code, path+" "+w.Body.String())
			require.Equal(t, 1, *rig.calls, path)
			require.Equal(t, missingGet.Body.Bytes(), w.Body.Bytes(), path)
		}
	})

	t.Run("narrowed", func(t *testing.T) {
		narrow, _ := agent24UIReadFixture(t, []*types.TenantAPIKey{
			agent24UILiveKey([]string{"kb-a"}, false, nil, nil),
		}, nil)
		missingList := narrow.authed(base + "missing-kb/wiki/pages")
		require.Equal(t, http.StatusNotFound, missingList.Code, missingList.Body.String())
		missingGet := narrow.authed(base + "kb-a/wiki/pages/missing-page")
		require.Equal(t, http.StatusNotFound, missingGet.Code, missingGet.Body.String())
		for _, path := range []string{listPath, getPath} {
			narrow.resetCalls()
			w := narrow.authed(path)
			require.Equal(t, http.StatusNotFound, w.Code, path+" "+w.Body.String())
			require.Equal(t, 1, *narrow.calls, path)
			if path == listPath {
				require.Equal(t, missingList.Body.Bytes(), w.Body.Bytes(), path)
			} else {
				require.Equal(t, missingGet.Body.Bytes(), w.Body.Bytes(), path)
			}
		}
	})

	t.Run("query", func(t *testing.T) {
		for _, q := range []string{"?page_size=101", "?page=0"} {
			rig.resetCalls()
			w := rig.authed(listPath + q)
			require.Equal(t, http.StatusBadRequest, w.Code, q+" "+w.Body.String())
			require.Equal(t, 0, *rig.calls, q)
			errObj := agent24UIJSONMap(t, w.Body.Bytes())["error"].(map[string]any)
			require.Equal(t, "invalid_request", errObj["code"])
			agent24UIRequireNullEcho(t, w.Body.Bytes())
			agent24UIRequireNoStore(t, w)
			agent24UIAssertNoSecrets(t, w.Body.String(), rig.token, rig.proof)
		}
	})

	t.Run("slug audit", func(t *testing.T) {
		long := strings.Repeat("s", 300)
		before := len(rig.stub.entries)
		w := rig.authed(listPath + "/" + long)
		require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
		_, _, _, resource := agent24UIReadOp(t, rig.stub, before+1)
		require.Equal(t, "kb-b", resource["kb_id"])
		require.Equal(t, long[:255], resource["slug"])
		require.Len(t, resource["slug"].(string), 255)

		cut := strings.Repeat("a", 254) + "你" + "zzz"
		before = len(rig.stub.entries)
		w = rig.authed(listPath + "/" + cut)
		require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
		_, _, _, resource = agent24UIReadOp(t, rig.stub, before+1)
		require.Equal(t, strings.Repeat("a", 254), resource["slug"])
		agent24UIAssertNoSecrets(t, agent24AuditBlob(t, rig.stub.entries[before]), secrets...)

		// An invalid leading byte is replaced, not allowed to empty the audited slug.
		before = len(rig.stub.entries)
		w = rig.authed(listPath + "/%ff" + long)
		require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
		_, _, _, resource = agent24UIReadOp(t, rig.stub, before+1)
		require.Equal(t, "\uFFFD"+long[:252], resource["slug"])
	})
}

func TestAgent24UIReadWikiInLinksCap(t *testing.T) {
	stored := make([]string, 0, 206)
	live := map[string]string{}
	for i := 204; i >= 0; i-- {
		slug := fmt.Sprintf("p%03d", i)
		stored = append(stored, slug)
		live[slug] = "t-" + slug
	}
	stored = append(stored, "p000")
	got := agent24UIWikiFilterIn(stored, live)
	require.Len(t, got, 200)
	require.Equal(t, "p000", got[0].Slug)
	require.Equal(t, "t-p000", got[0].Title)
	require.Equal(t, "p199", got[199].Slug)
	require.Equal(t, "t-p199", got[199].Title)
}

// TestAgent24UIReadPathTextUnusable stands in for Postgres, which rejects invalid UTF-8 and NUL in
// a text parameter: a query that binds such a string fails. The path ids must be answered not_found
// before any query, identical to a missing row, instead of a retryable storage error.
func TestAgent24UIReadPathTextUnusable(t *testing.T) {
	rig, _ := agent24UIReadFixture(t, nil, nil)
	require.NoError(t, rig.h.db.Callback().Query().After("gorm:query").Register("agent24_pg_text",
		func(tx *gorm.DB) {
			for _, v := range tx.Statement.Vars {
				if s, ok := v.(string); ok && (!utf8.ValidString(s) || strings.ContainsRune(s, 0)) {
					_ = tx.AddError(errors.New(`invalid byte sequence for encoding "UTF8"`))
				}
			}
		}))
	ui := "/api/v1/agent24/knowledge/ui/"
	wiki := ui + "knowledge-bases/kb-b/wiki/pages/"
	missingPage := rig.authed(wiki + "missing-page")
	require.Equal(t, http.StatusNotFound, missingPage.Code, missingPage.Body.String())
	missingDoc := rig.authed(ui + "documents/missing-doc")
	require.Equal(t, http.StatusNotFound, missingDoc.Code, missingDoc.Body.String())
	missingChunks := rig.authed(ui + "documents/missing-doc/chunks")
	require.Equal(t, http.StatusNotFound, missingChunks.Code, missingChunks.Body.String())
	for _, bad := range []string{"%ffabc", "abc%00def"} {
		for _, tc := range []struct {
			path string
			want []byte
		}{
			{wiki + bad, missingPage.Body.Bytes()},
			{wiki + "entity/" + bad, missingPage.Body.Bytes()},
			{ui + "documents/" + bad, missingDoc.Body.Bytes()},
			{ui + "documents/" + bad + "/chunks", missingChunks.Body.Bytes()},
		} {
			rig.resetCalls()
			w := rig.authed(tc.path)
			require.Equal(t, http.StatusNotFound, w.Code, tc.path+" "+w.Body.String())
			require.Equal(t, 1, *rig.calls, tc.path)
			require.Equal(t, tc.want, w.Body.Bytes(), tc.path)
		}
	}
}
