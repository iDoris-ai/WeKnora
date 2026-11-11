// Agent24 fork product patch A24-KB-47 (KB-SOP-02 S4b, WeKnora#146, CONTRACT-OPS-v2 W8.3, W8.10).

package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	agent24WDTenant  = uint64(9)
	agent24WDKB      = "kb-a"
	agent24WDTag     = "tag-shared"
	agent24WDSlug    = "sources"
	agent24WDMissing = "missing-doc"
	agent24WDUI      = "/api/v1/agent24/knowledge/ui"
)

// agent24WDDoc is one completed (or other status) document seeded into the shared visible KB.
type agent24WDDoc struct {
	id, title, file, content, chunkID, status string
	tagged                                    bool
	created                                   time.Time
}

type agent24WDFix struct {
	t   *testing.T
	db  *gorm.DB
	rig agent24UIReadRig
}

func agent24WDKBPath() string {
	return agent24WDUI + "/knowledge-bases/" + agent24WDKB
}

func agent24WDDocPath(id string) string {
	return agent24WDUI + "/documents/" + id
}

func agent24WDChunkPath(id string) string {
	return agent24WDDocPath(id) + "/chunks"
}

// agent24WDOpen seeds one visible KB and the given documents, then mounts the UI read routes.
// wikiRefs nil skips the wiki page. A non-nil list is that page's source_refs, in order, even when the
// document does not exist yet.
func agent24WDOpen(t *testing.T, docs []agent24WDDoc, wikiRefs []string) *agent24WDFix {
	t.Helper()
	db, store := agent24UIHandlerDB(t)
	agent24UIReadMigrate(t, db)
	require.NoError(t, db.Create(&agent24UIReadKBRow{
		ID: agent24WDKB, Name: "Base", Description: "d", Type: "document", TenantID: agent24WDTenant,
		IndexingStrategy: types.IndexingStrategy{WikiEnabled: true},
	}).Error)
	require.NoError(t, db.Create(&types.KnowledgeTag{
		ID: agent24WDTag, TenantID: agent24WDTenant, KnowledgeBaseID: agent24WDKB,
		Name: "shared", Color: "red", SortOrder: 1,
		CreatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
	}).Error)
	for _, doc := range docs {
		agent24WDInsertDoc(t, db, doc)
	}
	if wikiRefs != nil {
		agent24WDInsertWiki(t, db, "wiki-1", agent24WDSlug, "wiki-body", wikiRefs, nil)
	}
	proof, vph := agent24UIViewSecret()
	raw := agent24UIRawTicket(91)
	ticket := agent24UISeedTicket(t, store, raw, vph, func(row *types.Agent24UITicket) {
		require.NoError(t, row.SetScopeKnowledgeBaseIDs([]string{agent24WDKB}))
	})
	keys := []*types.TenantAPIKey{agent24UILiveKey([]string{agent24WDKB}, false, nil, nil)}
	h := agent24UIReadHandler(db, store, keys, nil)
	rig := agent24UIReadMount(t, h)
	rig.token = agent24UIReadIssue(t, ticket, agent24uisession.UIRouteOps(), time.Now(), time.Minute)
	rig.proof = proof
	rig.raw = raw
	return &agent24WDFix{t: t, db: db, rig: rig}
}

func agent24WDInsertDoc(t *testing.T, db *gorm.DB, doc agent24WDDoc) {
	t.Helper()
	require.NoError(t, db.Create(&types.Knowledge{
		ID: doc.id, TenantID: agent24WDTenant, KnowledgeBaseID: agent24WDKB,
		ParseStatus: doc.status, Type: "file", Title: doc.title, FileName: doc.file,
		CreatedAt: doc.created, UpdatedAt: doc.created,
	}).Error)
	if doc.chunkID != "" {
		require.NoError(t, db.Create(&types.Chunk{
			ID: doc.chunkID, TenantID: agent24WDTenant, KnowledgeBaseID: agent24WDKB,
			KnowledgeID: doc.id, ChunkType: string(types.ChunkTypeText), IsEnabled: true,
			Content: doc.content, ChunkIndex: 0,
		}).Error)
	}
	if doc.tagged {
		require.NoError(t, db.Create(&types.KnowledgeTagRelation{
			KnowledgeID: doc.id, TagID: agent24WDTag,
		}).Error)
	}
}

