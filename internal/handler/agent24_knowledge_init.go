// Agent24 fork product patch A24-KB-53 (KB-C4-02a mock + KB-C4-02b, CONTRACT-OPS-v2 W4; Loop 7 first round F2).
//
// POST /api/v1/agent24/knowledge/init is init.default_kb under a MOCK activation: the request carries an activation
// code that the operator listed in AGENT24_KNOWLEDGE_MOCK_ACTIVATION_FILE (code hash → demo tenant, template KB,
// configuration version). It stands in for the trusted bootstrap of KB-C4-02a and is not KB-C1-03: there is no
// identity service, the owner is the tenant principal (api_tenant:<id>), and the route exists only in integration
// mode with that file configured. The code is the credential, so Auth and the credential gate do not run.
//
// The answer carries the owner's default KB (created or existing, W4.3) and a fresh tenant key limited to that KB
// with retrieve and ingest (an earlier onboarding key of the owner is revoked). No platform key leaves the server.
// The code and the key never reach a log line.

package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	// Agent24MockActivationFileEnv names the mock activation table; unset means no init route.
	Agent24MockActivationFileEnv = "AGENT24_KNOWLEDGE_MOCK_ACTIVATION_FILE"
	agent24InitPath              = "/api/v1/agent24/knowledge/init"
	agent24InitMaxBody           = 4 * 1024
	// agent24OnboardKeyTTL is the life of the key the init hands out; a later init issues a new one.
	agent24OnboardKeyTTL = 90 * 24 * time.Hour
)

// Agent24KnowledgeInitHandler serves POST /api/v1/agent24/knowledge/init.
type Agent24KnowledgeInitHandler struct {
	activations *service.Agent24MockActivations
	store       *repository.Agent24DefaultKBStore
	provision   func(templateKBID string) service.Agent24DefaultKBProvisioner
	issue       func(c *gin.Context, tenantID uint64, owner, kbID string) (service.Agent24OnboardKey, error)
	limit       *agent24UIExchangeLimiter
	// owners serialises init per owner in this process (#173 G2), so two activations of one owner never race on
	// key issuance. This mock assumes one integration process (as the fork's ledger does); across processes the
	// newest key stays in the store, but an earlier caller may hold a key a later one revoked (it retries).
	owners sync.Map // owner StorageID -> *sync.Mutex
	now    func() time.Time
}

// NewAgent24KnowledgeInitHandler returns nil (route not registered) outside integration mode or without the mock
// activation file. A file that is set but unusable is an error, so the container fails closed.
func NewAgent24KnowledgeInitHandler(
	db *gorm.DB, kbs interfaces.KnowledgeBaseService, tenants interfaces.TenantService,
	keys interfaces.TenantAPIKeyService,
) (*Agent24KnowledgeInitHandler, error) {
	mode, err := Agent24KnowledgeModeFromEnv()
	if err != nil || mode != Agent24KnowledgeModeIntegration {
		return nil, err
	}
	activations, err := service.LoadAgent24MockActivations(strings.TrimSpace(os.Getenv(Agent24MockActivationFileEnv)))
	if err != nil || activations == nil {
		return nil, err
	}
	if err := repository.EnsureAgent24DefaultKBTable(db); err != nil {
		return nil, err
	}
	h := &Agent24KnowledgeInitHandler{
		activations: activations, store: repository.NewAgent24DefaultKBStore(db),
		provision: func(templateKBID string) service.Agent24DefaultKBProvisioner {
			return service.NewAgent24DefaultKBProvisioner(kbs, tenants,
				service.Agent24DefaultKBTemplate{TemplateKBID: templateKBID})
		},
		limit: newAgent24UIExchangeLimiter(),
		now:   time.Now,
	}
	h.issue = func(c *gin.Context, tenantID uint64, owner, kbID string) (service.Agent24OnboardKey, error) {
		return service.IssueAgent24OnboardKey(c.Request.Context(), keys, tenantID, owner, kbID,
			agent24OnboardKeyTTL, h.now().UTC())
	}
	return h, nil
}

type agent24InitRequest struct {
	ActivationCode string `json:"activation_code"`
	IdempotencyKey string `json:"idempotency_key"`
}

