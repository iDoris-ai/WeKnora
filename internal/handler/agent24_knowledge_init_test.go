// Agent24 fork product patch A24-KB-53 (KB-C4-02a mock, Loop 7 first round F2).
package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
)

const agent24InitTestCode = "mock-activation-code-for-tests"

type agent24InitFakeKBs struct {
	mu      sync.Mutex
	kbs     map[string]bool
	block   chan struct{}
	entered chan struct{}
}

func (f *agent24InitFakeKBs) Create(_ context.Context, _ uint64, kbID string) error {
	if f.block != nil {
		close(f.entered)
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kbs[kbID] = true
	return nil
}

func (f *agent24InitFakeKBs) Remove(_ context.Context, _ uint64, kbID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.kbs, kbID)
	return nil
}

func (f *agent24InitFakeKBs) Exists(_ context.Context, _ uint64, kbID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.kbs[kbID], nil
}

func agent24InitActivationFile(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(agent24InitTestCode))
	body := `{"activations":[{"code_sha256":"` + hex.EncodeToString(sum[:]) +
		`","tenant_id":7,"template_kb_id":"tmpl","config_version":"cfg-1"}]}`
	path := filepath.Join(t.TempDir(), "activations.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

type agent24InitHarness struct {
	h      *Agent24KnowledgeInitHandler
	kbs    *agent24InitFakeKBs
	issued []string // owner|kb of each key issued
}

func newAgent24InitHarness(t *testing.T) *agent24InitHarness {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, repository.EnsureAgent24DefaultKBTable(db))
	activations, err := service.LoadAgent24MockActivations(agent24InitActivationFile(t))
	require.NoError(t, err)
	x := &agent24InitHarness{kbs: &agent24InitFakeKBs{kbs: map[string]bool{}}}
	x.h = &Agent24KnowledgeInitHandler{
		activations: activations, store: repository.NewAgent24DefaultKBStore(db),
		provision: func(string) service.Agent24DefaultKBProvisioner { return x.kbs },
		limit:     newAgent24UIExchangeLimiter(), now: time.Now,
	}
	x.h.issue = func(_ *gin.Context, _ uint64, owner, kbID string) (service.Agent24OnboardKey, error) {
		x.issued = append(x.issued, owner+"|"+kbID)
		return service.Agent24OnboardKey{Token: "sk-test-" + kbID, KeyID: uint64(len(x.issued))}, nil
	}
	return x
}

func (x *agent24InitHarness) post(body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST(agent24InitPath, x.h.Init)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, agent24InitPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func agent24InitBody(code, key string) string {
	b, _ := json.Marshal(map[string]string{"activation_code": code, "idempotency_key": key})
	return string(b)
}

func agent24InitErrorReason(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	reason, _ := out.Error.Details["reason"].(string)
	return out.Error.Code + "/" + reason
}

const (
	agent24InitKeyA = "aaaaaaaaaaaaaaaaaaaaaaaa"
	agent24InitKeyB = "bbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestAgent24KnowledgeInitCreatesThenReportsExisting(t *testing.T) {
	x := newAgent24InitHarness(t)
	w := x.post(agent24InitBody(agent24InitTestCode, agent24InitKeyA))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	var first agent24InitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &first))
	require.Equal(t, "created", first.Outcome)
	require.Equal(t, "cfg-1", first.ConfigVersion)
	require.Equal(t, "sk-test-"+first.KnowledgeBaseID, first.Credential.APIKey)

	// a second activation, under a new key, gets the same KB and a new credential for it
	w = x.post(agent24InitBody(agent24InitTestCode, agent24InitKeyB))
	require.Equal(t, http.StatusOK, w.Code)
	var again agent24InitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &again))
	require.Equal(t, "existing", again.Outcome)
	require.Equal(t, first.KnowledgeBaseID, again.KnowledgeBaseID)
	require.Len(t, x.kbs.kbs, 1)
	require.Equal(t, []string{
		"api_tenant:7|" + first.KnowledgeBaseID, "api_tenant:7|" + first.KnowledgeBaseID,
	}, x.issued, "the owner is the tenant principal, never taken from the body")
}

func TestAgent24KnowledgeInitRefusesAWrongCode(t *testing.T) {
	x := newAgent24InitHarness(t)
	w := x.post(agent24InitBody("not-the-code", agent24InitKeyA))
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, "unauthorized/credential_invalid", agent24InitErrorReason(t, w))
	require.NotContains(t, w.Body.String(), "not-the-code")
	require.Empty(t, x.kbs.kbs)
	require.Empty(t, x.issued)
}

