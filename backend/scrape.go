package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/antchfx/htmlquery"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const defaultUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15"
const desktopUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0.0.0 Safari/537.36"

func fail(msg string) map[string]any {
	return map[string]any{"isSuccess": false, "errorMsg": msg}
}

func uaOrDefault(ua string) string {
	if ua == "" {
		return defaultUA
	}
	return ua
}

// cgnatBlock 是 IANA 共享地址空间 (RFC 6598, 100.64.0.0/10)，用于运营商级 NAT，
// 不属于 net.IP.IsPrivate() 覆盖范围，但同样不应被代理请求访问。
var cgnatBlock = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("100.64.0.0/10")
	return n
}()

// ssrfSafeDial 是自定义 Dialer，在建立 TCP 连接时再次验证解析到的 IP。
// 这封闭了 isURLSafeForFetch 的 DNS 检查和实际连接之间的窗口，防止 DNS 重绑定攻击。
func ssrfSafeDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("ssrf: 解析地址失败: %w", err)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("ssrf: DNS 解析失败: %q", host)
	}
	for _, a := range ips {
		ip := a.IP
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() ||
			ip.IsLinkLocalUnicast() || ip.IsMulticast() || cgnatBlock.Contains(ip) {
			return nil, fmt.Errorf("ssrf: 拦截私网/保留 IP %s (%q)", ip, host)
		}
	}
	// 直接使用已解析的 IP 撨号建立连接，避免内部再次 DNS 询询
	target := net.JoinHostPort(ips[0].IP.String(), port)
	return (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext(ctx, network, target)
}

// ssrfSafeTransport 是所有导出 HTTP 请求共用的 Transport。
// 通过 ssrfSafeDial 在连接层拦截私网 IP，并具备连接池功能减少 TCP/TLS 重建开销。
var ssrfSafeTransport = &http.Transport{
	DialContext:           ssrfSafeDial,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	// 上游迟迟不返回响应头时终止（防挂起）；body 流式读取不受此限，
	// 大文件下载可长时间进行（handleProxyMedia 无整体 Timeout）
	ResponseHeaderTimeout: 30 * time.Second,
	MaxIdleConns:          200,
	MaxIdleConnsPerHost:   20,
	IdleConnTimeout:       90 * time.Second,
}

// sharedFetchClient 是 fetch 操作共用的 HTTP 客户端。使用 ssrfSafeTransport 提供
// DNS 重绑定防护和连接复用。
var sharedFetchClient = &http.Client{
	Timeout:   fetchReadTimeout,
	Transport: ssrfSafeTransport,
}

// safeHTTPClient 创建带 SSRF 安全 Transport 与指定超时的 HTTP 客户端。
// 所有出站 HTTP 请求必须通过此函数或 sharedFetchClient 创建客户端，
// 确保 DNS 重绑定防护在连接层生效。
func safeHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: ssrfSafeTransport,
	}
}

// safeHTTPClientNoRedirect 与 safeHTTPClient 相同，但不跟随重定向
// （仅返回首个响应的状态码，用于连通性检查）。
func safeHTTPClientNoRedirect(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: ssrfSafeTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// safeHTTPClientWithRedirect 与 safeHTTPClient 相同，但限制最多跟随 5 次重定向。
func safeHTTPClientWithRedirect(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: ssrfSafeTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
}

func isURLSafeForFetch(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return false
	}
	// ssrfSafeDial 会在连接时再次验证 IP（防止 DNS 重绑定）；此处先快速失败，过滤明显非法请求
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() ||
			ip.IsLinkLocalUnicast() || ip.IsMulticast() || cgnatBlock.Contains(ip) {
			return false
		}
	}
	return true
}

