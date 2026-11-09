// Agent24 fork product patch A24-KB-42 (KB-SOP-02 S1, WeKnora#146, CONTRACT-OPS-v2 W3.3, W8.9): the retention of
// the knowledge-entry records, which runs whether or not any request comes.

package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
)

// The W8.9 and W3.3 retention periods.
const (
	// agent24CandidateContentRetention: title, body and note of a returned or withdrawn candidate go within an
	// hour (W8.9). The sweep has its own loop, once a minute with a one-minute deadline (A24-KB-43,
	// container.agent24EveryMinute), so the content is gone at most two minutes after this: 57 minutes.
	agent24CandidateContentRetention = 55 * time.Minute
	// agent24CandidateRecordRetention: a returned or withdrawn candidate's metadata stays 90 days after that state.
	agent24CandidateRecordRetention = 90 * 24 * time.Hour
)

// Agent24CandidateRetention applies the retention of the knowledge-entry records.
type Agent24CandidateRetention struct {
	store *repository.Agent24CandidateStore
	now   func() time.Time
}

// NewAgent24CandidateRetention returns the retention sweep over store.
func NewAgent24CandidateRetention(store *repository.Agent24CandidateStore) *Agent24CandidateRetention {
	return &Agent24CandidateRetention{store: store, now: time.Now}
}

// Agent24CandidateRetentionResult counts what one sweep removed.
type Agent24CandidateRetentionResult struct {
	ContentPurged      int64
	CandidatesDeleted  int64
	IdempotencyDeleted int64
}

// Sweep purges the content of returned and withdrawn candidates past the content retention, deletes returned and
// withdrawn candidates past the record retention (approved ones stay: they mark their document an entry) and
// deletes expired idempotency records. Each step runs even when an earlier one
// failed; the errors are joined.
func (r *Agent24CandidateRetention) Sweep(ctx context.Context) (Agent24CandidateRetentionResult, error) {
	now := r.now().UTC()
	var res Agent24CandidateRetentionResult
	var errs []error
	var err error
	if res.ContentPurged, err = r.store.PurgeCandidateContent(
		ctx, now.Add(-agent24CandidateContentRetention), now); err != nil {
		errs = append(errs, fmt.Errorf("purge candidate content: %w", err))
	}
	if res.CandidatesDeleted, err = r.store.DeleteSettledCandidates(
		ctx, now.Add(-agent24CandidateRecordRetention)); err != nil {
		errs = append(errs, fmt.Errorf("delete settled candidates: %w", err))
	}
	if res.IdempotencyDeleted, err = r.store.DeleteExpiredIdempotency(ctx, now); err != nil {
		errs = append(errs, fmt.Errorf("delete expired idempotency records: %w", err))
	}
	return res, errors.Join(errs...)
}
