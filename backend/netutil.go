package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
)

var (
	reYearStrip   = regexp.MustCompile(`\s*\(\d{4}\)$`)
	reYearExtract = regexp.MustCompile(`.*\((\d{4})\)$`)
	reDesc        = regexp.MustCompile(`<meta[^>]+name="description"[^>]+content="([^"]+)"`)
	reWhitespace  = regexp.MustCompile(`\s+`)
)

// trustProxy 控制 clientIP 是否信任反向代理设置的 X-Forwarded-For / X-Real-IP。
// 默认关闭：服务直接对客户端开放时，XFF 完全可由客户端伪造，若被限流逻辑信任
// 即可绕过登录/注册/搜索限流。仅在服务确实位于可信反向代理之后（由部署者显式
// 通过 --trust-proxy 开启）时才应读取这些头。
var trustProxy = false

func clientIP(r *http.Request) string {
	// 仅在显式信任反向代理时读取 XFF/X-Real-IP；否则一律用 TCP 对端地址，
	// 防止客户端伪造 XFF 绕过 IP 限流。
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[0])
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return xri
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isUrlReachable(rawURL string) bool {
	client := safeHTTPClientNoRedirect(8 * time.Second)
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", desktopUA)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200 || resp.StatusCode == 301 || resp.StatusCode == 302
}

// maxFetchBodyBytes 限制从上游抓取的响应体大小（豆瓣接口/网页/远端配置）。
// 恶意或异常上游返回超大 body 时防止内存耗尽；正常页面/JSON 远小于该值。
const maxFetchBodyBytes = 8 << 20 // 8 MiB

func fetchURLContent(rawURL string) (string, error) {
	client := safeHTTPClient(10 * time.Second)
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", desktopUA)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBodyBytes))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func buildTestURL(baseURL, searchURL string) string {
	if searchURL != "" {
		t := strings.Replace(searchURL, "{search}", "test", 1)
		if strings.HasPrefix(t, "http") {
			return t
		}
		return baseURL + t
	}
	return baseURL
}

// ---- source update ----

func (s *Server) handleCheckSourceUpdate(w http.ResponseWriter, r *http.Request) {
	if _, err := s.requireAdmin(r); err != nil {
		writeJSON(w, 200, fail("无权限"))
		return
	}
	var baseURL string
	if r.Method == http.MethodPost {
		m, _ := readJSONMap(w, r)
		baseURL = getStr(m, "baseUrl")
	} else {
		baseURL = r.URL.Query().Get("baseUrl")
	}
	if baseURL == "" {
		s.handleCheckAllSourceUpdates(w, r)
		return
	}
	source, err := s.getSiteSourceByBaseURL(baseURL)
	if err != nil || source == nil {
		writeJSON(w, 200, fail("站源不存在"))
		return
	}
	result := map[string]any{
		"isSuccess":     true,
		"name":          source.Name,
		"baseUrl":       source.BaseURL,
		"backupBaseUrl": source.BackupBaseURL,
	}
	testURL := buildTestURL(source.BaseURL, source.SearchURL)
	apiReachable := isUrlReachable(testURL)
	result["apiReachable"] = apiReachable
	result["testUrl"] = testURL
	if !apiReachable && source.BackupBaseURL != "" {
		backupTest := buildTestURL(source.BackupBaseURL, source.SearchURL)
		result["backupReachable"] = isUrlReachable(backupTest)
		result["backupTestUrl"] = backupTest
	}
	if source.RemoteConfigURL != "" {
		if cfg, e := fetchURLContent(source.RemoteConfigURL); e == nil {
			result["hasRemoteConfig"] = true
			result["remoteConfig"] = cfg
		} else {
			result["hasRemoteConfig"] = false
			result["remoteConfigError"] = e.Error()
		}
	} else {
		result["hasRemoteConfig"] = false
	}
	writeJSON(w, 200, result)
}