// fetchPage fetches a page and returns its body as a string.
// When silent is true (e.g. proxy image/page requests that fire for every
// cover/poster on screen) no timing logs are emitted, to avoid flooding the
// dev log. Network timing is always captured and logged when debug is on and
// silent is false — this is the core signal for diagnosing slow play-source
// searches. ctx 只用于日志 trace（X-Trace-ID 贯穿，见 trace.go）。
func fetchPage(ctx context.Context, rawURL, ua, cs string, silent bool) (string, error) {
	if !isURLSafeForFetch(rawURL) {
		return "", fmt.Errorf("URL blocked by security policy: %s", rawURL)
	}
	if debugEnabled() && !silent {
		logInfoC(ctx, "fetch → %s", rawURL)
	}
	start := time.Now()
	client := sharedFetchClient
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", uaOrDefault(ua))
	resp, err := client.Do(req)
	if err != nil {
		if debugEnabled() && !silent {
			logInfoC(ctx, "fetch ✗ %s dur=%s err=%v", rawURL, time.Since(start), err)
		}
		return "", err
	}
	defer resp.Body.Close()
	// 上限保护：站点抓取（搜索/详情/播放页）的页面远小于该值，
	// 恶意/异常上游返回超大 body 时防止内存耗尽。
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBodyBytes))
	if err != nil {
		if debugEnabled() && !silent {
			logInfoC(ctx, "fetch ✗ %s dur=%s readErr=%v", rawURL, time.Since(start), err)
		}
		return "", err
	}
	if debugEnabled() && !silent {
		logInfoC(ctx, "fetch ✓ %s dur=%s status=%d bytes=%d", rawURL, time.Since(start), resp.StatusCode, len(raw))
	}
	// 非 2xx 视为抓取失败（资源下架时上游普遍返回 404 + 风格化错误页）。
	// 调用方不应拿着错误页继续正则提取/iframe 兜底。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if cs == "" || cs == "utf-8" || cs == "UTF-8" {
		return string(raw), nil
	}
	r, err := charset.NewReaderLabel(cs, bytes.NewReader(raw))
	if err != nil {
		return string(raw), nil
	}
	decoded, err := io.ReadAll(r)
	if err != nil {
		return string(raw), nil
	}
	return string(decoded), nil
}

func parseHTML(s string) (*html.Node, error) {
	return html.Parse(strings.NewReader(s))
}

// selectNodes selects elements matching rule within context, trying CSS first then XPath.
func selectNodes(context *html.Node, rule string) []*html.Node {
	if rule == "" || context == nil {
		return nil
	}
	if sel := goquery.NewDocumentFromNode(context).Find(rule); sel.Length() > 0 {
		return sel.Nodes
	}
	if nodes := htmlquery.Find(context, rule); len(nodes) > 0 {
		return nodes
	}
	return nil
}

func extractText(node *html.Node) string {
	if node == nil {
		return ""
	}
	return strings.TrimSpace(goquery.NewDocumentFromNode(node).Text())
}

// textOf returns the text of the first element matching rule within context.
func textOf(context *html.Node, rule string) string {
	if rule == "" {
		return extractText(context)
	}
	if nodes := selectNodes(context, rule); len(nodes) > 0 {
		return extractText(nodes[0])
	}
	return ""
}

func attrVal(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

func extractAttr(node *html.Node, attr string) string {
	if node == nil {
		return ""
	}
	v := attrVal(node, attr)
	if v == "" {
		v = attrVal(node, "data-"+attr)
	}
	return strings.TrimSpace(v)
}

// attrOf returns attr of the first element matching rule within context.
func attrOf(context *html.Node, rule, attr string) string {
	if rule == "" {
		return extractAttr(context, attr)
	}
	if nodes := selectNodes(context, rule); len(nodes) > 0 {
		return extractAttr(nodes[0], attr)
	}
	return ""
}

func getBaseURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return u.Scheme + "://" + u.Hostname()
}

func resolveURL(basePageURL, prefix, relative string) string {
	if strings.HasPrefix(relative, "//") {
		return "https:" + relative
	}
	if strings.HasPrefix(relative, "http") {
		return relative
	}
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if strings.HasPrefix(relative, "/") {
		return prefix + relative[1:]
	}
	return prefix + relative
}

// ---- search ----

func (s *Server) search(ctx context.Context, source *SiteSource, keyword string) (map[string]any, error) {
	if source.SourceType == "json_api" {
		return s.searchViaJsonApi(ctx, source, keyword)
	}
	return s.searchViaHtml(ctx, source, keyword)
}

