package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type imageReaderKBRepo struct {
	interfaces.KnowledgeBaseRepository
	kb *types.KnowledgeBase
}

func (r *imageReaderKBRepo) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	return r.kb, nil
}

type imageReaderFiles struct {
	interfaces.FileService
	body  []byte
	reads []string
}

func (f *imageReaderFiles) GetFile(_ context.Context, path string) (io.ReadCloser, error) {
	f.reads = append(f.reads, path)
	return io.NopCloser(bytes.NewReader(f.body)), nil
}

func imageResult(urls ...string) *types.SearchResult {
	infos := make([]types.ImageInfo, len(urls))
	for i, u := range urls {
		infos[i] = types.ImageInfo{URL: u}
	}
	raw, _ := json.Marshal(infos)
	return &types.SearchResult{ID: "c1", KnowledgeBaseID: "kb-1", ImageInfo: string(raw)}
}

func TestReadChunkImageReadsTheFirstImageFromItsKnowledgeBaseStorage(t *testing.T) {
	files := &imageReaderFiles{body: []byte("png bytes")}
	s := &knowledgeBaseService{
		repo:       &imageReaderKBRepo{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 7}},
		tenantRepo: &attrsTenantRepo{}, // no tenant row: the default FileService serves
		fileSvc:    files,
	}
	data, err := s.ReadChunkImage(context.Background(), imageResult("local://a.png", "local://b.png"))
	require.NoError(t, err)
	assert.Equal(t, []byte("png bytes"), data)
	assert.Equal(t, []string{"local://a.png"}, files.reads)
}

func TestReadChunkImageRefusesWhatItCannotOrShouldNotRead(t *testing.T) {
	files := &imageReaderFiles{body: bytes.Repeat([]byte{1}, maxRerankImageBytes+1)}
	s := &knowledgeBaseService{
		repo:       &imageReaderKBRepo{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 7}},
		tenantRepo: &attrsTenantRepo{},
		fileSvc:    files,
	}
	_, err := s.ReadChunkImage(context.Background(), imageResult("local://huge.png"))
	assert.ErrorContains(t, err, "larger than")

	_, err = s.ReadChunkImage(context.Background(), &types.SearchResult{ID: "c2", ImageInfo: ""})
	assert.ErrorContains(t, err, "carries no image")

	_, err = s.ReadChunkImage(context.Background(), imageResult(""))
	assert.ErrorContains(t, err, "no image URL")
}

// imageReaderLogTenant returns a tenant so fileService does not stop at the
// GetTenantByID warning. err, when set, is that lookup's failure.
type imageReaderLogTenant struct {
	interfaces.TenantRepository
	err error
}

func (t imageReaderLogTenant) GetTenantByID(context.Context, uint64) (*types.Tenant, error) {
	if t.err != nil {
		return nil, t.err
	}
	return &types.Tenant{ID: 7}, nil
}

// imageReaderLogCatalog fails Resolve so a resource:// URL hits the reference
// warning. The error text carries the sentinel so the log must omit it too.
type imageReaderLogCatalog struct {
	interfaces.ResourceCatalog
}

func (imageReaderLogCatalog) Resolve(context.Context, string) (*types.StoredResource, error) {
	return nil, errors.New("resource missing: S3NT1NEL")
}

// imageReaderLogResolver reaches the "resolving file service" log and then
// serves the read from the in-memory file service.
type imageReaderLogResolver struct {
	interfaces.StorageBackendResolver
	svc interfaces.FileService
	err error
}

func (r imageReaderLogResolver) ResolveFileService(
	context.Context, *types.Tenant, string, string, string,
) (interfaces.FileService, string, error) {
	if r.err != nil {
		return nil, "", r.err
	}
	return r.svc, "local", nil
}

// TestImageReaderFileServiceLogOmitsURL uses a resource:// URL that carries
// the sentinel. Resolve fails (line that logs the reference) and the resolver
// still runs (line that logs the URL length). Neither log may contain the URL.
func TestImageReaderFileServiceLogOmitsURL(t *testing.T) {
	t.Setenv("LOCAL_STORAGE_BASE_DIR", "/data/files")
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	handle := "S3NT1NEL" + strings.Repeat("a", types.ResourceHandleLength-len("S3NT1NEL"))
	imageURL := types.BuildResourcePath(handle)
	files := &imageReaderFiles{body: []byte("png")}
	s := &knowledgeBaseService{
		repo:            &imageReaderKBRepo{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 7}},
		tenantRepo:      imageReaderLogTenant{},
		fileSvc:         files,
		storageResolver: imageReaderLogResolver{svc: files},
		resourceCatalog: imageReaderLogCatalog{},
	}
	data, err := s.ReadChunkImage(context.Background(), imageResult(imageURL))
	require.NoError(t, err)
	assert.Equal(t, []byte("png"), data)

	logs := buf.String()
	require.NotContains(t, logs, "S3NT1NEL")
	require.Contains(t, logs, "[ImageReader] resolve resource reference failed:")
	require.Contains(t, logs, "[ImageReader] resolving file service:")
	require.Contains(t, logs, "url_len=")
}

// GetTenantByID's error is already logged as a length and a type. The sentinel
// stays in the error the fixture returns and must not reach the log.
func TestImageReaderGetTenantByIDLogOmitsErrorText(t *testing.T) {
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	files := &imageReaderFiles{body: []byte("png")}
	s := &knowledgeBaseService{
		repo:       &imageReaderKBRepo{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 7}},
		tenantRepo: imageReaderLogTenant{err: errors.New("tenant lookup S3NT1NEL")},
		fileSvc:    files,
	}
	data, err := s.ReadChunkImage(context.Background(), imageResult("local://a.png"))
	require.NoError(t, err)
	assert.Equal(t, []byte("png"), data)

	logs := buf.String()
	require.NotContains(t, logs, "S3NT1NEL")
	require.Contains(t, logs, "[ImageReader] GetTenantByID failed:")
	require.Contains(t, logs, "error_len=")
}

// Resolving the file service fails closed onto the default service. The
// sentinel stays in that error and must not reach the log.
func TestImageReaderResolveFileServiceLogOmitsErrorText(t *testing.T) {
	t.Setenv("LOCAL_STORAGE_BASE_DIR", "/data/files")
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	files := &imageReaderFiles{body: []byte("png")}
	s := &knowledgeBaseService{
		repo:            &imageReaderKBRepo{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 7}},
		tenantRepo:      imageReaderLogTenant{},
		fileSvc:         files,
		storageResolver: imageReaderLogResolver{err: errors.New("resolve file service S3NT1NEL")},
	}
	data, err := s.ReadChunkImage(context.Background(), imageResult("local://a.png"))
	require.NoError(t, err)
	assert.Equal(t, []byte("png"), data)

	logs := buf.String()
	require.NotContains(t, logs, "S3NT1NEL")
	require.Contains(t, logs, "[ImageReader] resolve file service failed")
	require.Contains(t, logs, "error_len=")
}
