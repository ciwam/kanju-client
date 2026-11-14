package backend

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// dbPathForExe 返回给定可执行文件路径对应的默认数据库路径：
// 恒为 <可执行文件所在目录>/data/kanju.db，与程序所在目录绑定。
func dbPathForExe(exe string) string {
	if dir := filepath.Dir(exe); dir != "" {
		return filepath.Join(dir, "data", "kanju.db")
	}
	return DefaultDBPath
}

// resolveDBPath returns the database file path.
// If dbPath is explicitly provided (via --db) it is used as-is (absolute or
// relative to the current working directory), after cleaning to prevent path
// traversal. Otherwise the database lives in a data/ subdirectory next to the
// executable, so a deployment is self-contained and the database travels with
// the binary.
func resolveDBPath(dbPath string) string {
	if dbPath != "" {
		// Clean the path to prevent path traversal, then validate.
		cleaned := filepath.Clean(dbPath)
		if strings.Contains(cleaned, "..") {
			log.Fatal("invalid database path (contains ..): " + dbPath)
		}
		return cleaned
	}
	if exe, err := os.Executable(); err == nil {
		return dbPathForExe(exe)
	}
	return DefaultDBPath
}

// ---- proxy (SSRF-safe) ----

// handleProxyMediaPath 是路径式媒体代理：/api/proxy/m/<scheme>/<host>/<path>?<query>
// 与 query 式 /api/proxy/media?url= 相比，URL 的路径结构保持与上游一致，
// hls.js 对播放列表里的相对分片引用（new URL(rel, manifestUrl)）会自然
// 解析回代理路径，无需前端做引用重写。例如：
//
//	/api/proxy/m/https/a.com/dir/index.m3u8?a=1
//	→ 上游 https://a.com/dir/index.m3u8?a=1
//	相对引用 seg/1.ts → /api/proxy/m/https/a.com/dir/seg/1.ts ✓
func (s *Server) handleProxyMediaPath(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/proxy/m/")
	scheme, hostAndPath, ok := strings.Cut(rest, "/")
	if !ok || (scheme != "http" && scheme != "https") || hostAndPath == "" {
		http.Error(w, "bad proxy path", 400)
		return
	}
	target := scheme + "://" + hostAndPath
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	s.handleProxyMedia(w, r.WithContext(context.WithValue(r.Context(), proxyTargetKey{}, target)))
}

// proxyTargetKey 供 handleProxyMediaPath 把上游目标 URL 传给 handleProxyMedia。
type proxyTargetKey struct{}

// allowedMediaTypes 是媒体代理允许透传的 Content-Type 白名单。
// 拒绝非媒体类型（如 text/html）防止颊存资源站返回风控页被当作媒体流发给浏览器。
var allowedMediaTypes = map[string]bool{
	"video/mp4":                     true,
	"video/x-flv":                   true,
	"video/webm":                    true,
	"video/ogg":                     true,
	"video/mp2t":                    true,
	"application/x-mpegurl":         true,
	"audio/mpegurl":                 true, // 部分 CDN 用 audio/mpegurl 标注 HLS 播放列表（如 GCS）
	"audio/x-mpegurl":               true,
	"application/vnd.apple.mpegurl": true,
	"application/dash+xml":          true,
	"application/octet-stream":      true,
	"audio/mpeg":                    true,
	"audio/aac":                     true,
	"audio/ogg":                     true,
}

func isSafeMediaContentType(ct string) bool {
	// 去掉参数部分（如 "; charset=utf-8"）再匹配
	ct = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	return allowedMediaTypes[ct]
}

func (s *Server) handleProxyPage(w http.ResponseWriter, r *http.Request) {
	rawURL := r.URL.Query().Get("url")
	if !isURLSafeForFetch(rawURL) {
		http.Error(w, "forbidden", 403)
		return
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: ssrfSafeTransport}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	req.Header.Set("User-Agent", desktopUA)
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	// 限制页面代理响应体大小，防止上游返回超大内容耗尽内存
	const maxProxyPageBytes int64 = 10 * 1024 * 1024 // 10 MiB
	_, _ = io.Copy(w, io.LimitReader(resp.Body, maxProxyPageBytes))
}

