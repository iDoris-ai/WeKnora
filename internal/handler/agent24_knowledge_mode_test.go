package handler

import (
	"encoding/base64"
	"os"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
)

func TestAgent24KnowledgeModeFromEnv(t *testing.T) {
	cases := []struct {
		name, val, want string
		set, wantErr    bool
	}{
		{"unset", "", "standalone", false, false},
		{"empty", "", "standalone", true, false},
		{"standalone", "standalone", "standalone", true, false},
		{"integration", "integration", "integration", true, false},
		{"trimmed", " integration ", "integration", true, false},
		{"case", "Integration", "", true, true},
		{"unknown", "open", "", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(Agent24KnowledgeModeEnv, tc.val)
			if !tc.set {
				// t.Setenv above restores the original value after the subtest.
				_ = os.Unsetenv(Agent24KnowledgeModeEnv)
			}
			got, err := Agent24KnowledgeModeFromEnv()
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got (%q, %v), want %q (err=%v)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestValidateAgent24IntegrationConfig(t *testing.T) {
	t.Setenv(Agent24KnowledgeTagKeyEnv, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	f, tr := false, true
	cases := []struct {
		name    string
		cfg     *config.Config
		wantErr bool
	}{
		{"nil cfg", nil, true},
		{"nil tenant", &config.Config{}, true},
		{"rbac off", &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &f}}, true},
		{"rbac nil", &config.Config{Tenant: &config.TenantConfig{}}, false},
		{"cross tenant", &config.Config{Tenant: &config.TenantConfig{
			EnableRBAC: &tr, EnableCrossTenantAccess: true,
		}}, true},
		{"ok", &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &tr}}, false},
	}
	for _, tc := range cases {
		if err := ValidateAgent24IntegrationConfig(tc.cfg); (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v, wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
}

func TestAgent24KnowledgeCapabilityIntegrationMode(t *testing.T) {
	t.Setenv(Agent24KnowledgeModeEnv, "integration")
	if got := BuildAgent24KnowledgeCapability().Mode; got != "integration" {
		t.Fatalf("mode = %q, want integration", got)
	}
}
