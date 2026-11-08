// Agent24 fork product patch A24-KB-41 (WeKnora#136).
package types

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAgent24ReconcileOccupyingJob(t *testing.T) {
	t0 := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	lim := Agent24ReconcileLimits{Saved: time.Hour, Enrichment: 2 * time.Hour}
	saved := Agent24IngestJob{Status: Agent24JobSaved, Attempt: 2, SavedAt: t0, Enrichment: Agent24EnrichmentNone}
	processing := saved
	processing.Status = Agent24JobProcessing
	view := func(status string) Agent24KnowledgeView {
		return Agent24KnowledgeView{Exists: true, ParseStatus: status, LatestAttempt: 2}
	}
	for _, tc := range []struct {
		name string
		job  Agent24IngestJob
		k    Agent24KnowledgeView
		now  time.Time
		want *Agent24JobChange
	}{
		{
			"row gone", processing,
			Agent24KnowledgeView{},
			t0,
			&Agent24JobChange{Status: Agent24JobCancelled, CancelReason: "document_deleted", Enrichment: "none"},
		},
		{
			"row deleting", saved, view(ParseStatusDeleting), t0,
			&Agent24JobChange{Status: Agent24JobCancelled, CancelReason: "document_deleted", Enrichment: "none"},
		},
		{
			"newer attempt", processing,
			Agent24KnowledgeView{Exists: true, ParseStatus: "processing", LatestAttempt: 3},
			t0,
			&Agent24JobChange{Status: Agent24JobCancelled, CancelReason: "superseded", Enrichment: "none"},
		},
		{
			"cancelled upstream", processing, view(ParseStatusCancelled), t0,
			&Agent24JobChange{Status: Agent24JobCancelled, CancelReason: "user", Enrichment: "none"},
		},
		{
			"failed in embedding", processing,
			Agent24KnowledgeView{Exists: true, ParseStatus: "failed", LatestAttempt: 2, FailureHint: "embedding"},
			t0,
			&Agent24JobChange{Status: Agent24JobFailed, FailureReason: "embedding_failed", Enrichment: "none"},
		},
		{
			"failed, stage unknown", processing, view(ParseStatusFailed), t0,
			&Agent24JobChange{Status: Agent24JobFailed, FailureReason: "internal", Enrichment: "none"},
		},
		{
			"finalizing is indexed with enrichment running", processing, view(ParseStatusFinalizing), t0,
			&Agent24JobChange{Status: Agent24JobIndexed, Enrichment: "running"},
		},
		{
			"completed without subtasks", saved, view(ParseStatusCompleted), t0,
			&Agent24JobChange{Status: Agent24JobIndexed, Enrichment: "none"},
		},
		{
			"saved picked up", saved, view(ParseStatusProcessing), t0,
			&Agent24JobChange{Status: Agent24JobProcessing, Enrichment: "none"},
		},
		{"saved within the limit", saved, view(ParseStatusPending), t0.Add(time.Hour), nil},
		{
			"saved past the limit", saved, view(ParseStatusPending), t0.Add(time.Hour + time.Second),
			&Agent24JobChange{
				Status: Agent24JobFailed, FailureReason: "timeout", Enrichment: "none", FailKnowledge: true,
			},
		},
		{"processing has no time limit here", processing, view(ParseStatusProcessing), t0.Add(48 * time.Hour), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Agent24ReconcileJob(tc.job, tc.k, tc.now, lim)
			if tc.want == nil {
				assert.False(t, ok, "%+v", got)
				return
			}
			assert.True(t, ok)
			assert.Equal(t, *tc.want, got)
		})
	}
}

