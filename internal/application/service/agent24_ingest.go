// Agent24 fork product patch A24-KB-41 (KB-C4-04, WeKnora#136, CONTRACT-OPS-v2 W1.3, W3.4, W3.6, W5.3, W5.4,
// W5.7, W5.10): the internal ingest entry. No integration route calls it in M3; KB-SOP-02 (WeKnora#146) calls it
// from candidate.approve, and the ingest probe calls it on the demo stack.
//
// An ingest is three steps, because W3.4 puts the source revision and the job in the caller's database
// transaction while the original's bytes go to object storage first:
//
//	Prepare (outside any transaction): check content_sha256 and that a worker can run the job, save the original.
//	Stage (inside the caller's tx): create or revise the knowledge row, end its earlier work, create the job.
//	Dispatch (after the commit): open the attempt and enqueue; an enqueue failure fails the job.
//
// A caller whose transaction fails calls Abandon, which deletes the saved original. A process that stops between
// Prepare and the commit leaves the bytes unreferenced: no knowledge row points at them, so no read surface can
// reach them (W3.4); reclaiming them is not done in M3.

package service

import (
	"context"
	"crypto/md5" //nolint:gosec // upstream file_hash is MD5; it is a dedupe key here, not a security property
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
)

// Agent24IngestExecutor reports whether a worker that will run document processing is alive. Prepare refuses when
// it is not, so a job is never created that nothing will pick up.
type Agent24IngestExecutor interface {
	Available(ctx context.Context) error
}

// Errors of the internal ingest entry. Callers map them to the contract envelope.
var (
	// ErrAgent24IngestUnavailable: no worker runs document processing (no Redis in integration mode, or no live
	// server consuming the default queue). Nothing was written.
	ErrAgent24IngestUnavailable = errors.New("agent24 ingest: the knowledge processing service is not running")
	// ErrAgent24ContentHashMismatch: the declared content_sha256 is not the content's (W3.6). Nothing was written.
	ErrAgent24ContentHashMismatch = errors.New("agent24 ingest: content_sha256 does not match the content")
	// ErrAgent24KnowledgeNotFound: a revision names a knowledge that is not in the tenant's target knowledge base.
	ErrAgent24KnowledgeNotFound = errors.New("agent24 ingest: knowledge not found")
	// ErrAgent24InvalidFileName: the file name is empty or not accepted by the upstream input validation.
	ErrAgent24InvalidFileName = errors.New("agent24 ingest: invalid file name")
	// ErrAgent24UnsupportedKB: the target knowledge base does not take document ingest (FAQ type).
	ErrAgent24UnsupportedKB = errors.New("agent24 ingest: the knowledge base does not take documents")
)

// The origin values of CONTRACT-OPS-v2 §6, stored as knowledge metadata agent24_origin.
const (
	Agent24OriginUpload = "upload"
	Agent24OriginEntry  = "entry"
)

// agent24OriginMetadataKey is the knowledge metadata key that records the document's origin.
const agent24OriginMetadataKey = "agent24_origin"

// Agent24IngestRequest is one submission. KnowledgeID empty creates a document; set, it revises that document
// (a new source revision of the same knowledge_id).
type Agent24IngestRequest struct {
	KnowledgeBaseID string
	KnowledgeID     string
	FileName        string
	Content         []byte
	ContentSHA256   string
	Origin          string
}

// Agent24IngestPrincipal is who established the job and under which versions (W1.3): recorded on the job, and
// the stage gates recheck it.
type Agent24IngestPrincipal struct {
	OpClass       types.Agent24JobOp
	UserID        string
	CredentialID  uint64
	AuthzVersion  string
	PolicyVersion string
	ConfigVersion string
	Generation    *int64
}

// Agent24IngestPrepared is the outcome of Prepare: the saved original, ready to stage.
type Agent24IngestPrepared struct {
	req         Agent24IngestRequest
	tenantID    uint64
	kb          *types.KnowledgeBase
	knowledgeID string
	filePath    string
	fileHash    string
	fileType    string
	fileSvc     interfaces.FileService
}