// agent24WDInsertWiki stores a published page whose source_refs are refs as given (bare ids or "id|title") and
// whose out_links and in_links are links.
func agent24WDInsertWiki(t *testing.T, db *gorm.DB, id, slug, content string, refs, links []string) {
	t.Helper()
	at := time.Date(2026, 11, 11, 2, 0, 0, 0, time.UTC)
	if links == nil {
		links = []string{}
	}
	require.NoError(t, db.Create(&types.WikiPage{
		ID: id, TenantID: agent24WDTenant, KnowledgeBaseID: agent24WDKB,
		Slug: slug, Title: "title " + content, PageType: types.WikiPageTypeSummary,
		Status: types.WikiPageStatusPublished, Summary: "summary " + content, Content: content,
		Aliases: types.StringArray{}, OutLinks: types.StringArray(links), InLinks: types.StringArray(links),
		SourceRefs: types.StringArray(refs), Version: 1, CreatedAt: at, UpdatedAt: at,
	}).Error)
}

func agent24WDPair(t *testing.T) *agent24WDFix {
	t.Helper()
	return agent24WDOpen(t, []agent24WDDoc{
		{
			id: "doc-a", title: "Doc A", file: "a.txt", content: "text-a", chunkID: "chunk-a",
			status: types.ParseStatusCompleted, tagged: true,
			created: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			id: "doc-b", title: "Doc B", file: "b.txt", content: "text-b", chunkID: "chunk-b",
			status: types.ParseStatusCompleted, tagged: true,
			created: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		},
	}, []string{types.FormatWikiSourceRef("doc-a", "doc-a"), types.FormatWikiSourceRef("doc-b", "doc-b")})
}

func (f *agent24WDFix) withdraw(id string) {
	f.t.Helper()
	require.NoError(f.t, f.db.Create(&types.Agent24EntryWithdrawal{
		TenantID: agent24WDTenant, KnowledgeID: id, WithdrawnBy: "api_tenant:withdrawer",
		WithdrawnAt: time.Date(2026, 11, 11, 1, 0, 0, 0, time.UTC),
	}).Error)
}

func (f *agent24WDFix) snapshot(chunkIDs ...string) agent24HitSnapshot {
	f.t.Helper()
	snap, err := agent24DBHitReader{db: f.db}.Snapshot(
		context.Background(), agent24WDTenant, []string{agent24WDKB}, chunkIDs)
	require.NoError(f.t, err)
	return snap
}

func (f *agent24WDFix) read(id string, w agent24ReadWindow) agent24ReadSnapshot {
	f.t.Helper()
	snap, err := agent24DBReadReader{db: f.db}.Snapshot(
		context.Background(), agent24WDTenant, []string{agent24WDKB}, id, w)
	require.NoError(f.t, err)
	return snap
}

func (f *agent24WDFix) get(path string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.rig.authed(path)
}

func (f *agent24WDFix) hits(chunkIDs ...string) (agent24HitSnapshot, []agent24SearchHit) {
	f.t.Helper()
	snap := f.snapshot(chunkIDs...)
	cands := make([]agent24SearchCandidate, len(chunkIDs))
	for i, id := range chunkIDs {
		cands[i] = agent24SearchCandidate{ChunkID: id, Score: 1}
	}
	return snap, agent24AssembleHits(agent24TestKey, "emb:e1", cands, snap, 50)
}

func agent24WDMapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func agent24WDHitIDs(hits []agent24SearchHit) (docs, chunks []string, ranks []int) {
	for _, h := range hits {
		docs = append(docs, h.KnowledgeID)
		chunks = append(chunks, h.ChunkID)
		ranks = append(ranks, h.Rank)
	}
	return docs, chunks, ranks
}

func (f *agent24WDFix) docList() (ids []string, total float64, body string) {
	f.t.Helper()
	w := f.get(agent24WDKBPath() + "/documents")
	require.Equal(f.t, http.StatusOK, w.Code, w.Body.String())
	parsed := agent24UIJSONMap(f.t, w.Body.Bytes())
	total = parsed["total"].(float64)
	for _, item := range parsed["documents"].([]any) {
		ids = append(ids, item.(map[string]any)["knowledge_id"].(string))
	}
	return ids, total, w.Body.String()
}

