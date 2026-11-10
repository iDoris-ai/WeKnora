// Agent24 fork product patch A24-KB-42 (WeKnora#146 S1).
package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

func agent24CandidateDB(t *testing.T) (*gorm.DB, *Agent24CandidateStore) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, EnsureAgent24IngestJobTable(db))
	require.NoError(t, EnsureAgent24CandidateTables(db))
	return db, NewAgent24CandidateStore(db)
}

var agent24T0 = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func agent24TestCandidate(id, author string) *types.Agent24KnowledgeCandidate {
	return &types.Agent24KnowledgeCandidate{
		ID: id, TenantID: 1, Author: author, TargetKnowledgeBaseID: "kb1", Mode: types.Agent24CandidateCreate,
		ContentSHA256: "h-" + id, Kind: types.Agent24EntryKindSOP, Title: "t", Body: "b",
		SessionRef: "s", MessageFrom: 0, MessageTo: 3, SkillVersion: "v1", RedactionRulesVersion: 1,
		Status: types.Agent24CandidateSubmitted, SubmittedAt: agent24T0,
	}
}

func TestEnsureAgent24CandidateTablesIsIdempotentAndIndexed(t *testing.T) {
	db, _ := agent24CandidateDB(t)
	require.NoError(t, EnsureAgent24CandidateTables(db))
	for model, names := range agent24CandidateIndexes {
		for _, name := range names {
			require.True(t, db.Migrator().HasIndex(model, name), name)
		}
	}
	// a table left without an index gets it back
	const dedup = "idx_agent24_candidates_submitted_dedup"
	require.NoError(t, db.Migrator().DropIndex(&types.Agent24KnowledgeCandidate{}, dedup))
	require.NoError(t, EnsureAgent24CandidateTables(db))
	require.True(t, db.Migrator().HasIndex(&types.Agent24KnowledgeCandidate{}, dedup))
}

// SQLite does not enforce a varchar length, so the widening is a PostgreSQL concern
// (TestAgent24CandidateTablesPostgres); here Ensure only has to leave an existing job table working.
func TestEnsureAgent24CandidateTablesKeepsAnExistingJobTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, EnsureAgent24IngestJobTable(db))
	require.NoError(t, EnsureAgent24CandidateTables(db))
	require.NoError(t, EnsureAgent24CandidateTables(db))
	job := agent24TestJob("j1", "k", types.Agent24JobSaved, agent24T0)
	job.PrincipalUserID = "api_external_user:1:" + fmt.Sprintf("%0128d", 7)
	require.NoError(t, db.Create(job).Error)
	// no job table yet is not an error either
	fresh, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, EnsureAgent24CandidateTables(fresh))
}

