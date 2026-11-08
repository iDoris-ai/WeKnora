// Agent24 fork product patch A24-KB-35 (KB-C2-01c2, WeKnora#133, READONLY-v1 R2.6, R2.8, R5.1).

package handler

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// agent24UIDocSecrets are values the document and chunk bodies, and the audit row, must not carry.
func agent24UIDocSecrets(rig agent24UIReadRig) []string {
	return []string{
		rig.token, rig.proof, rig.raw,
		"secret-file-path", "secret-file-hash", "secret-source", "secret-error", "secret-metadata",
		"disabled-chunk-text", "image-chunk-text", "deleted-chunk-text", "pending-chunk-text",
		"foreign-chunk-text",
	}
}

func agent24UIDocTime(t *testing.T, v any) time.Time {
	t.Helper()
	s, ok := v.(string)
	require.True(t, ok, "%#v", v)
	got, err := time.Parse(time.RFC3339Nano, s)
	require.NoError(t, err)
	return got
}

func TestAgent24CountedParseStatusesAreClassifiedVisible(t *testing.T) {
	// A new parse-status constant has to be listed here. The counted slice is exactly the statuses
	// ClassifyDoc does not call excluded, including when a servable count would otherwise change the class.
	known := []string{
		types.ParseStatusPending, types.ParseStatusProcessing, types.ParseStatusFailed,
		types.ParseStatusCancelled, types.ParseStatusCompleted, types.ParseStatusFinalizing,
		types.ParseStatusDeleting, types.ManualKnowledgeStatusDraft, "not-a-status", "",
	}
	var want []string
	for _, st := range known {
		if agent24ClassifyDoc(st, 0) != agent24DocExcluded {
			want = append(want, st)
		}
	}
	require.ElementsMatch(t, want, agent24CountedParseStatuses)
	seen := map[string]struct{}{}
	for _, st := range agent24CountedParseStatuses {
		_, dup := seen[st]
		require.False(t, dup, st)
		seen[st] = struct{}{}
		require.NotEqual(t, agent24DocExcluded, agent24ClassifyDoc(st, 0), st)
		require.NotEqual(t, agent24DocExcluded, agent24ClassifyDoc(st, 3), st)
	}
}