func (f *agent24WDFix) kbCount() (float64, string) {
	f.t.Helper()
	w := f.get(agent24WDKBPath())
	require.Equal(f.t, http.StatusOK, w.Code, w.Body.String())
	kb := agent24UIJSONMap(f.t, w.Body.Bytes())["knowledge_base"].(map[string]any)
	require.Equal(f.t, agent24WDKB, kb["id"])
	return kb["document_count"].(float64), w.Body.String()
}

func (f *agent24WDFix) tagCount() (float64, string) {
	f.t.Helper()
	w := f.get(agent24WDKBPath() + "/tags")
	require.Equal(f.t, http.StatusOK, w.Code, w.Body.String())
	parsed := agent24UIJSONMap(f.t, w.Body.Bytes())
	tags := parsed["tags"].([]any)
	require.Len(f.t, tags, 1)
	tag := tags[0].(map[string]any)
	require.Equal(f.t, agent24WDTag, tag["id"])
	return tag["document_count"].(float64), w.Body.String()
}

func (f *agent24WDFix) wikiIDs() (ids []string, body string) {
	f.t.Helper()
	w := f.get(agent24WDKBPath() + "/wiki/pages/" + agent24WDSlug)
	require.Equal(f.t, http.StatusOK, w.Code, w.Body.String())
	page := agent24UIJSONMap(f.t, w.Body.Bytes())["page"].(map[string]any)
	for _, item := range page["source_documents"].([]any) {
		ids = append(ids, item.(map[string]any)["knowledge_id"].(string))
	}
	return ids, w.Body.String()
}

func agent24WDWikiPath(slug string) string {
	return agent24WDKBPath() + "/wiki/pages/" + slug
}

// wikiSlugs is wiki.list's slugs, checked against its total.
func (f *agent24WDFix) wikiSlugs() []string {
	f.t.Helper()
	w := f.get(agent24WDKBPath() + "/wiki/pages")
	require.Equal(f.t, http.StatusOK, w.Code, w.Body.String())
	parsed := agent24UIJSONMap(f.t, w.Body.Bytes())
	slugs := []string{}
	for _, item := range parsed["pages"].([]any) {
		slugs = append(slugs, item.(map[string]any)["slug"].(string))
	}
	require.Equal(f.t, float64(len(slugs)), parsed["total"])
	slices.Sort(slugs)
	return slugs
}

func agent24WDSameGone(t *testing.T, got, missing *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, missing.Code, got.Code)
	require.Equal(t, missing.Body.Bytes(), got.Body.Bytes())
}

func agent24WDLoaded(t *testing.T, snap agent24ReadSnapshot) []string {
	t.Helper()
	var texts []string
	for _, c := range snap.Chunks {
		if c.Loaded {
			texts = append(texts, c.Content)
		}
	}
	return texts
}

