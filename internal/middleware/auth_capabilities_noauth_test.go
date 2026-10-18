package middleware

import (
	"net/http"
	"testing"
)

// Agent24 A24-KB-01 (CONTRACT-READONLY-v1 §9 R9.5): the capability declaration
// is readable by any valid API key, but never anonymously.
func TestSystemCapabilitiesRequiresAuth(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if isNoAuthAPI("/api/v1/system/capabilities", method) {
			t.Fatalf("%s /api/v1/system/capabilities must not be in the no-auth allowlist", method)
		}
	}
	// Positive control: the allowlist check itself works.
	if !isNoAuthAPI("/health", http.MethodGet) {
		t.Fatal("expected GET /health to be in the no-auth allowlist")
	}
}
