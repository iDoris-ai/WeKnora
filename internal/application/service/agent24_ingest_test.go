// Agent24 fork product patch A24-KB-41 (WeKnora#136).
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

type agent24IngestExec struct{ err error }

func (e agent24IngestExec) Available(context.Context) error { return e.err }

type agent24AttemptTracker struct {
	noopSpanTracker
	next int
}

func (t *agent24AttemptTracker) OpenAttempt(context.Context, string, string) (*Span, int, error) {
	t.next++
	return &Span{}, t.next, nil
}

type agent24IngestHarness struct {
	db      *gorm.DB
	jobs    *repository.Agent24IngestJobStore
	ingest  *Agent24Ingest
	store   *replaceFileStore
	tasks   *replaceFileEnqueuer
	events  []string
	ctx     context.Context
	tracker *agent24AttemptTracker
}

func newAgent24IngestHarness(t *testing.T, execErr error) *agent24IngestHarness {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Migrator().CreateTable(&types.Knowledge{}))
	require.NoError(t, repository.EnsureAgent24IngestJobTable(db))
	h := &agent24IngestHarness{db: db, jobs: repository.NewAgent24IngestJobStore(db), tracker: &agent24AttemptTracker{}}
	h.store = &replaceFileStore{events: &h.events}
	h.tasks = &replaceFileEnqueuer{events: &h.events}
	kb := &types.KnowledgeBase{ID: "kb-1", TenantID: 7, EmbeddingModelID: "emb-1"}
	svc := &knowledgeService{
		repo:          repository.NewKnowledgeRepository(db),
		kbService:     &reparseFailureKBService{kb: kb},
		fileSvc:       h.store,
		task:          h.tasks,
		taskInspector: &replaceFileInspector{events: &h.events},
		spanTracker:   h.tracker,
	}
	h.ingest, err = NewAgent24Ingest(svc, h.jobs, agent24IngestExec{err: execErr})
	require.NoError(t, err)
	h.ingest.now = func() time.Time { return time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC) }
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	h.ctx = context.WithValue(ctx, types.TenantInfoContextKey, &types.Tenant{ID: 7})
	return h
}

func agent24Sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

var agent24TestPrincipal = Agent24IngestPrincipal{
	OpClass: types.Agent24JobOpIngestSubmit, UserID: "u-1", CredentialID: 45, PolicyVersion: "demo-2",
}

func agent24Req(name, content, sha string) Agent24IngestRequest {
	return Agent24IngestRequest{KnowledgeBaseID: "kb-1", FileName: name, Content: []byte(content), ContentSHA256: sha}
}

func agent24Revision(k *types.Knowledge) string { return "rev:" + k.FileHash }

// submit runs the three steps the way a caller does: Prepare, Stage inside a transaction, Dispatch after commit.
func (h *agent24IngestHarness) submit(
	t *testing.T, req Agent24IngestRequest,
) (*types.Agent24IngestJob, *types.Knowledge, error) {
	t.Helper()
	p, err := h.ingest.Prepare(h.ctx, req)
	if err != nil {
		return nil, nil, err
	}
	var job *types.Agent24IngestJob
	var k *types.Knowledge
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		job, k, err = h.ingest.Stage(h.ctx, tx, p, agent24TestPrincipal, agent24Revision)
		return err
	}); err != nil {
		h.ingest.Abandon(h.ctx, p)
		return nil, nil, err
	}
	return job, k, h.ingest.Dispatch(h.ctx, job, k, p)
}

