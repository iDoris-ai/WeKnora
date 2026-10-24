package service

import (
	"bytes"
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/stretchr/testify/require"
)

// A refused rewrite logs how many rows it dropped, not the dropped rows: the
// first cell of a ledger row is page content (here a holder's name).
func TestUpdatePageRefusalLogOmitsDroppedRows(t *testing.T) {
	const sentinel = "s3nt1nel" // lower case: row identities are case-folded
	holders := []string{"a", "b", "c", sentinel}
	svc, page := newWikiWriteGuardStore(t, ledger("PMP", holders...))

	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	truncated := *page
	truncated.Content = ledger("PMP", holders[:3]...)
	_, err := svc.UpdatePage(context.Background(), &truncated)
	require.ErrorIs(t, err, ErrWikiWriteDroppedTableRows)

	logs := buf.String()
	require.Contains(t, logs, "wiki page write refused")
	require.Contains(t, logs, "examples_len=")
	require.NotContains(t, logs, sentinel)
}