func (s *Server) searchViaHtml(ctx context.Context, source *SiteSource, keyword string) (map[string]any, error) {
	searchURL := source.SearchURL
	if searchURL == "" {
		return fail("未配置搜索地址"), nil
	}
	u := strings.Replace(searchURL, "{search}", url.QueryEscape(keyword), 1)
	if !strings.HasPrefix(u, "http") {
		u = source.BaseURL + u
	}
	cs := source.ReqCharset
	if cs == "" {
		cs = "utf-8"
	}
	htmlText, err := fetchPage(ctx, u, source.UserAgent, cs, false)
	if err != nil {
		return nil, err
	}
	root, err := parseHTML(htmlText)
	if err != nil {
		return nil, err
	}

	results := []map[string]any{}
	for _, item := range selectNodes(root, source.RuleResultList) {
		video := map[string]any{}
		video["title"] = textOf(item, source.RuleResultTitle)
		cover := attrOf(item, source.RuleResultCover, "src")
		if cover == "" {
			cover = textOf(item, source.RuleResultCover)
		}
		if cover != "" && !strings.HasPrefix(cover, "http") {
			cover = resolveURL(u, firstNonEmpty(source.ResultCoverPrefix, getBaseURL(u)), cover)
		}
		video["cover"] = cover
		link := attrOf(item, source.RuleResultLink, "href")
		if link == "" {
			link = textOf(item, source.RuleResultLink)
		}
		if link != "" && !strings.HasPrefix(link, "http") {
			link = resolveURL(u, firstNonEmpty(source.ResultLinkPrefix, getBaseURL(u)), link)
		}
		video["url"] = link
		results = append(results, video)
	}
	return map[string]any{"isSuccess": true, "data": results}, nil
}

func (s *Server) searchViaJsonApi(ctx context.Context, source *SiteSource, keyword string) (map[string]any, error) {
	searchURL := source.SearchURL
	if searchURL == "" {
		return fail("未配置搜索地址"), nil
	}
	u := source.BaseURL + strings.Replace(searchURL, "{search}", url.QueryEscape(keyword), 1)
	cs := source.ReqCharset
	if cs == "" {
		cs = "utf-8"
	}
	text, err := fetchPage(ctx, u, source.UserAgent, cs, false)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Code int              `json:"code"`
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		return nil, err
	}
	if resp.Code != 1 {
		return fail("API返回错误"), nil
	}
	results := []map[string]any{}
	for _, vod := range resp.List {
		results = append(results, map[string]any{
			"title": getMapStr(vod, "vod_name"),
			"cover": getMapStr(vod, "vod_pic"),
			"url":   "json_api://vod/" + getMapStr(vod, "vod_id"),
		})
	}
	return map[string]any{"isSuccess": true, "data": results}, nil
}

// ---- detail ----

func (s *Server) getDetail(ctx context.Context, source *SiteSource, detailURL string) (map[string]any, error) {
	if strings.HasPrefix(detailURL, "json_api://vod/") {
		vodID := strings.TrimPrefix(detailURL, "json_api://vod/")
		return s.getDetailViaJsonApi(ctx, source, vodID)
	}
	// json_api 类型站源直接传入 detailURL（即 vod_id）即可
	if source.SourceType == "json_api" {
		return s.getDetailViaJsonApi(ctx, source, detailURL)
	}
	return s.getDetailViaHtml(ctx, source, detailURL)
}