func TestAgent24KnowledgeInitChecksTheShapeBeforeTheCode(t *testing.T) {
	x := newAgent24InitHarness(t)
	for body, want := range map[string]string{
		agent24InitBody(agent24InitTestCode, ""):      "invalid_request/idempotency_key_missing",
		agent24InitBody(agent24InitTestCode, "short"): "invalid_request/",
		agent24InitBody("", agent24InitKeyA):          "invalid_request/",
		`{"activation_code":"` + agent24InitTestCode + `","idempotency_key":"` + agent24InitKeyA +
			`","tenant_id":8}`: "invalid_request/",
		`not json`: "invalid_request/",
	} {
		w := x.post(body)
		require.Equal(t, http.StatusBadRequest, w.Code, body)
		require.Equal(t, want, agent24InitErrorReason(t, w), body)
	}
	require.Empty(t, x.kbs.kbs)
}

func TestAgent24KnowledgeInitWhileAnotherIsCreatingIsInProgress(t *testing.T) {
	x := newAgent24InitHarness(t)
	x.kbs.block, x.kbs.entered = make(chan struct{}), make(chan struct{})
	done := make(chan int, 1)
	go func() { done <- x.post(agent24InitBody(agent24InitTestCode, agent24InitKeyA)).Code }()
	<-x.kbs.entered
	w := x.post(agent24InitBody(agent24InitTestCode, agent24InitKeyB))
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Equal(t, "unavailable/request_in_progress", agent24InitErrorReason(t, w))
	close(x.kbs.block)
	require.Equal(t, http.StatusOK, <-done)
	require.Len(t, x.kbs.kbs, 1)
}

func TestAgent24KnowledgeInitKeyFailureKeepsTheKBForARetry(t *testing.T) {
	x := newAgent24InitHarness(t)
	ok := x.h.issue
	x.h.issue = func(*gin.Context, uint64, string, string) (service.Agent24OnboardKey, error) {
		return service.Agent24OnboardKey{}, errors.New("db down")
	}
	w := x.post(agent24InitBody(agent24InitTestCode, agent24InitKeyA))
	require.Equal(t, http.StatusInternalServerError, w.Code)
	x.h.issue = ok
	w = x.post(agent24InitBody(agent24InitTestCode, agent24InitKeyA))
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, x.kbs.kbs, 1)
}

func TestAgent24KnowledgeInitRouteExistsOnlyInIntegrationModeWithTheFile(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	file := agent24InitActivationFile(t)

	t.Setenv(Agent24KnowledgeModeEnv, Agent24KnowledgeModeStandalone)
	t.Setenv(Agent24MockActivationFileEnv, file)
	h, err := NewAgent24KnowledgeInitHandler(db, nil, nil, nil)
	require.NoError(t, err)
	require.Nil(t, h, "standalone never registers it")

	t.Setenv(Agent24KnowledgeModeEnv, Agent24KnowledgeModeIntegration)
	t.Setenv(Agent24MockActivationFileEnv, "")
	h, err = NewAgent24KnowledgeInitHandler(db, nil, nil, nil)
	require.NoError(t, err)
	require.Nil(t, h, "no file, no route")

	require.NoError(t, os.Chmod(file, 0o644))
	t.Setenv(Agent24MockActivationFileEnv, file)
	_, err = NewAgent24KnowledgeInitHandler(db, nil, nil, nil)
	require.Error(t, err, "an unusable file stops the container")

	require.NoError(t, os.Chmod(file, 0o600))
	h, err = NewAgent24KnowledgeInitHandler(db, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, h)
	require.True(t, db.Migrator().HasTable("agent24_default_kbs"))
}

// #174: two inits of one owner whose KB already exists (no lease to collide on) are serialised in this process:
// while one is issuing a key the other hears request_in_progress.
func TestAgent24KnowledgeInitSerialisesOneOwnerInThisProcess(t *testing.T) {
	x := newAgent24InitHarness(t)
	require.Equal(t, http.StatusOK, x.post(agent24InitBody(agent24InitTestCode, agent24InitKeyA)).Code)
	entered, release := make(chan struct{}), make(chan struct{})
	ok := x.h.issue
	var once sync.Once
	x.h.issue = func(c *gin.Context, tenantID uint64, owner, kbID string) (service.Agent24OnboardKey, error) {
		blocked := false
		once.Do(func() { blocked = true })
		if blocked {
			close(entered)
			<-release
		}
		return ok(c, tenantID, owner, kbID)
	}
	done := make(chan int, 1)
	go func() { done <- x.post(agent24InitBody(agent24InitTestCode, agent24InitKeyB)).Code }()
	<-entered
	w := x.post(agent24InitBody(agent24InitTestCode, "cccccccccccccccccccccccc"))
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Equal(t, "unavailable/request_in_progress", agent24InitErrorReason(t, w))
	close(release)
	require.Equal(t, http.StatusOK, <-done)
}

// #174 Nit: a body with anything after the one JSON object is refused.
func TestAgent24KnowledgeInitRefusesTrailingContent(t *testing.T) {
	x := newAgent24InitHarness(t)
	for _, tail := range []string{`{"x":1}`, `garbage`} {
		w := x.post(agent24InitBody(agent24InitTestCode, agent24InitKeyA) + tail)
		require.Equal(t, http.StatusBadRequest, w.Code, tail)
	}
	require.Empty(t, x.kbs.kbs)
}