func TestAgent24IngestCreatesDocumentAndSavedJob(t *testing.T) {
	h := newAgent24IngestHarness(t, nil)
	job, k, err := h.submit(t, Agent24IngestRequest{
		KnowledgeBaseID: "kb-1", FileName: "entry.md", Content: []byte("# hello"),
		ContentSHA256: agent24Sha("# hello"), Origin: Agent24OriginEntry,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"save", "enqueue"}, h.events)
	require.Equal(t, types.ParseStatusPending, k.ParseStatus)
	require.Equal(t, "new/file.md", k.FilePath)
	require.JSONEq(t, `{"agent24_origin":"entry"}`, string(k.Metadata))

	got, err := h.jobs.Get(h.ctx, 7, job.ID)
	require.NoError(t, err)
	require.Equal(t, types.Agent24JobSaved, got.Status)
	require.Equal(t, k.ID, got.KnowledgeID)
	require.Equal(t, types.Agent24JobOpIngestSubmit, got.OpClass)
	require.Equal(t, uint64(45), got.CredentialID)
	require.Equal(t, "demo-2", got.PolicyVersion)
	require.Equal(t, "rev:"+k.FileHash, got.SourceRevision)
	require.Equal(t, agent24Sha("# hello"), got.ContentSHA256)
	require.Equal(t, 1, got.Attempt)
	require.Len(t, got.Cost.Items, 2)
	require.Equal(t, int64(7), *got.Cost.Items[0].Units)
	require.Nil(t, got.Cost.Items[1].Units, "embedding is not metered upstream: unknown, never 0")
	require.Equal(t, "unknown", got.Cost.Items[1].Measured)
	require.False(t, got.Cost.Final)

	require.Len(t, h.tasks.payloads, 1)
	pl := h.tasks.payloads[0]
	require.Equal(t, k.ID, pl.KnowledgeID)
	require.Equal(t, 1, pl.Attempt)
	require.False(t, pl.EnableQuestionGeneration)
	require.False(t, pl.EnableMultimodel)
}

func TestAgent24IngestPrepareRefusesWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		name    string
		execErr error
		req     Agent24IngestRequest
		want    error
	}{
		{
			"no worker", errors.New("no live server"),
			agent24Req("a.md", "x", agent24Sha("x")),
			ErrAgent24IngestUnavailable,
		},
		{
			"hash mismatch", nil,
			agent24Req("a.md", "x", agent24Sha("y")),
			ErrAgent24ContentHashMismatch,
		},
		{
			"empty file name", nil,
			Agent24IngestRequest{KnowledgeBaseID: "kb-1", Content: []byte("x"), ContentSHA256: agent24Sha("x")},
			ErrAgent24InvalidFileName,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newAgent24IngestHarness(t, tc.execErr)
			_, _, err := h.submit(t, tc.req)
			require.ErrorIs(t, err, tc.want)
			require.Empty(t, h.events, "nothing saved, nothing enqueued")
			var n int64
			require.NoError(t, h.db.Model(&types.Knowledge{}).Count(&n).Error)
			require.Zero(t, n)
			require.NoError(t, h.db.Model(&types.Agent24IngestJob{}).Count(&n).Error)
			require.Zero(t, n)
		})
	}
}

func TestAgent24IngestRolledBackStageLeavesNothing(t *testing.T) {
	h := newAgent24IngestHarness(t, nil)
	p, err := h.ingest.Prepare(h.ctx, Agent24IngestRequest{
		KnowledgeBaseID: "kb-1", FileName: "a.md", Content: []byte("x"), ContentSHA256: agent24Sha("x"),
	})
	require.NoError(t, err)
	errCaller := errors.New("caller's candidate write failed")
	err = h.db.Transaction(func(tx *gorm.DB) error {
		_, _, serr := h.ingest.Stage(h.ctx, tx, p, agent24TestPrincipal, agent24Revision)
		require.NoError(t, serr)
		return errCaller
	})
	require.ErrorIs(t, err, errCaller)
	h.ingest.Abandon(h.ctx, p)
	require.Equal(t, []string{"save", "delete:new/file.md"}, h.events)
	var n int64
	require.NoError(t, h.db.Model(&types.Knowledge{}).Count(&n).Error)
	require.Zero(t, n)
	require.NoError(t, h.db.Model(&types.Agent24IngestJob{}).Count(&n).Error)
	require.Zero(t, n)
}

