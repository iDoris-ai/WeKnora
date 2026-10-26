package logger

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/trace"
	"gopkg.in/natefinch/lumberjack.v2"
)

// appLogger 使用私有实例，避免外部依赖改写 logrus 全局状态导致日志丢失
var appLogger = logrus.New()

var (
	loggerMu      sync.Mutex
	activeLogFile io.WriteCloser
	ansiEscapeRE  = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
)

// ansiStripWriter removes ANSI color/style sequences so file logs stay plain text
// while stdout can still render colors in a terminal.
type ansiStripWriter struct {
	w io.Writer
}

func (s *ansiStripWriter) Write(p []byte) (int, error) {
	_, err := s.w.Write(ansiEscapeRE.ReplaceAll(p, nil))
	return len(p), err
}

// LogLevel 日志级别类型
type LogLevel string

// 日志级别常量
const (
	LevelDebug LogLevel = "debug"
	LevelInfo  LogLevel = "info"
	LevelWarn  LogLevel = "warn"
	LevelError LogLevel = "error"
	LevelFatal LogLevel = "fatal"
)

// ANSI颜色代码
const (
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorPurple = "\033[35m"
	colorCyan   = "\033[36m"
	colorWhite  = "\033[37m"
	colorGray   = "\033[90m"
	colorBold   = "\033[1m"
	colorReset  = "\033[0m"
)

type CustomFormatter struct {
	ForceColor bool   // 是否强制使用颜色，即使在非终端环境下
	Template   string // 自定义日志格式模板，通过 LOG_FORMAT 环境变量配置，为空则使用内置默认格式
	// 模板占位符：%d=时间 %level=级别 %thread=goroutine %logger=caller %traceId=请求ID %msg=消息+结构化字段

	// threadNeeded 缓存模板是否引用了 %thread，避免每条日志都调用一次 runtime.Stack。
	threadNeeded bool
}

// levelColorFor 返回日志级别对应的 ANSI 颜色码，无颜色时返回空串。
func levelColorFor(level logrus.Level) string {
	switch level {
	case logrus.DebugLevel:
		return colorCyan
	case logrus.InfoLevel:
		return colorGreen
	case logrus.WarnLevel:
		return colorYellow
	case logrus.ErrorLevel:
		return colorRed
	case logrus.FatalLevel:
		return colorPurple
	}
	return ""
}

// logRuneNeedsEscape 报告会打断或伪造日志行的控制字符。
func logRuneNeedsEscape(r rune) bool {
	return r <= 0x1F || r == 0x7F || (r >= 0x80 && r <= 0x9F) || r == 0x2028 || r == 0x2029
}

