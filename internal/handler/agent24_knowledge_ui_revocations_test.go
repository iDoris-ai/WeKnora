// Agent24 fork product patch A24-KB-32 (KB-C2-01b1, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.1, U2.13, U3.7).

package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func agent24UIRevokeFor(
	store interfaces.Agent24UISessionStore, recheck agent24CredentialRechecker,
) *Agent24KnowledgeUIRevocationsHandler {
	if recheck == nil {
		recheck = agent24FixedRecheck{}
	}
	return &Agent24KnowledgeUIRevocationsHandler{recheck: recheck, store: store}
}

func agent24UIIssueOne(
	t *testing.T, store interfaces.Agent24UISessionStore, id agent24UIIdentity, vph string,
) string {
	t.Helper()
	h := agent24UITicketsFor(store, agent24UIResolver(agent24UIKBs(id.tenant, "kb-a", "kb-b")...), nil)
	body := agent24UITicketJSON(agent24UITestTag(1), agent24UITestTag(2), vph, "7")
	w := agent24UIServe(id.context(), h.Issue, agent24AuditPathUITickets, body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		TicketID string `json:"ticket_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
	require.NotEmpty(t, resp.TicketID)
	return resp.TicketID
}

func agent24UIFamilyRow(t *testing.T, db *gorm.DB, vph string) types.Agent24UIFamily {
	t.Helper()
	var row types.Agent24UIFamily
	require.NoError(t, db.Where("vph = ?", vph).Take(&row).Error)
	return row
}

func agent24UIRequireNoContent(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.Empty(t, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func TestAgent24UIRevokeByTicketAndVPH(t *testing.T) {
	db, store := agent24UIHandlerDB(t)
	id := agent24UIHappyIdentity(nil)
	rev := agent24UIRevokeFor(store, nil)
	vph := agent24UITestTag(3)
	ticketID := agent24UIIssueOne(t, store, id, vph)

	w := agent24UIServe(id.context(), rev.Revoke, agent24AuditPathUIRevocations,
		`{"ticket_id":"`+ticketID+`"}`)
	agent24UIRequireNoContent(t, w)
	require.NotNil(t, agent24UITicketRow(t, db, ticketID).RevokedAt)
	require.Nil(t, agent24UIFamilyRow(t, db, vph).RevokedAt)

	vph2 := agent24UITestTag(4)
	ticket2 := agent24UIIssueOne(t, store, id, vph2)
	w = agent24UIServe(id.context(), rev.Revoke, agent24AuditPathUIRevocations, `{"vph":"`+vph2+`"}`)
	agent24UIRequireNoContent(t, w)
	require.Nil(t, agent24UITicketRow(t, db, ticket2).RevokedAt)
	require.NotNil(t, agent24UIFamilyRow(t, db, vph2).RevokedAt)
	require.Nil(t, agent24UIFamilyRow(t, db, vph).RevokedAt)
}

func TestAgent24UIRevokeOtherMissingAndRepeat(t *testing.T) {
	db, store := agent24UIHandlerDB(t)
	id := agent24UIHappyIdentity(nil)
	rev := agent24UIRevokeFor(store, nil)
	vph := agent24UITestTag(3)
	ticketID := agent24UIIssueOne(t, store, id, vph)
	path := agent24AuditPathUIRevocations

	other := id
	other.principal.ID = "principal-8"
	w := agent24UIServe(other.context(), rev.Revoke, path, `{"ticket_id":"`+ticketID+`"}`)
	agent24UIRequireNoContent(t, w)
	w = agent24UIServe(other.context(), rev.Revoke, path, `{"vph":"`+vph+`"}`)
	agent24UIRequireNoContent(t, w)
	require.Nil(t, agent24UITicketRow(t, db, ticketID).RevokedAt)
	require.Nil(t, agent24UIFamilyRow(t, db, vph).RevokedAt)

	w = agent24UIServe(id.context(), rev.Revoke, path,
		`{"ticket_id":"11111111-1111-4111-8111-111111111111"}`)
	agent24UIRequireNoContent(t, w)
	upper := strings.ToUpper(ticketID)
	require.NotEqual(t, ticketID, upper)
	// A non-canonical spelling of the caller's own id is invalid_request and revokes nothing.
	w = agent24UIServe(id.context(), rev.Revoke, path, `{"ticket_id":"`+upper+`"}`)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	w = agent24UIServe(id.context(), rev.Revoke, path, `{"vph":"`+agent24UITestTag(9)+`"}`)
	agent24UIRequireNoContent(t, w)
	require.Nil(t, agent24UITicketRow(t, db, ticketID).RevokedAt)
	require.Nil(t, agent24UIFamilyRow(t, db, vph).RevokedAt)
	require.Equal(t, int64(1), agent24UICount(t, db, &types.Agent24UITicket{}))

	w = agent24UIServe(id.context(), rev.Revoke, path, `{"ticket_id":"`+ticketID+`"}`)
	agent24UIRequireNoContent(t, w)
	first := *agent24UITicketRow(t, db, ticketID).RevokedAt
	w = agent24UIServe(id.context(), rev.Revoke, path, `{"ticket_id":"`+ticketID+`"}`)
	agent24UIRequireNoContent(t, w)
	require.True(t, first.Equal(*agent24UITicketRow(t, db, ticketID).RevokedAt))

	w = agent24UIServe(id.context(), rev.Revoke, path, `{"vph":"`+vph+`"}`)
	agent24UIRequireNoContent(t, w)
	familyAt := *agent24UIFamilyRow(t, db, vph).RevokedAt
	w = agent24UIServe(id.context(), rev.Revoke, path, `{"vph":"`+vph+`"}`)
	agent24UIRequireNoContent(t, w)
	require.True(t, familyAt.Equal(*agent24UIFamilyRow(t, db, vph).RevokedAt))
}

func TestAgent24UIRevokeNarrowedCredential(t *testing.T) {
	db, store := agent24UIHandlerDB(t)
	id := agent24UIHappyIdentity(nil)
	rev := agent24UIRevokeFor(store, nil)
	vph := agent24UITestTag(3)
	ticketID := agent24UIIssueOne(t, store, id, vph)

	soon := time.Now().Add(time.Second)
	narrow := id
	narrow.scope = &types.TenantAPIKeyScope{KeyID: 0}
	narrow.exp = &soon
	w := agent24UIServe(narrow.context(), rev.Revoke, agent24AuditPathUIRevocations,
		`{"ticket_id":"`+ticketID+`"}`)
	agent24UIRequireNoContent(t, w)
	require.NotNil(t, agent24UITicketRow(t, db, ticketID).RevokedAt)
	require.Nil(t, agent24UIFamilyRow(t, db, vph).RevokedAt)
}

func TestAgent24UIRevokeRecheck(t *testing.T) {
	db, store := agent24UIHandlerDB(t)
	id := agent24UIHappyIdentity(nil)
	vph := agent24UITestTag(3)
	ticketID := agent24UIIssueOne(t, store, id, vph)
	body := `{"ticket_id":"` + ticketID + `"}`

	revoked := agent24UIRevokeFor(store, agent24FixedRecheck{reason: "credential_revoked"})
	w := agent24UIServe(id.context(), revoked.Revoke, agent24AuditPathUIRevocations, body)
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	code, reason, _ := agent24UIError(t, w)
	require.Equal(t, "unauthorized", code)
	require.Equal(t, "credential_revoked", reason)
	require.Nil(t, agent24UITicketRow(t, db, ticketID).RevokedAt)

	logs := agent24CaptureCredentialLogs(t)
	broken := agent24UIRevokeFor(store, agent24FixedRecheck{err: errors.New("S3NT1NEL-recheck")})
	w = agent24UIServe(id.context(), broken.Revoke, agent24AuditPathUIRevocations, body)
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	code, _, _ = agent24UIError(t, w)
	require.Equal(t, "internal", code)
	require.NotContains(t, w.Body.String(), "S3NT1NEL-recheck")
	require.NotContains(t, logs.String(), "S3NT1NEL-recheck")
	require.Nil(t, agent24UITicketRow(t, db, ticketID).RevokedAt)
}

func TestAgent24UIRevokeStoreError(t *testing.T) {
	id := agent24UIHappyIdentity(nil)
	store := agent24UIFailStore{err: errors.New("S3NT1NEL-revoke")}
	rev := agent24UIRevokeFor(store, nil)
	logs := agent24CaptureCredentialLogs(t)
	bodies := []string{
		`{"ticket_id":"11111111-1111-4111-8111-111111111111"}`,
		`{"vph":"` + agent24UITestTag(3) + `"}`,
	}
	for _, body := range bodies {
		w := agent24UIServe(id.context(), rev.Revoke, agent24AuditPathUIRevocations, body)
		require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
		code, _, _ := agent24UIError(t, w)
		require.Equal(t, "internal", code)
		require.NotContains(t, w.Body.String(), "S3NT1NEL-revoke")
	}
	require.NotContains(t, logs.String(), "S3NT1NEL-revoke")
}

func TestAgent24UIRevokeShape(t *testing.T) {
	id := agent24UIHappyIdentity(nil)
	rev := agent24UIRevokeFor(agent24UIFailStore{err: errors.New("S3NT1NEL-shape")}, nil)
	vph := agent24UITestTag(3)
	validID := "11111111-1111-4111-8111-111111111111"
	cases := []string{
		`{"ticket_id":"` + validID + `","vph":"` + vph + `"}`,
		`{}`,
		`{"extra":1}`,
		`{"ticket_id":"` + validID + `","extra":true}`,
		`{"ticket_id":"not-a-uuid"}`,
		`{"ticket_id":"6BA7B810-9DAD-11D1-80B4-00C04FD430C8"}`,
		`{"ticket_id":"urn:uuid:6ba7b810-9dad-11d1-80b4-00c04fd430c8"}`,
		`{"ticket_id":"{6ba7b810-9dad-11d1-80b4-00c04fd430c8}"}`,
		`{"ticket_id":"6ba7b8109dad11d180b400c04fd430c8"}`,
		`{"ticket_id":1}`,
		`{"vph":"` + strings.Repeat("A", 42) + `"}`,
		`{"vph":"` + strings.Repeat("A", 44) + `"}`,
		`{"vph":"` + strings.Repeat("A", 42) + `="}`,
		`{"vph":1}`,
		`null`,
		`[]`,
		`"x"`,
		`{"ticket_id":"` + validID + `"}{"a":1}`,
	}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			w := agent24UIServe(id.context(), rev.Revoke, agent24AuditPathUIRevocations, body)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			code, reason, details := agent24UIError(t, w)
			require.Equal(t, "invalid_request", code)
			require.Empty(t, reason)
			require.Nil(t, details["request_id"])
			require.Nil(t, details["generation"])
			require.NotContains(t, w.Body.String(), "S3NT1NEL-shape")
		})
	}
}

func TestAgent24UIRevokeAudit(t *testing.T) {
	db, store := agent24UIHandlerDB(t)
	id := agent24UIHappyIdentity(nil)
	rev := agent24UIRevokeFor(store, nil)
	vph := agent24UITestTag(3)
	ticketID := agent24UIIssueOne(t, store, id, vph)

	stub := &agent24AuditStub{}
	w := agent24UIAuditServe(t, stub, id, agent24AuditPathUIRevocations,
		`{"ticket_id":"`+ticketID+`"}`, rev.Revoke)
	agent24UIRequireNoContent(t, w)
	require.Len(t, stub.entries, 1)
	entry := stub.entries[0]
	require.Equal(t, types.AuditAction("knowledge.ui.revoke"), entry.Action)
	require.Equal(t, types.AuditOutcomeSuccess, entry.Outcome)
	var details struct {
		Outcome  string         `json:"outcome"`
		Resource map[string]any `json:"resource"`
	}
	require.NoError(t, json.Unmarshal(entry.Details, &details))
	require.Equal(t, "ok", details.Outcome)
	require.Len(t, details.Resource, 2)
	require.Equal(t, "ticket", details.Resource["target"])
	idHash := agent24uisession.H("kb-ticket-id-v1", []byte(ticketID))
	require.Equal(t, idHash, details.Resource["ticket_id_hash"])
	blob := agent24AuditBlob(t, entry)
	require.NotContains(t, blob, ticketID)
	require.NotContains(t, blob, vph)
	require.Empty(t, agent24UIUnexpectedTokens(blob, idHash))
	require.NotNil(t, agent24UITicketRow(t, db, ticketID).RevokedAt)

	vph2 := agent24UITestTag(4)
	ticket2 := agent24UIIssueOne(t, store, id, vph2)
	stub = &agent24AuditStub{}
	w = agent24UIAuditServe(t, stub, id, agent24AuditPathUIRevocations, `{"vph":"`+vph2+`"}`, rev.Revoke)
	agent24UIRequireNoContent(t, w)
	require.Len(t, stub.entries, 1)
	entry = stub.entries[0]
	require.Equal(t, types.AuditAction("knowledge.ui.revoke"), entry.Action)
	require.Equal(t, types.AuditOutcomeSuccess, entry.Outcome)
	details.Resource = nil // decoding into a used map keeps its old keys
	require.NoError(t, json.Unmarshal(entry.Details, &details))
	require.Equal(t, "ok", details.Outcome)
	require.Len(t, details.Resource, 2)
	require.Equal(t, "family", details.Resource["target"])
	vphHash := agent24uisession.H("kb-vph-audit-v1", []byte(vph2))
	require.Equal(t, vphHash, details.Resource["vph_hash"])
	blob = agent24AuditBlob(t, entry)
	require.NotContains(t, blob, vph2)
	require.NotContains(t, blob, ticket2)
	require.Empty(t, agent24UIUnexpectedTokens(blob, vphHash))
	require.Nil(t, agent24UITicketRow(t, db, ticket2).RevokedAt)
	require.NotNil(t, agent24UIFamilyRow(t, db, vph2).RevokedAt)
}