// KnowledgeID is the knowledge the submission creates or revises.
func (p *Agent24IngestPrepared) KnowledgeID() string { return p.knowledgeID }

// Agent24Ingest is the internal ingest entry.
type Agent24Ingest struct {
	ks   *knowledgeService
	jobs *repository.Agent24IngestJobStore
	exec Agent24IngestExecutor
	now  func() time.Time
}

// NewAgent24Ingest builds the entry over the upstream knowledge service (which must be this package's
// implementation: the entry reuses its storage, task and attempt plumbing).
func NewAgent24Ingest(
	ks interfaces.KnowledgeService, jobs *repository.Agent24IngestJobStore, exec Agent24IngestExecutor,
) (*Agent24Ingest, error) {
	impl, ok := ks.(*knowledgeService)
	if !ok || impl == nil || jobs == nil || exec == nil {
		return nil, errors.New("agent24 ingest: incomplete wiring")
	}
	return &Agent24Ingest{ks: impl, jobs: jobs, exec: exec, now: time.Now}, nil
}

// Prepare checks the submission and saves the original. It writes nothing to the database.
func (a *Agent24Ingest) Prepare(ctx context.Context, req Agent24IngestRequest) (*Agent24IngestPrepared, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return nil, errors.New("agent24 ingest: no tenant in context")
	}
	name, valid := secutils.ValidateInput(req.FileName)
	if !valid || name == "" || name != req.FileName {
		return nil, ErrAgent24InvalidFileName
	}
	sum := sha256.Sum256(req.Content)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), strings.TrimSpace(req.ContentSHA256)) {
		return nil, ErrAgent24ContentHashMismatch
	}
	if err := a.exec.Available(ctx); err != nil {
		logger.Warnf(ctx, "[agent24] ingest refused: executor unavailable: error_type=%T", err)
		return nil, ErrAgent24IngestUnavailable
	}
	kb, err := a.ks.kbService.GetKnowledgeBaseByID(ctx, req.KnowledgeBaseID)
	if err != nil {
		return nil, err
	}
	if kb == nil || kb.TenantID != tenantID {
		return nil, ErrAgent24KnowledgeNotFound
	}
	if kb.Type == types.KnowledgeBaseTypeFAQ {
		return nil, ErrAgent24UnsupportedKB
	}
	if err := a.ks.checkStorageEngineConfigured(ctx, kb); err != nil {
		return nil, err
	}
	knowledgeID := req.KnowledgeID
	if knowledgeID == "" {
		knowledgeID = uuid.New().String()
	}
	header, err := bytesToFileHeader(req.Content, req.FileName)
	if err != nil {
		return nil, err
	}
	fileSvc := a.ks.resolveFileService(ctx, kb)
	path, err := fileSvc.SaveFile(ctx, header, tenantID, knowledgeID)
	if err != nil {
		return nil, err
	}
	h := md5.Sum(req.Content) //nolint:gosec // see import
	return &Agent24IngestPrepared{
		req: req, tenantID: tenantID, kb: kb, knowledgeID: knowledgeID, filePath: path,
		fileHash: hex.EncodeToString(h[:]), fileType: getFileType(req.FileName), fileSvc: fileSvc,
	}, nil
}

// Abandon deletes the original of a submission whose transaction did not commit.
func (a *Agent24Ingest) Abandon(ctx context.Context, p *Agent24IngestPrepared) {
	if p == nil || p.filePath == "" {
		return
	}
	if err := p.fileSvc.DeleteFile(context.WithoutCancel(ctx), p.filePath); err != nil {
		logger.Warnf(ctx, "[agent24] ingest: deleting an abandoned original failed: error_type=%T", err)
	}
}

