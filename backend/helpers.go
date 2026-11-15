package backend

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// Server holds shared runtime state.
type Server struct {
	db       *sql.DB
	tokens   *TokenCache
	rl       *rateLimiter
	staticFS fs.FS // 静态资源（内嵌或磁盘覆盖）
	port     string
}

var defaultLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
	Level: slog.LevelInfo,
}))

// debugMode gates all verbose logging. When false (the default for release
// builds), no application logs are emitted at all. It is enabled by passing the
// --debug command-line flag (development mode).
var debugMode = false

// debugEnabled reports whether verbose logging is active.
func debugEnabled() bool { return debugMode }

// logPrint always writes a line to the log. It is NOT gated by debugMode and is
// used for operational essentials that must appear in production logs: startup
// banner, configuration summary, and so on. Verbose per-request logs stay in
// logInfo/logWarn (gated by --debug) so release builds don't flood.
func logPrint(format string, args ...any) {
	defaultLogger.Info(fmt.Sprintf(format, args...))
}

// logInfoC/logWarnC/logErrorC 是带 context 的变体：日志行带
// [trace_id] 前缀，从请求 context 取（见 trace.go）。无 trace 的
// 后台任务（预热/清理循环）用无 C 后缀的原函数。
func logInfoC(ctx context.Context, format string, args ...any) {
	if !debugMode {
		return
	}
	defaultLogger.Info(tracePrefix(ctx) + fmt.Sprintf(format, args...))
}

func logWarnC(ctx context.Context, format string, args ...any) {
	if !debugMode {
		return
	}
	defaultLogger.Warn(tracePrefix(ctx) + fmt.Sprintf(format, args...))
}

func logErrorC(ctx context.Context, format string, args ...any) {
	defaultLogger.Error(tracePrefix(ctx) + fmt.Sprintf(format, args...))
}

func logInfo(format string, args ...any) {
	if !debugMode {
		return
	}
	defaultLogger.Info(fmt.Sprintf(format, args...))
}

func logWarn(format string, args ...any) {
	if !debugMode {
		return
	}
	defaultLogger.Warn(fmt.Sprintf(format, args...))
}

// logError always logs an error-level message. Operational errors must be
// visible in release, not swallowed by the debug gate — only verbose
// info/warn are gated behind --debug.
func logError(format string, args ...any) {
	defaultLogger.Error(fmt.Sprintf(format, args...))
}

// logErrorStack always logs an error together with the current goroutine stack
// trace. Use for unexpected failures where the call path matters for diagnosis.
func logErrorStack(format string, args ...any) {
	defaultLogger.Error(fmt.Sprintf(format, args...), "stack", string(debug.Stack()))
}

// newUUID returns an RFC4122 v4 UUID string without external deps.
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// maxRequestBodyBytes 限制 JSON 请求体大小，防止恶意超大 body 耗尽内存。
const maxRequestBodyBytes = 1 << 20 // 1 MiB

func readJSONMap(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func readJSONRaw(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
}

// ---- map helpers (mirror the Java JsonObject getters) ----

func getStr(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return formatNumber(t)
	case bool:
		return fmt.Sprintf("%v", t)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

func getStrDefault(m map[string]any, key, def string) string {
	v := getStr(m, key)
	if v == "" {
		return def
	}
	return v
}

func getInt(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case json.Number:
		i, _ := t.Int64()
		return int(i)
	case string:
		var i int
		if _, err := fmt.Sscanf(t, "%d", &i); err == nil {
			return i
		}
	}
	return 0
}

func getIntDefault(m map[string]any, key string, def int) int {
	if _, ok := m[key]; !ok {
		return def
	}
	v := getInt(m, key)
	if v == 0 {
		return def
	}
	return v
}

func getBool(m map[string]any, key string) bool {
	v, ok := m[key]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t == "true" || t == "1"
	}
	return false
}

func formatNumber(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%v", f)
}

