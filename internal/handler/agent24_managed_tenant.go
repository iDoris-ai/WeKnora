// Agent24 fork product patch A24-KB-61 (KB-LITE-02a, G6): the managed host's provisioned tenant.

package handler

import "sync/atomic"

var agent24ManagedTenant atomic.Uint64

// SetAgent24ManagedTenant records the tenant the managed host provisioned at startup. Only a managed process
// calls it, after provisioning and before serving requests.
func SetAgent24ManagedTenant(id uint64) { agent24ManagedTenant.Store(id) }

// Agent24ManagedTenantIs reports whether tenant is the managed host's provisioned tenant. Outside managed mode,
// or before provisioning, it is false for every tenant.
func Agent24ManagedTenantIs(tenant uint64) bool {
	return tenant != 0 && ManagedMode() && agent24ManagedTenant.Load() == tenant
}
