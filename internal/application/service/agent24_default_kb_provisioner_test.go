// Agent24 fork product patch A24-KB-52 (KB-C4-02a mock, Loop 7 first round F2a).
package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type fakeProvisionerKBs struct {
	interfaces.KnowledgeBaseService
	byID    map[string]*types.KnowledgeBase
	created []*types.KnowledgeBase
	deleted []string
	lastCtx context.Context
}

func (f *fakeProvisionerKBs) GetKnowledgeBaseByID(_ context.Context, id string) (*types.KnowledgeBase, error) {
	kb, ok := f.byID[id]
	if !ok || kb == nil {
		return nil, repository.ErrKnowledgeBaseNotFound
	}
	return kb, nil
}

func (f *fakeProvisionerKBs) CreateKnowledgeBase(
	ctx context.Context, kb *types.KnowledgeBase,
) (*types.KnowledgeBase, error) {
	f.lastCtx = ctx
	cp := *kb
	if f.byID == nil {
		f.byID = map[string]*types.KnowledgeBase{}
	}
	f.byID[cp.ID] = &cp
	f.created = append(f.created, &cp)
	return &cp, nil
}

func (f *fakeProvisionerKBs) DeleteKnowledgeBase(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	delete(f.byID, id)
	return nil
}

type fakeProvisionerTenants struct {
	interfaces.TenantService
	byID map[uint64]*types.Tenant
}

func (f *fakeProvisionerTenants) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	t, ok := f.byID[id]
	if !ok || t == nil {
		return nil, repository.ErrKnowledgeBaseNotFound
	}
	return t, nil
}

func newProvisionerHarness() (Agent24DefaultKBProvisioner, *fakeProvisionerKBs, *types.KnowledgeBase) {
	vs, sb := "vs-1", "sb-1"
	tmpl := &types.KnowledgeBase{
		ID:               "tmpl-kb",
		TenantID:         7,
		Name:             "template",
		ChunkingConfig:   types.ChunkingConfig{ChunkSize: 512, ChunkOverlap: 64},
		EmbeddingModelID: "emb-1",
		SummaryModelID:   "sum-1",
		StorageProviderConfig: &types.StorageProviderConfig{
			Provider: "minio",
		},
		StorageBackendID: &sb,
		VectorStoreID:    &vs,
		IndexingStrategy: types.IndexingStrategy{VectorEnabled: true, KeywordEnabled: true},
		VLMConfig:        types.VLMConfig{ModelID: "vlm-should-not-copy"},
	}
	kbs := &fakeProvisionerKBs{byID: map[string]*types.KnowledgeBase{"tmpl-kb": tmpl}}
	tenants := &fakeProvisionerTenants{byID: map[uint64]*types.Tenant{7: {ID: 7}}}
	p := NewAgent24DefaultKBProvisioner(kbs, tenants, Agent24DefaultKBTemplate{
		TemplateKBID: "tmpl-kb", Name: "业主知识库",
	})
	return p, kbs, tmpl
}

func TestAgent24DefaultKBProvisionerCreateCopiesTemplate(t *testing.T) {
	p, kbs, tmpl := newProvisionerHarness()
	require.NoError(t, p.Create(context.Background(), 7, "kb-new"))
	require.Len(t, kbs.created, 1)
	got := kbs.created[0]
	require.Equal(t, "kb-new", got.ID)
	require.Equal(t, "业主知识库", got.Name)
	require.Equal(t, "document", got.Type)
	require.Equal(t, "Agent24 default knowledge base", got.Description)
	require.Equal(t, tmpl.ChunkingConfig, got.ChunkingConfig)
	require.Equal(t, tmpl.EmbeddingModelID, got.EmbeddingModelID)
	require.Equal(t, tmpl.SummaryModelID, got.SummaryModelID)
	require.Equal(t, tmpl.StorageProviderConfig, got.StorageProviderConfig)
	require.Equal(t, tmpl.StorageBackendID, got.StorageBackendID)
	require.Equal(t, tmpl.VectorStoreID, got.VectorStoreID)
	require.Equal(t, tmpl.IndexingStrategy, got.IndexingStrategy)
	require.Zero(t, got.VLMConfig)
	require.Zero(t, got.ASRConfig)
	require.Nil(t, got.QuestionGenerationConfig)
	require.Nil(t, got.WikiConfig)
	require.Nil(t, got.FAQConfig)
	require.Nil(t, got.AutoTagConfig)
	require.Nil(t, got.ExtractConfig)
	require.Nil(t, got.ProfileConfig)
	tid, ok := types.TenantIDFromContext(kbs.lastCtx)
	require.True(t, ok)
	require.Equal(t, uint64(7), tid)
	info, ok := types.TenantInfoFromContext(kbs.lastCtx)
	require.True(t, ok)
	require.Equal(t, uint64(7), info.ID)
}

