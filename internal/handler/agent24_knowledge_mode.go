// Agent24 fork product patch A24-KB-04 (KB-C1-01, CONTRACT-READONLY-v1 R9.3).
//
// AGENT24_KNOWLEDGE_MODE selects standalone (default) or integration. In
// integration mode the router denies every route outside a fixed allowlist
// with 404, and an invalid mode or unsafe tenant config fails startup.

package handler

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/config"
)

// Agent24KnowledgeModeEnv names the variable that selects the mode; the other
// constants are its two valid values.
const (
	Agent24KnowledgeModeEnv         = "AGENT24_KNOWLEDGE_MODE"
	Agent24KnowledgeModeStandalone  = "standalone"
	Agent24KnowledgeModeIntegration = "integration"
)

// Agent24KnowledgeModeFromEnv returns the configured mode; unset or empty
// means standalone. Only surrounding whitespace is tolerated.
func Agent24KnowledgeModeFromEnv() (string, error) {
	switch v := strings.TrimSpace(os.Getenv(Agent24KnowledgeModeEnv)); v {
	case "", Agent24KnowledgeModeStandalone:
		return Agent24KnowledgeModeStandalone, nil
	case Agent24KnowledgeModeIntegration:
		return Agent24KnowledgeModeIntegration, nil
	default:
		return "", fmt.Errorf("%s=%q is invalid: use %q, %q or leave it unset",
			Agent24KnowledgeModeEnv, v, Agent24KnowledgeModeStandalone, Agent24KnowledgeModeIntegration)
	}
}

// ValidateAgent24IntegrationConfig checks the tenant settings integration mode requires.
func ValidateAgent24IntegrationConfig(cfg *config.Config) error {
	if cfg == nil || cfg.Tenant == nil {
		return errors.New("agent24 integration mode: tenant config is missing; configure the tenant section")
	}
	if !cfg.Tenant.IsRBACEnforced() {
		return errors.New("agent24 integration mode requires RBAC: set tenant.enable_rbac to true or remove it")
	}
	if cfg.Tenant.EnableCrossTenantAccess {
		return errors.New("agent24 integration mode forbids cross-tenant access: " +
			"set tenant.enable_cross_tenant_access to false")
	}
	if _, err := Agent24KnowledgeTagKeyFromEnv(); err != nil {
		return err
	}
	if _, err := agent24ProcessingPolicyFromEnv(); err != nil {
		return err
	}
	// A configured UI session key must be valid. Unset is not an error: the UI routes stay unregistered.
	if _, _, err := agent24uisession.KeyFromEnv(); err != nil {
		return err
	}
	// A24-KB-38: a configured UI origin allowlist must be valid. Unset is not an error: no CORS at all.
	if _, err := Agent24KnowledgeUIOriginsFromEnv(); err != nil {
		return err
	}
	// A24-KB-39: a configured embed page directory must hold the build. Unset is not an error: no page routes.
	if _, err := Agent24KnowledgeEmbedAdminDirFromEnv(); err != nil {
		return err
	}
	return nil
}
