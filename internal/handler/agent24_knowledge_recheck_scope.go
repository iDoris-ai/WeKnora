// Agent24 fork product patch A24-KB-23 (KB-C1-03 C3-d, WeKnora#41, CONTRACT-READONLY-v1 R7.6).

package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Tencent/WeKnora/internal/logger"
)

// agent24RecheckScope checks the credential again and resolves E again before a response is assembled
// (R7.6). scope is the E the request ran with; ids is the request's Q. It returns the narrowed E (scope
// ∩ the re-resolved E, in scope's order), or the error to answer, or gone when the caller aborted.
func agent24RecheckScope(
	ctx context.Context, recheck agent24CredentialRechecker, resolver agent24ScopeResolver,
	header http.Header, ids, scope []string,
) ([]string, *agent24KnowledgeError, bool) {
	if recheck == nil {
		logger.Warnf(ctx, "[agent24] credential recheck is not wired")
		return nil, agent24Internal(), false
	}
	rctx, reason, err := recheck.Recheck(ctx, header)
	if err != nil {
		if ctx.Err() != nil {
			e, gone := agent24StorageError(ctx, ctx.Err())
			return nil, e, gone
		}
		logger.Warnf(ctx, "[agent24] credential recheck failed: error_len=%d error_type=%T", len(fmt.Sprint(err)), err)
		return nil, agent24Internal(), false
	}
	if reason != "" {
		return nil, agent24Unauthorized(reason), false
	}
	kbs, err := resolver.Resolve(rctx, ids)
	if err != nil {
		e, gone := agent24StorageError(ctx, err)
		return nil, e, gone
	}
	fresh := agent24CollectedKBIDs(kbs)
	inFresh := make(map[string]struct{}, len(fresh))
	for _, id := range fresh {
		inFresh[id] = struct{}{}
	}
	narrowed := make([]string, 0, len(scope))
	for _, id := range scope {
		if _, ok := inFresh[id]; ok {
			narrowed = append(narrowed, id)
		}
	}
	if len(narrowed) == 0 {
		return nil, agent24Forbidden("empty_scope"), false
	}
	return narrowed, nil, false
}

// agent24NarrowSearch filters a search result down to scope (R7.6): hits outside it are dropped with no
// refill and the rest renumbered from 1 (R3.6); partial_failures keep only KBs in scope, and when filtering
// leaves none of a non-empty list completeness becomes complete. hits stays non-nil.
func agent24NarrowSearch(
	scope []string, completeness string, hits []agent24SearchHit, failures []agent24SearchFailure,
) (string, []agent24SearchHit, []agent24SearchFailure) {
	allowed := make(map[string]struct{}, len(scope))
	for _, id := range scope {
		allowed[id] = struct{}{}
	}
	outHits := make([]agent24SearchHit, 0, len(hits))
	for _, hit := range hits {
		if _, ok := allowed[hit.KnowledgeBaseID]; !ok {
			continue
		}
		hit.Rank = len(outHits) + 1
		outHits = append(outHits, hit)
	}
	if len(failures) == 0 {
		return completeness, outHits, nil
	}
	var outFailures []agent24SearchFailure
	for _, failure := range failures {
		if _, ok := allowed[failure.KnowledgeBaseID]; ok {
			outFailures = append(outFailures, failure)
		}
	}
	if len(outFailures) == 0 {
		return "complete", outHits, nil
	}
	return completeness, outHits, outFailures
}
