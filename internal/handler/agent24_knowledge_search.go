// Agent24 fork product patch A24-KB-10 (CONTRACT-READONLY-v1 r10 §3, R6.4, R9.3).
//
// POST /agent24/knowledge/search: the request (agent24_knowledge_search_plan.go), E, the branch plan,
// one upstream retrieval over the searchable KBs, then the hits read in one database snapshot
// (agent24_knowledge_search_hits.go).

package handler

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agent24TagKeyOnce reads AGENT24_KNOWLEDGE_TAG_KEY on its first call and returns that result afterwards
// (until ResetAgent24KnowledgeTagKeyForTest): the capability declaration, ready and search all read the key
// through it.
var agent24TagKeyOnce = sync.OnceValues(Agent24KnowledgeTagKeyFromEnv)

// ResetAgent24KnowledgeTagKeyForTest makes the next agent24SearchTagKey read the environment again. For tests;
// the swap is unsynchronized, so call it while no read is running.
func ResetAgent24KnowledgeTagKeyForTest() {
	agent24TagKeyOnce = sync.OnceValues(Agent24KnowledgeTagKeyFromEnv)
}

// agent24SearchTagKey is the revision identifier key (R5.1); nil when AGENT24_KNOWLEDGE_TAG_KEY is unset or
// invalid. Without it search and read are disabled (R6.4): the capability declaration leaves them out, ready
// reports them and the endpoints refuse, all from the one read of agent24TagKeyOnce. Only standalone can get here:
// integration mode refuses to start without the key.
func agent24SearchTagKey() []byte {
	key, err := agent24TagKeyOnce()
	if err != nil {
		return nil
	}
	return key
}

// agent24TagKeyMissingReason is the reason of operations.search and operations.read when they are disabled
// for want of the revision key.
const agent24TagKeyMissingReason = "tag_key_missing"

// agent24Searcher is the upstream retrieval (interfaces.KnowledgeBaseService.HybridSearch).
type agent24Searcher interface {
	HybridSearch(ctx context.Context, id string, params types.SearchParams) ([]*types.SearchResult, error)
}

// Agent24KnowledgeSearchHandler serves POST /agent24/knowledge/search.
type Agent24KnowledgeSearchHandler struct {
	key    []byte // nil: search is disabled
	probe  Agent24KnowledgeReadyProbe
	scope  agent24ScopeResolver
	index  agent24IndexReader
	search agent24Searcher
	hits   agent24HitReader
}

// NewAgent24KnowledgeSearchHandler builds the handler with the default dependencies; the key is taken once,
// at construction, from agent24SearchTagKey (the process-wide read).
func NewAgent24KnowledgeSearchHandler(
	db *gorm.DB, registry interfaces.RetrieveEngineRegistry,
	kbService interfaces.KnowledgeBaseService,
) *Agent24KnowledgeSearchHandler {
	return &Agent24KnowledgeSearchHandler{
		key:    agent24SearchTagKey(),
		probe:  agent24DefaultReadyProbe{db: db, registry: registry},
		scope:  agent24DefaultScopeResolver{kbs: kbService},
		index:  agent24DBIndexReader{db: db},
		search: kbService,
		hits:   agent24DBHitReader{db: db},
	}
}

type agent24EffectiveScope struct {
	KnowledgeBaseIDs []string `json:"knowledge_base_ids"`
}

// agent24SearchResponse is the R3 success body; Hits is never nil (serializes as []).
type agent24SearchResponse struct {
	ProtocolVersion int                    `json:"protocol_version"`
	RequestID       string                 `json:"request_id"`
	Generation      int64                  `json:"generation"`
	Completeness    string                 `json:"completeness"`
	EffectiveScope  agent24EffectiveScope  `json:"effective_scope"`
	AppliedBudget   agent24SearchBudget    `json:"applied_budget"`
	Hits            []agent24SearchHit     `json:"hits"`
	PartialFailures []agent24SearchFailure `json:"partial_failures,omitempty"`
}