// TestAgent24WithdrawnHiddenFromReadSurfaces: a withdrawn document disappears from every read surface,
// and each surface still showed it before the withdrawal row existed.
func TestAgent24WithdrawnHiddenFromReadSurfaces(t *testing.T) {
	const docA, docB, chunkA, chunkB = "doc-a", "doc-b", "chunk-a", "chunk-b"
	chunks := []string{chunkA, chunkB}
	readAll := agent24ReadWindow{Limit: 100}

	t.Run("search", func(t *testing.T) {
		f := agent24WDPair(t)
		snap, hits := f.hits(chunks...)
		require.Equal(t, []string{docA, docB}, agent24WDMapKeys(snap.Docs))
		docs, gotChunks, ranks := agent24WDHitIDs(hits)
		require.Equal(t, []string{docA, docB}, docs)
		require.Equal(t, []string{chunkA, chunkB}, gotChunks)
		require.Equal(t, []int{1, 2}, ranks)

		f.withdraw(docA)
		snap, hits = f.hits(chunks...)
		require.Equal(t, []string{docB}, agent24WDMapKeys(snap.Docs))
		docs, gotChunks, ranks = agent24WDHitIDs(hits)
		require.Equal(t, []string{docB}, docs)
		require.Equal(t, []string{chunkB}, gotChunks)
		require.Equal(t, []int{1}, ranks)
	})

	t.Run("read", func(t *testing.T) {
		f := agent24WDPair(t)
		before := f.read(docA, readAll)
		require.True(t, before.Found)
		require.Equal(t, "Doc A", before.Title)
		require.Equal(t, []string{"text-a"}, agent24WDLoaded(t, before))

		f.withdraw(docA)
		require.Equal(t, f.read(agent24WDMissing, readAll), f.read(docA, readAll))
		kept := f.read(docB, readAll)
		require.True(t, kept.Found)
		require.Equal(t, agent24WDKB, kept.KnowledgeBaseID)
		require.Equal(t, "Doc B", kept.Title)
		require.Equal(t, types.ParseStatusCompleted, kept.ParseStatus)
		require.Equal(t, []string{"text-b"}, agent24WDLoaded(t, kept))
	})

	t.Run("doc.get", func(t *testing.T) {
		f := agent24WDPair(t)
		before := f.get(agent24WDDocPath(docA))
		require.Equal(t, http.StatusOK, before.Code, before.Body.String())
		require.Contains(t, before.Body.String(), docA)

		f.withdraw(docA)
		agent24WDSameGone(t, f.get(agent24WDDocPath(docA)), f.get(agent24WDDocPath(agent24WDMissing)))
	})

	t.Run("chunk.list", func(t *testing.T) {
		f := agent24WDPair(t)
		before := f.get(agent24WDChunkPath(docA))
		require.Equal(t, http.StatusOK, before.Code, before.Body.String())
		require.Contains(t, before.Body.String(), chunkA)
		require.Contains(t, before.Body.String(), "text-a")

		f.withdraw(docA)
		agent24WDSameGone(t, f.get(agent24WDChunkPath(docA)), f.get(agent24WDChunkPath(agent24WDMissing)))
	})

	t.Run("doc.list", func(t *testing.T) {
		f := agent24WDPair(t)
		ids, total, _ := f.docList()
		require.Equal(t, []string{docB, docA}, ids)
		require.Equal(t, float64(2), total)

		f.withdraw(docA)
		ids, total, _ = f.docList()
		require.Equal(t, []string{docB}, ids)
		require.Equal(t, float64(1), total)
	})

	t.Run("kb-count", func(t *testing.T) {
		f := agent24WDPair(t)
		n, _ := f.kbCount()
		require.Equal(t, float64(2), n)

		f.withdraw(docA)
		n, _ = f.kbCount()
		require.Equal(t, float64(1), n)
	})

	t.Run("tag-count", func(t *testing.T) {
		f := agent24WDPair(t)
		n, _ := f.tagCount()
		require.Equal(t, float64(2), n)

		f.withdraw(docA)
		n, _ = f.tagCount()
		require.Equal(t, float64(1), n)
	})

	t.Run("wiki", func(t *testing.T) {
		f := agent24WDPair(t)
		ids, _ := f.wikiIDs()
		require.Equal(t, []string{docA, docB}, ids)
		require.Equal(t, []string{agent24WDSlug}, f.wikiSlugs())

		// review C1: the page cites A, so its synthesized text may come from A; it is hidden whole
		f.withdraw(docA)
		agent24WDSameGone(t, f.get(agent24WDWikiPath(agent24WDSlug)), f.get(agent24WDWikiPath("no-such-page")))
		require.Empty(t, f.wikiSlugs())
	})
}

// agent24WDSurface is one pass over the read surfaces the sentinel test compares to the baseline.
type agent24WDSurface struct {
	docs     []string
	chunks   []string
	hits     []agent24SearchHit
	docIDs   []string
	docTotal float64
	kbCount  float64
	tagCount float64
	wikiIDs  []string
	wikiList []string
}

func (f *agent24WDFix) surface(chunkIDs []string) agent24WDSurface {
	f.t.Helper()
	snap, hits := f.hits(chunkIDs...)
	ids, total, _ := f.docList()
	kb, _ := f.kbCount()
	tag, _ := f.tagCount()
	wiki, _ := f.wikiIDs()
	return agent24WDSurface{
		docs: agent24WDMapKeys(snap.Docs), chunks: agent24WDMapKeys(snap.Chunks), hits: hits,
		docIDs: ids, docTotal: total, kbCount: kb, tagCount: tag, wikiIDs: wiki, wikiList: f.wikiSlugs(),
	}
}

