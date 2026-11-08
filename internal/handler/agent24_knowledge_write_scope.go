// Agent24 fork product patch A24-KB-40 (WeKnora#135, CONTRACT-OPS-v2 W1.1, W1.9 ③(a), W2.5): the credential
// scope of the Agent24 write operation classes, and the one entry that turns it into the W1.9 ③ outcome.
//
// No write route uses it yet (KB-SOP-02, WeKnora#146, adds them); the integration gate serves none.

package handler

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// Agent24WriteOp is a CONTRACT-OPS-v2 §2 write operation class.
type Agent24WriteOp string

// The four write operation classes of CONTRACT-OPS-v2 §2 that a credential can carry (W2.1).
const (
	Agent24WriteCandidatePropose Agent24WriteOp = "candidate.propose"
	Agent24WriteCandidateReview  Agent24WriteOp = "candidate.review"
	Agent24WriteCandidateApprove Agent24WriteOp = "candidate.approve"
	Agent24WriteEntryWithdraw    Agent24WriteOp = "entry.withdraw"
)

// agent24WriteCapabilities is the closed map from operation class to the credential capability that grants it.
// An operation class missing here is held by no credential.
var agent24WriteCapabilities = map[Agent24WriteOp]types.APIKeyCapability{
	Agent24WriteCandidatePropose: types.APIKeyCapabilityKBCandidatePropose,
	Agent24WriteCandidateReview:  types.APIKeyCapabilityKBCandidateReview,
	Agent24WriteCandidateApprove: types.APIKeyCapabilityKBCandidateApprove,
	Agent24WriteEntryWithdraw:    types.APIKeyCapabilityKBEntryWithdraw,
}

// agent24WriteGrants is the authoritative side A of W1.1: whether the principal holds the operation class in any
// scope (W1.9 ③(a), second half). Errors are storage failures.
type agent24WriteGrants interface {
	HoldsAnywhere(ctx context.Context, op Agent24WriteOp) (bool, error)
}

// agent24DefaultWriteGrants is A until the organisation roles of W2.5 are wired (WeKnora#146): candidate.propose
// needs only the target in E, so it is held; review, approve and withdraw are organisation-administrator
// grants and are held by nobody yet. Default deny.
type agent24DefaultWriteGrants struct{}

func (agent24DefaultWriteGrants) HoldsAnywhere(_ context.Context, op Agent24WriteOp) (bool, error) {
	return op == Agent24WriteCandidatePropose, nil
}

// agent24CredentialHoldsWrite reports whether the request's credential C carries the capability of op.
// FullAccess is not consulted (HasCapability does not look at it): a full-access key holds a write operation
// class only when it is granted explicitly. A platform key and a request without a tenant API key hold none.
func agent24CredentialHoldsWrite(ctx context.Context, op Agent24WriteOp) bool {
	capability, known := agent24WriteCapabilities[op]
	if !known {
		return false
	}
	scope, ok := types.TenantAPIKeyScopeFromContext(ctx)
	if !ok || scope.IsPlatform() {
		return false
	}
	return scope.HasCapability(capability)
}

// agent24WriteAuthorizer computes W1.9 ③(a) for a write request.
type agent24WriteAuthorizer struct {
	grants agent24WriteGrants
	scope  agent24ScopeResolver
}

// Authorize returns nil when op may go on to target resolution, in this order (the first that fails):
//  1. C does not carry op → forbidden/operation_not_allowed, before any target is looked at;
//  2. A grants op to the principal nowhere → forbidden/operation_not_allowed;
//  3. targetKB is named by the request and is not in E → forbidden/empty_scope.
//
// targetKB "" means the request names no knowledge base: its target is a candidate or a document, and whether
// that target's knowledge base is in E is W1.9 ③(b) (not_found), answered by InScope. gone reports that the
// caller went away (write nothing).
func (a agent24WriteAuthorizer) Authorize(
	ctx context.Context, op Agent24WriteOp, targetKB string,
) (kerr *agent24KnowledgeError, gone bool) {
	if !agent24CredentialHoldsWrite(ctx, op) {
		return agent24Forbidden("operation_not_allowed"), false
	}
	held, err := a.grants.HoldsAnywhere(ctx, op)
	if err != nil {
		return agent24StorageError(ctx, err)
	}
	if !held {
		return agent24Forbidden("operation_not_allowed"), false
	}
	if targetKB == "" {
		return nil, false
	}
	in, err := a.InScope(ctx, targetKB)
	if err != nil {
		return agent24StorageError(ctx, err)
	}
	if !in {
		return agent24Forbidden("empty_scope"), false
	}
	return nil, false
}

// InScope reports whether kbID is in the write E of the request: the key's own knowledge_base_ids, narrowed by
// the scope resolver (same tenant, existing). Like the UI entries (agent24_knowledge_ui_exchange.go), a write
// never reads an empty allow-list or a full-access key as "all" (R2.2–R2.4): both give an empty E. A request
// without a tenant API key has no write E.
func (a agent24WriteAuthorizer) InScope(ctx context.Context, kbID string) (bool, error) {
	scope, ok := types.TenantAPIKeyScopeFromContext(ctx)
	if !ok || scope.FullAccess || len(scope.KnowledgeBaseIDs) == 0 || kbID == "" {
		return false, nil
	}
	kbs, err := a.scope.Resolve(ctx, []string{kbID})
	if err != nil {
		return false, err
	}
	for _, kb := range kbs {
		if kb != nil && kb.ID == kbID {
			return true, nil
		}
	}
	return false, nil
}
