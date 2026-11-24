// Agent24 fork product patch A24-KB-51 (KB-C4-02b).
package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

// fakeDefaultKBs stands in for the knowledge base service: it keeps the KBs it was asked to create.
type fakeDefaultKBs struct {
	mu        sync.Mutex
	kbs       map[string]bool
	creates   int
	removed   []string
	createErr error
	// block, when set, holds Create until it is closed (entered is closed once Create is waiting)
	block   chan struct{}
	entered chan struct{}
	// onCreate, when set, runs first in every Create with its 1-based call number; its error is Create's.
	onCreate func(n int, kbID string) error
}

func newFakeDefaultKBs() *fakeDefaultKBs { return &fakeDefaultKBs{kbs: map[string]bool{}} }

func (f *fakeDefaultKBs) Create(_ context.Context, _ uint64, kbID string) error {
	f.mu.Lock()
	f.creates++
	n, hook := f.creates, f.onCreate
	f.mu.Unlock()
	if hook != nil {
		if err := hook(n, kbID); err != nil {
			f.mu.Lock()
			f.kbs[kbID] = true // a failure may leave a half-made KB behind
			f.mu.Unlock()
			return err
		}
	}
	if f.block != nil {
		close(f.entered)
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		f.kbs[kbID] = true // a failure may leave a half-made KB behind
		return f.createErr
	}
	f.kbs[kbID] = true
	return nil
}

func (f *fakeDefaultKBs) Remove(_ context.Context, _ uint64, kbID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, kbID)
	delete(f.kbs, kbID)
	return nil
}

func (f *fakeDefaultKBs) Exists(_ context.Context, _ uint64, kbID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.kbs[kbID], nil
}

func (f *fakeDefaultKBs) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.kbs)
}

type defaultKBHarness struct {
	init  *Agent24DefaultKBInit
	store *repository.Agent24DefaultKBStore
	kbs   *fakeDefaultKBs
	now   time.Time
}

func newDefaultKBHarness(t *testing.T) *defaultKBHarness {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // one in-memory database for every goroutine
	require.NoError(t, repository.EnsureAgent24DefaultKBTable(db))
	require.NoError(t, repository.EnsureAgent24DefaultKBTable(db)) // idempotent
	h := &defaultKBHarness{
		store: repository.NewAgent24DefaultKBStore(db), kbs: newFakeDefaultKBs(),
		now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC),
	}
	h.init = NewAgent24DefaultKBInit(h.store, h.kbs)
	h.init.now = func() time.Time { return h.now }
	n := 0
	h.init.newID = func() string { n++; return fmt.Sprintf("kb-%d", n) }
	return h
}

const defaultKBOwner = "api_tenant:7"

func TestAgent24DefaultKBCreatesOnceAndThenReportsExisting(t *testing.T) {
	h := newDefaultKBHarness(t)
	ctx := context.Background()
	first, err := h.init.Init(ctx, 7, defaultKBOwner, "key-a", "cfg-1")
	require.NoError(t, err)
	require.Equal(t, Agent24DefaultKBCreated, first.Outcome)
	require.Equal(t, "kb-1", first.KnowledgeBaseID)

	// a repeated activation, under another key and a newer configuration, finds the same KB and changes nothing
	again, err := h.init.Init(ctx, 7, defaultKBOwner, "key-b", "cfg-2")
	require.NoError(t, err)
	require.Equal(t, Agent24DefaultKBExisting, again.Outcome)
	require.Equal(t, "kb-1", again.KnowledgeBaseID)
	require.Equal(t, "cfg-1", again.ConfigVersion)
	require.True(t, again.ConfigMismatch)
	require.Equal(t, 1, h.kbs.creates)
	row, err := h.store.Get(ctx, 7, defaultKBOwner)
	require.NoError(t, err)
	require.Equal(t, "cfg-1", row.ConfigVersion)

	// another owner gets a KB of its own
	other, err := h.init.Init(ctx, 7, "api_tenant:8", "key-a", "cfg-1")
	require.NoError(t, err)
	require.Equal(t, Agent24DefaultKBCreated, other.Outcome)
	require.Equal(t, 2, h.kbs.count())
}