// handleCheckAllSourceUpdates 并发检查所有站源的连通性/远程配置，
// 避免串行逐个探测（每个源最长 8-10s 超时）把 admin 检查拖到几十秒。
func (s *Server) handleCheckAllSourceUpdates(w http.ResponseWriter, r *http.Request) {
	sources, err := s.getAllSiteSources()
	if err != nil {
		writeJSON(w, 200, fail("获取站源失败"))
		return
	}
	const checkConcurrency = 6
	type srcResult struct {
		res map[string]any
	}
	ch := make(chan srcResult, len(sources))
	sem := make(chan struct{}, checkConcurrency)
	var wg sync.WaitGroup
	for _, source := range sources {
		wg.Add(1)
		sem <- struct{}{}
		go func(source *SiteSource) {
			defer wg.Done()
			defer func() { <-sem }()
			res := map[string]any{
				"name":          source.Name,
				"baseUrl":       source.BaseURL,
				"backupBaseUrl": source.BackupBaseURL,
				"enabled":       source.IsDisable == 0,
			}
			testURL := buildTestURL(source.BaseURL, source.SearchURL)
			reachable := isUrlReachable(testURL)
			res["apiReachable"] = reachable
			res["testUrl"] = testURL
			if !reachable && source.BackupBaseURL != "" {
				backupTest := buildTestURL(source.BackupBaseURL, source.SearchURL)
				res["backupReachable"] = isUrlReachable(backupTest)
				res["backupTestUrl"] = backupTest
			}
			if source.RemoteConfigURL != "" {
				if cfg, e := fetchURLContent(source.RemoteConfigURL); e == nil {
					res["hasRemoteConfig"] = true
					res["remoteConfigUpdated"] = true
					res["remoteConfig"] = cfg
				} else {
					res["hasRemoteConfig"] = true
					res["remoteConfigUpdated"] = false
					res["remoteConfigError"] = e.Error()
				}
			} else {
				res["hasRemoteConfig"] = false
				res["remoteConfigUpdated"] = false
			}
			ch <- srcResult{res: res}
		}(source)
	}
	go func() { wg.Wait(); close(ch) }()
	results := []map[string]any{}
	for r := range ch {
		results = append(results, r.res)
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": results})
}

func (s *Server) handleApplySourceUpdate(w http.ResponseWriter, r *http.Request) {
	if _, err := s.requireAdmin(r); err != nil {
		writeJSON(w, 200, fail("无权限"))
		return
	}
	m, err := readJSONMap(w, r)
	if err != nil {
		writeJSON(w, 200, fail("请求格式错误"))
		return
	}
	baseURL := getStr(m, "baseUrl")
	remoteConfig := getStr(m, "remoteConfig")
	if baseURL == "" || remoteConfig == "" {
		writeJSON(w, 200, fail("参数不完整"))
		return
	}
	source, err := s.getSiteSourceByBaseURL(baseURL)
	if err != nil || source == nil {
		writeJSON(w, 200, fail("站源不存在"))
		return
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(remoteConfig), &cfg); err != nil {
		writeJSON(w, 200, fail("更新失败: "+err.Error()))
		return
	}
	if v := getMapStr(cfg, "name"); v != "" {
		source.Name = v
	}
	if v := getMapStr(cfg, "baseUrl"); v != "" {
		source.BaseURL = v
	}
	if v := getMapStr(cfg, "searchUrl"); v != "" {
		source.SearchURL = v
	}
	if v := getMapStr(cfg, "apiDetailUrl"); v != "" {
		source.ApiDetailURL = v
	}
	if v := getMapStr(cfg, "sourceType"); v != "" {
		source.SourceType = v
	}
	if v := getMapStr(cfg, "userAgent"); v != "" {
		source.UserAgent = v
	}
	if err := s.saveSiteSource(toMap(source)); err != nil {
		writeJSON(w, 200, fail("更新失败: "+err.Error()))
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true, "msg": "站源已更新"})
}