func (s *Server) getDetailViaHtml(ctx context.Context, source *SiteSource, detailURL string) (map[string]any, error) {
	cs := source.ReqCharset
	if cs == "" {
		cs = "utf-8"
	}
	htmlText, err := fetchPage(ctx, detailURL, source.UserAgent, cs, false)
	if err != nil {
		return nil, err
	}
	root, err := parseHTML(htmlText)
	if err != nil {
		return nil, err
	}

	result := map[string]any{"isSuccess": true}

	if source.RuleDetailCover != "" {
		if nodes := selectNodes(root, source.RuleDetailCover); len(nodes) > 0 {
			cover := attrOf(nodes[0], "", "src")
			if cover == "" {
				cover = textOf(nodes[0], "")
			}
			if cover != "" && !strings.HasPrefix(cover, "http") {
				cover = resolveURL(detailURL, firstNonEmpty(source.RuleDetailCoverPrefix, getBaseURL(detailURL)), cover)
			}
			result["cover"] = cover
		}
	}
	if source.RuleDetailIntro != "" {
		if nodes := selectNodes(root, source.RuleDetailIntro); len(nodes) > 0 {
			result["intro"] = textOf(nodes[0], "")
		}
	}

	channels := []map[string]any{}
	if source.RuleChannelGroups != "" && source.RuleEpList != "" {
		for _, ch := range selectNodes(root, source.RuleChannelGroups) {
			channel := map[string]any{"name": extractText(ch)}
			episodes := []map[string]any{}
			for _, ep := range selectNodes(ch, source.RuleEpList) {
				episode := map[string]any{}
				episode["title"] = textOf(ep, source.RuleEpTitle)
				link := attrOf(ep, source.RuleEpLink, "href")
				if link == "" {
					link = textOf(ep, source.RuleEpLink)
				}
				if link != "" && !strings.HasPrefix(link, "http") {
					link = resolveURL(detailURL, firstNonEmpty(source.RuleEpLinkPrefix, getBaseURL(detailURL)), link)
				}
				episode["url"] = link
				episodes = append(episodes, episode)
			}
			channel["episodes"] = episodes
			channels = append(channels, channel)
		}
	} else if source.RuleEpList != "" {
		channel := map[string]any{"name": "默认线路"}
		episodes := []map[string]any{}
		for _, ep := range selectNodes(root, source.RuleEpList) {
			episode := map[string]any{}
			episode["title"] = textOf(ep, source.RuleEpTitle)
			link := attrOf(ep, source.RuleEpLink, "href")
			if link == "" {
				link = textOf(ep, source.RuleEpLink)
			}
			if link != "" && !strings.HasPrefix(link, "http") {
				link = resolveURL(detailURL, firstNonEmpty(source.RuleEpLinkPrefix, getBaseURL(detailURL)), link)
			}
			episode["url"] = link
			episodes = append(episodes, episode)
		}
		channel["episodes"] = episodes
		channels = append(channels, channel)
	}
	result["channels"] = channels
	return result, nil
}

func (s *Server) getDetailViaJsonApi(ctx context.Context, source *SiteSource, vodID string) (map[string]any, error) {
	// vodID 必须是纯数字：json_api 的 ids 参数按数字解析。把豆瓣 URL 等
	// 非法值透传时，部分站源会从 URL 里抽出数字片段（如 subject/36837352/
	// 的 36837352），返回站内同 ID 的无关影片（几百上千集）冒充详情，
	// 聚合层无法分辨。这里直接失败，交给上层 title fallback 正确处理。
	if vodID == "" || !isDigits(vodID) {
		return fail("详情获取失败"), nil
	}
	detailURL := source.ApiDetailURL
	if detailURL == "" {
		detailURL = strings.Replace(source.SearchURL, "ac=list&wd={search}", "ac=detail&ids={id}", 1)
	}
	if detailURL == "" {
		return fail("未配置详情地址"), nil
	}
	u := source.BaseURL + strings.Replace(detailURL, "{id}", vodID, 1)
	cs := source.ReqCharset
	if cs == "" {
		cs = "utf-8"
	}
	text, err := fetchPage(ctx, u, source.UserAgent, cs, false)
	if err != nil {
		return nil, err
	}
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		return nil, err
	}
	if len(resp.List) == 0 {
		return fail("详情获取失败"), nil
	}
	vod := resp.List[0]
	result := map[string]any{"isSuccess": true}
	result["cover"] = getMapStr(vod, "vod_pic")

	content := getMapStr(vod, "vod_content")
	if content != "" {
		content = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(content, "")
		result["intro"] = strings.TrimSpace(content)
	}

	playFrom := getMapStr(vod, "vod_play_from")
	playURL := getMapStr(vod, "vod_play_url")
	channels := []map[string]any{}
	skippedNoSplit := 0
	if playFrom != "" && playURL != "" {
		sourceURLs := strings.Split(playURL, "$$$")
		var sourceNames []string
		if strings.Contains(playFrom, "$$$") {
			sourceNames = strings.Split(playFrom, "$$$")
		} else {
			sourceNames = strings.Split(playFrom, ",")
		}
		for i := 0; i < len(sourceURLs) && i < len(sourceNames); i++ {
			channel := map[string]any{"name": strings.TrimSpace(sourceNames[i])}
			episodes := []map[string]any{}
			for _, epItem := range strings.Split(sourceURLs[i], "#") {
				if epItem == "" {
					continue
				}
				parts := strings.SplitN(epItem, "$", 2)
				if len(parts) < 2 {
					skippedNoSplit++
					continue
				}
				epURL := strings.TrimSpace(parts[1])
				if !strings.HasPrefix(epURL, "http") {
					epURL = resolveURL(u, source.BaseURL, epURL)
				}
				episodes = append(episodes, map[string]any{"title": strings.TrimSpace(parts[0]), "url": epURL})
			}
			channel["episodes"] = episodes
			channels = append(channels, channel)
		}
	}
	if debugEnabled() {
		logEpisodeDiagnostics(source.Name, playFrom, playURL, channels, skippedNoSplit)
	}
	result["channels"] = channels
	return result, nil
}