func TestAgent24UIReadDocuments(t *testing.T) {
	rig, _ := agent24UIReadFixture(t, nil, nil)
	secrets := agent24UIDocSecrets(rig)
	listPath := "/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a/documents"
	getPath := "/api/v1/agent24/knowledge/ui/documents/doc-completed"
	chunkPath := "/api/v1/agent24/knowledge/ui/documents/doc-completed/chunks"
	docFields := []string{
		"knowledge_id", "knowledge_base_id", "title", "file_name", "parse_status", "created_at", "updated_at",
	}
	hiddenDocs := []string{
		"doc-draft", "doc-deleting", "doc-unknown", "doc-deleted", "doc-foreign", "doc-out", "doc-on-gone",
	}

	t.Run("list", func(t *testing.T) {
		rig.resetCalls()
		before := len(rig.stub.entries)
		w := rig.authed(listPath)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, 1, *rig.calls)
		agent24UIRequireNoStore(t, w)
		body := agent24UIJSONMap(t, w.Body.Bytes())
		agent24UIKeySet(t, body, "documents", "total", "page", "page_size")
		require.Equal(t, float64(3), body["total"])
		require.Equal(t, float64(1), body["page"])
		require.Equal(t, float64(20), body["page_size"])
		items := body["documents"].([]any)
		require.Len(t, items, 3)
		want := []struct{ id, title, file, status string }{
			{"doc-finalizing", "Finalizing", "finalizing.pdf", types.ParseStatusFinalizing},
			{"doc-completed", "Completed", "completed.pdf", types.ParseStatusCompleted},
			{"doc-pending", "Pending", "pending.txt", types.ParseStatusPending},
		}
		for i, row := range want {
			item := items[i].(map[string]any)
			agent24UIKeySet(t, item, docFields...)
			require.Equal(t, row.id, item["knowledge_id"])
			require.Equal(t, "kb-a", item["knowledge_base_id"])
			require.Equal(t, row.title, item["title"])
			require.Equal(t, row.file, item["file_name"])
			require.Equal(t, row.status, item["parse_status"])
			require.False(t, agent24UIDocTime(t, item["created_at"]).IsZero())
			require.False(t, agent24UIDocTime(t, item["updated_at"]).IsZero())
		}
		first := items[0].(map[string]any)
		second := items[1].(map[string]any)
		third := items[2].(map[string]any)
		require.Equal(t, first["created_at"], second["created_at"])
		require.True(t, agent24UIDocTime(t, third["created_at"]).Before(agent24UIDocTime(t, first["created_at"])))
		for _, id := range hiddenDocs {
			require.NotContains(t, w.Body.String(), id)
		}
		require.NotContains(t, w.Body.String(), "doc-kb-b")
		op, outcome, reason, resource := agent24UIReadOp(t, rig.stub, before+1)
		require.Equal(t, before+1, len(rig.stub.entries))
		require.Equal(t, "doc.list", op)
		require.Equal(t, "ok", outcome)
		require.Empty(t, reason)
		require.Equal(t, "kb-a", resource["kb_id"])
		require.NotContains(t, resource, "knowledge_id")
		agent24UIAssertNoSecrets(t, w.Body.String(), secrets...)
		agent24UIAssertNoSecrets(t, agent24AuditBlob(t, rig.stub.entries[before]), secrets...)
		agent24UIAssertNoSecrets(t, agent24AuditBlob(t, rig.stub.entries[before]), "servable-alpha", "servable-bravo")
	})

	t.Run("list page", func(t *testing.T) {
		page2 := rig.authed(listPath + "?page=2&page_size=2")
		require.Equal(t, http.StatusOK, page2.Code, page2.Body.String())
		p2 := agent24UIJSONMap(t, page2.Body.Bytes())
		require.Equal(t, float64(3), p2["total"])
		require.Equal(t, float64(2), p2["page"])
		require.Equal(t, float64(2), p2["page_size"])
		got := p2["documents"].([]any)
		require.Len(t, got, 1)
		require.Equal(t, "doc-pending", got[0].(map[string]any)["knowledge_id"])

		past := rig.authed(listPath + "?page=3&page_size=2")
		require.Equal(t, http.StatusOK, past.Code, past.Body.String())
		pastBody := agent24UIJSONMap(t, past.Body.Bytes())
		require.Equal(t, float64(3), pastBody["total"])
		require.Empty(t, pastBody["documents"].([]any))

		capOK := rig.authed(listPath + "?page_size=100")
		require.Equal(t, http.StatusOK, capOK.Code, capOK.Body.String())
		require.Equal(t, float64(100), agent24UIJSONMap(t, capOK.Body.Bytes())["page_size"])
	})

	t.Run("get", func(t *testing.T) {
		rig.resetCalls()
		before := len(rig.stub.entries)
		w := rig.authed(getPath)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, 1, *rig.calls)
		agent24UIRequireNoStore(t, w)
		body := agent24UIJSONMap(t, w.Body.Bytes())
		agent24UIKeySet(t, body, "document")
		doc := body["document"].(map[string]any)
		agent24UIKeySet(t, doc,
			"knowledge_id", "knowledge_base_id", "title", "file_name", "parse_status", "revision", "chunk_count")
		require.Equal(t, "doc-completed", doc["knowledge_id"])
		require.Equal(t, "kb-a", doc["knowledge_base_id"])
		require.Equal(t, "Completed", doc["title"])
		require.Equal(t, "completed.pdf", doc["file_name"])
		require.Equal(t, types.ParseStatusCompleted, doc["parse_status"])
		require.Equal(t, float64(2), doc["chunk_count"])
		rev := doc["revision"].(map[string]any)
		agent24UIKeySet(t, rev, "source_revision", "index_build_id")
		require.NotEmpty(t, rev["source_revision"])
		require.NotEmpty(t, rev["index_build_id"])
		require.NotContains(t, w.Body.String(), "servable-alpha")
		op, outcome, _, resource := agent24UIReadOp(t, rig.stub, before+1)
		require.Equal(t, before+1, len(rig.stub.entries))
		require.Equal(t, "doc.get", op)
		require.Equal(t, "ok", outcome)
		require.Equal(t, "doc-completed", resource["knowledge_id"])
		require.NotContains(t, resource, "kb_id")
		agent24UIAssertNoSecrets(t, w.Body.String(), secrets...)
		agent24UIAssertNoSecrets(t, agent24AuditBlob(t, rig.stub.entries[before]), secrets...)
		agent24UIAssertNoSecrets(t, agent24AuditBlob(t, rig.stub.entries[before]), "servable-alpha")
	})

	t.Run("chunks", func(t *testing.T) {
		rig.resetCalls()
		before := len(rig.stub.entries)
		w := rig.authed(chunkPath)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, 1, *rig.calls)
		agent24UIRequireNoStore(t, w)
		require.False(t, bytes.Contains(w.Body.Bytes(), []byte{0xff}))
		body := agent24UIJSONMap(t, w.Body.Bytes())
		agent24UIKeySet(t, body,
			"knowledge_id", "knowledge_base_id", "revision", "chunks", "total", "page", "page_size")
		require.Equal(t, "doc-completed", body["knowledge_id"])
		require.Equal(t, "kb-a", body["knowledge_base_id"])
		require.Equal(t, float64(2), body["total"])
		require.Equal(t, float64(1), body["page"])
		require.Equal(t, float64(25), body["page_size"])
		rev := body["revision"].(map[string]any)
		agent24UIKeySet(t, rev, "source_revision", "index_build_id")
		require.NotEmpty(t, rev["index_build_id"])
		items := body["chunks"].([]any)
		require.Len(t, items, 2)
		wantID := []string{"chk-a", "chk-b"}
		wantText := []string{"servable-alpha", ""}
		wantRev := []float64{3, 7}
		for i, item := range items {
			ch := item.(map[string]any)
			agent24UIKeySet(t, ch, "chunk_id", "position", "chunk_revision", "content_tag", "text")
			require.Equal(t, wantID[i], ch["chunk_id"])
			require.Equal(t, float64(i), ch["position"])
			require.Equal(t, wantRev[i], ch["chunk_revision"])
			require.NotEmpty(t, ch["content_tag"])
			if i == 0 {
				require.Equal(t, wantText[0], ch["text"])
			} else {
				require.NotContains(t, ch["text"], "\xff")
				require.Contains(t, ch["text"].(string), "servable-bravo")
			}
		}
		for _, hidden := range []string{"chk-off", "chk-img", "chk-del", "chk-foreign", "chk-pending"} {
			require.NotContains(t, w.Body.String(), hidden)
		}
		op, outcome, _, resource := agent24UIReadOp(t, rig.stub, before+1)
		require.Equal(t, before+1, len(rig.stub.entries))
		require.Equal(t, "chunk.list", op)
		require.Equal(t, "ok", outcome)
		require.Equal(t, "doc-completed", resource["knowledge_id"])
		require.NotContains(t, resource, "kb_id")
		agent24UIAssertNoSecrets(t, w.Body.String(), secrets...)
		blob := agent24AuditBlob(t, rig.stub.entries[before])
		agent24UIAssertNoSecrets(t, blob, secrets...)
		agent24UIAssertNoSecrets(t, blob, "servable-alpha", "servable-bravo")
		require.Contains(t, w.Body.String(), "servable-alpha")
	})

	t.Run("chunks page", func(t *testing.T) {
		page2 := rig.authed(chunkPath + "?page=2&page_size=1")
		require.Equal(t, http.StatusOK, page2.Code, page2.Body.String())
		p2 := agent24UIJSONMap(t, page2.Body.Bytes())
		require.Equal(t, float64(2), p2["total"])
		require.Equal(t, float64(2), p2["page"])
		require.Equal(t, float64(1), p2["page_size"])
		got := p2["chunks"].([]any)
		require.Len(t, got, 1)
		require.Equal(t, "chk-b", got[0].(map[string]any)["chunk_id"])
		require.Equal(t, float64(1), got[0].(map[string]any)["position"])

		past := rig.authed(chunkPath + "?page=3&page_size=1")
		require.Equal(t, http.StatusOK, past.Code, past.Body.String())
		pastBody := agent24UIJSONMap(t, past.Body.Bytes())
		require.Equal(t, float64(2), pastBody["total"])
		require.Empty(t, pastBody["chunks"].([]any))

		capOK := rig.authed(chunkPath + "?page_size=100")
		require.Equal(t, http.StatusOK, capOK.Code, capOK.Body.String())
		require.Equal(t, float64(100), agent24UIJSONMap(t, capOK.Body.Bytes())["page_size"])
	})

	t.Run("pending", func(t *testing.T) {
		w := rig.authed("/api/v1/agent24/knowledge/ui/documents/doc-pending")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		agent24UIRequireNoStore(t, w)
		doc := agent24UIJSONMap(t, w.Body.Bytes())["document"].(map[string]any)
		require.Equal(t, types.ParseStatusPending, doc["parse_status"])
		require.Equal(t, float64(0), doc["chunk_count"])
		rev := doc["revision"].(map[string]any)
		require.NotEmpty(t, rev["source_revision"])
		require.Contains(t, rev, "index_build_id")
		require.Nil(t, rev["index_build_id"])
		require.NotContains(t, w.Body.String(), "pending-chunk-text")

		cw := rig.authed("/api/v1/agent24/knowledge/ui/documents/doc-pending/chunks")
		require.Equal(t, http.StatusOK, cw.Code, cw.Body.String())
		agent24UIRequireNoStore(t, cw)
		body := agent24UIJSONMap(t, cw.Body.Bytes())
		require.Equal(t, float64(0), body["total"])
		require.Empty(t, body["chunks"].([]any))
		crev := body["revision"].(map[string]any)
		require.Equal(t, rev["source_revision"], crev["source_revision"])
		require.Nil(t, crev["index_build_id"])
		require.NotContains(t, cw.Body.String(), "pending-chunk-text")
		agent24UIAssertNoSecrets(t, w.Body.String(), secrets...)
		agent24UIAssertNoSecrets(t, cw.Body.String(), secrets...)
	})

	t.Run("invisible", func(t *testing.T) {
		missing := rig.authed("/api/v1/agent24/knowledge/ui/documents/missing-doc")
		require.Equal(t, http.StatusNotFound, missing.Code, missing.Body.String())
		agent24UIRequireNoStore(t, missing)
		agent24UIRequireNullEcho(t, missing.Body.Bytes())
		for _, id := range append(append([]string{}, hiddenDocs...), "missing-doc") {
			for _, path := range []string{
				"/api/v1/agent24/knowledge/ui/documents/" + id,
				"/api/v1/agent24/knowledge/ui/documents/" + id + "/chunks",
			} {
				rig.resetCalls()
				before := len(rig.stub.entries)
				w := rig.authed(path)
				require.Equal(t, http.StatusNotFound, w.Code, path+" "+w.Body.String())
				require.Equal(t, 1, *rig.calls, path)
				require.Equal(t, missing.Body.Bytes(), w.Body.Bytes(), path)
				op, outcome, _, resource := agent24UIReadOp(t, rig.stub, before+1)
				if path[len(path)-1] == 's' && len(path) > len("/chunks") && path[len(path)-6:] == "chunks" {
					require.Equal(t, "chunk.list", op)
				} else {
					require.Equal(t, "doc.get", op)
				}
				require.Equal(t, "not_found", outcome)
				require.Equal(t, id, resource["knowledge_id"])
				agent24UIAssertNoSecrets(t, w.Body.String(), secrets...)
				agent24UIAssertNoSecrets(t, agent24AuditBlob(t, rig.stub.entries[before]), secrets...)
			}
		}
		for _, id := range []string{"kb-local-out", "kb-other", "kb-gone", "missing-kb"} {
			rig.resetCalls()
			before := len(rig.stub.entries)
			w := rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/" + id + "/documents")
			require.Equal(t, http.StatusNotFound, w.Code, id)
			require.Equal(t, 1, *rig.calls, id)
			require.Equal(t, missing.Body.Bytes(), w.Body.Bytes(), id)
			op, outcome, _, resource := agent24UIReadOp(t, rig.stub, before+1)
			require.Equal(t, "doc.list", op)
			require.Equal(t, "not_found", outcome)
			require.Equal(t, id, resource["kb_id"])
			agent24UIAssertNoSecrets(t, agent24AuditBlob(t, rig.stub.entries[before]), secrets...)
		}
	})

	t.Run("agrees with read and search", func(t *testing.T) {
		w := rig.authed(chunkPath)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		body := agent24UIJSONMap(t, w.Body.Bytes())
		byID := map[string]map[string]any{}
		for _, item := range body["chunks"].([]any) {
			ch := item.(map[string]any)
			byID[ch["chunk_id"].(string)] = ch
		}
		snap, err := (agent24DBReadReader{db: rig.h.db}).Snapshot(context.Background(), 9, []string{"kb-a", "kb-b"},
			"doc-completed", agent24ReadWindow{Position: 0, Limit: 100})
		require.NoError(t, err)
		require.True(t, snap.Found)
		require.Len(t, snap.Chunks, 2)
		uiRev := body["revision"].(map[string]any)
		var cands []agent24SearchCandidate
		hitSnap := agent24HitSnapshot{
			Chunks: map[string]agent24HitChunk{},
			Docs: map[string]agent24HitDoc{
				"doc-completed": {
					ID: "doc-completed", KnowledgeBaseID: snap.KnowledgeBaseID,
					Title: snap.Title, FileName: snap.FileName, Fingerprint: snap.Fingerprint,
				},
			},
		}
		for _, ch := range snap.Chunks {
			require.True(t, ch.Loaded, ch.ID)
			page, kerr := agent24JudgeRead(agent24TestKey, agent24ReadRequest{
				KnowledgeID: "doc-completed", ChunkID: ch.ID,
				PageLimit: 1, MaxBytes: agent24KnowledgeMaxReadBytes,
			}, snap)
			require.Nil(t, kerr, ch.ID)
			require.Len(t, page.Segments, 1)
			got := byID[ch.ID]
			require.Equal(t, page.Segments[0].ContentTag, got["content_tag"], ch.ID)
			require.Equal(t, float64(page.Segments[0].ChunkRevision), got["chunk_revision"], ch.ID)
			require.Equal(t, page.Segments[0].Text, got["text"], ch.ID)
			require.Equal(t, page.Revision.SourceRevision, uiRev["source_revision"])
			require.Equal(t, page.Revision.IndexBuildID, uiRev["index_build_id"])
			hitSnap.Chunks[ch.ID] = agent24HitChunk{
				ID: ch.ID, KnowledgeID: "doc-completed", KnowledgeBaseID: snap.KnowledgeBaseID,
				Content: ch.Content, ContentRevision: ch.ContentRevision,
			}
			hitSnap.Docs["doc-completed"] = agent24HitDoc{
				ID: "doc-completed", KnowledgeBaseID: snap.KnowledgeBaseID,
				Title: snap.Title, FileName: snap.FileName, Fingerprint: snap.Fingerprint,
				ServableChunkIDs: append(hitSnap.Docs["doc-completed"].ServableChunkIDs, ch.ID),
			}
			cands = append(cands, agent24SearchCandidate{ChunkID: ch.ID, Score: 1})
		}
		hits := agent24AssembleHits(agent24TestKey, "cosine", cands, hitSnap, 10)
		require.Len(t, hits, 2)
		for _, hit := range hits {
			require.Equal(t, hit.Revision.ContentTag, byID[hit.ChunkID]["content_tag"], hit.ChunkID)
		}

		pendingSnap, err := (agent24DBReadReader{db: rig.h.db}).Snapshot(context.Background(), 9,
			[]string{"kb-a"}, "doc-pending", agent24ReadWindow{Position: -1})
		require.NoError(t, err)
		_, kerr := agent24JudgeRead(agent24TestKey, agent24ReadRequest{
			KnowledgeID: "doc-pending", ChunkID: "chk-pending",
			PageLimit: 1, MaxBytes: agent24KnowledgeMaxReadBytes,
		}, pendingSnap)
		require.NotNil(t, kerr)
		current, ok := kerr.Extra["current_revision"].(map[string]any)
		require.True(t, ok)
		pw := rig.authed("/api/v1/agent24/knowledge/ui/documents/doc-pending")
		prev := agent24UIJSONMap(t, pw.Body.Bytes())["document"].(map[string]any)["revision"].(map[string]any)
		require.Equal(t, prev["source_revision"], current["source_revision"])
		build, ok := current["index_build_id"].(*string)
		require.True(t, ok)
		require.Nil(t, build)
	})

	t.Run("query", func(t *testing.T) {
		for _, path := range []string{listPath, chunkPath} {
			for _, q := range []string{
				"?page_size=101", "?page=0", "?page=01", "?page=1&page=2", "?page_size=",
				"?page=" + strconv.FormatInt(int64(math.MaxInt), 10) + "&page_size=2",
			} {
				rig.resetCalls()
				w := rig.authed(path + q)
				require.Equal(t, http.StatusBadRequest, w.Code, path+q+" "+w.Body.String())
				require.Equal(t, 0, *rig.calls, path+q)
				errObj := agent24UIJSONMap(t, w.Body.Bytes())["error"].(map[string]any)
				require.Equal(t, "invalid_request", errObj["code"])
				agent24UIRequireNullEcho(t, w.Body.Bytes())
				agent24UIRequireNoStore(t, w)
				agent24UIAssertNoSecrets(t, w.Body.String(), rig.token, rig.proof)
			}
		}
		plain := rig.authed(listPath)
		noisy := rig.authed(listPath + "?agent_id=" + agent24UISentinelAgent)
		require.Equal(t, http.StatusOK, noisy.Code, noisy.Body.String())
		require.Equal(t, plain.Body.Bytes(), noisy.Body.Bytes())
		agent24UIAssertNoSecrets(t, noisy.Body.String(), agent24UISentinelAgent)

		getPlain := rig.authed(getPath)
		getNoisy := rig.authed(getPath + "?page=0&page_size=101&agent_id=" + agent24UISentinelAgent)
		require.Equal(t, http.StatusOK, getNoisy.Code, getNoisy.Body.String())
		require.Equal(t, getPlain.Body.Bytes(), getNoisy.Body.Bytes())
	})
}