// doubanImageHosts 是豆瓣图床的所有镜像主机；同一张海报在各主机上的路径完全一致，
// 因此当某一个主机返回风控挑战页（200 + text/html）或 418 时，轮换到其它主机通常能取到真图。
var doubanImageHosts = []string{
	"img1.doubanio.com", "img2.doubanio.com", "img3.doubanio.com",
	"img4.doubanio.com", "img5.doubanio.com", "img6.doubanio.com",
	"img7.doubanio.com", "img8.doubanio.com", "img9.doubanio.com",
}

// buildImageAttempts 生成图床重试候选：豆瓣图床轮换同源主机；其它主机（或非 http(s)）原样返回。
// 最多返回 maxAttempts 个，避免极端情况下把请求拖太久。
func buildImageAttempts(rawURL string, maxAttempts int) []string {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.HasSuffix(u.Host, "doubanio.com") {
		return []string{rawURL}
	}
	order := []string{u.Host}
	for _, h := range doubanImageHosts {
		if h != u.Host {
			order = append(order, h)
		}
	}
	out := make([]string, 0, maxAttempts)
	for i, h := range order {
		if i >= maxAttempts {
			break
		}
		cu := *u
		cu.Host = h
		out = append(out, cu.String())
	}
	return out
}

// lastGoodImageHost 记录最近一次成功取到真图的豆瓣图床主机。实测中豆瓣风控会让某些主机
// (如 img9 返回 200+HTML 挑战页、img1 返回 418) 长期不可用，而另一些(如 img2) 长期可用；
// 优先尝试上次成功的主机，可把每张图的冗余请求从 ~3 次降到 1 次。会随风控变化自愈。
var (
	imageHostMu       sync.Mutex
	lastGoodImageHost string
)

// prioritizeGoodHost 把最近成功的图床主机提到候选首位（去重），其余保持原顺序。
func prioritizeGoodHost(attempts []string, good string) []string {
	if good == "" {
		return attempts
	}
	out := make([]string, 0, len(attempts))
	first := true
	for _, a := range attempts {
		u, err := url.Parse(a)
		if err != nil {
			continue
		}
		if first && u.Host == good {
			out = append(out, a)
			first = false
		}
	}
	seen := make(map[string]bool, len(out))
	for _, a := range out {
		seen[a] = true
	}
	for _, a := range attempts {
		if seen[a] {
			continue
		}
		out = append(out, a)
	}
	return out
}

// statusWriter 包装 http.ResponseWriter 以捕获最终写回的状态码，供访问日志使用。
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

// imageFailCache 图片代理负缓存：上游超时/全挂的 URL 短期内记住，期间
// 直接 404 不再出网——一屏几十张图同时超时（TMDB 不通的网络环境）时，
// 没有负缓存每次滚动都会把同样的超时重放一遍，既拖慢渲染又刷屏日志。
var (
	imageFailMu    sync.Mutex
	imageFailCache = map[string]time.Time{}
)

const imageFailTTL = 5 * time.Minute

func imageFailGet(rawURL string) bool {
	imageFailMu.Lock()
	defer imageFailMu.Unlock()
	t, ok := imageFailCache[rawURL]
	return ok && time.Since(t) < imageFailTTL
}

func imageFailPut(rawURL string) {
	imageFailMu.Lock()
	defer imageFailMu.Unlock()
	if len(imageFailCache) > 500 { // 简单上限：超了整体重建
		imageFailCache = map[string]time.Time{}
	}
	imageFailCache[rawURL] = time.Now()
}