func (s *Server) handleSwitchToBackup(w http.ResponseWriter, r *http.Request) {
	if _, err := s.requireAdmin(r); err != nil {
		writeJSON(w, 200, fail("无权限"))
		return
	}
	m, err := readJSONMap(w, r)
	if err != nil {
		writeJSON(w, 200, fail("请求格式错误"))
		return
	}
	baseURL := getStr(m, "baseUrl")
	source, err := s.getSiteSourceByBaseURL(baseURL)
	if err != nil || source == nil {
		writeJSON(w, 200, fail("站源不存在"))
		return
	}
	if source.BackupBaseURL == "" {
		writeJSON(w, 200, fail("该站源没有备用地址"))
		return
	}
	oldBase := source.BaseURL
	newBase := source.BackupBaseURL
	if source.SearchURL != "" {
		source.SearchURL = strings.Replace(source.SearchURL, oldBase, newBase, 1)
	}
	source.BaseURL = newBase
	source.BackupBaseURL = oldBase
	if err := s.saveSiteSource(toMap(source)); err != nil {
		writeJSON(w, 200, fail("切换失败: "+err.Error()))
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true, "msg": "已切换到备用地址: " + newBase})
}

// toMap converts a SiteSource back into a map for re-saving.
func toMap(ss *SiteSource) map[string]any {
	b, _ := json.Marshal(ss)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// ---- douban ----

func (s *Server) handleDouban(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// keyword search: reuse the homepage's douban-subject detail flow by
	// returning one card per unique film (deduplicated by douban id), so the
	// search page can navigate to /subject/:id just like the homepage grid.
	if kw := strings.TrimSpace(q.Get("q")); kw != "" {
		s.handleDoubanSearch(r.Context(), w, kw)
		return
	}
	dType := q.Get("type")
	tag := q.Get("tag")
	limit := q.Get("limit")
	start := q.Get("start")
	sort := q.Get("sort")
	prefer := q.Get("prefer") // 数据源偏好：tmdb=优先 TMDB（需已配置 key）
	if limit == "" {
		limit = "20"
	}
	if start == "" {
		start = "0"
	}
	// 两级缓存命中即返回（毫秒级，不再等上游）：
	//   1. 进程内存缓存（15 分钟 TTL，扛并发与突发）；
	//   2. SQLite 持久缓存（预热循环维护，应用冷启动即有全量数据）。
	cacheKey := dType + "|" + tag + "|" + sort + "|" + limit + "|" + start
	if cached, ok := doubanListCacheGet(cacheKey); ok {
		logInfoC(r.Context(), "douban list: key=%q cache HIT %d items", cacheKey, len(cached))
		writeJSON(w, 200, map[string]any{"isSuccess": true, "data": cached})
		return
	}
	if cached, ok := s.doubanStoreGet(cacheKey); ok {
		logInfoC(r.Context(), "douban list: key=%q store HIT %d items", cacheKey, len(cached))
		doubanListCachePut(cacheKey, cached)
		writeJSON(w, 200, map[string]any{"isSuccess": true, "data": cached})
		// stale-while-revalidate：仅当数据真正过期（超过持久缓存 TTL）
		// 才后台刷新，且同 key 单飞——快速滚动分页每页都是 store HIT，
		// 不过滤会起几十轮重复刷新打爆上游（曾致 warm 风暴 + 图片代理超时）。
		if _, fresh := s.doubanStoreGetFresh(cacheKey); !fresh {
			go s.refreshDoubanCategoryOnce(dType, tag, sort, limit, start, prefer)
		}
		return
	}
	// 无任何缓存（首次使用 / 预热未跑完）：同步走上游，避免首屏空转。
	res := fetchDoubanListDirect(dType, tag, sort, limit, start)
	if prefer == "tmdb" && res.source != "tmdb" && tmdbEnabled() {
		// 用户设置 TMDB 优先：上游结果若非 TMDB（说明豆瓣通了），仍按
		// 偏好回源 TMDB 一次；TMDB 拿不到就用豆瓣结果兜底。
		if list, ok := tmdbList(dType, tag, limit, start); ok {
			res = fetchResult{list: list, source: "tmdb"}
		}
	}
	if len(res.list) == 0 {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": "豆瓣与备用数据源均不可用，请稍后重试"})
		return
	}
	doubanListCachePut(cacheKey, res.list)
	s.doubanStorePut(cacheKey, res.list)
	resp := map[string]any{"isSuccess": true, "data": res.list}
	if res.source != "" {
		resp["source"] = res.source
	}
	writeJSON(w, 200, resp)
}

// rexxarCollection 映射首页分类 tag → m.douban.com rexxar 的榜单集合 id。
// j/search_subjects 被风控时的备用通道（m.douban.com 域名独立限流）。
var rexxarCollection = map[string]string{
	"热门":   "tv_hot",
	"国产剧":  "tv_domestic",
	"美剧":   "tv_american",
	"日剧":   "tv_japanese",
	"韩剧":   "tv_korean",
	"日本动画": "tv_animation",
	"动漫":   "tv_animation",
	"纪录片":  "tv_documentary",
	"动画":   "tv_animation",
}

// doubanRexxarList 从 m.douban.com 的 rexxar API 抓榜单（字段与
// j/search_subjects 对齐）。rexxar 的 collection 不支持 sort 参数。
func doubanRexxarList(dType, tag, limit, start string) ([]map[string]any, bool) {
	col := rexxarCollection[tag]
	if col == "" {
		// 类型级默认榜单：综艺/高分/冷门等无映射时回退到类型热门。
		if dType == "tv" {
			col = "tv_hot"
		} else {
			col = "movie_hot"
		}
	}
	u := "https://m.douban.com/rexxar/api/v2/subject_collection/" + col + "/items?start=" + start + "&limit=" + limit
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15")
	req.Header.Set("Referer", "https://m.douban.com/")
	resp, err := safeHTTPClient(8 * time.Second).Do(req)
	if err != nil {
		logPrint("douban rexxar fallback failed: %v", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		logPrint("douban rexxar fallback: status %d", resp.StatusCode)
		return nil, false
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxFetchBodyBytes))
	var obj struct {
		Items []struct {
			ID           string `json:"id"`
			Title        string `json:"title"`
			EpisodesInfo string `json:"episodes_info"`
			Rating       struct {
				Value float64 `json:"value"`
			} `json:"rating"`
			Pic struct {
				Normal string `json:"normal"`
				Large  string `json:"large"`
			} `json:"pic"`
			URI string `json:"uri"`
		} `json:"subject_collection_items"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		logPrint("douban rexxar fallback: 解析失败 %v", err)
		return nil, false
	}
	list := []map[string]any{}
	for _, v := range obj.Items {
		if v.ID == "" || v.Title == "" {
			continue
		}
		rate := ""
		if v.Rating.Value > 0 {
			rate = strconv.FormatFloat(v.Rating.Value, 'f', 1, 64)
		}
		list = append(list, map[string]any{
			"title":        v.Title,
			"cover":        v.Pic.Normal,
			"url":          "https://movie.douban.com/subject/" + v.ID + "/",
			"rate":         rate,
			"episodesInfo": v.EpisodesInfo,
		})
	}
	logPrint("douban rexxar fallback: tag=%q %d items", tag, len(list))
	return list, len(list) > 0
}

// handleDoubanSearch performs a free-text keyword search against Douban and
// returns one subject per unique film (deduplicated by douban id), matching the
// shape of the homepage grid so the search page can reuse the same cards and
// navigate to /subject/:id. It uses Douban's suggest endpoint, which is stable
// and returns id/title/url/cover for each match.
//
// 风控降级链路（豆瓣 suggest 被风控/限流时依次尝试）：
//  1. suggest 接口（JSON，最稳定）
//  2. 网页搜索页抓取（search/?q=，桌面 UA 模拟浏览器）
//  3. 站源关键词搜索（全部启用站源聚合，见 searchSiteSources）
//
// 结果按关键词缓存（5 分钟 TTL），风控期间重复搜索命中缓存仍有结果。
func (s *Server) handleDoubanSearch(ctx context.Context, w http.ResponseWriter, keyword string) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		writeJSON(w, 200, fail("搜索词不能为空"))
		return
	}
	if cached, ok := doubanSearchCacheGet(keyword); ok {
		logInfoC(ctx, "douban search: keyword=%q cache HIT results=%d", keyword, len(cached))
		writeJSON(w, 200, map[string]any{"isSuccess": true, "data": cached})
		return
	}

	var list []map[string]any
	// 1) suggest JSON 接口
	if items, ok := doubanSuggest(keyword); ok {
		list = dedupeDouban(items)
		logInfoC(ctx, "douban search: keyword=%q via suggest results=%d", keyword, len(list))
		if len(list) > 0 {
			doubanSearchCachePut(keyword, list)
			writeJSON(w, 200, map[string]any{"isSuccess": true, "data": list})
			return
		}
	}
	// 2) 网页搜索页抓取（suggest 风控/空结果时的降级）
	if items, ok := doubanSearchHTML(keyword); ok && len(items) > 0 {
		list = dedupeDouban(items)
		logInfoC(ctx, "douban search: keyword=%q via html results=%d", keyword, len(list))
		doubanSearchCachePut(keyword, list)
		writeJSON(w, 200, map[string]any{"isSuccess": true, "data": list})
		return
	}
	// 3) 站源聚合搜索兜底（豆瓣完全不可用时的最后手段）
	if items, ok := s.searchSiteSources(ctx, keyword); ok && len(items) > 0 {
		logInfoC(ctx, "douban search: keyword=%q via sites results=%d", keyword, len(items))
		doubanSearchCachePut(keyword, items)
		writeJSON(w, 200, map[string]any{"isSuccess": true, "data": items})
		return
	}
	// 全部降级失败：返回空结果（前端提示）
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": []any{}})
}

// doubanSuggest 请求豆瓣 suggest JSON 接口。返回 (结果, 是否成功)；
// 风控返回非 JSON 或网络错误时 ok=false。
func doubanSuggest(keyword string) ([]map[string]any, bool) {
	u := "https://movie.douban.com/j/subject_suggest?q=" + url.QueryEscape(keyword)
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://movie.douban.com/")
	resp, err := safeHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, false
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxFetchBodyBytes))
	var items []map[string]any
	if err := json.Unmarshal(b, &items); err != nil {
		// 风控挑战页 / 空响应：非 JSON 视为失败（触发降级）
		return nil, false
	}
	return items, true
}

// dedupeDouban 按豆瓣 id 去重并规整为前端卡片字段。
func dedupeDouban(items []map[string]any) []map[string]any {
	seen := map[string]bool{}
	list := []map[string]any{}
	for _, it := range items {
		id := getMapStr(it, "id")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		list = append(list, map[string]any{
			"id":           id,
			"title":        getMapStr(it, "title"),
			"cover":        getMapStr(it, "img"),
			"url":          getMapStr(it, "url"),
			"rate":         "",
			"year":         getMapStr(it, "year"),
			"episodesInfo": "",
		})
	}
	return list
}

// doubanSearchHTML 抓取豆瓣网页搜索页并解析结果列表。
// suggest 接口被风控时的降级路径：以桌面浏览器 UA + Referer 模拟真实访问。
func doubanSearchHTML(keyword string) ([]map[string]any, bool) {
	u := "https://www.douban.com/search?q=" + url.QueryEscape(keyword)
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("User-Agent", desktopUA)
	req.Header.Set("Referer", "https://www.douban.com/")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	client := safeHTTPClient(12 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, false
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxFetchBodyBytes))
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(b)))
	if err != nil {
		return nil, false
	}
	items := []map[string]any{}
	// 豆瓣搜索页结构：.result 每条包含 .title a（href 含 subject/<id>/）与 img
	doc.Find(".result").Each(func(_ int, sel *goquery.Selection) {
		href, _ := sel.Find(".title a").First().Attr("href")
		id := ""
		if m := regexp.MustCompile(`subject/(\d+)/`).FindStringSubmatch(href); len(m) > 1 {
			id = m[1]
		}
		title := strings.TrimSpace(sel.Find(".title").First().Text())
		if id == "" || title == "" {
			return
		}
		cover, _ := sel.Find("img").First().Attr("src")
		items = append(items, map[string]any{
			"id":    id,
			"title": title,
			"img":   cover,
			"url":   "https://movie.douban.com/subject/" + id + "/",
			"year":  "",
		})
	})
	return items, len(items) > 0
}

// searchSiteSources 对所有启用站源做关键词搜索并聚合结果（去重）。
// 豆瓣完全不可用时的最后兜底：结果卡片直接可进站源详情页播放。
func (s *Server) searchSiteSources(ctx context.Context, keyword string) ([]map[string]any, bool) {
	sources, err := s.getActiveSiteSources()
	if err != nil || len(sources) == 0 {
		return nil, false
	}
	const siteSearchConcurrency = 8
	type siteResult struct {
		items []map[string]any
	}
	ch := make(chan siteResult, len(sources))
	sem := make(chan struct{}, siteSearchConcurrency)
	var wg sync.WaitGroup
	for _, source := range sources {
		wg.Add(1)
		sem <- struct{}{}
		go func(source *SiteSource) {
			defer wg.Done()
			defer func() { <-sem }()
			res, e := s.search(ctx, source, keyword)
			if e != nil {
				ch <- siteResult{}
				return
			}
			items := dataArray(res)
			for _, it := range items {
				it["sourceName"] = source.Name
			}
			ch <- siteResult{items: items}
		}(source)
	}
	go func() { wg.Wait(); close(ch) }()
	seen := map[string]bool{}
	list := []map[string]any{}
	for r := range ch {
		for _, it := range r.items {
			title := getMapStr(it, "title")
			// 标题把关：部分站源的 ac=list&wd= 接口失效，无视关键词
			// 返回最新更新列表（实测 zuid 搜「我独自升级」返回
			// total=125898 的 NBA/杂片）。聚合层过 titlesMatch，
			// 与关键词无包含关系的条目直接丢弃。
			if !titlesMatch(title, keyword) {
				continue
			}
			key := getMapStr(it, "sourceName") + "|" + title
			if seen[key] {
				continue
			}
			seen[key] = true
			list = append(list, map[string]any{
				"title":        title,
				"cover":        getMapStr(it, "cover"),
				"url":          getMapStr(it, "url"),
				"sourceName":   getMapStr(it, "sourceName"),
				"rate":         "",
				"year":         "",
				"episodesInfo": "",
			})
		}
	}
	return list, len(list) > 0
}

// ---- 豆瓣搜索结果缓存（风控期间仍能返回历史结果） ----

type doubanCacheEntry struct {
	items []map[string]any
	exp   time.Time
}

var (
	doubanCacheMu sync.Mutex
	doubanCache   = map[string]doubanCacheEntry{}
)

const doubanCacheTTL = 10 * time.Minute
const doubanCacheMaxSize = 200

// doubanListCache 豆瓣榜单缓存：key 为 type|tag|sort|limit|start。
var doubanListCache = struct {
	sync.RWMutex
	m map[string]doubanCacheEntry
}{m: map[string]doubanCacheEntry{}}

// doubanListCacheTTL 榜单缓存 TTL：榜单内容小时级变化，15 分钟足够新鲜，
// 同时把上游请求频率压到最低（风控缓解）。
const doubanListCacheTTL = 15 * time.Minute

// doubanGuard 熔断器（连续 3 次失败跳闸 5 分钟）与请求合并器。
var (
	doubanBreaker = newDoubanCircuit(3, 5*time.Minute)
	doubanMerge   inflight
)

func doubanListCacheGet(key string) ([]map[string]any, bool) {
	doubanListCache.RLock()
	defer doubanListCache.RUnlock()
	e, ok := doubanListCache.m[key]
	if !ok || time.Since(e.exp) > doubanListCacheTTL {
		return nil, false
	}
	return e.items, true
}

func doubanListCachePut(key string, items []map[string]any) {
	doubanListCache.Lock()
	defer doubanListCache.Unlock()
	if len(doubanListCache.m) > 200 {
		doubanListCache.m = map[string]doubanCacheEntry{} // 简单清空重建
	}
	doubanListCache.m[key] = doubanCacheEntry{exp: time.Now(), items: items}
}

func doubanSearchCacheGet(keyword string) ([]map[string]any, bool) {
	doubanCacheMu.Lock()
	defer doubanCacheMu.Unlock()
	e, ok := doubanCache[keyword]
	if !ok || time.Now().After(e.exp) {
		if ok {
			delete(doubanCache, keyword)
		}
		return nil, false
	}
	return e.items, true
}

func doubanSearchCachePut(keyword string, items []map[string]any) {
	doubanCacheMu.Lock()
	defer doubanCacheMu.Unlock()
	if _, exists := doubanCache[keyword]; !exists && len(doubanCache) >= doubanCacheMaxSize {
		for k := range doubanCache {
			delete(doubanCache, k)
			break
		}
	}
	doubanCache[keyword] = doubanCacheEntry{items: items, exp: time.Now().Add(doubanCacheTTL)}
}

// doubanCoverViaSuggest resolves a subject's poster by querying Douban's
// suggest endpoint with the (year-stripped) title and matching the returned
// subject id. This is far more reliable than scraping og:image from the
// mobile subject page (which Douban serves inconsistently to non-browser
// clients) and reuses the same cover data the app shows on the home/search
// grids. Returns "" if no cover can be resolved.
func doubanCoverViaSuggest(id, title string) string {
	if id == "" || title == "" {
		return ""
	}
	u := "https://movie.douban.com/j/subject_suggest?q=" + url.QueryEscape(cleanSuggestTitle(title))
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://movie.douban.com/")
	client := safeHTTPClient(8 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxFetchBodyBytes))
	var items []map[string]any
	if err := json.Unmarshal(b, &items); err != nil {
		return ""
	}
	// Prefer the exact id match; fall back to the first result with a cover.
	for _, it := range items {
		if getMapStr(it, "id") == id {
			if c := getMapStr(it, "img"); c != "" {
				return c
			}
		}
	}
	for _, it := range items {
		if c := getMapStr(it, "img"); c != "" {
			return c
		}
	}
	return ""
}

// cleanSuggestTitle trims the trailing foreign/English portion of a bilingual
// title (e.g. "肖申克的救赎 The Shawshank Redemption" -> "肖申克的救赎") so the
// Douban suggest endpoint matches the original name. Titles that do not start
// with CJK are returned unchanged.
func cleanSuggestTitle(title string) string {
	t := strings.TrimSpace(title)
	if t == "" {
		return t
	}
	r0 := []rune(t)[0]
	isCJK := r0 >= 0x4e00 && r0 <= 0x9fff
	if !isCJK {
		return t
	}
	for i, r := range t {
		if r == ' ' {
			return strings.TrimSpace(t[:i])
		}
	}
	return t
}

func (s *Server) handleDoubanSubject(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id := q.Get("id")
	if id == "" {
		writeJSON(w, 200, fail("缺少 id 参数"))
		return
	}
	cover := q.Get("cover")
	refresh := q.Get("refresh") == "1"

	// 缓存优先：命中且新鲜（或强制刷新关闭时命中即回），未命中才走
	// 网络。过期命中仍先返回旧数据（SWR），后台静默刷新写库——客户端
	// 靠 revision 感知更新。
	if !refresh {
		if payload, fresh, ok := s.subjectCacheGet(id); ok {
			var data map[string]any
			if json.Unmarshal(payload, &data) == nil && len(data) > 0 {
				// stale/revision 同时放 data 内（客户端 Subject 结构直解）
				// 与顶层（调试直观）。
				if !fresh {
					data["stale"] = true
					if rev := subjectCacheGetRevisionStable(s, id); rev != "" {
						data["revision"] = rev
					}
					go s.refreshSubject(id, cover)
				}
				writeJSON(w, 200, map[string]any{"isSuccess": true, "data": data, "revision": data["revision"]})
				return
			}
		}
	}

	data, err := fetchDoubanSubject(id, cover)
	if err != nil {
		// 网络失败但有旧缓存：返回旧数据（标记 stale），不白屏。
		if payload, _, ok := s.subjectCacheGet(id); ok {
			var old map[string]any
			if json.Unmarshal(payload, &old) == nil && len(old) > 0 {
				old["stale"] = true
				if rev := subjectCacheGetRevisionStable(s, id); rev != "" {
					old["revision"] = rev
				}
				writeJSON(w, 200, map[string]any{"isSuccess": true, "data": old})
				return
			}
		}
		writeJSON(w, 200, fail("获取影片详情失败"))
		return
	}
	rev := s.subjectCachePut(id, marshalStable(data))
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": data, "revision": rev})
}

// subjectCacheGetRevisionStable subjectCacheRevision 的 Server 方法别名
// （保持调用点简洁）。
func subjectCacheGetRevisionStable(s *Server, id string) string { return s.subjectCacheRevision(id) }

// refreshSubject SWR 后台刷新：单飞，成功写库。失败静默（旧数据仍在）。
func (s *Server) refreshSubject(id, cover string) {
	if _, loaded := subjectRefreshInFlight.LoadOrStore(id, true); loaded {
		return
	}
	defer subjectRefreshInFlight.Delete(id)
	data, err := fetchDoubanSubject(id, cover)
	if err != nil {
		logInfo("subject refresh %s failed: %v", id, err)
		return
	}
	s.subjectCachePut(id, marshalStable(data))
}

// fetchDoubanSubject 抓取豆瓣影片详情并组装为响应 data（原
// handleDoubanSubject 的网络与组装段，缓存逻辑拆出后独立可复用）。
func fetchDoubanSubject(id, cover string) (map[string]any, error) {
	apiURL := "https://movie.douban.com/j/subject_abstract?subject_id=" + url.QueryEscape(id)
	req, _ := http.NewRequest(http.MethodGet, apiURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://movie.douban.com/")
	resp, err := safeHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxFetchBodyBytes))
	var obj struct {
		R       int            `json:"r"`
		Subject map[string]any `json:"subject"`
	}
	if err := json.Unmarshal(b, &obj); err != nil || obj.R != 0 {
		return nil, fmt.Errorf("bad subject response")
	}
	subject := obj.Subject
	data := map[string]any{}
	data["id"] = id
	data["url"] = "https://movie.douban.com/subject/" + id + "/"
	rawTitle := getMapStr(subject, "title")
	data["title"] = strings.TrimSpace(reYearStrip.ReplaceAllString(rawTitle, ""))
	yr := reYearExtract.ReplaceAllString(rawTitle, "$1")
	if yr == rawTitle {
		yr = getMapStr(subject, "release_year")
	}
	data["year"] = yr
	coverURL := cover
	if coverURL == "" {
		// The subject_abstract response does not carry a usable `cover`, and
		// scraping og:image from the mobile subject page is unreliable (Douban
		// serves inconsistent HTML to non-browser clients). Instead resolve the
		// poster via Douban's suggest endpoint — the same cover data the app
		// already uses on the home/search grids — matching by subject id.
		coverURL = doubanCoverViaSuggest(id, getMapStr(data, "title"))
	}
	data["cover"] = coverURL
	data["rating"] = getMapStr(subject, "rate")
	data["directors"] = subject["directors"]
	data["actors"] = subject["actors"]
	data["genres"] = subject["types"]
	data["region"] = getMapStr(subject, "region")
	data["episodes"] = getMapStr(subject, "episodes_count")
	data["duration"] = getMapStr(subject, "duration")
	data["subtype"] = getMapStr(subject, "subject_type")

	intro := getMapStr(subject, "intro")
	if intro == "" {
		intro = getMapStr(subject, "description")
	}
	if intro == "" {
		intro = getMapStr(subject, "card_subtitle")
	}
	if intro = sanitizeIntro(intro); intro == "" {
		subjectType := getMapStr(subject, "subject_type")
		mobileURL := "https://m.douban.com/movie/subject/" + id + "/"
		if subjectType != "movie" {
			mobileURL = "https://m.douban.com/tv/subject/" + id + "/"
		}
		if doc, e := fetchDoc(mobileURL); e == nil {
			html, _ := doc.Html()
			if m := reDesc.FindStringSubmatch(html); len(m) > 1 {
				if idx := strings.Index(m[1], "简介："); idx >= 0 {
					intro = strings.TrimSpace(m[1][idx+len("简介："):])
				} else {
					intro = strings.TrimSpace(m[1])
				}
			}
		}
	}
	data["intro"] = sanitizeIntro(intro)
	return data, nil
}

func fetchDoc(rawURL string) (*goquery.Document, error) {
	if !isURLSafeForFetch(rawURL) {
		return nil, fmt.Errorf("blocked")
	}
	client := safeHTTPClient(8 * time.Second)
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148")
	req.Header.Set("Referer", "https://m.douban.com/")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBodyBytes))
	if err != nil {
		return nil, err
	}
	return goquery.NewDocumentFromReader(strings.NewReader(string(b)))
}