func TestAgent24IngestRevisionSupersedesTheEarlierJob(t *testing.T) {
	h := newAgent24IngestHarness(t, nil)
	first, k, err := h.submit(t, Agent24IngestRequest{
		KnowledgeBaseID: "kb-1", FileName: "a.md", Content: []byte("v1"), ContentSHA256: agent24Sha("v1"),
	})
	require.NoError(t, err)
	h.events = nil
	second, k2, err := h.submit(t, Agent24IngestRequest{
		KnowledgeBaseID: "kb-1", KnowledgeID: k.ID, FileName: "a.md", Content: []byte("v2"),
		ContentSHA256: agent24Sha("v2"),
	})
	require.NoError(t, err)
	require.Equal(t, k.ID, k2.ID, "same document, new source revision")
	require.NotEqual(t, k.FileHash, k2.FileHash)
	require.Equal(t, types.ParseStatusPending, k2.ParseStatus)
	require.Equal(t, []string{"save", "dequeue:" + k.ID, "enqueue"}, h.events, "old tasks dequeued before the new one")

	old, err := h.jobs.Get(h.ctx, 7, first.ID)
	require.NoError(t, err)
	require.Equal(t, types.Agent24JobCancelled, old.Status)
	require.Equal(t, types.Agent24JobCancelSuperseded, old.CancelReason)
	cur, err := h.jobs.Occupying(h.ctx, 7, k.ID)
	require.NoError(t, err)
	require.Equal(t, second.ID, cur.ID)
	require.Equal(t, 2, cur.Attempt)
	require.Equal(t, 2, h.tasks.payloads[1].Attempt)
}

func TestAgent24IngestRevisionOfAnUnknownOrDeletingDocument(t *testing.T) {
	h := newAgent24IngestHarness(t, nil)
	_, _, err := h.submit(t, Agent24IngestRequest{
		KnowledgeBaseID: "kb-1", KnowledgeID: "missing", FileName: "a.md", Content: []byte("x"),
		ContentSHA256: agent24Sha("x"),
	})
	require.ErrorIs(t, err, ErrAgent24KnowledgeNotFound)
	require.Equal(t, []string{"save", "delete:new/file.md"}, h.events, "the saved original is abandoned")

	h.events = nil
	_, k, err := h.submit(t, Agent24IngestRequest{
		KnowledgeBaseID: "kb-1", FileName: "a.md", Content: []byte("x"), ContentSHA256: agent24Sha("x"),
	})
	require.NoError(t, err)
	require.NoError(t, h.db.Model(&types.Knowledge{}).Where("id = ?", k.ID).
		Update("parse_status", types.ParseStatusDeleting).Error)
	_, _, err = h.submit(t, Agent24IngestRequest{
		KnowledgeBaseID: "kb-1", KnowledgeID: k.ID, FileName: "a.md", Content: []byte("y"),
		ContentSHA256: agent24Sha("y"),
	})
	require.ErrorIs(t, err, ErrAgent24KnowledgeNotFound)
}

func TestAgent24IngestEnqueueFailureFailsTheJob(t *testing.T) {
	h := newAgent24IngestHarness(t, nil)
	h.tasks.err = errors.New("redis down")
	job, k, err := h.submit(t, Agent24IngestRequest{
		KnowledgeBaseID: "kb-1", FileName: "a.md", Content: []byte("x"), ContentSHA256: agent24Sha("x"),
	})
	require.Error(t, err)
	got, gerr := h.jobs.Get(h.ctx, 7, job.ID)
	require.NoError(t, gerr)
	require.Equal(t, types.Agent24JobFailed, got.Status)
	require.Equal(t, types.Agent24JobFailEnqueue, got.FailureReason)
	require.NotNil(t, got.TerminalAt)
	var row types.Knowledge
	require.NoError(t, h.db.Where("id = ?", k.ID).Take(&row).Error)
	require.Equal(t, types.ParseStatusFailed, row.ParseStatus)
}
