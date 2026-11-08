// Command ingestprobe drives the Agent24 internal ingest entry (A24-KB-41, WeKnora#136) on a deployed stack, for
// the M3 acceptance cases that need a submission while no integration route serves one. It is a tool in the fork's
// scripts namespace, not part of the product image.
//
// It runs with the app's own environment (database, Redis, storage, tag key, policy), for example as a one-off
// app container. It always runs in integration mode, so it starts no background work and never processes a task
// itself: the standalone worker does.
//
//	ingestprobe submit  --key-file FILE --kb KB_ID --file PATH [--name NAME] [--knowledge KNOWLEDGE_ID] [--sha256 HEX]
//	ingestprobe status  --tenant TENANT_ID --job JOB_ID
//	ingestprobe workers
//
// submit reads a tenant API key from FILE (mode 0600 or stricter; the key is never printed), checks it the way an
// ingest route would (tenant key, not full access, ingest capability, the target knowledge base in its non-empty
// allow-list), applies the W5.2 format and size checks, then runs Prepare, Stage and Dispatch. status and workers
// are read-only and never read a key: status reconciles and prints one job with its knowledge's parse_status,
// workers lists live asynq servers and the default queue.
//
// Output is one JSON object on stdout. Exit status: 0 done, 2 refused (the JSON says why), 1 any other error.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/hibiken/asynq"
	"go.uber.org/dig"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/container"
	"github.com/Tencent/WeKnora/internal/envfile"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/runtime"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// errRefused marks an outcome the JSON already explains (exit status 2).
var errRefused = errors.New("refused")

