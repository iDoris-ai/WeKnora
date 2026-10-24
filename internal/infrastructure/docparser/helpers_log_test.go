package docparser

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
)

func TestLogResponseStructureOmitsStringValues(t *testing.T) {
	const sentinel = "S3NT1NEL"
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	logResponseStructure("MinerU", map[string]interface{}{
		"md":       "# " + sentinel,
		"warnings": []interface{}{"warn " + sentinel},
	}, "root")
	logResponseStructure("MinerU", "raw "+sentinel, "root")

	out := buf.String()
	if strings.Contains(out, sentinel) {
		t.Fatalf("log carries response text: %s", out)
	}
	if strings.Count(out, "bytes)") < 3 {
		t.Fatalf("expected three string lengths, got: %s", out)
	}
}
