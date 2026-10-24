package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// recordingAudit keeps the entries written through Log.
type recordingAudit struct {
	interfaces.AuditLogService
	entries []*types.AuditLog
}

func (r *recordingAudit) Log(_ context.Context, entry *types.AuditLog) error {
	r.entries = append(r.entries, entry)
	return nil
}

// A secret setting's audit row names the key but carries neither value (fork patch A24-OBS-01); a plain
// setting keeps both, and a reset of a secret keeps new_value null.
func TestEmitChangeAuditRedactsSecretValues(t *testing.T) {
	audit := &recordingAudit{}
	svc := &systemSettingService{audit: audit}
	ctx := context.Background()
	svc.emitChangeAudit(ctx, "k.secret", "string", types.JSON(`"S3NT1NEL-old"`), types.JSON(`"S3NT1NEL-new"`), true)
	svc.emitChangeAudit(ctx, "k.secret", "string", types.JSON(`"S3NT1NEL-old"`), nil, true)
	svc.emitChangeAudit(ctx, "k.plain", "string", types.JSON(`"old"`), types.JSON(`"new"`), false)
	if len(audit.entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(audit.entries))
	}
	want := []map[string]any{
		{"key": "k.secret", "value_type": "string", "old_value": "***", "new_value": "***", "redacted": true},
		{"key": "k.secret", "value_type": "string", "old_value": "***", "new_value": nil, "redacted": true},
		{"key": "k.plain", "value_type": "string", "old_value": "old", "new_value": "new"},
	}
	for i, e := range audit.entries {
		if strings.Contains(string(e.Details), "S3NT1NEL") {
			t.Fatalf("entry %d leaks the value: %s", i, e.Details)
		}
		var got map[string]any
		if err := json.Unmarshal(e.Details, &got); err != nil {
			t.Fatal(err)
		}
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want[i])
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("entry %d details = %s, want %s", i, gotJSON, wantJSON)
		}
	}
}