func TestAgent24UIReadDocumentTagKey(t *testing.T) {
	rig, _ := agent24UIReadFixture(t, nil, nil)
	counter := &agent24UIScopeCalls{inner: rig.h.scope}
	rig.h.scope = counter
	rig.h.key = nil
	chunks := "/api/v1/agent24/knowledge/ui/documents/doc-completed/chunks"
	doc := "/api/v1/agent24/knowledge/ui/documents/doc-completed"

	rig.resetCalls()
	w := rig.authed(chunks + "?page=0")
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Equal(t, 0, counter.calls)
	require.Equal(t, 0, *rig.calls)
	require.Equal(t, "invalid_request", agent24UIJSONMap(t, w.Body.Bytes())["error"].(map[string]any)["code"])

	for _, path := range []string{chunks, doc} {
		rig.resetCalls()
		w = rig.authed(path)
		require.Equal(t, http.StatusForbidden, w.Code, path+" "+w.Body.String())
		require.Equal(t, 0, counter.calls, path)
		require.Equal(t, 0, *rig.calls, path)
		details := agent24UIJSONMap(t, w.Body.Bytes())["error"].(map[string]any)["details"].(map[string]any)
		require.Equal(t, "operation_disabled", details["reason"])
	}

	rig.resetCalls()
	w = rig.authed("/api/v1/agent24/knowledge/ui/knowledge-bases/kb-a/documents")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, counter.calls)
	require.Equal(t, 1, *rig.calls)
}