func agent24WDForbidSentinel(t *testing.T, surface, body string) {
	t.Helper()
	require.NotContains(t, body, "sentinel-", surface)
}

func agent24WDJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

func agent24WDCandidate(status types.Agent24CandidateStatus) *types.Agent24KnowledgeCandidate {
	marker := "sentinel-cand-" + string(status)
	sum := sha256.Sum256([]byte(marker))
	return &types.Agent24KnowledgeCandidate{
		ID: "cand-" + string(status), TenantID: agent24WDTenant, Author: "api_tenant:author",
		TargetKnowledgeBaseID: agent24WDKB, Mode: types.Agent24CandidateCreate,
		ContentSHA256: hex.EncodeToString(sum[:]), Kind: types.Agent24EntryKindNote,
		Title: marker, Body: marker, SessionRef: "sess", MessageFrom: 1, MessageTo: 2,
		SkillVersion: "v1", RedactionRulesVersion: 1, Status: status,
		SubmittedAt: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC),
	}
}

// TestAgent24WithdrawnSentinels: candidates never reach a read surface, and a withdrawn document stays
// hidden while its parse_status is still completed or finalizing. Filtering is the withdrawal table.
func TestAgent24WithdrawnSentinels(t *testing.T) {
	const docB, chunkB = "doc-b", "chunk-b"
	const docW1, chunkW1, docW2, chunkW2 = "doc-w1", "chunk-w1", "doc-w2", "chunk-w2"
	f := agent24WDOpen(t, []agent24WDDoc{{
		id: docB, title: "Doc B", file: "b.txt", content: "body-b", chunkID: chunkB,
		status: types.ParseStatusCompleted, tagged: true,
		created: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
	}}, []string{docB})
	chunkIDs := []string{chunkB, chunkW1, chunkW2}
	before := f.surface(chunkIDs)
	require.Equal(t, []string{docB}, before.docs)
	require.Equal(t, []string{chunkB}, before.chunks)
	require.Len(t, before.hits, 1)
	require.Equal(t, docB, before.hits[0].KnowledgeID)
	require.Equal(t, chunkB, before.hits[0].ChunkID)
	require.Equal(t, 1, before.hits[0].Rank)
	require.Equal(t, []string{docB}, before.docIDs)
	require.Equal(t, float64(1), before.docTotal)
	require.Equal(t, float64(1), before.kbCount)
	require.Equal(t, float64(1), before.tagCount)
	require.Equal(t, []string{docB}, before.wikiIDs)
	require.Equal(t, []string{agent24WDSlug}, before.wikiList)

	require.NoError(t, apprepo.EnsureAgent24CandidateTables(f.db))
	for _, status := range []types.Agent24CandidateStatus{
		types.Agent24CandidateSubmitted, types.Agent24CandidateReturned, types.Agent24CandidateWithdrawn,
	} {
		row := agent24WDCandidate(status)
		require.NoError(t, f.db.Create(row).Error)
		var n int64
		require.NoError(t, f.db.Model(&types.Knowledge{}).Where("id = ?", row.ID).Count(&n).Error)
		require.Equal(t, int64(0), n)
		var got types.Agent24KnowledgeCandidate
		require.NoError(t, f.db.Where("id = ?", row.ID).First(&got).Error)
		require.Equal(t, status, got.Status)
		require.Contains(t, got.Body, "sentinel-cand-"+string(status))
	}
	agent24WDInsertWiki(t, f.db, "wiki-w1", "sentinel-page", "sentinel-wiki-w1", []string{docW1}, nil)
	agent24WDInsertDoc(t, f.db, agent24WDDoc{
		id: docW1, title: "sentinel-w1", file: "sentinel-w1.txt", content: "sentinel-w1",
		chunkID: chunkW1, status: types.ParseStatusCompleted, tagged: true,
		created: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC),
	})
	f.withdraw(docW1)
	agent24WDInsertDoc(t, f.db, agent24WDDoc{
		id: docW2, title: "sentinel-w2", file: "sentinel-w2.txt", content: "sentinel-w2",
		chunkID: chunkW2, status: types.ParseStatusFinalizing, tagged: false,
		created: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC),
	})
	f.withdraw(docW2)

	require.Equal(t, before, f.surface(chunkIDs))

	snap, hits := f.hits(chunkIDs...)
	agent24WDForbidSentinel(t, "search", agent24WDJSON(t, snap)+agent24WDJSON(t, hits))
	readAll := agent24ReadWindow{Limit: 100}
	for _, id := range []string{docB, docW1, docW2, agent24WDMissing} {
		agent24WDForbidSentinel(t, "read "+id, agent24WDJSON(t, f.read(id, readAll)))
		agent24WDForbidSentinel(t, "doc.get "+id, f.get(agent24WDDocPath(id)).Body.String())
		agent24WDForbidSentinel(t, "chunk.list "+id, f.get(agent24WDChunkPath(id)).Body.String())
	}
	_, _, listBody := f.docList()
	agent24WDForbidSentinel(t, "doc.list", listBody)
	_, kbBody := f.kbCount()
	agent24WDForbidSentinel(t, "kb.get", kbBody)
	_, tagBody := f.tagCount()
	agent24WDForbidSentinel(t, "tag.list", tagBody)
	_, wikiBody := f.wikiIDs()
	agent24WDForbidSentinel(t, "wiki.get", wikiBody)
	agent24WDForbidSentinel(t, "wiki.list", f.get(agent24WDKBPath()+"/wiki/pages").Body.String())
	agent24WDSameGone(t, f.get(agent24WDWikiPath("sentinel-page")), f.get(agent24WDWikiPath("no-such-page")))

	for _, id := range []string{docW1, docW2} {
		require.Equal(t, f.read(agent24WDMissing, readAll), f.read(id, readAll), id)
		agent24WDSameGone(t, f.get(agent24WDDocPath(id)), f.get(agent24WDDocPath(agent24WDMissing)))
		agent24WDSameGone(t, f.get(agent24WDChunkPath(id)), f.get(agent24WDChunkPath(agent24WDMissing)))
	}
}

