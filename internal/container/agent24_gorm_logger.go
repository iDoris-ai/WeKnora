// Agent24 fork patch A24-OBS-01 (KB-OBS-01, WeKnora#40).

package container

import (
	"context"
	"io"
	"log"
	"time"

	gormlogger "gorm.io/gorm/logger"

	"github.com/Tencent/WeKnora/internal/logger"
)

// agent24GormLogger is gorm's default logger (slow and failed SQL at Warn, 200ms threshold) with the bound
// values left as placeholders: they carry queries and document text. ParameterizedQueries covers statements
// logged by this logger; Scan logs the SQL its Recorder captured, which RecorderParamsFilter decides, so that
// process-wide filter drops the values too. Both only shape the logged SQL, not the executed statement.
func agent24GormLogger(w io.Writer) gormlogger.Interface {
	gormlogger.RecorderParamsFilter = func(_ context.Context, sql string, _ ...interface{}) (string, []interface{}) {
		return sql, nil
	}
	// gorm breaks between the file:line and the SQL; escaping turns that into a "\n" on the same line.
	// Colorful is off: an ANSI ESC would be escaped to "\u001b", and the color only helps on a terminal.
	return gormlogger.New(log.New(logger.LineSafeWriter(w), "", log.LstdFlags), gormlogger.Config{
		SlowThreshold: 200 * time.Millisecond, LogLevel: gormlogger.Warn, Colorful: false,
		ParameterizedQueries: true,
	})
}
