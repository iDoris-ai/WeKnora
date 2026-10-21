// Agent24 fork product patch A24-KB-02 (CONTRACT-READONLY-v1 §8): per-operation readiness.
// Limitation: only confirms a retrieve engine is configured (env-registered engine or a vector store
// recorded in the database); external vector stores are not dialed for connectivity, see WeKnora#54.

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// Agent24KnowledgeReadyProbe checks the dependencies behind each operation.
type Agent24KnowledgeReadyProbe interface {
	Storage(ctx context.Context) error
	RetrieveEngine(ctx context.Context) error
}

var agent24ReadyProbeTimeout = 2 * time.Second // one deadline for the probes and the scope lookup; tests shorten it

type agent24DefaultReadyProbe struct {
	db       *gorm.DB
	registry interfaces.RetrieveEngineRegistry
}

func (p agent24DefaultReadyProbe) Storage(ctx context.Context) error {
	sqlDB, err := p.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

func (p agent24DefaultReadyProbe) RetrieveEngine(ctx context.Context) error {
	if p.registry != nil && len(p.registry.GetAllRetrieveEngineServices()) > 0 {
		return nil
	}
	tenantID := types.CallerFromContext(ctx).TenantID
	if tenantID == 0 {
		return errors.New("no caller tenant")
	}
	var count int64
	if err := p.db.WithContext(ctx).Model(&types.VectorStore{}).Where("tenant_id = ?", tenantID).Limit(1).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return errors.New("no retrieve engine registered")
	}
	return nil
}

// agent24ScopeResolver returns E: the readable KBs of ids, in order; error = not a "not found".
type agent24ScopeResolver interface {
	Resolve(context.Context, []string) ([]*types.KnowledgeBase, error)
}
type agent24IndexReader interface {
	Facts(context.Context, *types.KnowledgeBase) (agent24KBIndexFacts, error)
}

// Agent24KnowledgeReadyHandler serves POST /agent24/knowledge/ready.
type Agent24KnowledgeReadyHandler struct {
	probe Agent24KnowledgeReadyProbe
	scope agent24ScopeResolver
	index agent24IndexReader
}

// NewAgent24KnowledgeReadyHandler builds the handler with the default dependencies.
func NewAgent24KnowledgeReadyHandler(
	db *gorm.DB, registry interfaces.RetrieveEngineRegistry,
	kbService interfaces.KnowledgeBaseService,
) *Agent24KnowledgeReadyHandler {
	return &Agent24KnowledgeReadyHandler{
		probe: agent24DefaultReadyProbe{db: db, registry: registry},
		scope: agent24DefaultScopeResolver{kbs: kbService},
		index: agent24DBIndexReader{db: db},
	}
}

type agent24OperationState struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

func agent24State(ok bool) agent24OperationState {
	if ok {
		return agent24OperationState{State: "ready"}
	}
	return agent24OperationState{State: "not_ready", Reason: "dependency"}
}

// agent24KBIndexFacts counts non-deleting docs; Building = pending/processing; Active = completed/finalizing
// (index build proxy until KB-C1-01c); HasTextChunks = enabled text chunk of a completed/finalizing/cancelled doc.
type agent24KBIndexFacts struct {
	Docs, Active, Building int
	HasTextChunks          bool
}

func agent24KBState(op agent24OperationState, f agent24KBIndexFacts) agent24OperationState {
	notReady := func(r string) agent24OperationState { return agent24OperationState{State: "not_ready", Reason: r} }
	switch {
	case op.State == "disabled":
		return notReady("operation_disabled")
	case op.State != "ready":
		return notReady("dependency")
	case f.Docs == 0 || f.HasTextChunks:
		return agent24OperationState{State: "ready"}
	case f.Active > 0:
		return notReady("unsupported_kb_type")
	case f.Building > 0:
		return notReady("index_building")
	default:
		return notReady("index_failed")
	}
}