func TestAgent24ReconcileIndexedJobMovesOnlyEnrichment(t *testing.T) {
	t0 := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	lim := Agent24ReconcileLimits{Saved: time.Hour, Enrichment: 2 * time.Hour}
	running := Agent24IngestJob{
		Status: Agent24JobIndexed, Attempt: 1, Enrichment: Agent24EnrichmentRunning, IndexedAt: &t0,
	}
	stopped := func(reason string) *Agent24JobChange {
		return &Agent24JobChange{Status: Agent24JobIndexed, Enrichment: "stopped", StopReason: reason}
	}
	for _, tc := range []struct {
		name string
		job  Agent24IngestJob
		k    Agent24KnowledgeView
		now  time.Time
		want *Agent24JobChange
	}{
		{
			"completed", running,
			Agent24KnowledgeView{Exists: true, ParseStatus: "completed", LatestAttempt: 1},
			t0,
			&Agent24JobChange{Status: Agent24JobIndexed, Enrichment: "done"},
		},
		{"deleted", running, Agent24KnowledgeView{}, t0, stopped("document_deleted")},
		{
			"superseded", running,
			Agent24KnowledgeView{Exists: true, ParseStatus: "pending", LatestAttempt: 2},
			t0,
			stopped("superseded"),
		},
		{
			"cancelled upstream", running,
			Agent24KnowledgeView{Exists: true, ParseStatus: "cancelled", LatestAttempt: 1},
			t0,
			stopped("user"),
		},
		{
			"failed", running,
			Agent24KnowledgeView{Exists: true, ParseStatus: "failed", LatestAttempt: 1},
			t0,
			stopped("failed"),
		},
		{
			"still finalizing within the limit", running,
			Agent24KnowledgeView{Exists: true, ParseStatus: "finalizing", LatestAttempt: 1},
			t0.Add(2 * time.Hour), nil,
		},
		{
			"still finalizing past the limit", running,
			Agent24KnowledgeView{Exists: true, ParseStatus: "finalizing", LatestAttempt: 1},
			t0.Add(2*time.Hour + time.Second),
			stopped("timeout"),
		},
		{
			"enrichment already done",
			Agent24IngestJob{Status: Agent24JobIndexed, Enrichment: "done"},
			Agent24KnowledgeView{},
			t0, nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Agent24ReconcileJob(tc.job, tc.k, tc.now, lim)
			if tc.want == nil {
				assert.False(t, ok, "%+v", got)
				return
			}
			assert.True(t, ok)
			assert.Equal(t, *tc.want, got)
		})
	}
}

func TestAgent24ReconcileNeverMovesTerminalJobs(t *testing.T) {
	for _, s := range []Agent24JobStatus{Agent24JobFailed, Agent24JobCancelled} {
		for _, k := range []Agent24KnowledgeView{
			{}, {Exists: true, ParseStatus: "completed"}, {Exists: true, ParseStatus: "processing"},
		} {
			_, ok := Agent24ReconcileJob(Agent24IngestJob{Status: s}, k, time.Now(), Agent24ReconcileLimits{Saved: 1})
			assert.False(t, ok, "%s %+v", s, k)
		}
	}
	assert.True(t, Agent24JobSaved.Occupying())
	assert.True(t, Agent24JobProcessing.Occupying())
	assert.False(t, Agent24JobIndexed.Occupying())
	assert.False(t, Agent24JobIndexed.Terminal())
}

func TestAgent24JobCostRoundTrip(t *testing.T) {
	n := int64(42)
	c := Agent24JobCost{Final: false, Items: []Agent24JobCostItem{
		{Kind: "storage", Units: &n, UnitType: "bytes", Measured: "exact"},
		{Kind: "embedding", Destination: "m", Units: nil, UnitType: "tokens", Measured: "unknown"},
	}}
	v, err := c.Value()
	assert.NoError(t, err)
	assert.Contains(t, string(v.([]byte)), `"units":null`)
	var back Agent24JobCost
	assert.NoError(t, back.Scan(v))
	assert.Equal(t, c, back)
	assert.NoError(t, back.Scan(nil))
	assert.Equal(t, Agent24JobCost{}, back)
	assert.Error(t, back.Scan(7))
}