// agent24StorageError classifies a database failure (resolving E, index facts, the search or read snapshot): clientGone
// when the caller aborted, write nothing (R7.1); timeout once the request deadline has passed; otherwise
// unavailable/dependency. The raw error is for the log only (R6.3).
func agent24StorageError(ctx context.Context, err error) (*agent24KnowledgeError, bool) {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return nil, true
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return agent24Timeout(), false
	}
	logger.Warnf(ctx, "agent24 knowledge: storage: %v", err)
	return agent24Unavailable("dependency"), false
}

// agent24FiniteScores reports whether every retrieval candidate has a finite score. A NaN or Inf anywhere
// leaves the ranking, and so the cut to max_results, untrustworthy; in a hit it would also fail json.Marshal.
func agent24FiniteScores(results []*types.SearchResult) bool {
	for _, r := range results {
		if r != nil && (math.IsNaN(r.Score) || math.IsInf(r.Score, 0)) {
			return false
		}
	}
	return true
}

// Search runs one search (R3). The decision order follows R6.4: shape, disabled operation, E,
// mixed_score_space, execution.
func (h *Agent24KnowledgeSearchHandler) Search(c *gin.Context) {
	body, ok := agent24ReadBody(c)
	if !ok {
		writeAgent24KnowledgeError(c, agent24InvalidRequest(), agent24KnowledgeEcho{})
		return
	}
	req, echo, kerr := parseAgent24SearchRequest(body)
	if kerr == nil && h.key == nil {
		kerr = agent24Forbidden("operation_disabled")
	}
	if kerr != nil {
		writeAgent24KnowledgeError(c, kerr, echo)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Duration(req.Budget.DeadlineMS)*time.Millisecond)
	defer cancel()
	plan, kerr, gone := h.plan(ctx, req.KBIDs)
	if gone {
		agent24NoteCancelled(c, echo, plan.Scope)
		return
	}
	if kerr != nil {
		agent24NoteScope(c, plan.Scope)
		writeAgent24KnowledgeError(c, kerr, echo)
		return
	}
	hits, searchErr, gone := h.retrieve(ctx, req, plan)
	if gone {
		agent24NoteCancelled(c, echo, plan.Scope)
		return
	}
	completeness, failures, kerr := agent24SearchOutcome(plan, searchErr)
	if kerr != nil {
		agent24NoteScope(c, plan.Scope)
		writeAgent24KnowledgeError(c, kerr, echo)
		return
	}
	outcome := completeness
	if completeness == "complete" {
		outcome = agent24AuditOK
	}
	resource, revision := agent24SearchAuditResource(hits)
	agent24NoteAudit(c, func(f *agent24AuditFacts) {
		f.Echo = echo
		f.Outcome = outcome
		if len(plan.Scope) > 0 {
			f.EffectiveScope = append([]string(nil), plan.Scope...)
		}
		f.Resource = resource
		f.Revision = revision
		f.ResultCount = len(hits)
	})
	c.JSON(http.StatusOK, agent24SearchResponse{
		ProtocolVersion: agent24KnowledgeProtocolVersion, RequestID: *echo.RequestID, Generation: *echo.Generation,
		Completeness: completeness, EffectiveScope: agent24EffectiveScope{KnowledgeBaseIDs: plan.Scope},
		AppliedBudget: req.Budget, Hits: hits, PartialFailures: failures,
	})
}