func (s *Server) handleProxyImage(w http.ResponseWriter, r *http.Request) {
	rawURL := r.URL.Query().Get("url")
	if !isURLSafeForFetch(rawURL) {
		logWarn("[proxy-image] 非法图片地址，已拒发 403: url=%s", rawURL)
		http.Error(w, "forbidden", 403)
		return
	}
	// 负缓存命中：已知这张图近期取不到，直接 404（客户端 <img> 静默占位）。
	if imageFailGet(rawURL) {
		http.Error(w, "not an image", 404)
		return
	}
	// 豆瓣图床命中风控时，轮换到其它 imgN 主机重试（路径一致），并交替带/不带 Referer；
	// 部分边缘节点只对其中一种放行。仅在确有候选时才重试，普通站点原样请求。
	attempts := buildImageAttempts(rawURL, 4)
	imageHostMu.Lock()
	good := lastGoodImageHost
	imageHostMu.Unlock()
	attempts = prioritizeGoodHost(attempts, good)
	client := &http.Client{Timeout: 5 * time.Second, Transport: ssrfSafeTransport}
	var lastErr error
	for i, u := range attempts {
		if i > 0 {
			// 轻量退避，缓解风控速率限制
			time.Sleep(time.Duration(100+i*80) * time.Millisecond)
		}
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", desktopUA)
		req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
		// 交替尝试带/不带 Referer：部分豆瓣边缘节点仅对其中一种放行
		if i%2 == 0 {
			req.Header.Set("Referer", "https://movie.douban.com/")
		} else {
			req.Header.Del("Referer")
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		// 校验状态码与内容类型：风控挑战页是 200 + text/html，绝不能作为图片下发给浏览器（否则灰屏）。
		ct := resp.Header.Get("Content-Type")
		if resp.StatusCode != http.StatusOK || ct == "" || !strings.HasPrefix(ct, "image/") {
			resp.Body.Close()
			lastErr = fmt.Errorf("status=%d ct=%q", resp.StatusCode, ct)
			continue
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 10*1024*1024))
		resp.Body.Close()
		// 记下来，下次优先走这个图床，少做冗余重试
		if hu, e := url.Parse(u); e == nil {
			imageHostMu.Lock()
			lastGoodImageHost = hu.Host
			imageHostMu.Unlock()
		}
		return
	}
	imageFailPut(rawURL)
	logWarn("[proxy-image] 全部图床尝试失败，已拒发 404（5 分钟内同 URL 不再重试）: url=%s err=%v", rawURL, lastErr)
	// 豆瓣图床全挂：按片名（客户端随请求带 t= 参数）到 TMDB 检索替代海报。
	if title := strings.TrimSpace(r.URL.Query().Get("t")); title != "" {
		poster, ok := s.tmdbPosterFallback(title)
		if ok {
			posterReq, err := http.NewRequest(http.MethodGet, poster, nil)
			if err == nil {
				posterReq.Header.Set("User-Agent", desktopUA)
				pResp, err := (&http.Client{Timeout: 5 * time.Second, Transport: ssrfSafeTransport}).Do(posterReq)
				if err == nil && pResp.StatusCode == http.StatusOK &&
					strings.HasPrefix(pResp.Header.Get("Content-Type"), "image/") {
					defer pResp.Body.Close()
					w.Header().Set("Content-Type", pResp.Header.Get("Content-Type"))
					w.Header().Set("Cache-Control", "public, max-age=86400")
					_, _ = io.Copy(w, io.LimitReader(pResp.Body, 10*1024*1024))
					logInfo("[proxy-image] 豆瓣图挂，TMDB 海报顶上: %q", title)
					return
				}
				if pResp != nil {
					pResp.Body.Close()
				}
			}
		}
	}
	http.Error(w, "not an image", 404)
}

// tmdbPosterFallback 带缓存的按片名 TMDB 海报检索（结果正负都缓存 6h，
// 风控期间不反复打 TMDB）。
func (s *Server) tmdbPosterFallback(title string) (string, bool) {
	if poster, ok := cachedTMDBPoster(title); ok {
		return poster, poster != ""
	}
	poster, ok := tmdbPosterByTitle(title)
	putTMDBPosterCache(title, poster)
	if !ok {
		logInfo("[proxy-image] TMDB 海报检索无结果: %q", title)
	}
	return poster, ok
}

