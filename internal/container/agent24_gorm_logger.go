// Agent24 fork patch A24-OBS-01 (KB-OBS-01, WeKnora#40).

package container

import (
	"context"
	"io"
	"log"
	"time"

	gormlogger "gorm.io/gorm/logger"
)

// agent24GormLogger is gorm's default logger (slow and failed SQL at Warn, 200ms threshold) with the bound
// values left as placeholders: they carry queries and document text. ParameterizedQueries covers statements
// logged by this logger; Scan logs the SQL its Recorder captured, which RecorderParamsFilter decides, so that
// process-wide filter drops the values too. Both only shape the logged SQL, not the executed statement.
func agent24GormLogger(w io.Writer) gormlogger.Interface {
	gormlogger.RecorderParamsFilter = func(_ context.Context, sql string, _ ...interface{}) (string, []interface{}) {
		return sql, nil
	}
	return gormlogger.New(log.New(w, "\r\n", log.LstdFlags), gormlogger.Config{
		SlowThreshold: 200 * time.Millisecond, LogLevel: gormlogger.Warn, Colorful: true,
		ParameterizedQueries: true,
	})
}
