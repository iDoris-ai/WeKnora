package container

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Failed statements through Exec, Find and Raw(...).Scan (the keyword search path) log their SQL with
// placeholders: the sentinel bound value never reaches the log (A24-OBS-01).
func TestAgent24GormLoggerHidesBoundValues(t *testing.T) {
	var buf bytes.Buffer
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: agent24GormLogger(&buf)})
	if err != nil {
		t.Fatal(err)
	}
	const sentinel = "S3NT1NEL-query"
	var rows []map[string]any
	_ = db.Raw("SELECT x FROM missing WHERE y = ?", sentinel).Scan(&rows).Error
	_ = db.Table("missing").Where("y = ?", sentinel).Find(&rows).Error
	_ = db.Exec("INSERT INTO missing (y) VALUES (?)", sentinel).Error
	out := buf.String()
	if strings.Count(out, "missing") < 3 {
		t.Fatalf("want three failed statements logged, got:\n%s", out)
	}
	if strings.Contains(out, sentinel) {
		t.Fatalf("gorm log contains the bound value:\n%s", out)
	}
	if !strings.Contains(out, "y = ?") {
		t.Fatalf("want placeholders in the logged SQL, got:\n%s", out)
	}
}

func TestAgent24GormLoggerEscapesInteriorNewlines(t *testing.T) {
	var buf bytes.Buffer
	l := agent24GormLogger(&buf)
	l.Error(context.Background(), "x %s", "a\nERROR forged")
	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("want a single line, got %q", out)
	}
}

func TestNewGormConfigHidesBoundValues(t *testing.T) {
	var buf bytes.Buffer
	db, err := gorm.Open(sqlite.Open(":memory:"), newGormConfig(&buf))
	if err != nil {
		t.Fatal(err)
	}
	const sentinel = "S3NT1NEL"
	var rows []map[string]any
	_ = db.Raw("SELECT x FROM missing WHERE y = ?", sentinel).Scan(&rows).Error
	out := buf.String()
	if !strings.Contains(out, "SELECT x FROM missing") {
		t.Fatalf("want SQL in the gorm log, got:\n%s", out)
	}
	if strings.Contains(out, sentinel) {
		t.Fatalf("gorm log contains the bound value:\n%s", out)
	}
}