// handleProxyMedia streams arbitrary media (m3u8 playlists, .ts segments, mp4,
// AES keys, ...) through the SSRF-safe fetcher so the browser can download
// offline content without being blocked by cross-origin restrictions. Bytes are
// streamed (not buffered) so large files do not exhaust memory.
//
// 注意：不能给 http.Client 设置整体 Timeout——那是"发起请求到读完 body"的
// 总超时，大视频下载（数十分钟）会在接近 120s 时被强制掐断，浏览器端表现
// 为"下载快完成时 Failed to Fetch"。改为仅限制连接建立与响应头等待：
//   - ssrfSafeTransport.DialContext 已有连接超时（防挂起拨号）
//   - ResponseHeaderTimeout 防上游迟迟不返回响应头
//
// body 流式读取不受总时长限制，靠客户端断开（r.Context()）与字节上限终止。
func (s *Server) handleProxyMedia(w http.ResponseWriter, r *http.Request) {
	rawURL, ok := r.Context().Value(proxyTargetKey{}).(string)
	if !ok || rawURL == "" {
		rawURL = r.URL.Query().Get("url")
	}
	if !isURLSafeForFetch(rawURL) {
		http.Error(w, "forbidden", 403)
		return
	}
	// 复用带连接/响应头超时的 Transport；Client 不设整体 Timeout（见上注释）
	client := &http.Client{Transport: ssrfSafeTransport}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	req.Header.Set("User-Agent", desktopUA)
	req.Header.Set("Referer", rawURL)
	// 透传浏览器的 Range 头（hls.js 分片直链/直链 mp4 拖进度条时会发
	// Range 请求）：不带 Range 时上游会从头发全量数据且不返回 206，
	// 浏览器端 seek 表现为重新缓冲整个前缀。
	if rng := r.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	defer resp.Body.Close()
	// 206 Partial Content 是对 Range 请求的正常应答，放行；
	// 其余非 2xx 视为上游失败。
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		logWarn("[proxy-media] 上游返回非 200/206，已拒发 404: url=%s status=%d", rawURL, resp.StatusCode)
		http.Error(w, "upstream error", 404)
		return
	}
	if resp.StatusCode == http.StatusPartialContent {
		w.WriteHeader(http.StatusPartialContent)
	}
	// 透传上游的 Content-Range（206 时浏览器靠它确定总长与切片位置）
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		w.Header().Set("Content-Range", cr)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "" {
		// 白名单验证：拒绝非媒体类型（如风控页 text/html）防止 XSS 潜在风险
		if !isSafeMediaContentType(ct) {
			logWarn("[proxy-media] 上游返回不允许的 Content-Type，已拒绝: ct=%q url=%s", ct, rawURL)
			http.Error(w, "unsupported media type", 415)
			return
		}
		w.Header().Set("Content-Type", ct)
	}
	// Forward the upstream Content-Length when known so the client can report
	// the full file size (used by progressive playback for the "downloaded /
	// total" badge and a definite Range total from the streaming proxy).
	if resp.ContentLength > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	// 媒体可能很大（整段 mp4），仅做上限保护，默认流式透传。
	const maxMediaBytes int64 = 3 << 30 // 3 GiB
	_, _ = io.Copy(w, io.LimitReader(resp.Body, maxMediaBytes))
}

// ---- poster fallback (alternate source) ----
//
// 豆瓣图床对「部分」海报做了全主机风控（img1..img9 全部返回 200+HTML 挑战页 / 418），
// 这种海报无法通过轮换同源主机救回。此时回退到已配置的 MacCMS 资源站，按片名检索 vod_pic
// 作为替代海报。MacCMS 海报走既有 /api/proxy/image（已 SSRF 加固），前端拿到的仍是
// 一个可被同源代理下来的地址。按片名检索开销较大（需逐个站源发网络请求），故做了内存缓存
// （含负缓存，避免未命中时反复检索）与并发上限（避免打爆站源）。

var (
	posterCacheMu sync.Mutex
	posterCache   = map[string]posterCacheEntry{}
)

type posterCacheEntry struct {
	poster string
	exp    time.Time
}

const posterCacheTTL = 24 * time.Hour

// maxPosterCacheEntries 限制内存中缓存的最大条目数，防止无界增长。
const maxPosterCacheEntries = 500

func getCachedPoster(title string) (string, bool) {
	posterCacheMu.Lock()
	defer posterCacheMu.Unlock()
	e, ok := posterCache[title]
	if !ok || time.Now().After(e.exp) {
		if ok {
			delete(posterCache, title)
		}
		return "", false
	}
	return e.poster, true
}

func setCachedPoster(title, poster string) {
	posterCacheMu.Lock()
	defer posterCacheMu.Unlock()
	// 达到上限时逐出一条旧缓存（map 迭代顺序随机，效果等同 LRU 近似）
	if _, exists := posterCache[title]; !exists && len(posterCache) >= maxPosterCacheEntries {
		for k := range posterCache {
			delete(posterCache, k)
			break
		}
	}
	posterCache[title] = posterCacheEntry{poster: poster, exp: time.Now().Add(posterCacheTTL)}
}

// normalizeTitle 去掉空格（半/全角）并转小写，便于片名模糊比对。
func normalizeTitle(t string) string {
	r := strings.ToLower(strings.TrimSpace(t))
	r = strings.ReplaceAll(r, " ", "")
	r = strings.ReplaceAll(r, "　", "")
	return r
}

