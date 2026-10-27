// Agent24 fork product patch A24-KB-15 (CONTRACT-READONLY-v1 r10 §4, R6.4, R9.3).
//
// POST /agent24/knowledge/read: the request (agent24_knowledge_read_request.go), E, then the document read
// in one database snapshot and judged (agent24_knowledge_read.go).

package handler

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// Agent24KnowledgeReadHandler serves POST /agent24/knowledge/read.
type Agent24KnowledgeReadHandler struct {
	key     []byte // nil: read is disabled
	scope   agent24ScopeResolver
	reads   agent24ReadReader
	recheck agent24CredentialRechecker
}

// NewAgent24KnowledgeReadHandler builds the handler with the default dependencies; the key is the
// process-wide read of agent24SearchTagKey, as for search and ready.
func NewAgent24KnowledgeReadHandler(
	db *gorm.DB, kbService interfaces.KnowledgeBaseService,
	tenants interfaces.TenantService, keys interfaces.TenantAPIKeyService,
	revocations interfaces.Agent24CredentialRevocationStore,
) *Agent24KnowledgeReadHandler {
	return &Agent24KnowledgeReadHandler{
		key:     agent24SearchTagKey(),
		scope:   agent24DefaultScopeResolver{kbs: kbService},
		reads:   agent24DBReadReader{db: db},
		recheck: newAgent24Rechecker(tenants, keys, revocations),
	}
}

// agent24ReadResponse is the R4 success body (§11.5); Segments is never nil, NextCursor is null at the end.
type agent24ReadResponse struct {
	ProtocolVersion int                   `json:"protocol_version"`
	RequestID       string                `json:"request_id"`
	Generation      int64                 `json:"generation"`
	EffectiveScope  agent24EffectiveScope `json:"effective_scope"`
	KnowledgeID     string                `json:"knowledge_id"`
	Revision        agent24DocRevision    `json:"revision"`
	Display         *agent24HitDisplay    `json:"display,omitempty"`
	Segments        []agent24ReadSegment  `json:"segments"`
	Truncated       bool                  `json:"truncated"`
	NextCursor      *string               `json:"next_cursor"`
}

// Read serves one read (R4). The decision order follows R6.4: shape, disabled operation, E, then the
// document, anchor and revision in one snapshot (agent24JudgeRead).
func (h *Agent24KnowledgeReadHandler) Read(c *gin.Context) {
	body, ok := agent24ReadBody(c)
	if !ok {
		writeAgent24KnowledgeError(c, agent24InvalidRequest(), agent24KnowledgeEcho{})
		return
	}
	req, echo, kerr := parseAgent24ReadRequest(h.key, body)
	if kerr == nil && h.key == nil {
		kerr = agent24Forbidden("operation_disabled")
	}
	if kerr != nil {
		writeAgent24KnowledgeError(c, kerr, echo)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Duration(req.DeadlineMS)*time.Millisecond)
	defer cancel()
	kbs, err := h.scope.Resolve(ctx, req.KBIDs)
	if err != nil {
		if e, gone := agent24StorageError(ctx, err); gone {
			agent24NoteCancelled(c, echo, nil)
		} else {
			writeAgent24KnowledgeError(c, e, echo)
		}
		return
	}
	if len(kbs) == 0 {
		writeAgent24KnowledgeError(c, agent24Forbidden("empty_scope"), echo)
		return
	}
	scope := agent24CollectedKBIDs(kbs)
	w := agent24ReadWindow{ChunkID: req.ChunkID, Limit: req.PageLimit}
	if req.ChunkID == "" && req.Cursor != nil {
		w.Position = req.Cursor.Position
	}
	snap, err := h.reads.Snapshot(ctx, types.CallerFromContext(ctx).TenantID, scope, req.KnowledgeID, w)
	if err == nil {
		err = ctx.Err() // a snapshot that answered as the caller left or the deadline passed
	}
	if err != nil {
		if e, gone := agent24StorageError(ctx, err); gone {
			agent24NoteCancelled(c, echo, scope)
		} else {
			agent24NoteScope(c, scope)
			writeAgent24KnowledgeError(c, e, echo)
		}
		return
	}
	// The judgement is written only after the recheck: a stale answer carries the current revisions (R4.5),
	// which a credential that no longer stands, or a document whose KB left E, must not see (R7.6). The
	// recheck stays the last step before writing, so its deadline check covers the judgement too.
	page, judged := agent24JudgeRead(h.key, req, snap)
	newScope, kerr, gone := agent24RecheckScope(ctx, h.recheck, h.scope, c.Request.Header, req.KBIDs, scope)
	if gone {
		agent24NoteCancelled(c, echo, scope)
		return
	}
	if kerr != nil {
		if kerr.Code == agent24CodeInternal || kerr.Code == agent24CodeTimeout || kerr.Code == agent24CodeUnavailable {
			agent24NoteScope(c, scope)
		}
		writeAgent24KnowledgeError(c, kerr, echo)
		return
	}
	if !slices.Contains(newScope, snap.KnowledgeBaseID) {
		agent24NoteScope(c, newScope)
		writeAgent24KnowledgeError(c, agent24NotFound(), echo)
		return
	}
	if judged != nil {
		agent24NoteScope(c, newScope)
		writeAgent24KnowledgeError(c, judged, echo)
		return
	}
	resource, revision := agent24ReadAuditResource(req.KnowledgeID, page.Revision, page.Segments)
	agent24NoteAudit(c, func(f *agent24AuditFacts) {
		f.Echo = echo
		f.Outcome = agent24AuditOK
		if len(newScope) > 0 {
			f.EffectiveScope = append([]string(nil), newScope...)
		}
		f.Resource = resource
		f.Revision = revision
		f.ResultCount = len(page.Segments)
	})
	c.JSON(http.StatusOK, agent24ReadResponse{
		ProtocolVersion: agent24KnowledgeProtocolVersion, RequestID: *echo.RequestID, Generation: *echo.Generation,
		EffectiveScope: agent24EffectiveScope{KnowledgeBaseIDs: newScope}, KnowledgeID: req.KnowledgeID,
		Revision: page.Revision, Display: page.Display, Segments: page.Segments, Truncated: page.Truncated,
		NextCursor: page.NextCursor,
	})
}
