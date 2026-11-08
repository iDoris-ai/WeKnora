// Agent24 fork product patch A24-KB-40 (WeKnora#135).
package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

var agent24WriteCapabilityValues = []APIKeyCapability{
	APIKeyCapabilityKBCandidatePropose, APIKeyCapabilityKBCandidateReview,
	APIKeyCapabilityKBCandidateApprove, APIKeyCapabilityKBEntryWithdraw,
}

func TestAgent24WriteCapabilitiesNormalize(t *testing.T) {
	assert.Equal(t, []APIKeyCapability{
		"kb_candidate_propose", "kb_candidate_review", "kb_candidate_approve",
		"kb_entry_withdraw",
	}, agent24WriteCapabilityValues)
	for _, c := range agent24WriteCapabilityValues {
		assert.Equal(t, c, NormalizeAPIKeyCapability(c))
		assert.Equal(t, c, NormalizeAPIKeyCapability(" "+APIKeyCapability(strings.ToUpper(string(c)))+" "))
	}
	assert.Equal(t, APIKeyCapability(""), NormalizeAPIKeyCapability("kb_candidate_publish"))
	// the operation class name is not a capability value
	assert.Equal(t, APIKeyCapability(""), NormalizeAPIKeyCapability("candidate.propose"))
	assert.Equal(t, StringArray{"retrieve", "kb_candidate_propose"},
		NormalizeAPIKeyCapabilities(StringArray{"retrieve", "kb_candidate_propose", "KB_CANDIDATE_PROPOSE", "x"}))
}

// The write capabilities are their own grants: none of them is retrieve or ingest, and neither of those carries
// any of them; FullAccess does not imply them (HasCapability does not look at FullAccess).
func TestAgent24WriteCapabilitiesAreSeparateGrants(t *testing.T) {
	all := StringArray{"kb_candidate_propose", "kb_candidate_review", "kb_candidate_approve", "kb_entry_withdraw"}
	writer := TenantAPIKeyScope{Capabilities: all}
	assert.False(t, writer.HasCapability(APIKeyCapabilityIngest))
	assert.False(t, writer.HasCapability(APIKeyCapabilityRetrieve))
	for _, base := range []TenantAPIKeyScope{
		{Capabilities: StringArray{"retrieve", "ingest", "manage_kbs"}},
		{FullAccess: true},
		{FullAccess: true, Capabilities: StringArray{"retrieve"}},
	} {
		for _, c := range agent24WriteCapabilityValues {
			assert.False(t, base.HasCapability(c), "%+v %s", base, c)
		}
	}
	for _, c := range agent24WriteCapabilityValues {
		assert.True(t, writer.HasCapability(c), c)
	}
}

// Roll back to a build without A24-KB-40: an old Normalize drops the values it does not know, so a key that
// carries them holds no write grant there (fail closed). The old switch is modelled by dropping exactly these.
func TestAgent24WriteCapabilitiesFailClosedOnRollback(t *testing.T) {
	stored := StringArray{"retrieve", "kb_candidate_propose", "kb_candidate_approve"}
	old := make(StringArray, 0, len(stored))
	for _, c := range NormalizeAPIKeyCapabilities(stored) {
		known := true
		for _, w := range agent24WriteCapabilityValues {
			if c == string(w) {
				known = false
			}
		}
		if known {
			old = append(old, c)
		}
	}
	assert.Equal(t, StringArray{"retrieve"}, old)
	scope := TenantAPIKeyScope{Capabilities: old}.Normalize()
	for _, c := range agent24WriteCapabilityValues {
		assert.False(t, scope.HasCapability(c))
	}
	assert.True(t, scope.HasCapability(APIKeyCapabilityRetrieve))
}