// titlesMatch 判断两个片名是否指向同一作品：标准化后全等，或一方（≥2 字符）被另一方包含。
// 后者用于「银魂」⊂「银魂 走光篇」、「钢铁侠」⊂「钢铁侠3」这类常见差异。
func titlesMatch(a, b string) bool {
	an := normalizeTitle(a)
	bn := normalizeTitle(b)
	if an == "" || bn == "" {
		return false
	}
	if an == bn {
		return true
	}
	short, long := an, bn
	if len(short) > len(long) {
		short, long = long, short
	}
	if utf8.RuneCountInString(short) >= 2 && strings.Contains(long, short) {
		return true
	}
	return false
}

// findPosterByTitle 在已启用的资源站中按片名检索替代海报。并发检索、首个命中即返回，
// 失败（全部未命中）返回 ("", false)。结果按片名缓存（含负缓存）。
func (s *Server) findPosterByTitle(ctx context.Context, title string) (string, bool) {
	if title == "" {
		return "", false
	}
	if p, ok := getCachedPoster(title); ok {
		if p == "" {
			return "", false
		}
		return p, true
	}
	sources, err := s.getActiveSiteSources()
	if err != nil {
		logErrorStack("[poster] 读取资源站失败: %v", err)
		return "", false
	}
	const maxSources = 4
	if len(sources) > maxSources {
		sources = sources[:maxSources]
	}
	type posterResult struct {
		poster string
		source string
	}
	ch := make(chan posterResult, len(sources))
	sem := make(chan struct{}, 4) // 并发上限，避免一次性打爆所有站源
	var wg sync.WaitGroup
	for _, src := range sources {
		wg.Add(1)
		go func(src *SiteSource) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res, serr := s.search(ctx, src, title)
			if serr != nil {
				ch <- posterResult{}
				return
			}
			data, ok := res["data"].([]map[string]any)
			if !ok {
				ch <- posterResult{}
				return
			}
			for _, item := range data {
				t := getMapStr(item, "title")
				cover := getMapStr(item, "cover")
				if cover == "" {
					continue
				}
				if titlesMatch(t, title) {
					ch <- posterResult{poster: cover, source: src.Name}
					return
				}
			}
			ch <- posterResult{}
		}(src)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()
	found := ""
	for res := range ch {
		if res.poster != "" {
			found = res.poster
			break
		}
	}
	setCachedPoster(title, found)
	if found == "" {
		return "", false
	}
	return found, true
}

// handlePoster 供前端在豆瓣海报彻底失败时，按片名回退检索替代海报。
// 返回 { "poster": "<rawURL>" }，前端再经 /api/proxy/image 同源代理下来。
func (s *Server) handlePoster(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	if title == "" {
		writeJSON(w, 400, map[string]any{"poster": ""})
		return
	}
	poster, ok := s.findPosterByTitle(r.Context(), title)
	if !ok {
		writeJSON(w, 404, map[string]any{"poster": ""})
		return
	}
	writeJSON(w, 200, map[string]any{"poster": poster})
}

// fallbackKeywords 生成站源搜索的渐进关键词：完整标题 → 去掉「-…-」
// 副标题段 → 主标题。TMDB/豆瓣的完整标题常带副标题与外文名，站源
// 搜不到，简化后能命中；titlesMatch 仍然把关，不会聚错剧。
func fallbackKeywords(title string) []string {
	kws := []string{title}
	// 去掉「-…-」副标题段（TMDB 中文标题的常见格式：主标题 -副标题-）。
	if idx := strings.Index(title, " -"); idx > 0 {
		kws = append(kws, strings.TrimSpace(title[:idx]))
	}
	// 「主标题 第N季」再降为主标题。
	if idx := strings.LastIndex(title, " 第"); idx > 0 {
		kws = append(kws, strings.TrimSpace(title[:idx]))
	}
	// 去重（保持顺序）。
	seen := map[string]bool{}
	var out []string
	for _, k := range kws {
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"status": "UP"})
}

// ---- CORS ----