func TestAgent24DefaultKBConcurrentInitsCreateOne(t *testing.T) {
	h := newDefaultKBHarness(t)
	ctx := context.Background()
	h.kbs.block, h.kbs.entered = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := h.init.Init(ctx, 7, defaultKBOwner, "key-a", "cfg-1")
		done <- err
	}()
	<-h.kbs.entered

	// while the first is creating, the same key and another key both hear it is in progress
	_, err := h.init.Init(ctx, 7, defaultKBOwner, "key-a", "cfg-1")
	require.ErrorIs(t, err, ErrAgent24DefaultKBInProgress)
	_, err = h.init.Init(ctx, 7, defaultKBOwner, "key-b", "cfg-1")
	require.ErrorIs(t, err, ErrAgent24DefaultKBInProgress)

	close(h.kbs.block)
	require.NoError(t, <-done)
	require.Equal(t, 1, h.kbs.creates)
	require.Equal(t, 1, h.kbs.count())
}

func TestAgent24DefaultKBInterruptedInitIsFinishedBySameKeyRetry(t *testing.T) {
	h := newDefaultKBHarness(t)
	ctx := context.Background()
	// a request that stopped after creating the KB and before marking the row ready
	row := &types.Agent24DefaultKB{
		TenantID: 7, Owner: defaultKBOwner, KnowledgeBaseID: "kb-left", State: types.Agent24DefaultKBPending,
		KeyHash: agent24DefaultKBKeyHash("key-a"), Claim: "c-old", ConfigVersion: "cfg-1",
		LeaseUntil: h.now.Add(time.Minute), CreatedAt: h.now, UpdatedAt: h.now,
	}
	inserted, err := h.store.Insert(ctx, row)
	require.NoError(t, err)
	require.True(t, inserted)
	h.kbs.kbs["kb-left"] = true

	// before the lease runs out, the retry waits
	_, err = h.init.Init(ctx, 7, defaultKBOwner, "key-a", "cfg-1")
	require.ErrorIs(t, err, ErrAgent24DefaultKBInProgress)

	h.now = h.now.Add(2 * time.Minute)
	got, err := h.init.Init(ctx, 7, defaultKBOwner, "key-a", "cfg-1")
	require.NoError(t, err)
	require.Equal(t, Agent24DefaultKBCreated, got.Outcome)
	require.Equal(t, "kb-left", got.KnowledgeBaseID, "the same-key retry finishes the KB already started")
	require.Equal(t, 1, h.kbs.count())
	require.Empty(t, h.kbs.removed)
}

func TestAgent24DefaultKBStalePendingIsReclaimedForAnotherKey(t *testing.T) {
	h := newDefaultKBHarness(t)
	ctx := context.Background()
	row := &types.Agent24DefaultKB{
		TenantID: 7, Owner: defaultKBOwner, KnowledgeBaseID: "kb-half", State: types.Agent24DefaultKBPending,
		KeyHash: agent24DefaultKBKeyHash("key-a"), Claim: "c-old", LeaseUntil: h.now.Add(-time.Second),
		CreatedAt: h.now, UpdatedAt: h.now,
	}
	_, err := h.store.Insert(ctx, row)
	require.NoError(t, err)
	h.kbs.kbs["kb-half"] = true

	got, err := h.init.Init(ctx, 7, defaultKBOwner, "key-b", "cfg-1")
	require.NoError(t, err)
	require.Equal(t, Agent24DefaultKBCreated, got.Outcome)
	require.NotEqual(t, "kb-half", got.KnowledgeBaseID)
	require.Equal(t, []string{"kb-half"}, h.kbs.removed, "the half-made KB is reclaimed")
	require.Equal(t, 1, h.kbs.count())
}

func TestAgent24DefaultKBFailedCreateLeavesNothing(t *testing.T) {
	h := newDefaultKBHarness(t)
	ctx := context.Background()
	h.kbs.createErr = errors.New("storage down")
	_, err := h.init.Init(ctx, 7, defaultKBOwner, "key-a", "cfg-1")
	require.Error(t, err)
	require.Equal(t, 0, h.kbs.count(), "no half-made KB")
	row, err := h.store.Get(ctx, 7, defaultKBOwner)
	require.NoError(t, err)
	require.Nil(t, row, "the pending row is released")

	// the next attempt starts clean
	h.kbs.createErr = nil
	got, err := h.init.Init(ctx, 7, defaultKBOwner, "key-a", "cfg-1")
	require.NoError(t, err)
	require.Equal(t, Agent24DefaultKBCreated, got.Outcome)
	require.Equal(t, 1, h.kbs.count())
}

