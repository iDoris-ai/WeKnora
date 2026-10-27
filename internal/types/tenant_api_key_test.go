package types

import (
	"context"
	"testing"
)

func TestTenantAPIKeyScopeNormalizePreservesFullAccess(t *testing.T) {
	scope := TenantAPIKeyScope{FullAccess: true}.Normalize()
	if !scope.FullAccess {
		t.Fatal("normalized scope lost full access")
	}
}

func TestTenantAPIKeyScopeNormalizeDefaultsToScopedAccess(t *testing.T) {
	scope := TenantAPIKeyScope{}.Normalize()
	if scope.FullAccess {
		t.Fatal("empty scope must not become full access")
	}
}

func TestTenantAPIKeyScopeNormalizeDropsInvalidCapabilities(t *testing.T) {
	scope := TenantAPIKeyScope{
		Capabilities: StringArray{"chat", "bogus", "retrieve", "chat"},
	}.Normalize()
	want := StringArray{"chat", "retrieve"}
	if len(scope.Capabilities) != len(want) {
		t.Fatalf("capabilities = %#v, want %#v", scope.Capabilities, want)
	}
	for i := range want {
		if scope.Capabilities[i] != want[i] {
			t.Fatalf("capabilities = %#v, want %#v", scope.Capabilities, want)
		}
	}
}

func TestCredentialIDFromContext(t *testing.T) {
	if id, ok := CredentialIDFromContext(context.Background()); ok || id != "" {
		t.Fatalf("no scope: got %q, %v", id, ok)
	}
	zero := WithTenantAPIKeyScope(context.Background(), TenantAPIKeyScope{})
	if id, ok := CredentialIDFromContext(zero); ok || id != "" {
		t.Fatalf("key id 0: got %q, %v", id, ok)
	}
	ctx := WithTenantAPIKeyScope(context.Background(), TenantAPIKeyScope{KeyID: 42})
	got, ok := CredentialIDFromContext(ctx)
	if !ok || got != "key:42" {
		t.Fatalf("key id 42: got %q, %v", got, ok)
	}
}

func TestCredentialIDFromContextPrefersJTI(t *testing.T) {
	ctx := WithAgent24TokenJTI(context.Background(), "abc")
	ctx = WithTenantAPIKeyScope(ctx, TenantAPIKeyScope{KeyID: 42})
	got, ok := CredentialIDFromContext(ctx)
	if !ok || got != "jti:abc" {
		t.Fatalf("jti: got %q, %v", got, ok)
	}

	empty := WithAgent24TokenJTI(context.Background(), "")
	empty = WithTenantAPIKeyScope(empty, TenantAPIKeyScope{KeyID: 42})
	got, ok = CredentialIDFromContext(empty)
	if !ok || got != "key:42" {
		t.Fatalf("empty jti: got %q, %v", got, ok)
	}
}