// TestAgent24WithdrawnCitationReadback: a chunk search returned cannot be read back after its document
// is withdrawn, and a later search does not return it.
func TestAgent24WithdrawnCitationReadback(t *testing.T) {
	const docD, chunkC = "doc-d", "chunk-c"
	f := agent24WDOpen(t, []agent24WDDoc{{
		id: docD, title: "Doc D", file: "d.txt", content: "text-d", chunkID: chunkC,
		status:  types.ParseStatusCompleted,
		created: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
	}}, nil)
	snap := f.snapshot(chunkC)
	ch, ok := snap.Chunks[chunkC]
	require.True(t, ok)
	require.Contains(t, snap.Docs, ch.KnowledgeID)
	knowledgeID, chunkID := ch.KnowledgeID, ch.ID
	require.Equal(t, docD, knowledgeID)
	require.Equal(t, chunkC, chunkID)

	f.withdraw(knowledgeID)
	win := agent24ReadWindow{ChunkID: chunkID}
	require.Equal(t, f.read(agent24WDMissing, win), f.read(knowledgeID, win))
	again := f.snapshot(chunkID)
	_, ok = again.Chunks[chunkID]
	require.False(t, ok)
	_, ok = again.Docs[knowledgeID]
	require.False(t, ok)

	agent24WDSameGone(t, f.get(agent24WDChunkPath(docD)), f.get(agent24WDChunkPath(agent24WDMissing)))
}

// TestAgent24WithdrawnTableMissingFailsClosed: without the withdrawal table the filtered reads fail with a
// storage error; none of them falls back to an unfiltered read.
func TestAgent24WithdrawnTableMissingFailsClosed(t *testing.T) {
	f := agent24WDPair(t)
	require.Equal(t, http.StatusOK, f.get(agent24WDKBPath()+"/documents").Code)
	require.NoError(t, f.db.Migrator().DropTable(&types.Agent24EntryWithdrawal{}))

	_, err := agent24DBHitReader{db: f.db}.Snapshot(
		context.Background(), agent24WDTenant, []string{agent24WDKB}, []string{"chunk-a"})
	require.Error(t, err)
	_, err = agent24DBReadReader{db: f.db}.Snapshot(
		context.Background(), agent24WDTenant, []string{agent24WDKB}, "doc-a", agent24ReadWindow{Limit: 100})
	require.Error(t, err)
	for _, path := range []string{
		agent24WDKBPath() + "/documents", agent24WDKBPath(), agent24WDKBPath() + "/tags",
		agent24WDKBPath() + "/wiki/pages/" + agent24WDSlug, agent24WDDocPath("doc-a"), agent24WDChunkPath("doc-a"),
	} {
		w := f.get(path)
		require.GreaterOrEqual(t, w.Code, http.StatusInternalServerError, path+" "+w.Body.String())
		require.NotContains(t, w.Body.String(), "text-a", path)
	}
}