// logEpisodeDiagnostics 打印某站源下各「线路(子源)」的选集诊断信息，便于排查
// 部分影片在个别站源下「选集」显示异常的问题。仅在 DEBUG 模式（--debug）输出。
func logEpisodeDiagnostics(sourceName, playFrom, playURL string, channels []map[string]any, skipped int) {
	const sampleN = 3
	for _, c := range channels {
		name := getMapStr(c, "name")
		eps, _ := c["episodes"].([]map[string]any)
		titles := make([]string, 0, sampleN)
		emptyTitle := 0
		nonHTTP := 0
		for _, e := range eps {
			t := getMapStr(e, "title")
			u := getMapStr(e, "url")
			if t == "" {
				emptyTitle++
			}
			if u == "" || !strings.HasPrefix(u, "http") {
				nonHTTP++
			}
			if len(titles) < sampleN {
				tt := t
				if len(tt) > 40 {
					tt = tt[:40] + "..."
				}
				if tt == "" {
					tt = "(空标题)"
				}
				titles = append(titles, tt)
			}
		}
		logInfo("[EPISODES] source=%q channel=%q episodes=%d emptyTitle=%d nonHttpUrl=%d sample=%v",
			sourceName, name, len(eps), emptyTitle, nonHTTP, titles)
	}
	pf := playFrom
	if len(pf) > 120 {
		pf = pf[:120] + "..."
	}
	logInfo("[EPISODES] source=%q totalChannels=%d skippedNoSplit=%d playFrom=%q playURLLen=%d",
		sourceName, len(channels), skipped, pf, len(playURL))
}

// ---- play URL ----

var playPatterns = []*regexp.Regexp{
	regexp.MustCompile(`["']([^"']*\.m3u8[^"']*)["']`),
	regexp.MustCompile(`["']([^"']*\.mp4[^"']*)["']`),
	regexp.MustCompile(`["']([^"']*\.flv[^"']*)["']`),
	regexp.MustCompile(`["']([^"']*\.mpd[^"']*)["']`),
	regexp.MustCompile(`src["']?\s*[=:]\s*["']([^"']+)["']`),
	regexp.MustCompile(`(?:url|file|path|video_url|play_url|source)["']?\s*[=:]\s*["']([^"']*(?:\.m3u8|\.mp4|\.flv)[^"']*)["']`),
	regexp.MustCompile(`https?://[^"'\s]+\.m3u8[^"'\s]*`),
	regexp.MustCompile(`https?://[^"'\s]+\.mp4[^"'\s]*`),
	regexp.MustCompile(`https?://[^"'\s]+\.flv[^"'\s]*`),
}

