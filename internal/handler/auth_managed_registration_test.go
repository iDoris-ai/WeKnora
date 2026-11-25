// Agent24 fork product patch A24-KB-60 (KB-LITE-01a): managed registration stays invite-only.

package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
)

func TestManagedModeRejectsSelfServeRegistration(t *testing.T) {
	SetManagedMode(true)
	t.Cleanup(func() { SetManagedMode(false) })

	called := false
	us := &stubRegisterUserService{
		register: func(context.Context, *types.RegisterRequest) (*types.User, error) {
			called = true
			return &types.User{ID: "u1"}, nil
		},
	}
	h := NewAuthHandler(&config.Config{
		Auth: &config.AuthConfig{RegistrationMode: config.AuthRegistrationModeSelfServe},
	}, us, nil, &registrationModeSettings{mode: config.AuthRegistrationModeSelfServe}, nil)

	if got := h.resolveRegistrationMode(context.Background()); got != config.AuthRegistrationModeInviteOnly {
		t.Fatalf("managed registration mode = %q", got)
	}
	w := doRegister(t, newRegisterTestRouter(h), validRegisterBody())
	if w.Code != http.StatusForbidden {
		t.Fatalf("managed register status = %d body=%s", w.Code, w.Body.String())
	}
	if called {
		t.Fatal("UserService.Register was called")
	}
}

func TestManagedModeOffKeepsSelfServeRegistration(t *testing.T) {
	SetManagedMode(false)
	t.Cleanup(func() { SetManagedMode(false) })

	called := false
	us := &stubRegisterUserService{
		register: func(context.Context, *types.RegisterRequest) (*types.User, error) {
			called = true
			return &types.User{ID: "u1", Email: "alice@example.com"}, nil
		},
	}
	h := NewAuthHandler(&config.Config{
		Auth: &config.AuthConfig{RegistrationMode: config.AuthRegistrationModeSelfServe},
	}, us, nil, &registrationModeSettings{mode: config.AuthRegistrationModeSelfServe}, nil)

	if got := h.resolveRegistrationMode(context.Background()); got != config.AuthRegistrationModeSelfServe {
		t.Fatalf("standalone registration mode = %q", got)
	}
	w := doRegister(t, newRegisterTestRouter(h), validRegisterBody())
	if w.Code != http.StatusCreated {
		t.Fatalf("standalone register status = %d body=%s", w.Code, w.Body.String())
	}
	if !called {
		t.Fatal("UserService.Register was not called")
	}
}