type agent24InitCredential struct {
	APIKey    string     `json:"api_key"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type agent24InitResponse struct {
	KnowledgeBaseID string                `json:"knowledge_base_id"`
	Outcome         string                `json:"outcome"`
	ConfigVersion   string                `json:"config_version"`
	Credential      agent24InitCredential `json:"credential"`
	Details         map[string]any        `json:"details,omitempty"`
}

// agent24IdempotencyKeyOK wants at least 128 bits written as 22 or more URL-safe characters (W3.1).
func agent24IdempotencyKeyOK(key string) bool {
	if len(key) < 22 || len(key) > 128 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		urlSafe := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
		if !urlSafe {
			return false
		}
	}
	return true
}

// Init handles the request: limit, shape, code, default KB, key.
func (h *Agent24KnowledgeInitHandler) Init(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !h.limit.allow(c.ClientIP()) {
		writeAgent24KnowledgeError(c, agent24Unavailable("overloaded"), agent24KnowledgeEcho{})
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, agent24InitMaxBody+1))
	if err != nil || len(body) > agent24InitMaxBody {
		writeAgent24KnowledgeError(c, agent24InvalidRequest(), agent24KnowledgeEcho{})
		return
	}
	var req agent24InitRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.ActivationCode == "" || dec.Decode(&struct{}{}) != io.EOF {
		writeAgent24KnowledgeError(c, agent24InvalidRequest(), agent24KnowledgeEcho{})
		return
	}
	if req.IdempotencyKey == "" {
		writeAgent24KnowledgeError(c, agent24InvalidWrite("idempotency_key_missing"), agent24KnowledgeEcho{})
		return
	}
	if !agent24IdempotencyKeyOK(req.IdempotencyKey) {
		writeAgent24KnowledgeError(c, agent24InvalidRequest(), agent24KnowledgeEcho{})
		return
	}
	act, ok := h.activations.Lookup(req.ActivationCode)
	if !ok {
		writeAgent24KnowledgeError(c, agent24Unauthorized("credential_invalid"), agent24KnowledgeEcho{})
		return
	}
	ctx := c.Request.Context()
	owner := types.Principal{Type: types.PrincipalAPITenant, ID: strconv.FormatUint(act.TenantID, 10)}.StorageID()
	lock, _ := h.owners.LoadOrStore(owner, &sync.Mutex{})
	if !lock.(*sync.Mutex).TryLock() {
		// another init of this owner is running here: W4.5, the caller retries
		writeAgent24KnowledgeError(c, agent24Unavailable("request_in_progress"), agent24KnowledgeEcho{})
		return
	}
	defer lock.(*sync.Mutex).Unlock()
	init := service.NewAgent24DefaultKBInit(h.store, h.provision(act.TemplateKBID))
	result, err := init.Init(ctx, act.TenantID, owner, req.IdempotencyKey, act.ConfigVersion)
	if errors.Is(err, service.ErrAgent24DefaultKBInProgress) {
		writeAgent24KnowledgeError(c, agent24Unavailable("request_in_progress"), agent24KnowledgeEcho{})
		return
	}
	if err != nil {
		logger.Warnf(ctx, "[agent24 init] tenant=%d default kb failed: %v", act.TenantID, err)
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	key, err := h.issue(c, act.TenantID, owner, result.KnowledgeBaseID)
	if err != nil {
		// the KB stays; a retry finds it existing and issues the key again
		logger.Warnf(ctx, "[agent24 init] tenant=%d kb=%s key issue failed: %v", act.TenantID,
			result.KnowledgeBaseID, err)
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	logger.Infof(ctx, "[agent24 init] tenant=%d kb=%s outcome=%s key_id=%d", act.TenantID,
		result.KnowledgeBaseID, result.Outcome, key.KeyID)
	resp := agent24InitResponse{
		KnowledgeBaseID: result.KnowledgeBaseID, Outcome: string(result.Outcome),
		ConfigVersion: result.ConfigVersion,
		Credential:    agent24InitCredential{APIKey: key.Token, ExpiresAt: key.ExpiresAt},
	}
	if result.ConfigMismatch {
		resp.Details = map[string]any{"config_mismatch": true}
	}
	c.JSON(http.StatusOK, resp)
}
