package backend

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestIsURLSafeForFetch 验证 SSRF URL 预检：IP 字面量 host 无需真实 DNS，
// net.LookupIP 对字面量直接返回该 IP，测试确定且不依赖网络。
func TestIsURLSafeForFetch(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"loopback IPv4", "http://127.0.0.1/x", false},
		{"loopback IPv6", "http://[::1]/x", false},
		{"private 10/8", "http://10.0.0.1/x", false},
		{"private 192.168/16", "http://192.168.1.1/x", false},
		{"private 172.16/12", "http://172.16.0.1/x", false},
		{"link-local", "http://169.254.0.1/x", false},
		{"CGNAT 100.64/10", "http://100.64.0.1/x", false},
		{"unspecified", "http://0.0.0.0/x", false},
		{"public IPv4", "http://8.8.8.8/x", true},
		{"public IPv6", "http://[2606:4700:4700::1111]/x", true},
		{"non-http scheme", "ftp://8.8.8.8/x", false},
		{"javascript scheme", "javascript:alert(1)", false},
		{"no host", "http:///path", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		if got := isURLSafeForFetch(c.url); got != c.want {
			t.Errorf("%s: isURLSafeForFetch(%q) = %v, want %v", c.name, c.url, got, c.want)
		}
	}
}

// TestIsSafeMediaContentType 验证媒体 Content-Type 白名单：拒绝 HTML/风控页。
func TestIsSafeMediaContentType(t *testing.T) {
	cases := []struct {
		ct   string
		want bool
	}{
		{"video/mp4", true},
		{"application/vnd.apple.mpegurl", true},
		{"video/mp4; charset=utf-8", true},
		{"Video/MP4", true}, // 大小写不敏感
		{"text/html", false},
		{"text/html; charset=utf-8", false},
		{"application/json", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isSafeMediaContentType(c.ct); got != c.want {
			t.Errorf("isSafeMediaContentType(%q) = %v, want %v", c.ct, got, c.want)
		}
	}
}

// TestClientIPIgnoresSpoofedXFF 验证默认（trustProxy=false）下客户端伪造的
// X-Forwarded-For 不被信任，限流以 TCP 对端地址为准。
func TestClientIPIgnoresSpoofedXFF(t *testing.T) {
	orig := trustProxy
	defer func() { trustProxy = orig }()

	trustProxy = false
	r := httptest.NewRequest("GET", "http://h/api/x", nil)
	r.RemoteAddr = "203.0.113.5:54321"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	r.Header.Set("X-Real-IP", "5.6.7.8")
	if got := clientIP(r); got != "203.0.113.5" {
		t.Fatalf("trustProxy=false: clientIP = %q, want TCP peer 203.0.113.5", got)
	}

	trustProxy = true
	if got := clientIP(r); got != "1.2.3.4" {
		t.Fatalf("trustProxy=true: clientIP = %q, want XFF first 1.2.3.4", got)
	}
}

// TestGzipMiddlewareClosesStream 回归测试：gzip 响应必须完整输出。
// 曾出现 gzip.Writer 未 Close 导致响应体为空（浏览器 fetch 收到 200 但
// body 为空）的线上级 bug。
func TestGzipMiddlewareClosesStream(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"isSuccess":true,"data":[1,2,3]}`))
	})
	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	gzipMiddleware(inner).ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if string(body) != `{"isSuccess":true,"data":[1,2,3]}` {
		t.Fatalf("gzip 响应体不完整: got %q", body)
	}
}

// TestGzipMiddlewareSkipsProxy 验证代理路径不启用 gzip（流式媒体透传）。
func TestGzipMiddlewareSkipsProxy(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`x`))
	})
	req := httptest.NewRequest("GET", "/api/proxy/media?url=http://8.8.8.8/v.mp4", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	gzipMiddleware(inner).ServeHTTP(rec, req)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("proxy 路径不应压缩，got Content-Encoding=%q", got)
	}
}