// escapeLogText 转义 C0/DEL/C1 与 U+2028/U+2029。反斜杠不转义；非法 UTF-8 字节原样保留。
// 没有命中时原样返回，避免分配。
func escapeLogText(s string) string {
	needs := false
	for _, r := range s {
		if logRuneNeedsEscape(r) {
			needs = true
			break
		}
	}
	if !needs {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for i, r := range s {
		if r == utf8.RuneError {
			if _, size := utf8.DecodeRuneInString(s[i:]); size == 1 {
				b.WriteByte(s[i])
				continue
			}
		}
		switch r {
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if logRuneNeedsEscape(r) {
				fmt.Fprintf(&b, "\\u%04x", r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// lineSafeWriter 把一次 Write 收成一行。标准库 log 每次日志调用一次 Write。
type lineSafeWriter struct {
	w io.Writer
}

// LineSafeWriter 返回按行转义的 io.Writer，供 container 等包套住自建的标准库 log。
// 标准库 log、默认 slog 与 gorm logger 不经过 CustomFormatter（fork patch A24-OBS-11）。
func LineSafeWriter(w io.Writer) io.Writer {
	return lineSafeWriter{w: w}
}

// Write 去掉结尾的一个 \n（若有），对其余部分做 escapeLogText，再写出并补回结尾 \n。
// 成功时返回 len(p)，调用方按原长度判断成功；写失败时返回 0 与 err。
func (s lineSafeWriter) Write(p []byte) (int, error) {
	body := p
	trailingNL := false
	if len(body) > 0 && body[len(body)-1] == '\n' {
		body = body[:len(body)-1]
		trailingNL = true
	}
	out := escapeLogText(string(body))
	if trailingNL {
		out += "\n"
	}
	if _, err := s.w.Write([]byte(out)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (f *CustomFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	timestamp := entry.Time.Format("2006-01-02 15:04:05.000")
	level := strings.ToUpper(entry.Level.String())

	// 提取已知字段
	caller, _ := entry.Data["caller"].(string)
	traceID, _ := entry.Data["request_id"].(string)

	// 剩余结构化字段
	keys := make([]string, 0, len(entry.Data))
	for k := range entry.Data {
		if k != "caller" && k != "request_id" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	// 自定义模板模式
	if f.Template != "" {
		msg := escapeLogText(entry.Message)
		for _, k := range keys {
			msg += fmt.Sprintf(" %s=%s", escapeLogText(k), escapeLogText(fmt.Sprintf("%v", entry.Data[k])))
		}
		shortCaller := caller
		if len(shortCaller) > 50 {
			shortCaller = shortCaller[len(shortCaller)-50:]
		}
		shortCaller = escapeLogText(shortCaller)
		// 仅在模板引用 %thread 时才取 goroutine ID，避免每条日志都执行 runtime.Stack
		thread := ""
		if f.threadNeeded {
			thread = getGoroutineID()
		}
		// 级别染色在占位符替换阶段完成，避免后续在整行做 ReplaceAll
		// 误染消息内容里出现的 "INFO"/"ERROR" 等字面字符串。
		levelOut := level
		if f.ForceColor {
			if c := levelColorFor(entry.Level); c != "" {
				levelOut = c + level + colorReset
			}
		}
		// 使用 NewReplacer 做单趟替换，避免链式 ReplaceAll 时
		// 前一个占位符的值里恰好包含后续占位符字面串导致的二次替换。
		r := strings.NewReplacer(
			"%d", timestamp,
			"%level", levelOut,
			"%thread", thread,
			"%logger", shortCaller,
			"%traceId", escapeLogText(traceID),
			"%msg", msg,
		)
		return []byte(r.Replace(f.Template) + "\n"), nil
	}

	// 默认格式（保持原有行为）
	var levelColor, resetColor string
	if f.ForceColor {
		switch entry.Level {
		case logrus.DebugLevel:
			levelColor = colorCyan
		case logrus.InfoLevel:
			levelColor = colorGreen
		case logrus.WarnLevel:
			levelColor = colorYellow
		case logrus.ErrorLevel:
			levelColor = colorRed
		case logrus.FatalLevel:
			levelColor = colorPurple
		default:
			levelColor = colorReset
		}
		resetColor = colorReset
	}

	fields := ""

	// request_id 优先输出
	if v, ok := entry.Data["request_id"]; ok {
		req := escapeLogText(fmt.Sprintf("%v", v))
		if f.ForceColor {
			fields += fmt.Sprintf("%s%s%s ",
				colorBlue, req, colorReset)
		} else {
			fields += fmt.Sprintf("%s ", req)
		}
	}

	// 其余字段排序后输出
	for _, k := range keys {
		val := escapeLogText(fmt.Sprintf("%v", entry.Data[k]))
		if f.ForceColor {
			coloredVal := fmt.Sprintf("%s%s%s", colorWhite, val, colorReset)
			if k == "error" {
				coloredVal = fmt.Sprintf("%s%s%s", colorRed, val, colorReset)
			}
			fields += fmt.Sprintf("%s%s%s=%s ",
				colorCyan, escapeLogText(k), colorReset, coloredVal)
		} else {
			fields += fmt.Sprintf("%s=%s ", escapeLogText(k), val)
		}
	}

	fields = strings.TrimSpace(fields)

	// 拼接最终输出内容，添加颜色
	escapedCaller := escapeLogText(caller)
	escapedMsg := escapeLogText(entry.Message)
	if f.ForceColor {
		coloredTimestamp := fmt.Sprintf("%s%s%s", colorGray, timestamp, resetColor)
		coloredCaller := escapedCaller
		if caller != "" {
			coloredCaller = fmt.Sprintf("%s%s%s", colorPurple, escapedCaller, resetColor)
		}
		return []byte(fmt.Sprintf("%s%-5s%s[%s] [%s] %-20s | %s\n",
			levelColor, level, resetColor, coloredTimestamp, fields, coloredCaller, escapedMsg)), nil
	}

	return []byte(fmt.Sprintf("%-5s[%s] [%s] %-20s | %s\n",
		level, timestamp, fields, escapedCaller, escapedMsg)), nil
}

func getGoroutineID() string {
	buf := make([]byte, 64)
	buf = buf[:runtime.Stack(buf, false)]
	// buf 格式: "goroutine 123 [running]:\n..."
	i := 0
	for i < len(buf) && buf[i] != ' ' {
		i++
	}
	if i >= len(buf) {
		return "0"
	}
	buf = buf[i+1:]
	j := 0
	for j < len(buf) && buf[j] != ' ' {
		j++
	}
	return string(buf[:j])
}

// 初始化全局日志设置
func init() {
	ConfigureFromEnv()
}

// ConfigureFromEnv 重新从环境变量应用日志配置。
// 这允许在 main() 中加载 .env 后，让 LOG_LEVEL / LOG_PATH 立即生效。
func ConfigureFromEnv() {
	loggerMu.Lock()
	defer loggerMu.Unlock()

	if activeLogFile != nil {
		_ = activeLogFile.Close()
		activeLogFile = nil
	}

	// 根据环境变量设置全局日志级别
	logLevel := getLogLevelFromEnv()
	appLogger.SetLevel(logLevel)

	writer := io.Writer(os.Stdout)
	logPath := resolveLogPathFromEnv()
	if logPath != "" {
		file, err := openLogFile(logPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "logger: failed to open log file %s: %v\n", logPath, err)
		} else {
			activeLogFile = file
			writer = io.MultiWriter(os.Stdout, &ansiStripWriter{w: file})
		}
	}

	// 默认继续输出到 stdout，同时在可用时落盘到文件
	appLogger.SetOutput(writer)

	// 非终端（如 Docker 日志采集）禁用 ANSI 颜色，避免日志聚合/检索异常
	forceColor := false
	if fi, err := os.Stdout.Stat(); err == nil {
		forceColor = (fi.Mode() & os.ModeCharDevice) != 0
	}

	// 设置日志格式而不修改全局时区
	tmpl := resolveLogFormatFromEnv()
	appLogger.SetFormatter(&CustomFormatter{
		ForceColor:   forceColor,
		Template:     tmpl,
		threadNeeded: strings.Contains(tmpl, "%thread"),
	})
	appLogger.SetReportCaller(false)

	// 标准库 log 原本就写 stderr。默认 slog 的输出经 log.Default()，因此一并覆盖。
	log.SetOutput(LineSafeWriter(os.Stderr))
}

// GetLogger 获取日志实例
func GetLogger(c context.Context) *logrus.Entry {
	if logger := c.Value(types.LoggerContextKey); logger != nil {
		return logger.(*logrus.Entry)
	}
	return logrus.NewEntry(appLogger)
}

// SetOutput overrides the internal logger's output destination.
// Intended for use in tests that need to capture and assert on log content
// (e.g. verifying secrets are not written out). Restore the original writer
// (usually os.Stdout) in a defer after the test.
func SetOutput(w io.Writer) {
	loggerMu.Lock()
	defer loggerMu.Unlock()
	appLogger.SetOutput(w)
}

// SetLogLevel 设置日志级别
func SetLogLevel(level LogLevel) {
	var logLevel logrus.Level

	switch level {
	case LevelDebug:
		logLevel = logrus.DebugLevel
	case LevelInfo:
		logLevel = logrus.InfoLevel
	case LevelWarn:
		logLevel = logrus.WarnLevel
	case LevelError:
		logLevel = logrus.ErrorLevel
	case LevelFatal:
		logLevel = logrus.FatalLevel
	default:
		logLevel = logrus.InfoLevel
	}

	appLogger.SetLevel(logLevel)
}

// getLogLevelFromEnv 从环境变量读取日志级别配置
func getLogLevelFromEnv() logrus.Level {
	// 从环境变量读取LOG_LEVEL配置
	logLevelStr := strings.ToLower(os.Getenv("LOG_LEVEL"))

	switch logLevelStr {
	case "debug":
		return logrus.DebugLevel
	case "info":
		return logrus.InfoLevel
	case "warn", "warning":
		return logrus.WarnLevel
	case "error":
		return logrus.ErrorLevel
	case "fatal":
		return logrus.FatalLevel
	default:
		return logrus.DebugLevel // 无效配置时使用默认值
	}
}

func resolveLogPathFromEnv() string {
	if logPath := strings.TrimSpace(os.Getenv("LOG_PATH")); logPath != "" {
		return filepath.Clean(logPath)
	}
	return defaultMacAppLogPath()
}

// resolveLogFormatFromEnv 从环境变量 LOG_FORMAT 读取自定义日志格式模板。
// 为空则使用内置默认格式；非空则作为模板，支持占位符：
// %d=时间 %level=级别 %thread=goroutine %logger=caller %traceId=请求ID %msg=消息+结构化字段
func resolveLogFormatFromEnv() string {
	return strings.TrimSpace(os.Getenv("LOG_FORMAT"))
}

func defaultMacAppLogPath() string {
	execPath, err := os.Executable()
	if err != nil || !strings.Contains(execPath, ".app/Contents/MacOS") {
		return ""
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	appName := "WeKnora Lite"
	if idx := strings.Index(execPath, ".app/Contents/MacOS"); idx >= 0 {
		bundleName := filepath.Base(execPath[:idx+4])
		if trimmed := strings.TrimSuffix(bundleName, ".app"); trimmed != "" {
			appName = trimmed
		}
	}

	return filepath.Join(homeDir, "Library", "Logs", appName, appName+".log")
}

func openLogFile(logPath string) (io.WriteCloser, error) {
	dir := filepath.Dir(logPath)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	return &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    50, // megabytes
		MaxBackups: 3,
		MaxAge:     28, // days
		Compress:   true,
	}, nil
}

// 添加调用者字段
func addCaller(entry *logrus.Entry, skip int) *logrus.Entry {
	pc, file, line, ok := runtime.Caller(skip)
	if !ok {
		return entry
	}
	shortFile := path.Base(file)
	funcName := "unknown"
	if fn := runtime.FuncForPC(pc); fn != nil {
		// 只保留函数名，不带包路径（如 doSomething）
		fullName := path.Base(fn.Name())
		parts := strings.Split(fullName, ".")
		funcName = parts[len(parts)-1]
	}
	return entry.WithField("caller", fmt.Sprintf("%s:%d[%s]", shortFile, line, funcName))
}

// WithRequestID 在日志中添加请求ID
func WithRequestID(c context.Context, requestID string) context.Context {
	return WithField(c, "request_id", requestID)
}

// WithField 向日志中添加一个字段
func WithField(c context.Context, key string, value interface{}) context.Context {
	logger := GetLogger(c).WithField(key, value)
	return context.WithValue(c, types.LoggerContextKey, logger)
}

// WithFields 向日志中添加多个字段
func WithFields(c context.Context, fields logrus.Fields) context.Context {
	logger := GetLogger(c).WithFields(fields)
	return context.WithValue(c, types.LoggerContextKey, logger)
}

// Debug 输出调试级别的日志
func Debug(c context.Context, args ...interface{}) {
	addCaller(GetLogger(c), 2).Debug(args...)
}

// Debugf 使用格式化字符串输出调试级别的日志
func Debugf(c context.Context, format string, args ...interface{}) {
	addCaller(GetLogger(c), 2).Debugf(format, args...)
}

// Info 输出信息级别的日志
func Info(c context.Context, args ...interface{}) {
	addCaller(GetLogger(c), 2).Info(args...)
}

// Infof 使用格式化字符串输出信息级别的日志
func Infof(c context.Context, format string, args ...interface{}) {
	addCaller(GetLogger(c), 2).Infof(format, args...)
}

// Warn 输出警告级别的日志
func Warn(c context.Context, args ...interface{}) {
	addCaller(GetLogger(c), 2).Warn(args...)
}

// Warnf 使用格式化字符串输出警告级别的日志
func Warnf(c context.Context, format string, args ...interface{}) {
	addCaller(GetLogger(c), 2).Warnf(format, args...)
}

// Fields aliases logrus.Fields so callers in other packages can use the
// short form `logger.Fields{...}` without importing logrus directly.
type Fields = logrus.Fields

// WarnWithFields emits a warning with structured fields. Use this for
// audit-relevant events (cross-tenant probes, invariant violations) so that
// log aggregators can index the tenant/resource identifiers without
// parsing free-form text. Format-string style (Warnf) is appropriate for
// low-stakes diagnostic messages.
func WarnWithFields(c context.Context, fields Fields, msg string) {
	if fields == nil {
		fields = Fields{}
	}
	addCaller(GetLogger(c), 2).WithFields(fields).Warn(msg)
}

// Error 输出错误级别的日志
func Error(c context.Context, args ...interface{}) {
	addCaller(GetLogger(c), 2).Error(args...)
}

// Errorf 使用格式化字符串输出错误级别的日志
func Errorf(c context.Context, format string, args ...interface{}) {
	addCaller(GetLogger(c), 2).Errorf(format, args...)
}

// ErrorWithFields 输出带有额外字段的错误级别日志
//
// Agent24 KB-OBS-01: only the "error" key is guaranteed. A caller-supplied "error" value is always
// removed, and a non-nil err is recorded as error_len and error_type instead, because the error text
// can quote a user question, a vendor reply or a user path. The map is copied so the delete leaves
// the caller's map unchanged.
// Every other field is the caller's responsibility and is written as given.
func ErrorWithFields(c context.Context, err error, fields logrus.Fields) {
	copied := make(logrus.Fields, len(fields)+2)
	for k, v := range fields {
		copied[k] = v
	}
	delete(copied, "error")
	if err != nil {
		copied["error_len"] = len(fmt.Sprint(err))
		copied["error_type"] = fmt.Sprintf("%T", err)
	}
	addCaller(GetLogger(c), 2).WithFields(copied).Error("发生错误")
}

// Fatal 输出致命级别的日志并退出程序
func Fatal(c context.Context, args ...interface{}) {
	addCaller(GetLogger(c), 2).Fatal(args...)
}

// Fatalf 使用格式化字符串输出致命级别的日志并退出程序
func Fatalf(c context.Context, format string, args ...interface{}) {
	addCaller(GetLogger(c), 2).Fatalf(format, args...)
}

// CloneContext 复制上下文中的关键信息到新上下文
//
// Which keys survive is decided by types.contextCloneAcrossDetach, which lives
// next to where context keys are declared so that adding a key and deciding
// its fate are the same edit. Keeping that decision here instead meant every
// new key silently defaulted to being dropped.
func CloneContext(ctx context.Context) context.Context {
	newCtx := context.Background()

	for _, k := range types.ContextKeysClonedAcrossDetach() {
		if v := ctx.Value(k); v != nil {
			newCtx = context.WithValue(newCtx, k, v)
		}
	}

	// Preserve the active OpenTelemetry span across the rebuild. The Langfuse
	// *Trace handle above carries the trace id, but span PARENTING flows through
	// the OTel span context (trace.SpanFromContext), which CloneContext would
	// otherwise drop — orphaning child spans opened after a CloneContext (e.g.
	// the agent engine's agent.execute becoming a separate trace from the HTTP
	// root). Re-inject the recording span so children stitch to the same trace.
	if sp := trace.SpanFromContext(ctx); sp.IsRecording() {
		newCtx = trace.ContextWithSpan(newCtx, sp)
	}

	return newCtx
}

// CloneContextWithoutTrace copies the same identity keys as CloneContext but
// drops the Langfuse *Trace handle and the OpenTelemetry span. Use it for
// background work that must keep tenant/session identity yet must not attach
// child spans (Docker Engine HTTP, idle sweeps, image pulls kicked off after
// a tool has returned) onto the originating chat trace.
func CloneContextWithoutTrace(ctx context.Context) context.Context {
	newCtx := context.Background()
	for _, k := range types.ContextKeysClonedAcrossDetach() {
		if k == types.LangfuseTraceContextKey {
			continue
		}
		if v := ctx.Value(k); v != nil {
			newCtx = context.WithValue(newCtx, k, v)
		}
	}
	return newCtx
}

// PanicValue 格式化 panic 值。runtime 自己的错误只带下标、长度和类型名，
// 所以仅当值实现 runtime.Error，且动态类型（指针取 Elem）的 PkgPath 为 "runtime" 时返回 Error()。
// 其余值只记类型和长度，避免正文进入日志。
func PanicValue(r any) string {
	if re, ok := r.(runtime.Error); ok {
		t := reflect.TypeOf(re)
		if t != nil {
			if t.Kind() == reflect.Pointer {
				t = t.Elem()
			}
			if t.PkgPath() == "runtime" {
				return re.Error()
			}
		}
	}
	return fmt.Sprintf("%T (len=%d)", r, len(fmt.Sprint(r)))
}
