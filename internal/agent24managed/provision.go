// Agent24 fork product patch A24-KB-61 (KB-LITE-02a, G4/G6): in-process provisioning for the managed host.
//
// The managed host serves only the integration routes (AGENT24_KNOWLEDGE_MODE=integration), so neither
// /auth/auto-setup nor any other route that mints an administrator credential is reachable over HTTP.
// Instead the process provisions itself at startup and hands the host one credential over the handshake
// pipe (close-on-exec, held only by the host): a tenant API key limited to the default knowledge base with
// retrieve and ingest. Every start issues a new key and revokes the previous one, so the host never has to
// store it, and a key copied out of an old handshake stops working at the next start.

package agent24managed

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	managedUserEmail = "admin@weknora.local"
	// ManagedConfigVersion is the default KB's configuration version on the managed host.
	ManagedConfigVersion = "lite-managed-1"
	// managedKeyTTL bounds a key that outlives its process (a copy taken from a handshake). Every start
	// replaces the key, so the host only meets this limit when one Lite process runs for a year.
	managedKeyTTL = 365 * 24 * time.Hour
)

// Credential is what the host uses on the integration routes: X-API-Key: <APIKey>, against KnowledgeBaseID.
type Credential struct {
	APIKey          string     `json:"api_key"`
	KnowledgeBaseID string     `json:"knowledge_base_id"`
	ExpiresAt       *time.Time `json:"expires_at"`
}

// Provisioner holds what provisioning needs from the container.
type Provisioner struct {
	DB      *gorm.DB
	Users   interfaces.UserService
	Tenants interfaces.TenantService
	KBs     interfaces.KnowledgeBaseService
	Keys    interfaces.TenantAPIKeyService
	Now     func() time.Time
}

// Provision makes sure the local user, its tenant and the default knowledge base exist, then issues a fresh
// key and revokes the older ones. It returns the tenant id (for the ingest check) and the credential.
// Errors never carry the key.
func (p *Provisioner) Provision(ctx context.Context) (uint64, Credential, error) {
	user, err := p.ensureUser(ctx)
	if err != nil {
		return 0, Credential{}, err
	}
	tenantID := user.TenantID
	if tenantID == 0 {
		return 0, Credential{}, errors.New("managed provisioning: local user has no tenant")
	}
	owner := types.Principal{Type: types.PrincipalAPITenant, ID: strconv.FormatUint(tenantID, 10)}.StorageID()
	if err := repository.EnsureAgent24DefaultKBTable(p.DB); err != nil {
		return 0, Credential{}, fmt.Errorf("managed provisioning: %w", err)
	}
	embeddingID, err := p.builtinEmbeddingID(ctx)
	if err != nil {
		return 0, Credential{}, err
	}
	prov := service.NewAgent24DefaultKBProvisioner(p.KBs, p.Tenants,
		service.Agent24DefaultKBTemplate{EmbeddingModelID: embeddingID})
	idem, err := randomRawURL(24)
	if err != nil {
		return 0, Credential{}, err
	}
	result, err := service.NewAgent24DefaultKBInit(repository.NewAgent24DefaultKBStore(p.DB), prov).
		Init(ctx, tenantID, owner, idem, ManagedConfigVersion)
	if err != nil {
		return 0, Credential{}, fmt.Errorf("managed provisioning: default knowledge base: %w", err)
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	key, err := service.IssueAgent24OnboardKey(ctx, p.Keys, tenantID, owner, result.KnowledgeBaseID,
		managedKeyTTL, now().UTC())
	if err != nil {
		return 0, Credential{}, fmt.Errorf("managed provisioning: issue key: %w", err)
	}
	return tenantID, Credential{
		APIKey: key.Token, KnowledgeBaseID: result.KnowledgeBaseID, ExpiresAt: key.ExpiresAt,
	}, nil
}

func (p *Provisioner) ensureUser(ctx context.Context) (*types.User, error) {
	if user, _ := p.Users.GetUserByEmail(ctx, managedUserEmail); user != nil {
		return user, nil
	}
	// The password is never used: nobody logs in to the managed host.
	pw := make([]byte, 24)
	if _, err := rand.Read(pw); err != nil {
		return nil, fmt.Errorf("managed provisioning: %w", err)
	}
	if _, err := p.Users.Register(ctx, &types.RegisterRequest{
		Username: "user_" + base64.RawURLEncoding.EncodeToString(pw[:6]),
		Email:    managedUserEmail,
		Password: base64.RawURLEncoding.EncodeToString(pw),
	}); err != nil {
		return nil, fmt.Errorf("managed provisioning: register local user: %w", err)
	}
	user, err := p.Users.GetUserByEmail(ctx, managedUserEmail)
	if err != nil || user == nil {
		return nil, errors.New("managed provisioning: local user not found after registration")
	}
	return user, nil
}

// builtinEmbeddingID is the oldest built-in embedding model, or "" when builtin_models.yaml has none (the KB is
// then created without one and ingestion fails until the host supplies a model).
func (p *Provisioner) builtinEmbeddingID(ctx context.Context) (string, error) {
	var m types.Model
	err := p.DB.WithContext(ctx).
		Where("is_builtin = ? AND type = ?", true, types.ModelTypeEmbedding).
		Order("created_at ASC").
		First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("managed provisioning: read built-in models: %w", err)
	}
	return m.ID, nil
}
