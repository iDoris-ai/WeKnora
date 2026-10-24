package api

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/stretchr/testify/require"
)

func TestLogRequestOmitsBody(t *testing.T) {
	const sentinel = "S3NT1NEL"
	body := []byte(`{"prompt":"` + sentinel + `"}`)

	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(func() {
		logger.SetOutput(os.Stdout)
		logger.SetLogLevel(logLevelFromEnv())
	})

	LogRequest(context.Background(), "https://example.test/v1", "m", body, true)

	logs := buf.String()
	require.NotContains(t, logs, sentinel)
	require.Contains(t, logs, fmt.Sprintf("request_bytes=%d", len(body)))
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