func TestAgent24DefaultKBRefusesAnEmptyOwnerOrKey(t *testing.T) {
	h := newDefaultKBHarness(t)
	ctx := context.Background()
	for _, c := range []struct {
		tenant     uint64
		owner, key string
	}{{0, defaultKBOwner, "k"}, {7, "", "k"}, {7, defaultKBOwner, ""}} {
		_, err := h.init.Init(ctx, c.tenant, c.owner, c.key, "cfg-1")
		require.Error(t, err)
	}
	require.Equal(t, 0, h.kbs.creates)
}

// #172 B1: a request whose lease ran out and whose Create then fails must not remove the KB a same-key retry
// finished meanwhile.
func TestAgent24DefaultKBStaleFailureDoesNotRemoveTheRetrysKB(t *testing.T) {
	h := newDefaultKBHarness(t)
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	h.kbs.onCreate = func(n int, _ string) error {
		if n == 1 {
			close(entered)
			<-release
			return errors.New("slow create finally failed")
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := h.init.Init(ctx, 7, defaultKBOwner, "key-a", "cfg-1")
		done <- err
	}()
	<-entered
	h.now = h.now.Add(3 * time.Minute) // A's lease has run out
	got, err := h.init.Init(ctx, 7, defaultKBOwner, "key-a", "cfg-1")
	require.NoError(t, err)
	require.Equal(t, Agent24DefaultKBCreated, got.Outcome)
	close(release)
	require.Error(t, <-done)
	row, err := h.store.Get(ctx, 7, defaultKBOwner)
	require.NoError(t, err)
	require.Equal(t, types.Agent24DefaultKBReady, row.State)
	exists, _ := h.kbs.Exists(ctx, 7, row.KnowledgeBaseID)
	require.True(t, exists, "the ready row's KB is still there")
}

// #172 non-blocking: a request overtaken under another key removes the KB it made after losing the row.
func TestAgent24DefaultKBOvertakenRequestRemovesItsOrphan(t *testing.T) {
	h := newDefaultKBHarness(t)
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	h.kbs.onCreate = func(n int, _ string) error {
		if n == 1 {
			close(entered)
			<-release
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := h.init.Init(ctx, 7, defaultKBOwner, "key-a", "cfg-1")
		done <- err
	}()
	<-entered
	h.now = h.now.Add(3 * time.Minute)
	got, err := h.init.Init(ctx, 7, defaultKBOwner, "key-b", "cfg-1")
	require.NoError(t, err)
	close(release)
	require.ErrorIs(t, <-done, ErrAgent24DefaultKBInProgress)
	require.Equal(t, 1, h.kbs.count(), "only the winner's KB is left")
	exists, _ := h.kbs.Exists(ctx, 7, got.KnowledgeBaseID)
	require.True(t, exists)
}

// F2 gate: a default KB the user deleted is set up again instead of being reported as existing.
func TestAgent24DefaultKBDeletedKBIsSetUpAgain(t *testing.T) {
	h := newDefaultKBHarness(t)
	ctx := context.Background()
	first, err := h.init.Init(ctx, 7, defaultKBOwner, "key-a", "cfg-1")
	require.NoError(t, err)
	require.NoError(t, h.kbs.Remove(ctx, 7, first.KnowledgeBaseID))
	again, err := h.init.Init(ctx, 7, defaultKBOwner, "key-b", "cfg-1")
	require.NoError(t, err)
	require.Equal(t, Agent24DefaultKBCreated, again.Outcome)
	require.NotEqual(t, first.KnowledgeBaseID, again.KnowledgeBaseID)
}

// #172 Low: a same-key retry that finishes under the earlier configuration reports the mismatch.
func TestAgent24DefaultKBSameKeyRetryReportsConfigMismatch(t *testing.T) {
	h := newDefaultKBHarness(t)
	ctx := context.Background()
	_, err := h.store.Insert(ctx, &types.Agent24DefaultKB{
		TenantID: 7, Owner: defaultKBOwner, KnowledgeBaseID: "kb-old", State: types.Agent24DefaultKBPending,
		KeyHash: agent24DefaultKBKeyHash("key-a"), Claim: "c-old", ConfigVersion: "cfg-1",
		LeaseUntil: h.now.Add(-time.Second), CreatedAt: h.now, UpdatedAt: h.now,
	})
	require.NoError(t, err)
	got, err := h.init.Init(ctx, 7, defaultKBOwner, "key-a", "cfg-2")
	require.NoError(t, err)
	require.Equal(t, "cfg-1", got.ConfigVersion)
	require.True(t, got.ConfigMismatch)
}