func TestAgent24CandidateCreateGetAndDedup(t *testing.T) {
	_, s := agent24CandidateDB(t)
	ctx := context.Background()
	// only a submitted candidate can be created
	bad := agent24TestCandidate("c0", "api_external_user:1:a")
	bad.Status = types.Agent24CandidateApproved
	require.Error(t, s.CreateCandidate(ctx, bad))
	none, err := s.GetCandidate(ctx, 1, "c0")
	require.NoError(t, err)
	require.Nil(t, none)

	c := agent24TestCandidate("c1", "api_external_user:1:a")
	require.NoError(t, s.CreateCandidate(ctx, c))
	got, err := s.GetCandidate(ctx, 1, "c1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "h-c1", got.ContentSHA256)
	// another tenant does not see it
	got, err = s.GetCandidate(ctx, 2, "c1")
	require.NoError(t, err)
	require.Nil(t, got)

	// the same dedup key while c1 is submitted: refused by the index, found by FindSubmittedDuplicate
	dup := agent24TestCandidate("c2", "api_external_user:1:a")
	dup.ContentSHA256 = "h-c1"
	require.ErrorIs(t, s.CreateCandidate(ctx, dup), ErrAgent24CandidateDuplicate)
	found, err := s.FindSubmittedDuplicate(ctx, dup)
	require.NoError(t, err)
	require.NotNil(t, found)
	require.Equal(t, "c1", found.ID)
	// A24-KB-43 (PR-Daemon on #156): a taken id with other content is not a duplicate
	clash := agent24TestCandidate("c1", "api_external_user:1:a")
	clash.ContentSHA256 = "h-other"
	require.ErrorIs(t, s.CreateCandidate(ctx, clash), ErrAgent24CandidateIDTaken)
	// IsWithdrawn on an empty id answers false without a query
	withdrawn, err := s.IsWithdrawn(ctx, 1, "")
	require.NoError(t, err)
	require.False(t, withdrawn)

	// any one field of the key differing is a different candidate
	for _, mutate := range []func(*types.Agent24KnowledgeCandidate){
		func(x *types.Agent24KnowledgeCandidate) { x.Author = "api_external_user:1:b" },
		func(x *types.Agent24KnowledgeCandidate) { x.TargetKnowledgeBaseID = "kb2" },
		func(x *types.Agent24KnowledgeCandidate) {
			x.Mode, x.KnowledgeID, x.BaseRevision = types.Agent24CandidateRevise, "k1", "r1"
		},
		func(x *types.Agent24KnowledgeCandidate) { x.ContentSHA256 = "h-other" },
	} {
		other := agent24TestCandidate("", "api_external_user:1:a")
		other.ContentSHA256 = "h-c1"
		mutate(other)
		f, err := s.FindSubmittedDuplicate(ctx, other)
		require.NoError(t, err)
		require.Nil(t, f)
	}

	// once c1 left submitted, the same key can be submitted again
	ok, err := s.TransitionCandidate(ctx, 1, "c1",
		types.Agent24CandidateTransition{To: types.Agent24CandidateWithdrawn}, agent24T0)
	require.NoError(t, err)
	require.True(t, ok)
	f, err := s.FindSubmittedDuplicate(ctx, dup)
	require.NoError(t, err)
	require.Nil(t, f)
	require.NoError(t, s.CreateCandidate(ctx, dup))
}

func TestAgent24CandidateTransitionIsCompareAndSet(t *testing.T) {
	_, s := agent24CandidateDB(t)
	ctx := context.Background()
	require.NoError(t, s.CreateCandidate(ctx, agent24TestCandidate("c1", "api_external_user:1:a")))
	approve := types.Agent24CandidateTransition{
		To: types.Agent24CandidateApproved, Approver: "api_external_user:1:a", AuthorIsApprover: true,
		KnowledgeID: "k9", SourceRevision: "r9", JobID: "j9",
		ReturnReason: "ignored", Note: "ignored",
	}
	t1 := agent24T0.Add(time.Minute)
	ok, err := s.TransitionCandidate(ctx, 1, "c1", approve, t1)
	require.NoError(t, err)
	require.True(t, ok)
	got, err := s.GetCandidate(ctx, 1, "c1")
	require.NoError(t, err)
	require.Equal(t, types.Agent24CandidateApproved, got.Status)
	require.Equal(t, "k9", got.KnowledgeID)
	require.Equal(t, "r9", got.SourceRevision)
	require.Equal(t, "j9", got.JobID)
	require.True(t, got.AuthorIsApprover)
	require.Equal(t, "api_external_user:1:a", got.Approver)
	require.Empty(t, got.ReturnReason)
	require.Empty(t, got.Note)
	require.NotNil(t, got.TerminalAt)
	require.True(t, got.TerminalAt.Equal(t1))

	// a second transition out of approved does nothing
	ok, err = s.TransitionCandidate(ctx, 1, "c1",
		types.Agent24CandidateTransition{To: types.Agent24CandidateReturned, ReturnReason: types.Agent24ReturnReviewer},
		t1.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, ok)
	got, err = s.GetCandidate(ctx, 1, "c1")
	require.NoError(t, err)
	require.Equal(t, types.Agent24CandidateApproved, got.Status)

	// returned carries reason, note and the reviewer in approver; job_id stays empty
	require.NoError(t, s.CreateCandidate(ctx, agent24TestCandidate("c2", "api_external_user:1:a")))
	ok, err = s.TransitionCandidate(ctx, 1, "c2", types.Agent24CandidateTransition{
		To: types.Agent24CandidateReturned, ReturnReason: types.Agent24ReturnReviewer, Note: "see note",
		Approver: "ignored", JobID: "ignored",
	}, t1)
	require.NoError(t, err)
	require.True(t, ok)
	got, err = s.GetCandidate(ctx, 1, "c2")
	require.NoError(t, err)
	require.Equal(t, types.Agent24ReturnReviewer, got.ReturnReason)
	require.Equal(t, "see note", got.Note)
	require.Equal(t, "ignored", got.Approver)
	require.Empty(t, got.JobID)

	// a non-terminal target, an unknown id and another tenant all write nothing
	require.NoError(t, s.CreateCandidate(ctx, agent24TestCandidate("c3", "api_external_user:1:a")))
	_, err = s.TransitionCandidate(ctx, 1, "c3",
		types.Agent24CandidateTransition{To: types.Agent24CandidateSubmitted}, t1)
	require.Error(t, err)
	ok, err = s.TransitionCandidate(ctx, 1, "nope",
		types.Agent24CandidateTransition{To: types.Agent24CandidateWithdrawn}, t1)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = s.TransitionCandidate(ctx, 2, "c3",
		types.Agent24CandidateTransition{To: types.Agent24CandidateWithdrawn}, t1)
	require.NoError(t, err)
	require.False(t, ok)
	got, err = s.GetCandidate(ctx, 1, "c3")
	require.NoError(t, err)
	require.Equal(t, types.Agent24CandidateSubmitted, got.Status)
}

func TestAgent24CandidateCountAndLists(t *testing.T) {
	_, s := agent24CandidateDB(t)
	ctx := context.Background()
	a := "api_external_user:1:a"
	for i := 0; i < 5; i++ {
		c := agent24TestCandidate(fmt.Sprintf("c%d", i), a)
		c.SubmittedAt = agent24T0.Add(time.Duration(i) * time.Minute)
		if i%2 == 1 {
			c.TargetKnowledgeBaseID = "kb2"
		}
		require.NoError(t, s.CreateCandidate(ctx, c))
	}
	other := agent24TestCandidate("x", "api_external_user:1:b")
	other.SubmittedAt = agent24T0.Add(10 * time.Minute)
	require.NoError(t, s.CreateCandidate(ctx, other))
	ok, err := s.TransitionCandidate(ctx, 1, "c4",
		types.Agent24CandidateTransition{To: types.Agent24CandidateWithdrawn}, agent24T0)
	require.NoError(t, err)
	require.True(t, ok)

	n, err := s.CountSubmitted(ctx, 1, a)
	require.NoError(t, err)
	require.Equal(t, int64(4), n)
	n, err = s.CountSubmitted(ctx, 2, a)
	require.NoError(t, err)
	require.Zero(t, n)

	// review list: only submitted, only listed knowledge bases, oldest first, paged
	page, err := s.ListSubmittedForReview(ctx, 1, []string{"kb1"}, "", 2)
	require.NoError(t, err)
	require.Len(t, page.Candidates, 2)
	require.Equal(t, "c0", page.Candidates[0].ID)
	require.Equal(t, "c2", page.Candidates[1].ID)
	require.NotEmpty(t, page.Next)
	page, err = s.ListSubmittedForReview(ctx, 1, []string{"kb1"}, page.Next, 2)
	require.NoError(t, err)
	require.Len(t, page.Candidates, 1)
	require.Equal(t, "x", page.Candidates[0].ID)
	require.Empty(t, page.Next)
	_, err = s.ListSubmittedForReview(ctx, 1, []string{"kb1"}, "not-a-cursor!", 2)
	require.Error(t, err)
	page, err = s.ListSubmittedForReview(ctx, 1, nil, "", 10)
	require.NoError(t, err)
	require.Empty(t, page.Candidates)
	page, err = s.ListSubmittedForReview(ctx, 2, []string{"kb1", "kb2"}, "", 10)
	require.NoError(t, err)
	require.Empty(t, page.Candidates)

	list, err := s.ListSubmittedForKnowledgeBase(ctx, 1, "kb2")
	require.NoError(t, err)
	ids := []string{}
	for _, c := range list {
		ids = append(ids, c.ID)
	}
	require.ElementsMatch(t, []string{"c1", "c3"}, ids)
}

func TestAgent24CandidateIsEntryDocument(t *testing.T) {
	_, s := agent24CandidateDB(t)
	ctx := context.Background()
	require.NoError(t, s.CreateCandidate(ctx, agent24TestCandidate("c1", "api_external_user:1:a")))
	is, err := s.IsEntryDocument(ctx, 1, "k1")
	require.NoError(t, err)
	require.False(t, is)
	ok, err := s.TransitionCandidate(ctx, 1, "c1",
		types.Agent24CandidateTransition{To: types.Agent24CandidateApproved, Approver: "p", KnowledgeID: "k1"},
		agent24T0)
	require.NoError(t, err)
	require.True(t, ok)
	is, err = s.IsEntryDocument(ctx, 1, "k1")
	require.NoError(t, err)
	require.True(t, is)
	is, err = s.IsEntryDocument(ctx, 2, "k1")
	require.NoError(t, err)
	require.False(t, is)
	// a returned revise candidate naming a document does not make it an entry
	rev := agent24TestCandidate("c2", "api_external_user:1:a")
	rev.Mode, rev.KnowledgeID, rev.BaseRevision = types.Agent24CandidateRevise, "k2", "r"
	require.NoError(t, s.CreateCandidate(ctx, rev))
	ok, err = s.TransitionCandidate(ctx, 1, "c2",
		types.Agent24CandidateTransition{
			To: types.Agent24CandidateReturned, ReturnReason: types.Agent24ReturnConflicted,
		}, agent24T0)
	require.NoError(t, err)
	require.True(t, ok)
	is, err = s.IsEntryDocument(ctx, 1, "k2")
	require.NoError(t, err)
	require.False(t, is)
	is, err = s.IsEntryDocument(ctx, 1, "")
	require.NoError(t, err)
	require.False(t, is)
}

func TestAgent24CandidateRetention(t *testing.T) {
	_, s := agent24CandidateDB(t)
	ctx := context.Background()
	mk := func(id string, to types.Agent24CandidateStatus, at time.Time) {
		require.NoError(t, s.CreateCandidate(ctx, agent24TestCandidate(id, "api_external_user:1:a")))
		if to == types.Agent24CandidateSubmitted {
			return
		}
		ok, err := s.TransitionCandidate(ctx, 1, id, types.Agent24CandidateTransition{
			To: to, ReturnReason: types.Agent24ReturnReviewer, Note: "n", Approver: "p", KnowledgeID: "k-" + id,
		}, at)
		require.NoError(t, err)
		require.True(t, ok)
	}
	mk("ret-old", types.Agent24CandidateReturned, agent24T0)
	mk("wd-old", types.Agent24CandidateWithdrawn, agent24T0)
	mk("ap-old", types.Agent24CandidateApproved, agent24T0)
	mk("ret-new", types.Agent24CandidateReturned, agent24T0.Add(2*time.Hour))
	mk("sub", types.Agent24CandidateSubmitted, agent24T0)

	now := agent24T0.Add(90 * time.Minute)
	n, err := s.PurgeCandidateContent(ctx, now.Add(-time.Hour), now)
	require.NoError(t, err)
	require.Equal(t, int64(2), n)
	for id, purged := range map[string]bool{
		"ret-old": true, "wd-old": true, "ap-old": false, "ret-new": false, "sub": false,
	} {
		c, err := s.GetCandidate(ctx, 1, id)
		require.NoError(t, err)
		if purged {
			require.Empty(t, c.Title, id)
			require.Empty(t, c.Body, id)
			require.Empty(t, c.Note, id)
			require.NotNil(t, c.PurgedAt, id)
			require.Equal(t, "h-"+id, c.ContentSHA256, id)
		} else {
			require.NotEmpty(t, c.Body, id)
			require.Nil(t, c.PurgedAt, id)
		}
	}
	// a second run purges nothing more
	n, err = s.PurgeCandidateContent(ctx, now.Add(-time.Hour), now)
	require.NoError(t, err)
	require.Zero(t, n)

	// 90 days after the terminal state a returned or withdrawn row goes; submitted and approved never do (the
	// approved row is what keeps its document an entry)
	n, err = s.DeleteSettledCandidates(ctx, agent24T0.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(2), n)
	is, err := s.IsEntryDocument(ctx, 1, "k-ap-old")
	require.NoError(t, err)
	require.True(t, is)
	for _, id := range []string{"ret-new", "sub", "ap-old"} {
		c, err := s.GetCandidate(ctx, 1, id)
		require.NoError(t, err)
		require.NotNil(t, c, id)
	}
}

func TestAgent24WriteIdempotency(t *testing.T) {
	_, s := agent24CandidateDB(t)
	ctx := context.Background()
	rec := &types.Agent24WriteIdempotency{
		TenantID: 1, Principal: "p", Op: "candidate.propose", Key: "k", Fingerprint: "f1",
		StatusCode: 200, Response: `{"a":1}`, CreatedAt: agent24T0, ExpiresAt: agent24T0.Add(90 * 24 * time.Hour),
	}
	require.NoError(t, s.PutIdempotency(ctx, rec))
	got, err := s.GetIdempotency(ctx, 1, "p", "candidate.propose", "k", agent24T0.Add(time.Hour))
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "f1", got.Fingerprint)
	require.Equal(t, `{"a":1}`, got.Response)
	// the scope is (tenant, principal, op, key)
	for _, q := range [][4]any{
		{uint64(2), "p", "candidate.propose", "k"},
		{uint64(1), "q", "candidate.propose", "k"},
		{uint64(1), "p", "candidate.approve", "k"},
		{uint64(1), "p", "candidate.propose", "k2"},
	} {
		g, err := s.GetIdempotency(ctx, q[0].(uint64), q[1].(string), q[2].(string), q[3].(string), agent24T0)
		require.NoError(t, err)
		require.Nil(t, g, q)
	}
	// the same key again, live: refused, the first record stays
	again := *rec
	again.Fingerprint = "f2"
	require.ErrorIs(t, s.PutIdempotency(ctx, &again), ErrAgent24IdempotencyExists)
	got, err = s.GetIdempotency(ctx, 1, "p", "candidate.propose", "k", agent24T0)
	require.NoError(t, err)
	require.Equal(t, "f1", got.Fingerprint)
	// past the expiry the record is not returned, and the same key is a new request
	late := rec.ExpiresAt.Add(time.Second)
	got, err = s.GetIdempotency(ctx, 1, "p", "candidate.propose", "k", late)
	require.NoError(t, err)
	require.Nil(t, got)
	fresh := *rec
	fresh.Fingerprint, fresh.CreatedAt, fresh.ExpiresAt = "f3", late, late.Add(time.Hour)
	require.NoError(t, s.PutIdempotency(ctx, &fresh))
	got, err = s.GetIdempotency(ctx, 1, "p", "candidate.propose", "k", late)
	require.NoError(t, err)
	require.Equal(t, "f3", got.Fingerprint)
	// a rolled-back transaction leaves no record
	err = s.Transaction(ctx, func(tx *Agent24CandidateStore) error {
		r := *rec
		r.Key = "rolled"
		require.NoError(t, tx.PutIdempotency(ctx, &r))
		return fmt.Errorf("rollback")
	})
	require.Error(t, err)
	got, err = s.GetIdempotency(ctx, 1, "p", "candidate.propose", "rolled", agent24T0)
	require.NoError(t, err)
	require.Nil(t, got)
	// expired records are deleted
	n, err := s.DeleteExpiredIdempotency(ctx, late.Add(2*time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
}

func TestAgent24KnowledgeAdminGrants(t *testing.T) {
	db, s := agent24CandidateDB(t)
	ctx := context.Background()
	revoked := agent24T0
	require.NoError(t, db.Create([]types.Agent24KnowledgeAdmin{
		{TenantID: 1, Principal: "pa", KnowledgeBaseID: "kb1", CreatedAt: agent24T0},
		{TenantID: 1, Principal: "pstar", KnowledgeBaseID: types.Agent24KnowledgeAdminAnyKB, CreatedAt: agent24T0},
		{TenantID: 1, Principal: "pr", KnowledgeBaseID: "kb1", CreatedAt: agent24T0, RevokedAt: &revoked},
		// an operator row with no knowledge base grants nothing anywhere
		{TenantID: 1, Principal: "pempty", KnowledgeBaseID: "", CreatedAt: agent24T0},
	}).Error)
	for _, c := range []struct {
		tenant    uint64
		principal string
		kb        string
		holds     bool
	}{
		{1, "pa", "kb1", true},
		{1, "pa", "kb2", false},
		{2, "pa", "kb1", false},
		{1, "pstar", "kb1", true},
		{1, "pstar", "kb9", true},
		{2, "pstar", "kb1", false},
		{1, "pr", "kb1", false},
		{1, "pb", "kb1", false},
		{1, "", "kb1", false},
		{1, "pa", "", false},
		{1, "pstar", "", false},
		{1, "pempty", "kb1", false},
		{1, "pempty", "", false},
	} {
		got, err := s.AdminHolds(ctx, c.tenant, c.principal, c.kb)
		require.NoError(t, err)
		require.Equal(t, c.holds, got, "%+v", c)
	}
	for _, c := range []struct {
		tenant    uint64
		principal string
		holds     bool
	}{
		{1, "pa", true},
		{1, "pstar", true},
		{1, "pr", false},
		{1, "pb", false},
		{2, "pa", false},
		{1, "", false},
		{1, "pempty", false},
	} {
		got, err := s.AdminHoldsAnywhere(ctx, c.tenant, c.principal)
		require.NoError(t, err)
		require.Equal(t, c.holds, got, "%+v", c)
	}
}

func TestAgent24EntryWithdrawals(t *testing.T) {
	_, s := agent24CandidateDB(t)
	ctx := context.Background()
	w := &types.Agent24EntryWithdrawal{
		TenantID: 1, KnowledgeID: "k1", WithdrawnBy: "pa", SourceRevision: "r1", WithdrawnAt: agent24T0,
	}
	ok, err := s.Withdraw(ctx, w)
	require.NoError(t, err)
	require.True(t, ok)
	// already withdrawn: false, the first record stays
	again := *w
	again.WithdrawnBy, again.WithdrawnAt = "pz", agent24T0.Add(time.Hour)
	ok, err = s.Withdraw(ctx, &again)
	require.NoError(t, err)
	require.False(t, ok)
	is, err := s.IsWithdrawn(ctx, 1, "k1")
	require.NoError(t, err)
	require.True(t, is)
	is, err = s.IsWithdrawn(ctx, 2, "k1")
	require.NoError(t, err)
	require.False(t, is)
	is, err = s.IsWithdrawn(ctx, 1, "k2")
	require.NoError(t, err)
	require.False(t, is)

	ok, err = s.Withdraw(ctx, &types.Agent24EntryWithdrawal{
		TenantID: 1, KnowledgeID: "k3", WithdrawnBy: "pa",
		WithdrawnAt: agent24T0,
	})
	require.NoError(t, err)
	require.True(t, ok)
	set, err := s.WithdrawnAmong(ctx, 1, []string{"k1", "k2", "k3", "k4"})
	require.NoError(t, err)
	require.Equal(t, map[string]struct{}{"k1": {}, "k3": {}}, set)
	set, err = s.WithdrawnAmong(ctx, 2, []string{"k1"})
	require.NoError(t, err)
	require.Empty(t, set)
	set, err = s.WithdrawnAmong(ctx, 1, nil)
	require.NoError(t, err)
	require.NotNil(t, set)
	require.Empty(t, set)
}

func TestAgent24CandidateLockCandidate(t *testing.T) {
	_, s := agent24CandidateDB(t)
	ctx := context.Background()
	require.NoError(t, s.CreateCandidate(ctx, agent24TestCandidate("c1", "api_external_user:1:a")))
	err := s.Transaction(ctx, func(tx *Agent24CandidateStore) error {
		c, err := tx.LockCandidate(ctx, 1, "c1")
		require.NoError(t, err)
		require.NotNil(t, c)
		none, err := tx.LockCandidate(ctx, 1, "nope")
		require.NoError(t, err)
		require.Nil(t, none)
		return nil
	})
	require.NoError(t, err)
}

func TestAgent24FindApprovedByContent(t *testing.T) {
	_, s := agent24CandidateDB(t)
	ctx := context.Background()
	const hash = "same-hash"
	later := agent24T0.Add(time.Hour)
	mk := func(id, author string, tenant uint64, kb, sha string) {
		c := agent24TestCandidate(id, author)
		c.TenantID = tenant
		c.TargetKnowledgeBaseID = kb
		c.ContentSHA256 = sha
		require.NoError(t, s.CreateCandidate(ctx, c))
	}
	mk("m", "a1", 1, "kb1", hash)
	mk("n", "a2", 1, "kb1", hash)
	mk("c", "a3", 1, "kb1", hash)
	mk("other-tenant", "a4", 2, "kb1", hash)
	mk("other-kb", "a5", 1, "kb2", hash)
	mk("other-hash", "a6", 1, "kb1", "other")
	mk("still", "a7", 1, "kb1", hash)
	mk("nok", "a8", 1, "kb1", hash)
	approve := func(id, kid string, tenant uint64, at time.Time) {
		moved, err := s.TransitionCandidate(ctx, tenant, id, types.Agent24CandidateTransition{
			To: types.Agent24CandidateApproved, KnowledgeID: kid, Approver: "ap", SourceRevision: "r",
		}, at)
		require.NoError(t, err)
		require.True(t, moved)
	}
	approve("m", "k-m", 1, later)
	approve("n", "k-n", 1, later)
	approve("c", "k-c", 1, agent24T0)
	approve("other-tenant", "k-t", 2, later)
	approve("other-kb", "k-b", 1, later)
	approve("other-hash", "k-h", 1, later)
	approve("nok", "", 1, later)
	list, err := s.FindApprovedByContent(ctx, 1, "kb1", hash)
	require.NoError(t, err)
	ids := make([]string, len(list))
	for i := range list {
		ids[i] = list[i].ID
	}
	require.Equal(t, []string{"m", "n", "c"}, ids)
}