// isDevOrigin 返回 origin 是否是已知的本地开发源。
// 生产常景（前后端同源部署）请求没有 Origin 头，不会进入此函数。
// 开发模式（Vite dev server）需要允许跨源读写。
func isDevOrigin(origin string) bool {
	switch origin {
	case "http://localhost:5173",
		"http://localhost:3000",
		"http://127.0.0.1:5173",
		"http://127.0.0.1:3000":
		return true
	}
	return false
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		// 只对已知开发源开放 CORS，拒绝反射任意 Origin（否则与 Allow-Credentials 合用会允许任意站点携凭证认请求）。
		if isDevOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders 添加必要的 HTTP 安全响应头。
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		// CSP：跳过 /api/proxy/* —— 代理回来的第三方播放页常含内联脚本，
		// 若套用 'self' 的 script-src 会被拦截导致 iframe 播放失效。
		// connect-src/media-src 放行 http(s) 是 hls.js 直连上游媒体的需求。
		// wasm-unsafe-eval 允许 WebAssembly.instantiate（wasm UI 站点必需，
		// 不放行任意脚本执行，不影响 CSP 强度）。
		if !strings.HasPrefix(r.URL.Path, "/api/proxy/") {
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; "+
					"img-src 'self' data: blob: http: https:; media-src 'self' blob: http: https:; "+
					"frame-src 'self'; connect-src 'self' blob: http: https:; worker-src 'self' blob:; "+
					"object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		}
		next.ServeHTTP(w, r)
	})
}

// ---- gzip ----

// gzipResponseWriter 包装 ResponseWriter：在首次写响应头时按 Content-Type
// 决定是否启用 gzip 压缩，避免压缩媒体流（CPU 白耗且无收益）。
type gzipResponseWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	headerWrote bool
}