// plan computes E (empty: forbidden/empty_scope, R2.3), judges mixed_score_space before reading any index
// fact (R6.4), and makes the branch plan under the current operations.search state (R3.4–R3.6). gone: the
// caller aborted, write nothing.
func (h *Agent24KnowledgeSearchHandler) plan(ctx context.Context, ids []string) (
	agent24SearchPlan, *agent24KnowledgeError, bool,
) {
	storageErr, engineErr := h.probe.Storage(ctx), h.probe.RetrieveEngine(ctx)
	if storageErr != nil || engineErr != nil {
		logger.Warnf(ctx, "agent24 knowledge search: storage=%v retrieve_engine=%v", storageErr, engineErr)
	}
	kbs, err := h.scope.Resolve(ctx, ids)
	if err != nil {
		e, gone := agent24StorageError(ctx, err)
		return agent24SearchPlan{}, e, gone
	}
	if len(kbs) == 0 {
		return agent24SearchPlan{}, agent24Forbidden("empty_scope"), false
	}
	// E is known from here on. Keep it on the plan when a later check fails, so the audit can name it.
	scope := agent24CollectedKBIDs(kbs)
	if _, kerr := agent24SearchScoreSpace(kbs); kerr != nil {
		return agent24SearchPlan{Scope: scope}, kerr, false
	}
	facts := make([]agent24KBIndexFacts, len(kbs))
	for i, kb := range kbs {
		if facts[i], err = h.index.Facts(ctx, kb); err != nil {
			e, gone := agent24StorageError(ctx, err)
			return agent24SearchPlan{Scope: scope}, e, gone
		}
	}
	plan, kerr := agent24PlanSearch(agent24State(storageErr == nil && engineErr == nil), kbs, facts)
	return plan, kerr, false
}

// retrieve runs the one retrieval over plan.Searchable, without context expansion (R3.8), and reads its hits
// in one snapshot (R3.9). searchErr is the branch failure for agent24SearchOutcome: a retrieval error, a
// degraded retrieval (its store group failed in part; the group is the failure unit, R3.5, so its results are
// dropped), internal for a non-finite candidate score (the whole request fails, checked before degraded), or a
// snapshot error; timeout or gone whenever the deadline or the caller ended the retrieval or the snapshot. hits
// is never nil. gone: the caller aborted, write nothing.
func (h *Agent24KnowledgeSearchHandler) retrieve(ctx context.Context, req agent24SearchRequest,
	plan agent24SearchPlan,
) ([]agent24SearchHit, *agent24KnowledgeError, bool) {
	hits := []agent24SearchHit{}
	if len(plan.Searchable) == 0 {
		return hits, nil, false
	}
	rctx, degraded := types.WithRetrievalDegradedRecorder(ctx)
	results, err := h.search.HybridSearch(rctx, plan.Searchable[0], types.SearchParams{
		QueryText: req.Query, MatchCount: agent24ScopeBatch,
		KnowledgeBaseIDs: plan.Searchable, SkipContextEnrichment: true,
	})
	if err != nil {
		e, gone := classifyAgent24KnowledgeExecError(ctx, err)
		return hits, e, gone
	}
	if ctx.Err() != nil { // the caller or the deadline ended the retrieval; upstream may have swallowed it
		e, gone := agent24StorageError(ctx, ctx.Err())
		return hits, e, gone
	}
	if !agent24FiniteScores(results) { // before degraded: a broken score fails the request, dropped or not
		logger.Warnf(ctx, "agent24 knowledge search: non-finite candidate score")
		return hits, agent24Internal(), false
	}
	if degraded() {
		return hits, agent24Unavailable("dependency"), false
	}
	var cands []agent24SearchCandidate
	var chunkIDs []string
	for _, r := range results {
		if len(cands) == agent24ScopeBatch {
			break
		}
		if r != nil {
			cands = append(cands, agent24SearchCandidate{ChunkID: r.ID, Score: r.Score})
			chunkIDs = append(chunkIDs, r.ID)
		}
	}
	snap, err := h.hits.Snapshot(ctx, types.CallerFromContext(ctx).TenantID, plan.Searchable, chunkIDs)
	if err == nil {
		err = ctx.Err() // a snapshot that answered as the caller left or the deadline passed
	}
	if err != nil {
		e, gone := agent24StorageError(ctx, err)
		return hits, e, gone
	}
	if got := agent24AssembleHits(h.key, plan.ScoreSpace, cands, snap, req.Budget.MaxResults); got != nil {
		hits = got
	}
	return hits, nil, false
}