// extractPlayURL 从播放页 HTML 中按优先级匹配出第一个可用的媒体地址。
// 纯函数，便于单测。返回 (媒体URL, 是否命中)；命中但被判定为不可用的
// （data:/javascript:/about:blank 等）继续尝试下一条规则。
func extractPlayURL(htmlText string) (string, bool) {
	for _, p := range playPatterns {
		matches := p.FindStringSubmatch(htmlText)
		var u string
		if len(matches) >= 2 {
			u = matches[1]
		} else if m := p.FindString(htmlText); m != "" {
			u = m
		}
		if u == "" {
			continue
		}
		if u == "" || strings.HasPrefix(u, "data:") || strings.HasPrefix(u, "javascript:") || u == "about:blank" {
			continue
		}
		return u, true
	}
	return "", false
}

// mediaTypeFromContentType 把响应的 Content-Type 映射为播放类型
// （m3u8/mpd/mp4/flv），无法识别返回空串。纯函数便于单测。
func mediaTypeFromContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	switch {
	case ct == "application/vnd.apple.mpegurl" || ct == "application/x-mpegurl":
		return "m3u8"
	case ct == "application/dash+xml":
		return "mpd"
	case strings.HasPrefix(ct, "video/mp4"):
		return "mp4"
	case strings.HasPrefix(ct, "video/x-flv"):
		return "flv"
	}
	return ""
}

// probeURLMediaType 对无后缀但疑似媒体的 URL 做一次轻量探测：
// 发 GET 请求仅读响应头（body 立即关闭），按 Content-Type 判断是否为
// 可直接播放的媒体（m3u8/mp4/flv/mpd）。识别"目录式"媒体地址
// （如 https://.../video/ 实际返回 m3u8 播放列表），避免被误判为
// iframe 页面导致播放失败。返回空串表示不是媒体。
// 注意：请求头与 fetchPage 保持一致（仅 UA，不带 Referer）——部分上游
// 对跨路径 Referer 敏感会 403，而 fetchPage 不带 Referer 反而能取到。
func probeURLMediaType(rawURL string) string {
	if !isURLSafeForFetch(rawURL) {
		return ""
	}
	client := safeHTTPClient(8 * time.Second)
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", desktopUA)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return ""
	}
	return mediaTypeFromContentType(resp.Header.Get("Content-Type"))
}

// isPlaylistBody 判断响应体是否为 HLS 播放列表文本（#EXTM3U 开头）。
// 纯函数便于单测；上游直接返回播放列表时（无后缀目录式 m3u8 URL），
// 原 URL 即媒体地址，无需正则提取。
func isPlaylistBody(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "#EXTM3U")
}

// validateMediaReachable 服务端校验解析出的直链是否真实可达：
//   - m3u8：GET 读响应体首行确认是播放列表（#EXTM3U）；
//   - 其它（mp4/flv/mpd）：带 Range 请求，状态 2xx 即视为可达。
//
// 背景：站源 share 页里嵌的 m3u8 带时效 sign（或缓存了旧 sign），服务端
// 「解析成功」不代表「sign 仍有效」。不校验就把失效直链返回给浏览器，
// hls.js 直连 CDN 撞 404（manifest fatal）只能靠前端错误兜底，表现为
// 「一直在缓冲中」。校验失败返回 false，调用方按解析失败处理。
func validateMediaReachable(rawURL string) bool {
	if !isURLSafeForFetch(rawURL) {
		return false
	}
	client := safeHTTPClient(8 * time.Second)
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", desktopUA)
	req.Header.Set("Referer", getBaseURL(rawURL))
	if !strings.Contains(rawURL, ".m3u8") {
		req.Header.Set("Range", "bytes=0-0")
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return false
	}
	if strings.Contains(rawURL, ".m3u8") {
		buf := make([]byte, 64)
		n, _ := io.ReadFull(resp.Body, buf)
		return strings.HasPrefix(strings.TrimSpace(string(buf[:n])), "#EXTM3U")
	}
	return true
}

