package handler

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
)

func managedForTest(t *testing.T, tenant uint64) {
	t.Helper()
	SetManagedMode(true)
	SetAgent24ManagedTenant(tenant)
	t.Cleanup(func() { SetManagedMode(false); SetAgent24ManagedTenant(0) })
}

// G6: the managed host has no mock activation file, yet its ingest routes are registered.
func TestAgent24KnowledgeIngestConstructorManaged(t *testing.T) {
	ingest := &service.Agent24Ingest{}
	jobs := &repository.Agent24IngestJobStore{}
	candidates := &repository.Agent24CandidateStore{}
	t.Setenv(Agent24KnowledgeTagKeyEnv, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	t.Setenv(Agent24KnowledgeModeEnv, Agent24KnowledgeModeIntegration)
	t.Setenv(Agent24MockActivationFileEnv, "")

	h, err := NewAgent24KnowledgeIngestHandler(ingest, jobs, candidates, nil)
	require.NoError(t, err)
	require.Nil(t, h, "control: integration without a file and outside managed mode registers nothing")

	managedForTest(t, 1)
	h, err = NewAgent24KnowledgeIngestHandler(ingest, jobs, candidates, nil)
	require.NoError(t, err)
	require.NotNil(t, h)
}

// G6: on the managed host only the provisioned tenant may ingest; outside managed mode no tenant passes
// without the activation table.
func TestAgent24KnowledgeIngestManagedTenantOnly(t *testing.T) {
	e := newAgent24IngestEnv(t)
	e.h.activations = nil
	body := agent24IngestBody(t, nil, nil)
	as := agent24IngestAs([]string{"ingest"}) // tenant 1

	r := e.call(t, e.h.Submit, as, body)
	require.Equal(t, "forbidden/operation_not_allowed", r.outcome(), "not managed: %s", r.raw)

	managedForTest(t, 3)
	r = e.call(t, e.h.Submit, as, body)
	require.Equal(t, "forbidden/operation_not_allowed", r.outcome(), "another tenant: %s", r.raw)
	e.requireNoWrite(t)

	SetAgent24ManagedTenant(1)
	r = e.call(t, e.h.Submit, as, body)
	require.NotEqual(t, "forbidden/operation_not_allowed", r.outcome(), r.raw)
	require.Less(t, r.code, 300, r.raw)
}
