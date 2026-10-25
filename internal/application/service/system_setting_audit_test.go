package service

import (
	"context"
	"encoding/json"
	"errors"
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

const obsSecretSettingKey = "test.obs_secret"

type auditSettingRepo struct {
	interfaces.SystemSettingRepository
	row     *types.SystemSetting
	written *types.SystemSetting
	upserts int
	deletes int
	getErr  error
}

func (r *auditSettingRepo) Get(context.Context, string) (*types.SystemSetting, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.row, nil
}

func (r *auditSettingRepo) Upsert(_ context.Context, row *types.SystemSetting) error {
	r.upserts++
	copied := *row
	r.written = &copied
	r.row = &copied
	return nil
}

func (r *auditSettingRepo) Delete(context.Context, string) (bool, error) {
	r.deletes++
	if r.row == nil {
		return false, nil
	}
	r.row = nil
	return true, nil
}

func installObsSecretSpec(t *testing.T) {
	t.Helper()
	registry[obsSecretSettingKey] = settingSpec{
		Type:        "string",
		Category:    "test",
		Description: "obs secret audit",
		IsSecret:    true,
	}
	t.Cleanup(func() { delete(registry, obsSecretSettingKey) })
}

func newObsSecretService(repo *auditSettingRepo) (*systemSettingService, *recordingAudit) {
	audit := &recordingAudit{}
	svc := &systemSettingService{
		repo:  repo,
		audit: audit,
		cache: make(map[string]*types.SystemSetting),
	}
	return svc, audit
}

func TestUpdateRedactsRegistrySecret(t *testing.T) {
	installObsSecretSpec(t)
	repo := &auditSettingRepo{row: &types.SystemSetting{
		Key:       obsSecretSettingKey,
		Value:     types.JSON(`"S3NT1NEL-old"`),
		ValueType: "string",
		IsSecret:  false,
	}}
	svc, audit := newObsSecretService(repo)
	row, err := svc.Update(context.Background(), obsSecretSettingKey, "S3NT1NEL-new")
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || !row.IsSecret {
		t.Fatalf("returned IsSecret = %v, want true", row)
	}
	if repo.written == nil || !repo.written.IsSecret {
		t.Fatalf("written IsSecret = %v, want true", repo.written)
	}
	if len(audit.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(audit.entries))
	}
	assertAuditRedacted(t, audit.entries[0], "***", "***")
}

func TestResetRedactsRegistrySecret(t *testing.T) {
	installObsSecretSpec(t)
	repo := &auditSettingRepo{row: &types.SystemSetting{
		Key:       obsSecretSettingKey,
		Value:     types.JSON(`"S3NT1NEL-old"`),
		ValueType: "string",
		IsSecret:  false,
	}}
	svc, audit := newObsSecretService(repo)
	if err := svc.Reset(context.Background(), obsSecretSettingKey); err != nil {
		t.Fatal(err)
	}
	if len(audit.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(audit.entries))
	}
	assertAuditRedacted(t, audit.entries[0], "***", nil)
}

func TestUpdateFirstWriteRedactsRegistrySecret(t *testing.T) {
	installObsSecretSpec(t)
	repo := &auditSettingRepo{}
	svc, audit := newObsSecretService(repo)
	row, err := svc.Update(context.Background(), obsSecretSettingKey, "S3NT1NEL-new")
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || !row.IsSecret {
		t.Fatalf("returned IsSecret = %v, want true", row)
	}
	if repo.written == nil || !repo.written.IsSecret {
		t.Fatalf("written IsSecret = %v, want true", repo.written)
	}
	if len(audit.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(audit.entries))
	}
	assertAuditRedacted(t, audit.entries[0], nil, "***")
}

func TestResetReadErrorSkipsWriteAndAudit(t *testing.T) {
	installObsSecretSpec(t)
	existing := &types.SystemSetting{Key: obsSecretSettingKey}
	repo := &auditSettingRepo{row: existing, getErr: errors.New("db down")}
	svc, audit := newObsSecretService(repo)
	err := svc.Reset(context.Background(), obsSecretSettingKey)
	if err == nil {
		t.Fatal("expected read error")
	}
	if !strings.Contains(err.Error(), `read system setting "test.obs_secret"`) {
		t.Fatalf("err = %v", err)
	}
	if len(audit.entries) != 0 {
		t.Fatalf("audit entries = %d, want 0", len(audit.entries))
	}
	if repo.deletes != 0 || repo.row != existing {
		t.Fatalf("deletes = %d, row = %v; the row must survive a failed read", repo.deletes, repo.row)
	}
}

func TestUpdateReadErrorSkipsWriteAndAudit(t *testing.T) {
	installObsSecretSpec(t)
	repo := &auditSettingRepo{getErr: errors.New("db down")}
	svc, audit := newObsSecretService(repo)
	_, err := svc.Update(context.Background(), obsSecretSettingKey, "S3NT1NEL-new")
	if err == nil {
		t.Fatal("expected read error")
	}
	if !strings.Contains(err.Error(), `read system setting "test.obs_secret"`) {
		t.Fatalf("err = %v", err)
	}
	if len(audit.entries) != 0 {
		t.Fatalf("audit entries = %d, want 0", len(audit.entries))
	}
	if repo.upserts != 0 {
		t.Fatalf("upserts = %d, want 0", repo.upserts)
	}
}

func assertAuditRedacted(t *testing.T, entry *types.AuditLog, oldValue, newValue any) {
	t.Helper()
	if strings.Contains(string(entry.Details), "S3NT1NEL") {
		t.Fatalf("audit details leak the value: %s", entry.Details)
	}
	var got map[string]any
	if err := json.Unmarshal(entry.Details, &got); err != nil {
		t.Fatal(err)
	}
	if got["old_value"] != oldValue || got["new_value"] != newValue || got["redacted"] != true {
		t.Fatalf("details = %s, want old=%v new=%v redacted=true", entry.Details, oldValue, newValue)
	}
}
