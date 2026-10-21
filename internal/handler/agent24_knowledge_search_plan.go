// Agent24 fork product patch A24-KB-07 (CONTRACT-READONLY-v1 r10 §3 R3.2–R3.6, §6 R6.4).
//
// Search request parsing, budget clamping, branch planning over E and the R3.4 outcome. Pure
// functions; the endpoint wiring lands in a later slice (WeKnora#38).

package handler

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

const agent24SearchQueryMaxRunes = 4096

// agent24SearchBudget is the R3.3 budget; in a parsed request it holds the applied (clamped) values.
type agent24SearchBudget struct {
	DeadlineMS     int `json:"deadline_ms"`
	MaxResults     int `json:"max_results"`
	MaxConcurrency int `json:"max_concurrency"`
}

type agent24SearchRequest struct {
	Query  string
	KBIDs  []string // Q, deduped, request order
	Budget agent24SearchBudget
}

// agent24SearchFailure is one partial_failures item (R3.5).
type agent24SearchFailure struct {
	KnowledgeBaseID string `json:"knowledge_base_id"`
	Code            string `json:"code"`
	Reason          string `json:"reason"`
	Retryable       bool   `json:"retryable"`
}

// agent24ScoreSpaceGroup is one details.groups item of mixed_score_space (R3.6).
type agent24ScoreSpaceGroup struct {
	ScoreSpace       string   `json:"score_space"`
	KnowledgeBaseIDs []string `json:"knowledge_base_ids"`
}

// agent24SearchPlan splits E into branches. Every E id is in exactly one of Searchable, Empty, Failures.
type agent24SearchPlan struct {
	Scope      []string               // E ids, request order (effective_scope)
	ScoreSpace string                 // the single score_space of E
	Searchable []string               // has a servable chunk: searched in one retrieval call
	Empty      []string               // no counted documents: a successful branch with 0 hits (R3.4)
	Failures   []agent24SearchFailure // no searchable content (R3.5), E order
}

// parseAgent24SearchRequest validates the body after the common fields (R3.2, R3.3, R2.5).
func parseAgent24SearchRequest(body []byte) (agent24SearchRequest, agent24KnowledgeEcho, *agent24KnowledgeError) {
	fields, echo, kerr := parseAgent24KnowledgeCommon(body)
	if kerr != nil {
		return agent24SearchRequest{}, echo, kerr
	}
	if len(fields) != 6 || fields["query"] == nil || fields["scope"] == nil || fields["budget"] == nil ||
		fields["protocol_version"] == nil || fields["request_id"] == nil || fields["generation"] == nil {
		return agent24SearchRequest{}, echo, agent24InvalidRequest()
	}
	var query string
	if json.Unmarshal(fields["query"], &query) != nil || utf8.RuneCountInString(query) < 1 ||
		utf8.RuneCountInString(query) > agent24SearchQueryMaxRunes {
		return agent24SearchRequest{}, echo, agent24InvalidRequest()
	}
	ids, ok := agent24ParseScope(fields["scope"])
	if !ok {
		return agent24SearchRequest{}, echo, agent24InvalidRequest()
	}
	var bf map[string]json.RawMessage
	if json.Unmarshal(fields["budget"], &bf) != nil || len(bf) != 3 || bf["deadline_ms"] == nil ||
		bf["max_results"] == nil || bf["max_concurrency"] == nil {
		return agent24SearchRequest{}, echo, agent24InvalidRequest()
	}
	d, ok1 := agent24JSONInt64(bf["deadline_ms"])
	r, ok2 := agent24JSONInt64(bf["max_results"])
	c, ok3 := agent24JSONInt64(bf["max_concurrency"])
	if !ok1 || !ok2 || !ok3 || d < 1 || r < 1 || c < 1 {
		return agent24SearchRequest{}, echo, agent24InvalidRequest()
	}
	return agent24SearchRequest{Query: query, KBIDs: ids, Budget: agent24SearchBudget{
		DeadlineMS:     int(min(d, int64(agent24KnowledgeMaxDeadlineMS))),
		MaxResults:     int(min(r, int64(agent24KnowledgeMaxResults))),
		MaxConcurrency: int(min(c, int64(agent24KnowledgeMaxConcurrency))),
	}}, echo, nil
}

