package file

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedLocalObject writes a file under baseDir at the SaveBytes-style layout
// (tenant/exports) and returns its local:// path.
func seedLocalObject(t *testing.T, baseDir string, tenantID uint64, name string, data []byte) string {
	t.Helper()
	dir := filepath.Join(baseDir, "0", "src-knowledge")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	full := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(full, data, 0o644))
	rel, err := filepath.Rel(baseDir, full)
	require.NoError(t, err)
	return localScheme + filepath.ToSlash(rel)
}

func readLocal(t *testing.T, svc interface {
	GetFile(context.Context, string) (io.ReadCloser, error)
}, path string) []byte {
	t.Helper()
	rc, err := svc.GetFile(context.Background(), path)
	require.NoError(t, err)
	defer rc.Close()
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	return b
}

// TestLocalCopyFile_IndependentCopy verifies that CopyFile creates a real,
// independent copy: it exists at a new knowledge-owned path AND survives
// deletion of the source object (the C1/C2 regression this PR fixes).
func TestLocalCopyFile_IndependentCopy(t *testing.T) {
	base := t.TempDir()
	svc := NewLocalFileService(base, "")

	content := []byte("hello deep copy")
	srcPath := seedLocalObject(t, base, 0, "doc.txt", content)

	newPath, err := svc.CopyFile(context.Background(), srcPath, 42, "dst-knowledge")
	require.NoError(t, err)

	// New path must be knowledge-owned: local://42/dst-knowledge/<unique>.txt
	require.True(t, strings.HasPrefix(newPath, localScheme+"42/dst-knowledge/"),
		"unexpected dst path: %s", newPath)
	assert.Equal(t, ".txt", filepath.Ext(newPath))

	// Copy is readable and byte-identical.
	assert.Equal(t, content, readLocal(t, svc, newPath))

	// Delete the source — the copy must remain intact.
	require.NoError(t, svc.DeleteFile(context.Background(), srcPath))
	assert.Equal(t, content, readLocal(t, svc, newPath),
		"copy should survive deletion of source")
}

// TestLocalCopyFile_CrossBackend verifies that handing a non-local provider
// scheme to the local service is rejected with ErrCrossBackendCopy.
func TestLocalCopyFile_CrossBackend(t *testing.T) {
	base := t.TempDir()
	svc := NewLocalFileService(base, "")

	_, err := svc.CopyFile(context.Background(), "s3://bucket/10/exports/a.png", 7, "k")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCrossBackendCopy),
		"expected ErrCrossBackendCopy, got %v", err)
}

// TestLocalCopyFile_TraversalRejected verifies that the same path guard used by
// GetFile/DeleteFile rejects a traversal source path.
func TestLocalCopyFile_TraversalRejected(t *testing.T) {
	base := t.TempDir()
	svc := NewLocalFileService(base, "")

	_, err := svc.CopyFile(context.Background(), localScheme+"../../etc/passwd", 7, "k")
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrCrossBackendCopy),
		"traversal should be a path error, not cross-backend")
}

func captureFileServiceLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)
	return &buf
}

// TestLocalGetFile_LogOmitsMissingFileName reads a missing file whose name
// carries the sentinel. The open failure log must still be emitted, without
// the name, the resolved path, or the *os.PathError text.
func TestLocalGetFile_LogOmitsMissingFileName(t *testing.T) {
	base := t.TempDir()
	svc := NewLocalFileService(base, "")
	buf := captureFileServiceLog(t)

	_, err := svc.GetFile(context.Background(), "S3NT1NEL.txt")
	require.Error(t, err)

	logs := buf.String()
	require.NotContains(t, logs, "S3NT1NEL")
	require.Contains(t, logs, "Getting file: path_len=")
	require.Contains(t, logs, "Failed to open file:")
}

// TestLocalSaveBytes_LogOmitsFileName saves bytes under a sentinel file name.
// Both the request log and the success log must appear without that name.
func TestLocalSaveBytes_LogOmitsFileName(t *testing.T) {
	base := t.TempDir()
	svc := NewLocalFileService(base, "")
	buf := captureFileServiceLog(t)

	_, err := svc.SaveBytes(context.Background(), []byte("hi"), 7, "S3NT1NEL.txt", false)
	require.NoError(t, err)

	logs := buf.String()
	require.NotContains(t, logs, "S3NT1NEL")
	require.Contains(t, logs, "Saving bytes data:")
	require.Contains(t, logs, "Bytes data saved successfully:")
}

// TestLocalCopyFile_LogOmitsPaths copies a local object whose source path
// carries the sentinel. The copy log must name neither source nor destination.
func TestLocalCopyFile_LogOmitsPaths(t *testing.T) {
	base := t.TempDir()
	svc := NewLocalFileService(base, "")
	srcPath := seedLocalObject(t, base, 0, "S3NT1NEL.txt", []byte("x"))
	buf := captureFileServiceLog(t)

	_, err := svc.CopyFile(context.Background(), srcPath, 42, "dst-knowledge")
	require.NoError(t, err)

	logs := buf.String()
	require.NotContains(t, logs, "S3NT1NEL")
	require.Contains(t, logs, "Copied local file")
}

// TestLocalGetFile_PathTraversalLogOmitsSentinel rejects a path SafePathUnderBase
// refuses. The denial log must appear, and the sentinel in the caller path must not.
func TestLocalGetFile_PathTraversalLogOmitsSentinel(t *testing.T) {
	base := t.TempDir()
	svc := NewLocalFileService(base, "")
	buf := captureFileServiceLog(t)

	_, err := svc.GetFile(context.Background(), "../S3NT1NEL")
	require.Error(t, err)

	logs := buf.String()
	require.NotContains(t, logs, "S3NT1NEL")
	require.Contains(t, logs, "Path traversal denied for GetFile:")
}

// The dummy service's no-op copy warning keeps the source path's length only.
func TestDummyCopyFile_LogOmitsSourcePath(t *testing.T) {
	buf := captureFileServiceLog(t)

	got, err := NewDummyFileService().CopyFile(context.Background(), "dummy://1/S3NT1NEL.txt", 1, "k")
	require.NoError(t, err)
	require.Equal(t, "dummy://1/S3NT1NEL.txt", got)

	logs := buf.String()
	require.NotContains(t, logs, "S3NT1NEL")
	require.Contains(t, logs, "[dummy] CopyFile no-op: returning source path src_len=")
}