func (w *gzipResponseWriter) WriteHeader(code int) {
	if !w.headerWrote {
		w.headerWrote = true
		ct := w.Header().Get("Content-Type")
		if w.isCompressible(ct) {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Del("Content-Length") // 压缩后长度未知
			w.gz = gzip.NewWriter(w.ResponseWriter)
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if !w.headerWrote {
		w.WriteHeader(http.StatusOK)
	}
	if w.gz != nil {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// Flush 透传给底层 ResponseWriter（SSE 等流式场景需要）。
func (w *gzipResponseWriter) Flush() {
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// close 结束 gzip 流：gzip.Writer 有内部缓冲，必须 Close 才会把剩余
// 压缩数据与尾部 CRC 写到底层，否则响应体为空（浏览器收到 200 但无 body）。
func (w *gzipResponseWriter) close() {
	if w.gz != nil {
		_ = w.gz.Close()
		w.gz = nil
	}
}

func (w *gzipResponseWriter) isCompressible(ct string) bool {
	switch {
	case strings.HasPrefix(ct, "text/"),
		strings.HasPrefix(ct, "application/json"),
		strings.HasPrefix(ct, "application/javascript"):
		return true
	}
	return false
}

// gzipMiddleware 对可压缩响应启用 gzip。跳过：
//   - /api/proxy/*：媒体/页面代理为流式透传，压缩无收益且有 CPU 开销；
//   - /api/search/stream：SSE 需即时 flush，压缩会引入缓冲延迟。
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/proxy/") || r.URL.Path == "/api/search/stream" {
			next.ServeHTTP(w, r)
			return
		}
		// 客户端不声明支持 gzip 时原样透传
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gzw := &gzipResponseWriter{ResponseWriter: w}
		next.ServeHTTP(gzw, r)
		// 请求结束时关闭 gzip 流，把缓冲数据刷到底层（见 gzipResponseWriter.close）
		gzw.close()
	})
}

// ---- auth gate ----

// isPublicAPI 返回无需登录即可访问的公开接口。
// 仅注册 / 登录 / 登出三个端点匿名开放。
// 代理端点（/api/proxy/*）与搜索流（/api/search/stream）虽由 <img>/<iframe>/
// EventSource/fetch 等浏览器原生请求调用、无法携带 Authorization 头，但前端
// 鉴权已迁移至 httpOnly Cookie（kanju_token），同源请求会自动携带，故均要求
// 登录——否则任意匿名访客可把服务器当作免费 SSRF 代理 / 抓取器消耗带宽。
// 其余 /api/ 接口在非调试模式下均强制要求登录。
func isPublicAPI(path string) bool {
	switch path {
	case "/api/auth/register", "/api/auth/login", "/api/auth/logout":
		return true
	}
	return false
}

// authGate 是统一鉴权中间件：
//   - 调试模式（authBypass 为真，由 --no-auth 开启）：跳过全部 API 鉴权；
//   - 非 API 路径（SPA 静态资源、健康检查）不拦截；
//   - 公开接口（注册/登录/登出）始终放行；
//   - 其余 API 接口强制登录，未携带有效 token 时返回 401。
func (s *Server) authGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// 调试模式：跳过所有 API 鉴权，方便接口调试。
		if authBypass {
			next.ServeHTTP(w, r)
			return
		}

		// 非 API 路径（前端静态资源等）不要求登录。
		if !strings.HasPrefix(path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		// 公开接口始终允许访问。
		if isPublicAPI(path) {
			next.ServeHTTP(w, r)
			return
		}

		// 特例：读取「是否开放注册」需对未登录的登录页公开（否则无法判断是否展示
		// 注册入口），且仅暴露一个布尔值、无敏感信息；但「修改」该设置（POST）必须
		// 登录且为 admin（交由 handleSetRegistration 的 requireAdmin 强制），故此处的
		// 公开放行只针对 GET 方法，POST 仍走下方登录 + admin 鉴权。
		if path == "/api/settings/registration" && r.Method == "GET" {
			next.ServeHTTP(w, r)
			return
		}

		// 其余 API 接口：强制登录鉴权。
		if s.currentUser(r) == nil {
			writeJSON(w, 401, map[string]any{"isSuccess": false, "errorMsg": "未登录，请先登录"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- static (SPA) ----

// handleStatic 从静态文件系统（StaticFS 注入，旧仓库独立部署用）提供 SPA。
// 内嵌客户端不携带 Web 前端，staticFS 为 nil 时一律 404——API 之外
// 的 HTTP 访问只有误配的外部浏览器，不需要页面。
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if s.staticFS == nil {
		http.NotFound(w, r)
		return
	}
	p := strings.TrimPrefix(filepath.Clean(r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	// 文件不存在 → SPA 回退到 index.html（前端路由用）
	if _, err := fs.Stat(s.staticFS, p); err != nil {
		p = "index.html"
	}
	http.ServeFileFS(w, r, s.staticFS, p)
}

// ---- routes ----

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	reg := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc(pattern, h)
	}
	// auth
	reg("POST /api/auth/register", s.handleRegister)
	reg("POST /api/auth/login", s.handleLogin)
	reg("POST /api/auth/logout", s.handleLogout)
	reg("GET /api/auth/me", s.handleMe)
	// settings (注册开关：读取公开，修改仅 admin)
	reg("GET /api/settings/registration", s.handleGetRegistration)
	reg("POST /api/settings/registration", s.handleSetRegistration)
	// settings (默认播放方式：读取需登录，修改仅 admin)
	reg("GET /api/settings/defaultPlayMode", s.handleGetDefaultPlayMode)
	reg("POST /api/settings/defaultPlayMode", s.handleSetDefaultPlayMode)
	// settings (播放完成后自动删除已下载选集：用户级偏好，默认关闭)
	reg("GET /api/settings/autoDeletePlayed", s.handleGetAutoDeletePlayed)
	reg("POST /api/settings/autoDeletePlayed", s.handleSetAutoDeletePlayed)
	// network (出口区域检测：供前端海外网络弹窗；登录后调用)
	reg("GET /api/network/region", s.handleNetworkRegion)
	// admin
	reg("GET /api/admin/users", s.handleAdminUsers)
	reg("GET /api/admin/history", s.handleAdminHistory)
	reg("GET /api/admin/favorites", s.handleAdminFavorites)
	// search/detail/play
	reg("GET /api/search/stream", s.handleSearchStream)
	reg("POST /api/detail", s.handleDetail)
	reg("POST /api/play", s.handlePlay)
	// sources
	reg("GET /api/sources", s.handleGetSources)
	reg("POST /api/sources", s.handleSaveSource)
	reg("POST /api/sources/delete", s.handleDeleteSource)
	reg("POST /api/sources/toggle", s.handleToggleSource)
	reg("POST /api/sources/batch", s.handleBatchSaveSources)
	reg("POST /api/sources/check-update", s.handleCheckSourceUpdate)
	reg("POST /api/sources/apply-update", s.handleApplySourceUpdate)
	reg("POST /api/sources/switch-backup", s.handleSwitchToBackup)
	// history / favorites
	reg("GET /api/history", s.handleGetHistory)
	reg("POST /api/history", s.handleSaveHistory)
	reg("GET /api/favorites", s.handleGetFavorites)
	reg("POST /api/favorites", s.handleSaveFavorite)
	reg("POST /api/favorites/delete", s.handleRemoveFavorite)
	// live
	reg("GET /api/live", s.handleGetLiveChannels)
	// douban
	reg("GET /api/douban", s.handleDouban)
	reg("GET /api/douban/subject", s.handleDoubanSubject)
	// poster fallback (alternate source when Douban CDN blocks a poster)
	reg("GET /api/poster", s.handlePoster)
	// proxy
	reg("GET /api/proxy/page", s.handleProxyPage)
	reg("GET /api/proxy/image", s.handleProxyImage)
	reg("GET /api/proxy/media", s.handleProxyMedia)
	reg("GET /api/proxy/m/", s.handleProxyMediaPath)
	// health
	reg("GET /actuator/health", s.handleHealth)
	// 未知 /api/* 路径：明确返回 404 JSON，而非落入下方 SPA catch-all
	// 返回 index.html（200）。否则打错的接口路径会表现为「200 + HTML」，
	// 前端 JSON 解析失败且调试者无法区分「路由不存在」与「服务异常」。
	// 注意：Go 1.22 mux 精确路由优先于该前缀模式，已注册接口不受影响。
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]any{"isSuccess": false, "errorMsg": "接口不存在: " + r.URL.Path})
	})
	// 静态 SPA catch-all：不再内嵌一层 cors，外层已经覆盖所有路由
	mux.HandleFunc("/", s.handleStatic)
	return mux
}