func agent24ScoreSpace(kb *types.KnowledgeBase) string {
	if kb.EmbeddingModelID == "" {
		return "kb:" + kb.ID
	}
	if kb.VectorStoreID != nil && *kb.VectorStoreID != "" {
		return "emb:" + kb.EmbeddingModelID + "|vs:" + *kb.VectorStoreID
	}
	return "emb:" + kb.EmbeddingModelID
}

// agent24ParseScope accepts exactly {"knowledge_base_ids":[non-empty string, ...]} and dedups ids.
func agent24ParseScope(raw json.RawMessage) ([]string, bool) {
	var scope map[string]json.RawMessage
	var ids []string
	if json.Unmarshal(raw, &scope) != nil || len(scope) != 1 ||
		json.Unmarshal(scope["knowledge_base_ids"], &ids) != nil || len(ids) == 0 || slices.Contains(ids, "") {
		return nil, false
	}
	seen := make(map[string]bool, len(ids)) // keeps first occurrences, in order
	return slices.DeleteFunc(ids, func(id string) bool { dup := seen[id]; seen[id] = true; return dup }), true
}

// Ready reports liveness and per-operation readiness, plus per-KB readiness for a scope.
func (h *Agent24KnowledgeReadyHandler) Ready(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeAgent24KnowledgeError(c, agent24InvalidRequest(), agent24KnowledgeEcho{})
		return
	}
	fields, echo, kerr := parseAgent24KnowledgeCommon(body)
	if kerr != nil {
		writeAgent24KnowledgeError(c, kerr, echo)
		return
	}
	for k := range fields {
		if k != "protocol_version" && k != "request_id" && k != "generation" && k != "scope" {
			writeAgent24KnowledgeError(c, agent24InvalidRequest(), echo)
			return
		}
	}
	var ids []string
	if raw, ok := fields["scope"]; ok {
		if ids, ok = agent24ParseScope(raw); !ok {
			writeAgent24KnowledgeError(c, agent24InvalidRequest(), echo)
			return
		}
	}
	ctx := c.Request.Context()
	pctx, cancel := context.WithTimeout(ctx, agent24ReadyProbeTimeout)
	defer cancel()
	storageErr := h.probe.Storage(pctx)
	engineErr := h.probe.RetrieveEngine(pctx)
	if storageErr != nil || engineErr != nil {
		logger.Warnf(ctx, "agent24 knowledge ready: storage=%v retrieve_engine=%v", storageErr, engineErr)
	}
	search, read := agent24State(storageErr == nil && engineErr == nil), agent24State(storageErr == nil)
	resp := gin.H{
		"protocol_version": agent24KnowledgeProtocolVersion,
		"request_id":       echo.RequestID,
		"generation":       echo.Generation,
		"live":             true,
		"operations": gin.H{
			"search": search, "read": read,
			"ingest": agent24OperationState{State: "disabled", Reason: "read_only_v1"},
		},
	}
	if ids != nil {
		kbs, err := h.scope.Resolve(pctx, ids)
		var out []gin.H
		for i := 0; err == nil && i < len(kbs); i++ {
			var f agent24KBIndexFacts
			if f, err = h.index.Facts(pctx, kbs[i]); err == nil {
				out = append(out, gin.H{
					"knowledge_base_id": kbs[i].ID, "score_space": agent24ScoreSpace(kbs[i]),
					"search": agent24KBState(search, f), "read": agent24KBState(read, f),
				})
			}
		}
		if err != nil {
			logger.Warnf(ctx, "agent24 knowledge ready: scope: %v", err)
			writeAgent24KnowledgeError(c, agent24Unavailable("dependency"), echo)
			return
		}
		if len(out) == 0 {
			writeAgent24KnowledgeError(c, agent24Forbidden("empty_scope"), echo)
			return
		}
		resp["knowledge_bases"] = out
	}
	c.JSON(http.StatusOK, resp)
}
