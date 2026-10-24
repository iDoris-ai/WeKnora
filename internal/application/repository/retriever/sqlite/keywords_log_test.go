package sqlite

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestKeywordsRetrieveLogsLengthNotText(t *testing.T) {
	repository := newSQLiteRetrieverTestRepository(t)
	if !repository.db.Migrator().HasTable("lite_embeddings_fts") {
		t.Skip("FTS5 unavailable (build without sqlite_fts5)")
	}
	info := sqliteTestIndex("chunk-hit", "kb", "knowledge", "", true)
	info.Content = "alpha S3NT1NEL omega"
	saveSQLiteTestVector(t, repository, info, []float32{1, 0})

	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(func() {
		logger.SetOutput(os.Stdout)
		logger.SetLogLevel(logLevelFromEnv())
	})

	hit, err := repository.keywordsRetrieve(context.Background(), types.RetrieveParams{
		Query:         "S3NT1NEL",
		TopK:          10,
		RetrieverType: types.KeywordsRetrieverType,
	})
	require.NoError(t, err)
	require.Len(t, hit, 1)
	require.NotEmpty(t, hit[0].Results)

	miss, err := repository.keywordsRetrieve(context.Background(), types.RetrieveParams{
		Query:         "absentS3NT1NELtoken",
		TopK:          10,
		RetrieverType: types.KeywordsRetrieverType,
	})
	require.NoError(t, err)
	require.Len(t, miss, 1)
	require.Empty(t, miss[0].Results)

	logs := buf.String()
	require.NotContains(t, logs, "S3NT1NEL")
	require.Contains(t, logs, "query_len=")
}

func logLevelFromEnv() logger.LogLevel {
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "info":
		return logger.LevelInfo
	case "warn", "warning":
		return logger.LevelWarn
	case "error":
		return logger.LevelError
	case "fatal":
		return logger.LevelFatal
	default:
		return logger.LevelDebug
	}
}