// 构建期注入的版本信息（通过 -ldflags -X 注入；未注入时回退占位）。
// 这样无需重新编译即可在二进制中携带构建时间与 Git 提交，便于问题溯源。
var (
	buildTime = "unknown"
	gitCommit = "unknown"
)

// printVersion writes the build/version information to w.
func printVersion(w io.Writer) {
	fmt.Fprintf(w, "kanju (影视资源聚合服务)\n")
	fmt.Fprintf(w, "构建时间: %s\n", buildTime)
	fmt.Fprintf(w, "Git 提交: %s\n", gitCommit)
}

// printUsage writes the command-line help to w. It documents the program's
// purpose, every flag, and concrete usage examples so operators can configure
// the binary without guessing.
func printUsage(w io.Writer) {
	fmt.Fprint(w, `Kanju 影视资源聚合服务

用途:
  Kanju 是一个影视资源搜索与聚合服务，内置可播放的影视站点源，
  提供搜索、详情、播放与离线下载能力。本程序同时托管 HTTP API 与前端页面。

用法:
  kanju [选项]

选项:
  -p, --port <端口>        HTTP 监听端口 (默认 `+DefaultPort+`)
      --db <路径>          SQLite 数据库文件路径
                          (默认 <可执行文件所在目录>/data/kanju.db；
                           相对路径相对于当前工作目录解析)
      --debug             开启详细调试日志（全链路计时）
      --availability-check 站源可用性探测（默认开启；=false 关闭）
      --no-auth           调试模式：跳过所有 API 接口鉴权（默认关闭，仅本地调试用）
      --trust-proxy       信任反向代理的 X-Forwarded-For / X-Real-IP
                          （仅当服务位于可信反向代理之后时开启，否则客户端可伪造该头绕过限流）
  -h, --help               显示本帮助信息并退出
  -v, --version            显示版本信息 (构建时间 / Git 提交) 并退出

示例:
  kanju                           # 使用默认端口 `+DefaultPort+` 启动
  kanju -p 8080                  # 监听 8080 端口
  kanju --port 8080              # 同上 (-p / --port 等价)
  kanju --db /var/data/kanju.db  # 自定义数据库路径
  kanju --debug                  # 开启详细调试日志
  kanju --availability-check=false  # 关闭站源可用性探测
  kanju --no-auth                # 调试模式：跳过所有 API 鉴权（无需登录即可访问）
  kanju -h                       # 显示帮助信息并退出
  kanju -v                       # 显示版本信息 (构建时间 / Git 提交)

默认管理员账号: admin / admin
`)
}

// isValidPort reports whether s is a valid TCP port number (1-65535).
func isValidPort(s string) bool {
	if s == "" {
		return false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
		if n > 65535 {
			return false
		}
	}
	return n >= 1 && n <= 65535
}