type refusal struct {
	Refused string `json:"refused"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: ingestprobe submit|status|workers [flags]")
		return 1
	}
	var out any
	var err error
	switch args[0] {
	case "submit":
		out, err = submit(args[1:])
	case "status":
		out, err = status(args[1:])
	case "workers":
		out, err = workers(args[1:])
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	if out != nil {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if eerr := enc.Encode(out); eerr != nil && err == nil {
			err = eerr
		}
	}
	switch {
	case errors.Is(err, errRefused):
		return 2
	case err != nil:
		_, _ = fmt.Fprintln(stderr, "ingestprobe:", err)
		return 1
	}
	return 0
}

// buildContainer builds the app container in integration mode: the probe must not consume tasks or run any
// background job (A24-KB-30).
func buildContainer() (*dig.Container, error) {
	if err := envfile.Load(false); err != nil {
		return nil, err
	}
	if err := os.Setenv(handler.Agent24KnowledgeModeEnv, handler.Agent24KnowledgeModeIntegration); err != nil {
		return nil, err
	}
	logger.ConfigureFromEnv()
	return container.BuildContainer(runtime.GetContainer()), nil
}

type submitFlags struct {
	keyFile, kb, file, name, knowledge, sha string
}

func parseSubmit(args []string) (submitFlags, error) {
	var f submitFlags
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	fs.StringVar(&f.keyFile, "key-file", "", "file holding the tenant API key (mode 0600)")
	fs.StringVar(&f.kb, "kb", "", "target knowledge base id")
	fs.StringVar(&f.file, "file", "", "original to submit")
	fs.StringVar(&f.name, "name", "", "file name to store (default: the file's base name)")
	fs.StringVar(&f.knowledge, "knowledge", "", "revise this knowledge instead of creating one")
	fs.StringVar(&f.sha, "sha256", "", "declared content_sha256 (default: the content's)")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if f.keyFile == "" || f.kb == "" || f.file == "" {
		return f, errors.New("submit needs --key-file, --kb and --file")
	}
	if f.name == "" {
		f.name = filepath.Base(f.file)
	}
	return f, nil
}

// readKeyFile refuses a key file that group or others can read: the probe key must stay on the operator's side.
func readKeyFile(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("key file %s must not be readable by group or others (mode %o)", path, st.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return "", errors.New("key file is empty")
	}
	return key, nil
}

// probeScopeRefusal is what an ingest route would answer for this key and target before any write
// (CONTRACT-OPS-v2 W1.9 ③(a)), or "" when the key may submit to kb.
func probeScopeRefusal(key *types.TenantAPIKey, kb string) string {
	if key == nil || key.IsPlatform() || key.TenantID == nil {
		return "unauthorized/credential_invalid"
	}
	scope := types.TenantAPIKeyScope{
		KeyID: key.ID, FullAccess: key.FullAccess, KnowledgeBaseIDs: key.KnowledgeBaseIDs,
		Capabilities: key.Capabilities,
	}.Normalize()
	if scope.FullAccess || !scope.HasCapability(types.APIKeyCapabilityIngest) {
		return "forbidden/operation_not_allowed"
	}
	if !slices.Contains([]string(scope.KnowledgeBaseIDs), kb) {
		return "forbidden/empty_scope"
	}
	return ""
}

type submitResult struct {
	JobID          string `json:"job_id"`
	KnowledgeID    string `json:"knowledge_id"`
	Status         string `json:"status"`
	SourceRevision string `json:"source_revision"`
	DispatchError  string `json:"dispatch_error,omitempty"`
}

func submit(args []string) (any, error) {
	f, err := parseSubmit(args)
	if err != nil {
		return nil, err
	}
	rawKey, err := readKeyFile(f.keyFile)
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(f.file)
	if err != nil {
		return nil, err
	}
	// W5.2: refused before anything is saved or a job exists
	if !service.Agent24IngestFormatAllowed(f.name) {
		return refusal{"invalid_request/unsupported_format"}, errRefused
	}
	if service.Agent24IngestTooLarge(len(content)) {
		return refusal{"invalid_request/too_large"}, errRefused
	}
	declared := f.sha
	if declared == "" {
		sum := sha256.Sum256(content)
		declared = hex.EncodeToString(sum[:])
	}
	tagKey, err := handler.Agent24KnowledgeTagKeyFromEnv()
	if err != nil {
		return nil, err
	}
	c, err := buildContainer()
	if err != nil {
		return nil, err
	}
	var out any
	err = c.Invoke(func(
		keys interfaces.TenantAPIKeyService, tenants interfaces.TenantService,
		ingest *service.Agent24Ingest, db *gorm.DB,
	) error {
		ctx := context.Background()
		key, kerr := keys.AuthenticateAPIKey(ctx, rawKey)
		if kerr != nil {
			out = refusal{"unauthorized/credential_invalid"}
			return errRefused
		}
		if why := probeScopeRefusal(key, f.kb); why != "" {
			out = refusal{why}
			return errRefused
		}
		tenant, terr := tenants.GetTenantByID(ctx, *key.TenantID)
		if terr != nil || tenant == nil {
			out = refusal{"unauthorized/credential_invalid"}
			return errRefused
		}
		ctx = context.WithValue(ctx, types.TenantIDContextKey, tenant.ID)
		ctx = context.WithValue(ctx, types.TenantInfoContextKey, tenant)
		res, serr := runSubmit(ctx, ingest, db, f, content, declared, key.ID, tagKey)
		out = res
		return serr
	})
	return out, err
}

func runSubmit(
	ctx context.Context, ingest *service.Agent24Ingest, db *gorm.DB, f submitFlags, content []byte, declared string,
	keyID uint64, tagKey []byte,
) (any, error) {
	prep, err := ingest.Prepare(ctx, service.Agent24IngestRequest{
		KnowledgeBaseID: f.kb, KnowledgeID: f.knowledge, FileName: f.name, Content: content, ContentSHA256: declared,
		Origin: service.Agent24OriginUpload,
	})
	switch {
	case errors.Is(err, service.ErrAgent24ContentHashMismatch):
		return refusal{"invalid_request/content_hash_mismatch"}, errRefused
	case errors.Is(err, service.ErrAgent24IngestUnavailable):
		return refusal{"unavailable/ingest_unavailable"}, errRefused
	case errors.Is(err, service.ErrAgent24KnowledgeNotFound):
		return refusal{"not_found"}, errRefused
	case errors.Is(err, service.ErrAgent24InvalidFileName), errors.Is(err, service.ErrAgent24UnsupportedKB):
		return refusal{"invalid_request"}, errRefused
	case err != nil:
		return nil, err
	}
	who := service.Agent24IngestPrincipal{
		OpClass: types.Agent24JobOpIngestSubmit, CredentialID: keyID,
		PolicyVersion: handler.Agent24ProcessingPolicyVersion(),
	}
	var job *types.Agent24IngestJob
	var k *types.Knowledge
	err = db.Transaction(func(tx *gorm.DB) error {
		var serr error
		job, k, serr = ingest.Stage(ctx, tx, prep, who, handler.Agent24IngestSourceRevision(tagKey))
		return serr
	})
	if err != nil {
		ingest.Abandon(ctx, prep)
		if errors.Is(err, service.ErrAgent24KnowledgeNotFound) {
			return refusal{"not_found"}, errRefused
		}
		if errors.Is(err, service.ErrAgent24UnsupportedKnowledge) {
			return refusal{"invalid_request/unsupported_knowledge"}, errRefused
		}
		return nil, err
	}
	res := submitResult{
		JobID: job.ID, KnowledgeID: k.ID, Status: string(job.Status), SourceRevision: job.SourceRevision,
	}
	if derr := ingest.Dispatch(ctx, job, k, prep); derr != nil {
		res.Status = string(types.Agent24JobFailed)
		res.DispatchError = "enqueue_failed"
	}
	return res, nil
}

type statusResult struct {
	Job         *types.Agent24IngestJob `json:"job"`
	ParseStatus string                  `json:"knowledge_parse_status"`
}

func status(args []string) (any, error) {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	tenant := fs.String("tenant", "", "tenant id")
	jobID := fs.String("job", "", "job id")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	tid, err := strconv.ParseUint(*tenant, 10, 64)
	if err != nil || tid == 0 || *jobID == "" {
		return nil, errors.New("status needs --tenant and --job")
	}
	c, err := buildContainer()
	if err != nil {
		return nil, err
	}
	var out any
	err = c.Invoke(func(jobs *repository.Agent24IngestJobStore) error {
		ctx := context.Background()
		job, gerr := service.NewAgent24JobReconciler(jobs).Get(ctx, tid, *jobID)
		if gerr != nil {
			return gerr
		}
		if job == nil {
			out = refusal{"not_found"}
			return errRefused
		}
		view, verr := jobs.KnowledgeView(ctx, tid, job.KnowledgeID)
		if verr != nil {
			return verr
		}
		out = statusResult{Job: job, ParseStatus: view.ParseStatus}
		return nil
	})
	return out, err
}

type workerInfo struct {
	Host   string         `json:"host"`
	PID    int            `json:"pid"`
	Status string         `json:"status"`
	Queues map[string]int `json:"queues"`
}

type workersResult struct {
	Redis          bool         `json:"redis"`
	Servers        []workerInfo `json:"servers"`
	DefaultPending int          `json:"default_pending"`
	DefaultActive  int          `json:"default_active"`
	DefaultRetry   int          `json:"default_retry"`
}

func workers(args []string) (any, error) {
	if len(args) != 0 {
		return nil, errors.New("workers takes no flags")
	}
	if os.Getenv("REDIS_ADDR") == "" {
		return workersResult{}, nil
	}
	c, err := buildContainer()
	if err != nil {
		return nil, err
	}
	var out any
	err = c.Invoke(func(inspector *asynq.Inspector) error {
		servers, serr := inspector.Servers()
		if serr != nil {
			return serr
		}
		res := workersResult{Redis: true}
		for _, s := range servers {
			res.Servers = append(res.Servers, workerInfo{Host: s.Host, PID: s.PID, Status: s.Status, Queues: s.Queues})
		}
		if q, qerr := inspector.GetQueueInfo(types.QueueDefault); qerr == nil {
			res.DefaultPending, res.DefaultActive, res.DefaultRetry = q.Pending, q.Active, q.Retry
		}
		out = res
		return nil
	})
	return out, err
}