// agent24PlanSearch groups E (kbs, request order; facts[i] belongs to kbs[i]) by score space and
// classifies each KB with agent24KBState. More than one score space is mixed_score_space (R3.6).
func agent24PlanSearch(kbs []*types.KnowledgeBase, facts []agent24KBIndexFacts) (
	agent24SearchPlan, *agent24KnowledgeError,
) {
	var groups []agent24ScoreSpaceGroup
	index := map[string]int{}
	for _, kb := range kbs {
		space := agent24ScoreSpace(kb)
		i, ok := index[space]
		if !ok {
			i = len(groups)
			index[space] = i
			groups = append(groups, agent24ScoreSpaceGroup{ScoreSpace: space})
		}
		groups[i].KnowledgeBaseIDs = append(groups[i].KnowledgeBaseIDs, kb.ID)
	}
	if len(groups) > 1 {
		return agent24SearchPlan{}, &agent24KnowledgeError{
			Code: agent24CodeInvalidRequest, Reason: "mixed_score_space",
			Extra: map[string]any{"groups": groups},
		}
	}
	var plan agent24SearchPlan
	if len(groups) == 1 {
		plan.ScoreSpace = groups[0].ScoreSpace
	}
	for i, kb := range kbs {
		plan.Scope = append(plan.Scope, kb.ID)
		st := agent24KBState(agent24OperationState{State: "ready"}, facts[i])
		switch {
		case st.State == "ready" && facts[i].Docs == 0:
			plan.Empty = append(plan.Empty, kb.ID)
		case st.State == "ready" && facts[i].Docs > 0:
			plan.Searchable = append(plan.Searchable, kb.ID)
		default:
			plan.Failures = append(plan.Failures, agent24SearchFailure{
				KnowledgeBaseID: kb.ID, Code: "unavailable",
				Reason: st.Reason, Retryable: st.Reason == "index_building",
			})
		}
	}
	return plan, nil
}

// agent24SearchOutcome applies R3.4/R3.5 once the single retrieval call over plan.Searchable has run.
// searchErr is its classified error (nil: it succeeded, or Searchable is empty). It returns the
// completeness and partial_failures of a 200, or the top-level error.
func agent24SearchOutcome(plan agent24SearchPlan, searchErr *agent24KnowledgeError) (
	string, []agent24SearchFailure, *agent24KnowledgeError,
) {
	if searchErr != nil && searchErr.Code != agent24CodeTimeout && searchErr.Code != agent24CodeUnavailable {
		return "", nil, searchErr
	}
	byID := map[string]agent24SearchFailure{}
	for _, f := range plan.Failures {
		byID[f.KnowledgeBaseID] = f
	}
	if searchErr != nil {
		reason := searchErr.Reason
		if searchErr.Code == agent24CodeTimeout {
			reason = "timeout"
		} else if reason == "" {
			reason = "dependency"
		}
		for _, id := range plan.Searchable {
			byID[id] = agent24SearchFailure{
				KnowledgeBaseID: id, Code: string(searchErr.Code),
				Reason: reason, Retryable: searchErr.Retryable,
			}
		}
	}
	var fs []agent24SearchFailure
	for _, id := range plan.Scope {
		if f, ok := byID[id]; ok {
			fs = append(fs, f)
		}
	}
	if len(plan.Empty) > 0 || (len(plan.Searchable) > 0 && searchErr == nil) {
		if len(fs) == 0 {
			return "complete", nil, nil
		}
		return "partial", fs, nil
	}
	allTimeout := true
	var blocked, fallback *agent24KnowledgeError
	for _, f := range fs {
		if f.Code != string(agent24CodeTimeout) {
			allTimeout = false
		}
		if blocked == nil && !f.Retryable {
			blocked = agent24Unavailable(f.Reason)
		}
		if fallback == nil && f.Code == string(agent24CodeUnavailable) {
			fallback = agent24Unavailable(f.Reason)
		}
	}
	e := agent24Timeout()
	if !allTimeout {
		e = fallback
		if blocked != nil {
			e = blocked
		}
		if e == nil { // unreachable: fs codes are timeout or unavailable (agent24PlanSearch, the check on top)
			return "", nil, agent24Internal()
		}
	}
	e.Extra = map[string]any{"partial_failures": fs}
	return "", nil, e
}