func (s *Server) getPlayUrl(ctx context.Context, playURL string) (map[string]any, error) {
	logInfoC(ctx, "play resolve: url=%s", playURL)
	// 站源聚合搜索（豆瓣降级）返回的 json_api://vod/<id> 伪协议地址：
	// 先按详情解析出第一个可播放的选集，再走正常解析流程。
	if strings.HasPrefix(playURL, "json_api://vod/") {
		vodID := strings.TrimPrefix(playURL, "json_api://vod/")
		sources, err := s.getActiveSiteSources()
		if err != nil || len(sources) == 0 {
			return nil, fmt.Errorf("获取站源失败")
		}
		for _, source := range sources {
			detail, e := s.getDetailViaJsonApi(ctx, source, vodID)
			if e != nil {
				continue
			}
			for _, ch := range channelsOf(detail) {
				for _, ep := range episodesOf(ch) {
					if u := getMapStr(ep, "url"); u != "" {
						logInfoC(ctx, "play resolve(json_api): vod=%s first ep url=%s", vodID, u)
						return s.getPlayUrl(ctx, u)
					}
				}
			}
		}
		return nil, fmt.Errorf("站源未找到可播放地址")
	}
	if strings.Contains(playURL, ".m3u8") || strings.Contains(playURL, ".mp4") || strings.Contains(playURL, ".flv") {
		// 直链也校验：历史续播/缓存可能携带已失效的带 sign 地址。
		if !validateMediaReachable(playURL) {
			logInfoC(ctx, "play resolve: direct url unreachable, fail: %s", playURL)
			return nil, fmt.Errorf("播放地址已失效")
		}
		logInfoC(ctx, "play resolved: url=%s type=%s", playURL, playType(playURL))
		return map[string]any{"isSuccess": true, "url": playURL, "type": playType(playURL)}, nil
	}
	htmlText, err := fetchPage(ctx, playURL, desktopUA, "utf-8", false)
	if err != nil {
		return nil, err
	}
	baseURL := getBaseURL(playURL)
	// 上游直接返回播放列表文本（无后缀目录式 m3u8 URL）：无需正则提取，
	// 原 URL 即媒体地址，直接按 m3u8 处理（正则匹配不到相对路径的列表）。
	if isPlaylistBody(htmlText) {
		logInfoC(ctx, "play resolved(playlist-body): url=%s type=m3u8", playURL)
		return map[string]any{"isSuccess": true, "url": playURL, "type": "m3u8"}, nil
	}
	if u, ok := extractPlayURL(htmlText); ok {
		if !strings.HasPrefix(u, "http") {
			u = resolveURL(playURL, baseURL, u)
		}
		// 校验解析出的直链真实可达（sign 时效/资源下架场景），不可达按
		// 解析失败处理——绝不把必然 404 的地址交给浏览器去撞。
		if !validateMediaReachable(u) {
			logInfoC(ctx, "play resolve: extracted url unreachable, fail: %s", u)
			return nil, fmt.Errorf("播放地址已失效")
		}
		logInfoC(ctx, "play resolved: url=%s type=%s", u, playType(u))
		return map[string]any{"isSuccess": true, "url": u, "type": playType(u)}, nil
	}
	// 正则提取失败：URL 本身可能是无后缀的媒体地址（目录式 m3u8 等），
	// 探测 Content-Type 识别，避免把播放列表误当 iframe 页面导致播放失败。
	if mt := probeURLMediaType(playURL); mt != "" {
		logInfoC(ctx, "play resolved(probe): url=%s type=%s", playURL, mt)
		return map[string]any{"isSuccess": true, "url": playURL, "type": mt}, nil
	}
	logInfoC(ctx, "play resolved: url=%s type=iframe(fallback)", playURL)
	return map[string]any{"isSuccess": true, "url": playURL, "type": "iframe"}, nil
}

func playType(u string) string {
	switch {
	case strings.Contains(u, ".m3u8"):
		return "m3u8"
	case strings.Contains(u, ".mp4"):
		return "mp4"
	case strings.Contains(u, ".flv"):
		return "flv"
	case strings.Contains(u, ".mpd"):
		return "mpd"
	default:
		return "iframe"
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func getMapStr(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	if f, ok := v.(float64); ok {
		return formatNumber(f)
	}
	return fmt.Sprintf("%v", v)
}
