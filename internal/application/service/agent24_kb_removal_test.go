package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

// A24-KB-48 (W1.11/A14): DeleteKnowledgeBase runs the installed hook before anything else is removed, and the soft
// delete is the hook's within, on the hook's transaction (Q1-B).

type agent24S4CStubHook struct {
	db      *gorm.DB // within runs on a transaction of this database
	err     error    // returned before within when set
	tenant  uint64
	kbID    string
	calls   int
	gotNoop bool // within was nil
}

func (h *agent24S4CStubHook) BeforeRemoval(
	_ context.Context, tenantID uint64, kbID string, within func(tx *gorm.DB) error,
) error {
	h.calls++
	h.tenant, h.kbID, h.gotNoop = tenantID, kbID, within == nil
	if h.err != nil {
		return h.err
	}
	if within == nil {
		return nil
	}
	return h.db.Transaction(within)
}

func agent24S4CInstall(t *testing.T, h Agent24KBRemovalHook) {
	t.Helper()
	SetAgent24KBRemovalHook(h)
	t.Cleanup(func() { SetAgent24KBRemovalHook(nil) })
}

func agent24S4CKBDB(t *testing.T, kb *types.KnowledgeBase) *gorm.DB {
	t.Helper()
	// a file of its own per run: a shared in-memory name outlives the test under -count
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "kb.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.KnowledgeBase{}))
	require.NoError(t, db.Create(kb).Error)
	return db
}

func agent24S4CService(kbRepo *kbDeleteKBRepo) *knowledgeBaseService {
	return &knowledgeBaseService{repo: kbRepo, asynqClient: kbDeleteTaskEnqueuer{}}
}

func TestAgent24S4CDeleteKnowledgeBaseRunsHookWithSoftDeleteInside(t *testing.T) {
	const kbID = "kb-1"
	kb := &types.KnowledgeBase{ID: kbID, TenantID: 7, Name: "n"}
	db := agent24S4CKBDB(t, kb)
	hook := &agent24S4CStubHook{db: db}
	agent24S4CInstall(t, hook)
	kbRepo := &kbDeleteKBRepo{fakeKBRepo: *newFakeKBRepo()}
	kbRepo.rows[kbID] = kb

	// the caller's tenant is 1; the hook gets the base's own tenant
	require.NoError(t, agent24S4CService(kbRepo).DeleteKnowledgeBase(ctxWithTenantStorage(1, "local"), kbID))

	assert.Equal(t, 1, hook.calls)
	assert.Equal(t, uint64(7), hook.tenant)
	assert.Equal(t, kbID, hook.kbID)
	assert.False(t, hook.gotNoop, "the soft delete must be the hook's within")
	// the soft delete ran on the hook's transaction, not through the repository's own call
	assert.Empty(t, kbRepo.deletedID)
	var live int64
	require.NoError(t, db.Model(&types.KnowledgeBase{}).Where("id = ?", kbID).Count(&live).Error)
	assert.Zero(t, live)
	var all int64
	require.NoError(t, db.Unscoped().Model(&types.KnowledgeBase{}).Where("id = ?", kbID).Count(&all).Error)
	assert.EqualValues(t, 1, all, "a soft delete, as upstream")
}

func TestAgent24S4CDeleteKnowledgeBaseFailClosed(t *testing.T) {
	const kbID = "kb-1"
	kb := &types.KnowledgeBase{ID: kbID, TenantID: 1, Name: "n"}
	db := agent24S4CKBDB(t, kb)
	boom := errors.New("hook failed")
	agent24S4CInstall(t, &agent24S4CStubHook{db: db, err: boom})
	kbRepo := &kbDeleteKBRepo{fakeKBRepo: *newFakeKBRepo()}
	kbRepo.rows[kbID] = kb
	dsRepo := newKBDeleteDSRepo(kbID, &types.DataSource{ID: "ds-1", KnowledgeBaseID: kbID})
	svc := agent24S4CService(kbRepo)
	svc.dsRepo = dsRepo

	err := svc.DeleteKnowledgeBase(ctxWithTenantStorage(1, "local"), kbID)
	require.ErrorIs(t, err, boom)

	assert.Empty(t, kbRepo.deletedID)
	assert.Empty(t, dsRepo.deleteIDs, "nothing after the soft delete runs")
	var live int64
	require.NoError(t, db.Model(&types.KnowledgeBase{}).Where("id = ?", kbID).Count(&live).Error)
	assert.EqualValues(t, 1, live)
}

func TestAgent24S4CDeleteKnowledgeBaseWithoutHookIsUpstream(t *testing.T) {
	SetAgent24KBRemovalHook(nil)
	const kbID = "kb-1"
	kbRepo := &kbDeleteKBRepo{fakeKBRepo: *newFakeKBRepo()}
	kbRepo.rows[kbID] = &types.KnowledgeBase{ID: kbID, TenantID: 1, Name: "n"}
	require.NoError(t, agent24S4CService(kbRepo).DeleteKnowledgeBase(ctxWithTenantStorage(1, "local"), kbID))
	assert.Equal(t, kbID, kbRepo.deletedID)
}