// Stage writes the submission inside tx: the knowledge row (created, or revised to the new original), the end of
// the knowledge's earlier work (W5.7 superseded), and the saved job. sourceRevision derives the job's
// source_revision from the row as staged (READONLY R5.1; the derivation lives with the integration handlers).
func (a *Agent24Ingest) Stage(
	ctx context.Context, tx *gorm.DB, p *Agent24IngestPrepared, who Agent24IngestPrincipal,
	sourceRevision func(*types.Knowledge) string,
) (*types.Agent24IngestJob, *types.Knowledge, error) {
	if tx == nil || p == nil || sourceRevision == nil {
		return nil, nil, errors.New("agent24 ingest: Stage needs a transaction, a prepared submission and a revision")
	}
	now := a.now().UTC()
	var k *types.Knowledge
	var err error
	if p.req.KnowledgeID == "" {
		k, err = a.stageCreate(ctx, tx, p, now)
	} else {
		k, err = a.stageRevise(ctx, tx, p, now)
	}
	if err != nil {
		return nil, nil, err
	}
	if err := a.jobs.SupersedeForKnowledge(ctx, tx, p.tenantID, k.ID, now); err != nil {
		return nil, nil, err
	}
	size := int64(len(p.req.Content))
	job := &types.Agent24IngestJob{
		ID: uuid.New().String(), TenantID: p.tenantID, KnowledgeBaseID: p.kb.ID, KnowledgeID: k.ID,
		OpClass: who.OpClass, PrincipalUserID: who.UserID, CredentialID: who.CredentialID,
		AuthzVersion: who.AuthzVersion, PolicyVersion: who.PolicyVersion, ConfigVersion: who.ConfigVersion,
		Generation: who.Generation, SourceRevision: sourceRevision(k),
		ContentSHA256: strings.ToLower(strings.TrimSpace(p.req.ContentSHA256)), FileType: p.fileType,
		Status: types.Agent24JobSaved, Enrichment: types.Agent24EnrichmentNone,
		Cost: types.Agent24JobCost{Items: []types.Agent24JobCostItem{
			{Kind: "storage", Units: &size, UnitType: "bytes", Measured: "exact"},
			{Kind: "embedding", Destination: p.kb.EmbeddingModelID, UnitType: "tokens", Measured: "unknown"},
		}},
		SavedAt: now, UpdatedAt: now,
	}
	if err := a.jobs.Create(ctx, tx, job); err != nil {
		return nil, nil, err
	}
	return job, k, nil
}

func (a *Agent24Ingest) stageCreate(
	ctx context.Context, tx *gorm.DB, p *Agent24IngestPrepared, now time.Time,
) (*types.Knowledge, error) {
	meta, err := json.Marshal(map[string]string{agent24OriginMetadataKey: agent24Origin(p.req.Origin)})
	if err != nil {
		return nil, err
	}
	k := &types.Knowledge{
		ID: p.knowledgeID, TenantID: p.tenantID, KnowledgeBaseID: p.kb.ID, Type: "file",
		Channel: defaultChannel(""), Title: p.req.FileName, FileName: p.req.FileName, FileType: p.fileType,
		FileSize: int64(len(p.req.Content)), FileHash: p.fileHash, FilePath: p.filePath,
		ParseStatus: types.ParseStatusPending, EnableStatus: "disabled",
		CreatedAt: now, UpdatedAt: now, EmbeddingModelID: p.kb.EmbeddingModelID, Metadata: types.JSON(meta),
	}
	if err := tx.WithContext(ctx).Create(k).Error; err != nil {
		return nil, err
	}
	return k, nil
}