func TestAgent24DefaultKBProvisionerCreateExistingIsNoop(t *testing.T) {
	p, kbs, _ := newProvisionerHarness()
	kbs.byID["kb-new"] = &types.KnowledgeBase{ID: "kb-new", TenantID: 7}
	require.NoError(t, p.Create(context.Background(), 7, "kb-new"))
	require.Empty(t, kbs.created)
}

func TestAgent24DefaultKBProvisionerCreateTemplateMissing(t *testing.T) {
	p, kbs, _ := newProvisionerHarness()
	delete(kbs.byID, "tmpl-kb")
	err := p.Create(context.Background(), 7, "kb-new")
	require.Error(t, err)
	require.Empty(t, kbs.created)
}

func TestAgent24DefaultKBProvisionerCreateTemplateOtherTenant(t *testing.T) {
	p, kbs, tmpl := newProvisionerHarness()
	tmpl.TenantID = 9
	err := p.Create(context.Background(), 7, "kb-new")
	require.Error(t, err)
	require.Empty(t, kbs.created)
}

func TestAgent24DefaultKBProvisionerRemoveDeletesOwn(t *testing.T) {
	p, kbs, _ := newProvisionerHarness()
	kbs.byID["kb-new"] = &types.KnowledgeBase{ID: "kb-new", TenantID: 7}
	require.NoError(t, p.Remove(context.Background(), 7, "kb-new"))
	require.Equal(t, []string{"kb-new"}, kbs.deleted)
	_, err := kbs.GetKnowledgeBaseByID(context.Background(), "kb-new")
	require.ErrorIs(t, err, repository.ErrKnowledgeBaseNotFound)
}

func TestAgent24DefaultKBProvisionerRemoveMissing(t *testing.T) {
	p, kbs, _ := newProvisionerHarness()
	require.NoError(t, p.Remove(context.Background(), 7, "kb-missing"))
	require.Empty(t, kbs.deleted)
}

func TestAgent24DefaultKBProvisionerRemoveOtherTenant(t *testing.T) {
	p, kbs, _ := newProvisionerHarness()
	kbs.byID["kb-other"] = &types.KnowledgeBase{ID: "kb-other", TenantID: 9}
	require.NoError(t, p.Remove(context.Background(), 7, "kb-other"))
	require.Empty(t, kbs.deleted)
	got, err := kbs.GetKnowledgeBaseByID(context.Background(), "kb-other")
	require.NoError(t, err)
	require.Equal(t, uint64(9), got.TenantID)
}

func TestAgent24DefaultKBProvisionerExists(t *testing.T) {
	p, kbs, _ := newProvisionerHarness()
	kbs.byID["kb-own"] = &types.KnowledgeBase{ID: "kb-own", TenantID: 7}
	kbs.byID["kb-other"] = &types.KnowledgeBase{ID: "kb-other", TenantID: 9}
	ctx := context.Background()
	for id, want := range map[string]bool{"kb-own": true, "kb-other": false, "kb-missing": false} {
		got, err := p.Exists(ctx, 7, id)
		require.NoError(t, err)
		require.Equal(t, want, got, id)
	}
}
