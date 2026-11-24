// Agent24 fork product patch A24-KB-52 (KB-C4-02a mock, Loop 7 first round F2a).
//
// agent24DefaultKBProvisioner copies a tenant-owned template KB into the owner's default KB id.
// Create is a no-op when that KB already exists in the tenant; Remove is a no-op when it is missing
// or belongs to another tenant.

package service

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const agent24DefaultKBName = "默认知识库"

// Agent24DefaultKBTemplate names the source KB and the created default KB.
type Agent24DefaultKBTemplate struct {
	TemplateKBID string
	Name         string
}

type agent24DefaultKBProvisioner struct {
	kbs      interfaces.KnowledgeBaseService
	tenants  interfaces.TenantService
	template Agent24DefaultKBTemplate
}

// NewAgent24DefaultKBProvisioner returns a provisioner that copies template into each default KB.
func NewAgent24DefaultKBProvisioner(
	kbs interfaces.KnowledgeBaseService, tenants interfaces.TenantService, template Agent24DefaultKBTemplate,
) Agent24DefaultKBProvisioner {
	return &agent24DefaultKBProvisioner{kbs: kbs, tenants: tenants, template: template}
}

func (p *agent24DefaultKBProvisioner) withTenant(ctx context.Context, tenantID uint64) (context.Context, error) {
	tenant, err := p.tenants.GetTenantByID(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if tenant == nil {
		return nil, errors.New("agent24 default kb: tenant not found")
	}
	ctx = context.WithValue(ctx, types.TenantIDContextKey, tenantID)
	return context.WithValue(ctx, types.TenantInfoContextKey, tenant), nil
}

func agent24KBMissing(kb *types.KnowledgeBase, err error) (bool, error) {
	if err != nil {
		if errors.Is(err, repository.ErrKnowledgeBaseNotFound) {
			return true, nil
		}
		return false, err
	}
	return kb == nil, nil
}

// Create copies the template into kbID. A KB already in this tenant is left as-is.
func (p *agent24DefaultKBProvisioner) Create(ctx context.Context, tenantID uint64, kbID string) error {
	ctx, err := p.withTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	existing, err := p.kbs.GetKnowledgeBaseByID(ctx, kbID)
	missing, err := agent24KBMissing(existing, err)
	if err != nil {
		return err
	}
	if !missing {
		if existing.TenantID == tenantID {
			return nil
		}
		return errors.New("agent24 default kb: knowledge base belongs to another tenant")
	}
	tmpl, err := p.kbs.GetKnowledgeBaseByID(ctx, p.template.TemplateKBID)
	missing, err = agent24KBMissing(tmpl, err)
	if err != nil {
		return err
	}
	if missing {
		return errors.New("agent24 default kb: template knowledge base not found")
	}
	if tmpl.TenantID != tenantID {
		return errors.New("agent24 default kb: template knowledge base belongs to another tenant")
	}
	name := p.template.Name
	if name == "" {
		name = agent24DefaultKBName
	}
	kb := &types.KnowledgeBase{
		ID:                    kbID,
		Name:                  name,
		Type:                  "document",
		Description:           "Agent24 default knowledge base",
		ChunkingConfig:        tmpl.ChunkingConfig,
		EmbeddingModelID:      tmpl.EmbeddingModelID,
		SummaryModelID:        tmpl.SummaryModelID,
		StorageProviderConfig: tmpl.StorageProviderConfig,
		StorageBackendID:      tmpl.StorageBackendID,
		VectorStoreID:         tmpl.VectorStoreID,
		IndexingStrategy:      tmpl.IndexingStrategy,
	}
	_, err = p.kbs.CreateKnowledgeBase(ctx, kb)
	return err
}

// Remove deletes kbID when it belongs to the tenant. Missing or foreign KBs are ignored.
func (p *agent24DefaultKBProvisioner) Remove(ctx context.Context, tenantID uint64, kbID string) error {
	ctx, err := p.withTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	existing, err := p.kbs.GetKnowledgeBaseByID(ctx, kbID)
	missing, err := agent24KBMissing(existing, err)
	if err != nil {
		return err
	}
	if missing || existing.TenantID != tenantID {
		return nil
	}
	return p.kbs.DeleteKnowledgeBase(ctx, kbID)
}

// Exists reports whether kbID is a KB of the tenant now (A24-KB-51: a ready default whose KB was deleted is set
// up again). A KB of another tenant counts as missing.
func (p *agent24DefaultKBProvisioner) Exists(ctx context.Context, tenantID uint64, kbID string) (bool, error) {
	ctx, err := p.withTenant(ctx, tenantID)
	if err != nil {
		return false, err
	}
	existing, err := p.kbs.GetKnowledgeBaseByID(ctx, kbID)
	missing, err := agent24KBMissing(existing, err)
	if err != nil {
		return false, err
	}
	return !missing && existing.TenantID == tenantID, nil
}