// siteSourceFromMap builds a SiteSource from a request map (camelCase keys).
func siteSourceFromMap(m map[string]any) *SiteSource {
	return &SiteSource{
		Name:                  getStr(m, "name"),
		BaseURL:               getStr(m, "baseUrl"),
		SearchURL:             getStr(m, "searchUrl"),
		SearchPostData:        getStr(m, "searchPostData"),
		RuleResultList:        getStr(m, "ruleResultList"),
		RuleResultCover:       getStr(m, "ruleResultCover"),
		ResultCoverPrefix:     getStr(m, "resultCoverPrefix"),
		RuleResultLink:        getStr(m, "ruleResultLink"),
		ResultLinkPrefix:      getStr(m, "resultLinkPrefix"),
		RuleResultTitle:       getStr(m, "ruleResultTitle"),
		RuleDetailCover:       getStr(m, "ruleDetailCover"),
		RuleDetailCoverPrefix: getStr(m, "ruleDetailCoverPrefix"),
		RuleDetailIntro:       getStr(m, "ruleDetailIntro"),
		RuleChannelGroups:     getStr(m, "ruleChannelGroups"),
		RuleEpList:            getStr(m, "ruleEpList"),
		RuleEpTitle:           getStr(m, "ruleEpTitle"),
		RuleEpLink:            getStr(m, "ruleEpLink"),
		RuleEpLinkPrefix:      getStr(m, "ruleEpLinkPrefix"),
		RuleIndex1List:        getStr(m, "ruleIndex1List"),
		Index1Title:           getStr(m, "index1Title"),
		RuleIndex2List:        getStr(m, "ruleIndex2List"),
		Index2Title:           getStr(m, "index2Title"),
		RuleIndex3List:        getStr(m, "ruleIndex3List"),
		Index3Title:           getStr(m, "index3Title"),
		RuleIndex4List:        getStr(m, "ruleIndex4List"),
		Index4Title:           getStr(m, "index4Title"),
		RuleIndexLink:         getStr(m, "ruleIndexLink"),
		IndexLinkPrefix:       getStr(m, "indexLinkPrefix"),
		RuleIndexTitle:        getStr(m, "ruleIndexTitle"),
		RuleIndexCover:        getStr(m, "ruleIndexCover"),
		IndexCoverPrefix:      getStr(m, "indexCoverPrefix"),
		RuleIndexActor:        getStr(m, "ruleIndexActor"),
		WebIndexURL:           getStr(m, "webIndexUrl"),
		UserAgent:             getStr(m, "userAgent"),
		ReqType:               getStrDefault(m, "reqType", "get"),
		ReqCharset:            getStrDefault(m, "reqCharset", "utf-8"),
		HtmlCharset:           getStrDefault(m, "htmlCharset", "utf-8"),
		SerialNumber:          getIntDefault(m, "serialNumber", 1),
		IsDisable:             boolToInt(getBool(m, "isDisable")),
		SourceType:            getStrDefault(m, "sourceType", "html"),
		ApiDetailURL:          getStr(m, "apiDetailUrl"),
		RemoteConfigURL:       getStr(m, "remoteConfigUrl"),
		BackupBaseURL:         getStr(m, "backupBaseUrl"),
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// boolToSetting serialises a bool into the "true"/"false" string stored in the
// settings table (distinct from boolToInt, which targets the 0/1 columns used
// by site_source).
func boolToSetting(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// ---- simple in-memory rate limiter ----

type rateLimiter struct {
	mu       sync.Mutex
	attempts map[string]int
	windows  map[string]int64
}

func newRateLimiter() *rateLimiter {
	rl := &rateLimiter{attempts: map[string]int{}, windows: map[string]int64{}}
	go rl.cleanupLoop()
	return rl
}

// cleanupLoop 定期清理过期的频率限制记录，防止内存无界增长。
func (r *rateLimiter) cleanupLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		r.cleanup()
	}
}

// cleanup 移除超过最大窗口期的过期条目。
func (r *rateLimiter) cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().Unix()
	// 最大窗口为登录限流的 300 秒；超过 600 秒的条目可以安全删除。
	for key, last := range r.windows {
		if now-last > 600 {
			delete(r.windows, key)
			delete(r.attempts, key)
		}
	}
}

// limited reports whether the key has exceeded maxAttempts within windowSeconds.
func (r *rateLimiter) limited(key string, maxAttempts, windowSeconds int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().Unix()
	if w, ok := r.windows[key]; !ok || now-w > int64(windowSeconds) {
		r.attempts[key] = 1
		r.windows[key] = now
		return false
	}
	r.attempts[key]++
	return r.attempts[key] > maxAttempts
}

// bearerToken 从 Authorization header 或 httpOnly Cookie 中提取认证 token。
// 优先读取 Cookie（kanju_token），防范 XSS 窃取；同时支持 Authorization header
// 以兼容旧客户端和 API 工具（curl/postman 等）。
func bearerToken(r *http.Request) string {
	// 优先从 httpOnly Cookie 读取（XSS 安全）
	if c, err := r.Cookie("kanju_token"); err == nil && c.Value != "" {
		return c.Value
	}
	// 兼容旧版 Authorization: Bearer <token>
	t := r.Header.Get("Authorization")
	if strings.HasPrefix(t, "Bearer ") {
		return t[len("Bearer "):]
	}
	return t
}

// isDigits 报告 s 是否非空且全为数字（json_api 的 vod_id 校验）。
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sanitizeIntro 清理简介文本：解码 HTML 实体（&nbsp;/&amp;/&#39; 等豆瓣
// 接口与页面抓取常见残留）、全角空格与连续空白归一。UI 直接展示该文本。
func sanitizeIntro(s string) string {
	if s == "" {
		return ""
	}
	s = html.UnescapeString(s)
	// &nbsp; 解码为  ；与全角空格、普通空白一起压缩为单个空格，
	// 再去掉文本首尾（豆瓣简介常用成串 nbsp 做缩进/间隔）。
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '　' || r == 0x00a0 || r == 0x2003
	}), " ")
	return strings.TrimSpace(s)
}