// stageRevise points the knowledge at the new original the way upstream ReplaceKnowledgeFile does
// (knowledge_replace.go), but inside tx and under a row lock, so two revisions of one document cannot both win.
func (a *Agent24Ingest) stageRevise(
	ctx context.Context, tx *gorm.DB, p *Agent24IngestPrepared, now time.Time,
) (*types.Knowledge, error) {
	var k types.Knowledge
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND id = ? AND knowledge_base_id = ?", p.tenantID, p.req.KnowledgeID, p.kb.ID).
		Take(&k).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && k.ParseStatus == types.ParseStatusDeleting) {
		return nil, ErrAgent24KnowledgeNotFound
	}
	if err != nil {
		return nil, err
	}
	cols := map[string]any{
		"file_name": p.req.FileName, "file_type": p.fileType, "file_size": int64(len(p.req.Content)),
		"file_hash": p.fileHash, "file_path": p.filePath, "parse_status": types.ParseStatusPending,
		"enable_status": "disabled", "error_message": "", "updated_at": now,
	}
	if k.Title == k.FileName {
		cols["title"] = p.req.FileName
	}
	if err := tx.WithContext(ctx).Model(&types.Knowledge{}).
		Where("tenant_id = ? AND id = ?", p.tenantID, k.ID).Updates(cols).Error; err != nil {
		return nil, err
	}
	if err := tx.WithContext(ctx).Where("tenant_id = ? AND id = ?", p.tenantID, k.ID).Take(&k).Error; err != nil {
		return nil, err
	}
	return &k, nil
}

// Dispatch enqueues a committed job. Earlier tasks of a revised knowledge are dequeued first; the attempt is
// opened here so the payload carries it and an older worker stops at the upstream supersede checks. An enqueue
// failure leaves the job failed/enqueue_failed and the knowledge failed (W5.7): the job never waits in saved
// for a task that does not exist.
func (a *Agent24Ingest) Dispatch(
	ctx context.Context, job *types.Agent24IngestJob, k *types.Knowledge, p *Agent24IngestPrepared,
) error {
	if job == nil || k == nil || p == nil {
		return errors.New("agent24 ingest: Dispatch needs the staged job")
	}
	if p.req.KnowledgeID != "" {
		a.ks.dequeueKnowledgeTasks(context.WithoutCancel(ctx), k.ID)
	}
	attempt := 0
	if _, n, err := a.ks.tracker().OpenAttempt(ctx, k.ID, ""); err == nil && n > 0 {
		attempt = n
		if err := a.jobs.SetAttempt(ctx, job.TenantID, job.ID, n); err != nil {
			logger.Warnf(ctx, "[agent24] ingest: recording the attempt failed: error_type=%T", err)
		} else {
			job.Attempt = n
		}
	}
	payload := types.DocumentProcessPayload{
		TenantID: p.tenantID, KnowledgeID: k.ID, KnowledgeBaseID: p.kb.ID, FilePath: p.filePath,
		FileName: p.req.FileName, FileType: p.fileType, Attempt: attempt,
		Language: types.LanguageFromContextOrDefault(ctx),
	}
	err := a.enqueue(payload)
	if err == nil {
		return nil
	}
	logger.Warnf(ctx, "[agent24] ingest: enqueue failed: error_type=%T", err)
	a.ks.markKnowledgeEnqueueFailed(context.WithoutCancel(ctx), k)
	if _, aerr := a.jobs.Apply(context.WithoutCancel(ctx), *job, types.Agent24JobChange{
		Status: types.Agent24JobFailed, FailureReason: types.Agent24JobFailEnqueue, Enrichment: job.Enrichment,
	}, a.now()); aerr != nil {
		logger.Warnf(ctx, "[agent24] ingest: recording enqueue_failed failed: error_type=%T", aerr)
	}
	return fmt.Errorf("agent24 ingest: enqueue: %w", err)
}

func (a *Agent24Ingest) enqueue(payload types.DocumentProcessPayload) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	task := asynq.NewTask(types.TypeDocumentProcess, b, documentProcessTaskOptions(a.ks.config)...)
	_, err = a.ks.task.Enqueue(task)
	return err
}

func agent24Origin(o string) string {
	if o == Agent24OriginEntry {
		return Agent24OriginEntry
	}
	return Agent24OriginUpload
}
