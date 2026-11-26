// Agent24 fork product patch A24-KB-61 (KB-LITE-02a, G4/G6): provision the managed host before serving.

package main

import (
	"context"
	"fmt"
	"os"

	"go.uber.org/dig"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/agent24managed"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// provisionManaged runs agent24managed.Provisioner and records the tenant for the ingest check. A failure exits
// the process (status 1) before the handshake: the host sees no ready line and may start it again. The message
// carries the error class only, never the key.
func provisionManaged(c *dig.Container) agent24managed.Credential {
	var cred agent24managed.Credential
	err := c.Invoke(func(
		db *gorm.DB, users interfaces.UserService, tenants interfaces.TenantService,
		kbs interfaces.KnowledgeBaseService, keys interfaces.TenantAPIKeyService,
	) error {
		p := &agent24managed.Provisioner{DB: db, Users: users, Tenants: tenants, KBs: kbs, Keys: keys}
		tenantID, got, err := p.Provision(context.Background())
		if err != nil {
			return err
		}
		handler.SetAgent24ManagedTenant(tenantID)
		cred = got
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "managed provisioning failed: %v\n", err)
		os.Exit(1)
	}
	return cred
}
