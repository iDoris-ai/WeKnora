package reranking

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/rerank"
)

func TestRerankModelErrorLogOmitsVendorBody(t *testing.T) {
	const sentinel = "S3NT1NEL"
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	model := &stubReranker{err: errors.New("API request failed with status 500: " + sentinel)}
	res := Rerank(context.Background(), model, "q", rows("a", "b"), Options{Threshold: 0.3})

	out := buf.String()
	if strings.Contains(out, sentinel) {
		t.Fatalf("log carries the vendor error text: %s", out)
	}
	if !strings.Contains(out, "error_len=") {
		t.Fatalf("log lacks error_len: %s", out)
	}
	if !strings.Contains(res.Diagnostics.Error, sentinel) {
		t.Fatalf("diagnostics must keep the error, got %q", res.Diagnostics.Error)
	}
}

func TestImageScoringErrorLogOmitsVendorBody(t *testing.T) {
	const sentinel = "S3NT1NEL"
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	model := &visionScorer{
		textScorer: textScorer{scores: map[string]float64{"a": 0.5}},
		imageErr:   errors.New("API request failed with status 500: " + sentinel),
	}
	_, _, err := scoreCandidates(context.Background(), model, "q", []string{"a"}, []int{0}, []rerank.Image{{}})
	if err != nil {
		t.Fatalf("image failure must fall back to text scoring, got %v", err)
	}

	out := buf.String()
	if strings.Contains(out, sentinel) {
		t.Fatalf("log carries the vendor error text: %s", out)
	}
	if !strings.Contains(out, "Image scoring failed") || !strings.Contains(out, "error_len=") {
		t.Fatalf("log lacks the image failure with error_len: %s", out)
	}
}
