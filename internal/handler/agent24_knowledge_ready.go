// Agent24 fork product patch A24-KB-02 (CONTRACT-READONLY-v1 §8): per-operation readiness.
// Limitation: only confirms a retrieve engine is configured (env-registered engine or a vector store
// recorded in the database); external vector stores are not dialed for connectivity, see WeKnora#54.

package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
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

type agent24DefaultReadyProbe struct {
	db       *gorm.DB
	registry interfaces.RetrieveEngineRegistry
}

func (p agent24DefaultReadyProbe) Storage(ctx context.Context) error {
	sqlDB, err := p.db.DB()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return sqlDB.PingContext(ctx)
}

func (p agent24DefaultReadyProbe) RetrieveEngine(ctx context.Context) error {
	if p.registry != nil && len(p.registry.GetAllRetrieveEngineServices()) > 0 {
		return nil
	}
	var count int64
	if err := p.db.WithContext(ctx).Model(&types.VectorStore{}).Limit(1).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return errors.New("no retrieve engine registered")
	}
	return nil
}

// Agent24KnowledgeReadyHandler serves POST /agent24/knowledge/ready.
type Agent24KnowledgeReadyHandler struct {
	probe Agent24KnowledgeReadyProbe
}

// NewAgent24KnowledgeReadyHandler builds the handler with the default probe.
func NewAgent24KnowledgeReadyHandler(
	db *gorm.DB, registry interfaces.RetrieveEngineRegistry,
) *Agent24KnowledgeReadyHandler {
	return &Agent24KnowledgeReadyHandler{probe: agent24DefaultReadyProbe{db: db, registry: registry}}
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

// Ready reports liveness and per-operation readiness. Scope is not supported yet.
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
		if k != "protocol_version" && k != "request_id" && k != "generation" { // scope: next slice
			writeAgent24KnowledgeError(c, agent24InvalidRequest(), echo)
			return
		}
	}
	ctx := c.Request.Context()
	storageErr := h.probe.Storage(ctx)
	engineErr := h.probe.RetrieveEngine(ctx)
	if storageErr != nil || engineErr != nil {
		logger.Warnf(ctx, "agent24 knowledge ready: storage=%v retrieve_engine=%v", storageErr, engineErr)
	}
	c.JSON(http.StatusOK, gin.H{
		"protocol_version": agent24KnowledgeProtocolVersion,
		"request_id":       echo.RequestID,
		"generation":       echo.Generation,
		"live":             true,
		"operations": gin.H{
			"search": agent24State(storageErr == nil && engineErr == nil),
			"read":   agent24State(storageErr == nil),
			"ingest": agent24OperationState{State: "disabled", Reason: "read_only_v1"},
		},
	})
}