// TestAgent24WithdrawnFactsAsEmpty: the index facts ready and search share (agent24DBIndexReader.Facts) leave out a
// withdrawn document, so a KB whose only document is withdrawn has the facts of an empty KB. A cancelled document
// that is not withdrawn still counts.
func TestAgent24WithdrawnFactsAsEmpty(t *testing.T) {
	kb := &types.KnowledgeBase{ID: agent24WDKB, TenantID: agent24WDTenant}
	facts := func(f *agent24WDFix) agent24KBIndexFacts {
		t.Helper()
		got, err := agent24DBIndexReader{f.db}.Facts(context.Background(), kb)
		require.NoError(t, err)
		return got
	}
	empty := facts(agent24WDOpen(t, nil, nil))
	require.Equal(t, agent24KBIndexFacts{}, empty)
	for _, status := range []string{types.ParseStatusCancelled, types.ParseStatusCompleted} {
		f := agent24WDOpen(t, []agent24WDDoc{{
			id: "doc-only", title: "Only", file: "o.txt", content: "text-o", chunkID: "chunk-o", status: status,
			created: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		}}, nil)
		require.Equal(t, 1, facts(f).Docs, status)
		f.withdraw("doc-only")
		require.Equal(t, empty, facts(f), status)
	}
}

// TestAgent24WithdrawnWikiPagesHidden (review C1): a page whose source_refs cite a withdrawn document is hidden whole
// from wiki.list (and its total), wiki.get and other pages' links, for a bare-id ref and an "id|title" ref; a page
// citing only live documents is unchanged.
func TestAgent24WithdrawnWikiPagesHidden(t *testing.T) {
	f := agent24WDPair(t)
	agent24WDInsertWiki(t, f.db, "wiki-bare", "bare", "body-bare", []string{"doc-a"}, nil)
	agent24WDInsertWiki(t, f.db, "wiki-mixed", "mixed", "body-mixed",
		[]string{types.FormatWikiSourceRef("doc-b", "B"), types.FormatWikiSourceRef("doc-a", "A")}, nil)
	agent24WDInsertWiki(t, f.db, "wiki-b", "only-b", "body-b", []string{"doc-b"},
		[]string{"bare", "mixed", agent24WDSlug, "only-b"})
	links := func() (out, in []string) {
		w := f.get(agent24WDWikiPath("only-b"))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		page := agent24UIJSONMap(t, w.Body.Bytes())["page"].(map[string]any)
		for _, s := range page["out_links"].([]any) {
			out = append(out, s.(string))
		}
		for _, l := range page["in_links"].([]any) {
			in = append(in, l.(map[string]any)["slug"].(string))
		}
		return out, in
	}
	require.Equal(t, []string{"bare", "mixed", "only-b", agent24WDSlug}, f.wikiSlugs())
	out, in := links()
	require.Equal(t, []string{"bare", "mixed", agent24WDSlug, "only-b"}, out)
	require.Equal(t, []string{"bare", "mixed", "only-b", agent24WDSlug}, in)

	f.withdraw("doc-a")
	require.Equal(t, []string{"only-b"}, f.wikiSlugs())
	missing := f.get(agent24WDWikiPath("no-such-page"))
	for _, slug := range []string{"bare", "mixed", agent24WDSlug} {
		agent24WDSameGone(t, f.get(agent24WDWikiPath(slug)), missing)
	}
	out, in = links()
	require.Equal(t, []string{"only-b"}, out)
	require.Equal(t, []string{"only-b"}, in)
	body := f.get(agent24WDWikiPath("only-b")).Body.String()
	require.Contains(t, body, "body-b")
	for _, gone := range []string{"body-bare", "body-mixed", "wiki-body"} {
		require.NotContains(t, body, gone)
		require.NotContains(t, f.get(agent24WDKBPath()+"/wiki/pages").Body.String(), gone)
	}
}
