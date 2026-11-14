// trace 跨前后端的全链路追踪：前端每次 API 请求生成短 trace_id 放进
// X-Trace-ID 头，后端中间件接续（没有则生成）存入 request context；
// 日志行统一带 [trace_id] 前缀。前端日志行也带同一 id（[api t=xxx]），
// 一个操作在日志页里搜 id 即可串起前后端完整链路。
package backend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

type ctxKey int

const traceKey ctxKey = 0

// newTraceID 生成 6 字节 hex 短 id（12 字符），够一次会话内唯一且日志里不占宽。
func newTraceID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// WithTrace 把 trace_id 存入 context（空 id 原样返回）。
func WithTrace(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, traceKey, id)
}

// TraceFromCtx 取 context 里的 trace_id（无则空串）。
func TraceFromCtx(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(traceKey).(string)
	return id
}

// traceMiddleware 每个请求一个 trace_id：优先接续调用方（前端 api.Client）
// 传来的 X-Trace-ID，没有就自己生成。id 存入 request context 供日志取，
// 同时回写在响应头，方便浏览器 Network 面板对照。
func traceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Trace-ID")
		if id == "" || len(id) > 64 {
			id = newTraceID()
		}
		w.Header().Set("X-Trace-ID", id)
		next.ServeHTTP(w, r.WithContext(WithTrace(r.Context(), id)))
	})
}

// tracePrefix 日志行的 trace 段：有 id 返回 "[trace_id=abc123] "，
// 无 id 返回 "[trace_id=-] "（明确标记该行无追踪上下文，与后台任务日志区分）。
func tracePrefix(ctx context.Context) string {
	if id := TraceFromCtx(ctx); id != "" {
		return "[trace_id=" + id + "] "
	}
	return "[trace_id=-] "
}
