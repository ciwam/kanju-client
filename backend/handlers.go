package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

func dataArray(m map[string]any) []map[string]any {
	v, ok := m["data"]
	if !ok {
		return nil
	}
	if arr, ok := v.([]map[string]any); ok {
		return arr
	}
	if arr, ok := v.([]any); ok {
		out := []map[string]any{}
		for _, it := range arr {
			if mm, ok := it.(map[string]any); ok {
				out = append(out, mm)
			}
		}
		return out
	}
	return nil
}

func isSuccessWithData(resp map[string]any) bool {
	if resp == nil {
		return false
	}
	ok, _ := resp["isSuccess"].(bool)
	if !ok {
		return false
	}
	ch, ok := resp["channels"]
	if !ok {
		return false
	}
	switch arr := ch.(type) {
	case []any:
		return len(arr) > 0
	case []map[string]any:
		return len(arr) > 0
	default:
		return false
	}
}

func channelCount(resp map[string]any) int {
	if resp == nil {
		return 0
	}
	switch ch := resp["channels"].(type) {
	case []any:
		return len(ch)
	case []map[string]any:
		return len(ch)
	default:
		return 0
	}
}

// searchConcurrency caps how many site sources are queried in parallel during
// a keyword search. Site search is pure HTTP (no DB), so a high cap is safe;
// it bounds fan-out if the source list grows and avoids hammering upstreams.
const searchConcurrency = 10

// handleSearchStream is the progressive-search counterpart of handleSearch.
// Instead of aggregating every site source and returning one big JSON payload
// (which forces the client to wait for the SLOWEST source before showing
// anything), it fans the keyword search out across all active sources in
// parallel and streams each source's result back over Server-Sent Events as
// soon as that source completes. The frontend renders each source's content
// box the moment it arrives, so fast sources (good network) show up first and
// the slow outlier no longer blocks the whole page.
func (s *Server) handleSearchStream(w http.ResponseWriter, r *http.Request) {
	// 搜索流接口做频率限制，防止单 IP 过多并发搜索请求（15 次/分钟）。
	ip := clientIP(r)
	if s.rl.limited("search_stream:"+ip, 15, 60) {
		writeJSON(w, 200, fail("搜索请求过于频繁，请稍后再试"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, 200, fail("streaming unsupported"))
		return
	}
	keyword := strings.TrimSpace(r.URL.Query().Get("keyword"))
	fallback := strings.TrimSpace(r.URL.Query().Get("fallback"))
	if keyword == "" {
		writeJSON(w, 200, fail("keyword 不能为空"))
		return
	}
	sources, err := s.getActiveSiteSources()
	if err != nil {
		writeJSON(w, 200, fail("获取站源失败"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// send writes one SSE event and flushes. It returns false if the client
	// disconnected, so the writer loop can stop early.
	send := func(event string, payload any) bool {
		data, e := json.Marshal(payload)
		if e != nil {
			return false
		}
		select {
		case <-r.Context().Done():
			return false
		default:
		}
		fmt.Fprintf(w, "event: %s\n", event)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		return true
	}

	logInfoC(r.Context(), "search/stream: keyword=%q fallback=%q activeSources=%d", keyword, fallback, len(sources))

	if len(sources) == 0 {
		send("meta", map[string]any{"total": 0, "keyword": keyword})
		send("done", map[string]any{})
		return
	}

	send("meta", map[string]any{"total": len(sources), "keyword": keyword})

	// 慢源探测期间连接可能长时间无数据，需心跳保活（见函数注释）。
	stopHeartbeat := startSearchStreamHeartbeat(r.Context(), flusher, w)
	defer stopHeartbeat()

	// trace 随 ctx 进每个源的 goroutine：一个搜索的所有源日志共享同一 id。
	ctx := r.Context()

	type sres struct {
		name         string
		items        []map[string]any
		dur          time.Duration
		matched      string
		err          string
		unavailable  bool
		playable     bool
		downloadable bool
		reason       string
		metrics      mediaMetrics // 供前端「智能排序」按网络质量排序
	}
	ch := make(chan sres, len(sources))
	sem := make(chan struct{}, searchConcurrency)
	var wg sync.WaitGroup
	for _, source := range sources {
		wg.Add(1)
		sem <- struct{}{}
		go func(source *SiteSource) {
			defer wg.Done()
			defer func() { <-sem }()
			st := time.Now()
			res, e := s.search(ctx, source, keyword)
			matched := keyword
			// Per-source title fallback: if the CJK title yields nothing but a
			// cleaner title was supplied, retry with it (mirrors the old
			// client-side retry so we keep one stream instead of two).
			if e == nil && len(dataArray(res)) == 0 && fallback != "" && fallback != keyword {
				res, e = s.search(ctx, source, fallback)
				matched = fallback
			}
			dur := time.Since(st)
			if e != nil {
				logWarnC(ctx, "search/stream source=%q FAILED dur=%s err=%v", source.Name, dur, e)
				ch <- sres{name: source.Name, dur: dur, err: e.Error()}
				return
			}
			items := dataArray(res)
			for _, it := range items {
				it["sourceName"] = source.Name
			}
			logInfoC(ctx, "search/stream source=%q done dur=%s results=%d matched=%q", source.Name, dur, len(items), matched)

			// Availability gate: a source that is "searchable but dead" (its
			// episodes can neither play nor download) must be filtered out before
			// it is shown to the user. We sample one item's detail and prove at
			// least one episode's play/download link is live; if not, emit an
			// "unavailable" marker instead of the items so the client hides it.
			var metrics mediaMetrics
			if availabilityCheckEnabled && len(items) > 0 {
				ar := s.cachedAvailability(ctx, source, items)
				metrics = ar.Metrics
				if !ar.Available {
					logWarnC(ctx, "search/stream source=%q AVAILABILITY FAILED play=%v down=%v dur=%s reason=%s",
						source.Name, ar.Playable, ar.Downloadable, dur, ar.Reason)
					ch <- sres{name: source.Name, dur: dur, matched: matched, unavailable: true,
						playable: ar.Playable, downloadable: ar.Downloadable, reason: ar.Reason, metrics: metrics}
					return
				}
				logInfoC(ctx, "search/stream source=%q AVAILABILITY OK play=%v down=%v speed=%dKB/s ttfb=%dms bitrate=%dKbps",
					source.Name, ar.Playable, ar.Downloadable, metrics.SpeedKBps, metrics.TTFBMs, metrics.BitrateKBps)
			}

			ch <- sres{name: source.Name, items: items, dur: dur, matched: matched, metrics: metrics}
		}(source)
	}
	go func() { wg.Wait(); close(ch) }()

	for r2 := range ch {
		payload := map[string]any{
			"sourceName": r2.name,
			"dur":        r2.dur.Milliseconds(),
			"matched":    r2.matched,
		}
		// 网络质量指标：前端「智能排序」据此把播放最快/最不卡顿的站源排前面。
		if !r2.metrics.Empty() {
			payload["speedKbps"] = r2.metrics.SpeedKBps
			payload["ttfbMs"] = r2.metrics.TTFBMs
			if r2.metrics.BitrateKBps > 0 {
				payload["bitrateKbps"] = r2.metrics.BitrateKBps
			}
		}
		if r2.err != "" {
			payload["error"] = r2.err
		} else if r2.unavailable {
			// Dead source: tell the client to filter it out entirely, and
			// report which link kind failed so the client can log it.
			payload["unavailable"] = true
			payload["playable"] = r2.playable
			payload["downloadable"] = r2.downloadable
			if r2.reason != "" {
				payload["reason"] = r2.reason
			}
			payload["items"] = []any{}
		} else {
			payload["items"] = r2.items
		}
		if !send("search", payload) {
			break
		}
	}
	send("done", map[string]any{})
}

// startSearchStreamHeartbeat 每 15s 向 SSE 写一行注释（": ping"）并 flush，
// 保证慢源探测期间（单源最长 30s+，多慢源叠加可整分钟无数据）连接上
// 始终有字节流动，避免代理（nginx 默认 60s 读超时）/中间设备掐断空闲
// 连接导致剩余站源结果永远无法送达。SSE 注释行会被客户端自动忽略。
// 返回停止函数，流结束时必须调用以防 goroutine 泄漏。
func startSearchStreamHeartbeat(ctx context.Context, flusher http.Flusher, w io.Writer) (stop func()) {
	done := make(chan struct{})
	ticker := time.NewTicker(15 * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}()
	return func() { close(done) }
}

func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request) {
	m, err := readJSONMap(w, r)
	if err != nil {
		writeJSON(w, 200, fail("请求格式错误"))
		return
	}
	detailURL := getStr(m, "url")
	sourceName := getStrDefault(m, "sourceName", "")
	title := getStr(m, "title")
	ctx := r.Context()
	t0 := time.Now()
	logInfoC(ctx, "detail: start url=%s sourceName=%q title=%q", detailURL, sourceName, title)
	sources, err := s.getActiveSiteSources()
	if err != nil {
		writeJSON(w, 200, fail("获取站源失败"))
		return
	}
	logInfoC(ctx, "detail: activeSources=%d prepareDur=%s", len(sources), time.Since(t0))

	if sourceName != "" {
		for _, source := range sources {
			if source.Name == sourceName {
				st := time.Now()
				res, e := s.getDetail(ctx, source, detailURL)
				dur := time.Since(st)
				if e != nil {
					logWarnC(ctx, "detail source=%q FAILED dur=%s err=%v", source.Name, dur, e)
					writeJSON(w, 200, fail("获取详情失败: "+e.Error()))
					return
				}
				cnt := channelCount(res)
				logInfoC(ctx, "detail source=%q done dur=%s channels=%d", source.Name, dur, cnt)
				if isSuccessWithData(res) {
					if ch, ok := res["channels"].([]any); ok {
						for _, c := range ch {
							if cm, ok := c.(map[string]any); ok {
								cm["_sourceName"] = source.Name
							}
						}
					}
				}
				writeJSON(w, 200, res)
				return
			}
		}
	}

	allChannels := []map[string]any{}
	cover := ""
	intro := ""
	anySuccess := false
	for _, source := range sources {
		st := time.Now()
		res, e := s.getDetail(ctx, source, detailURL)
		dur := time.Since(st)
		if e != nil {
			logWarnC(ctx, "detail source=%q FAILED dur=%s err=%v", source.Name, dur, e)
			continue
		}
		cnt := 0
		if isSuccessWithData(res) {
			// channels 可能是 []map[string]any（内存直接返回）或 []any（JSON
			// 往返后）；用 channelsOf 统一兼容，避免类型断言漏掉全部选集
			for _, cm := range channelsOf(res) {
				cm["_sourceName"] = source.Name
				allChannels = append(allChannels, cm)
				cnt++
			}
			anySuccess = true
			if cover == "" {
				if cv, ok := res["cover"].(string); ok {
					cover = cv
				}
			}
			if intro == "" {
				if it, ok := res["intro"].(string); ok {
					intro = it
				}
			}
		} else if title != "" && source.SourceType == "json_api" {
			// Fallback: this source had no direct detail match, try searching by
			// title and fetching the best matching result's detail.
			//
			// 两个坑都要防：
			//   1. 盲取搜索第一条——站源模糊搜索常返回同名前作/无关影片
			//      （第一季只有几集、无关剧上千集），必须 titlesMatch 把关；
			//   2. 关键词太长——豆瓣/TMDB 的完整标题（带副标题、外文名）
			//      在站源搜不到任何匹配，需要渐进简化：完整标题 →
			//      去掉「-…-」副标题段 → 主标题，每级仍过 titlesMatch。
			sst := time.Now()
			for _, kw := range fallbackKeywords(title) {
				sres, se := s.search(ctx, source, kw)
				if se != nil {
					break // 站源搜索本身失败，不再换关键词重试
				}
				found := false
				for _, item := range dataArray(sres) {
					if !titlesMatch(getMapStr(item, "title"), title) {
						continue
					}
					dres, de := s.getDetail(ctx, source, getMapStr(item, "url"))
					if de == nil && dres != nil {
						for _, cm := range channelsOf(dres) {
							cm["_sourceName"] = source.Name
							allChannels = append(allChannels, cm)
							cnt++
						}
						anySuccess = true
						if cover == "" {
							if cv, ok := dres["cover"].(string); ok {
								cover = cv
							}
						}
						if intro == "" {
							if it, ok := dres["intro"].(string); ok {
								intro = it
							}
						}
					}
					found = true
					break // 取第一个标题匹配的条目即可
				}
				if found {
					break // 本级关键词已命中，不再简化
				}
			}
			logInfoC(ctx, "detail source=%q fallback(title→detail) dur=%s channels=%d", source.Name, time.Since(sst), cnt)
		}
		logInfoC(ctx, "detail source=%q done dur=%s channels=%d", source.Name, dur, cnt)
	}
	merged := map[string]any{"isSuccess": anySuccess, "channels": allChannels}
	if cover != "" {
		merged["cover"] = cover
	}
	if intro != "" {
		merged["intro"] = intro
	}
	logInfoC(ctx, "detail done: channels=%d dur=%s", len(allChannels), time.Since(t0))
	writeJSON(w, 200, merged)
}

func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	m, err := readJSONMap(w, r)
	if err != nil {
		writeJSON(w, 200, fail("请求格式错误"))
		return
	}
	playURL := getStr(m, "url")
	ctx := r.Context()
	t0 := time.Now()
	logInfoC(ctx, "play: start url=%s", playURL)
	// 命中缓存直接返回，避免同一选集反复抓取上游播放页（仅缓存成功结果）
	if cached, ok := playURLCache.get(playURL); ok {
		logInfoC(ctx, "play: cache HIT url=%s dur=%s type=%v", playURL, time.Since(t0), cached["type"])
		writeJSON(w, 200, cached)
		return
	}
	res, e := s.getPlayUrl(ctx, playURL)
	if e != nil {
		logWarnC(ctx, "play FAILED url=%s dur=%s err=%v", playURL, time.Since(t0), e)
		writeJSON(w, 200, fail("获取播放地址失败"))
		return
	}
	playURLCache.put(playURL, res)
	logInfoC(ctx, "play done url=%s dur=%s type=%v", playURL, time.Since(t0), res["type"])
	writeJSON(w, 200, res)
}

// ---- sources ----

func (s *Server) handleGetSources(w http.ResponseWriter, r *http.Request) {
	sources, err := s.getAllSiteSources()
	if err != nil {
		writeJSON(w, 200, fail("获取站源失败"))
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": sources})
}

func (s *Server) handleSaveSource(w http.ResponseWriter, r *http.Request) {
	if _, err := s.requireAdmin(r); err != nil {
		writeJSON(w, 200, fail("无权限"))
		return
	}
	body, err := readJSONRaw(w, r)
	if err != nil {
		writeJSON(w, 200, fail("请求格式错误"))
		return
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		writeJSON(w, 200, fail("请求格式错误"))
		return
	}
	baseURL := getStr(m, "baseUrl")
	if baseURL == "" || !isURLSafeForFetch(baseURL) {
		writeJSON(w, 200, fail("站源 URL 不合法"))
		return
	}
	if err := s.saveSiteSource(m); err != nil {
		writeJSON(w, 200, fail("保存站源失败: "+err.Error()))
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true})
}

func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	if _, err := s.requireAdmin(r); err != nil {
		writeJSON(w, 200, fail("无权限"))
		return
	}
	m, err := readJSONMap(w, r)
	if err != nil {
		writeJSON(w, 200, fail("请求格式错误"))
		return
	}
	if err := s.deleteSiteSource(getStr(m, "baseUrl")); err != nil {
		writeJSON(w, 200, fail("删除站源失败"))
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true})
}

func (s *Server) handleToggleSource(w http.ResponseWriter, r *http.Request) {
	if _, err := s.requireAdmin(r); err != nil {
		writeJSON(w, 200, fail("无权限"))
		return
	}
	m, err := readJSONMap(w, r)
	if err != nil {
		writeJSON(w, 200, fail("请求格式错误"))
		return
	}
	if err := s.toggleSiteSource(getStr(m, "baseUrl")); err != nil {
		writeJSON(w, 200, fail("切换站源状态失败"))
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true})
}

func (s *Server) handleBatchSaveSources(w http.ResponseWriter, r *http.Request) {
	if _, err := s.requireAdmin(r); err != nil {
		writeJSON(w, 200, fail("无权限"))
		return
	}
	body, err := readJSONRaw(w, r)
	if err != nil {
		writeJSON(w, 200, fail("请求格式错误"))
		return
	}
	var arr []map[string]any
	if err := json.Unmarshal(body, &arr); err != nil {
		writeJSON(w, 200, fail("批量导入失败"))
		return
	}
	for _, item := range arr {
		baseURL := getStr(item, "baseUrl")
		if baseURL == "" || !isURLSafeForFetch(baseURL) {
			logWarn("batch save source skipped: invalid baseUrl %q", baseURL)
			continue
		}
		if err := s.saveSiteSource(item); err != nil {
			logWarn("batch save source failed: %v", err)
		}
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true})
}

// ---- history ----

func (s *Server) handleGetHistory(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	if u == nil {
		writeJSON(w, 200, map[string]any{"isSuccess": true, "data": []any{}})
		return
	}
	hist, err := s.getHistory(&u.ID)
	if err != nil {
		writeJSON(w, 200, fail("获取历史失败"))
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": hist})
}

func (s *Server) handleSaveHistory(w http.ResponseWriter, r *http.Request) {
	m, err := readJSONMap(w, r)
	if err != nil {
		writeJSON(w, 200, fail("请求格式错误"))
		return
	}
	u := s.currentUser(r)
	var uid *int64
	if u != nil {
		uid = &u.ID
	}
	if err := s.saveHistory(uid,
		getStr(m, "title"), getStr(m, "cover"), getStr(m, "sourceName"),
		getStr(m, "sourceUrl"), getStr(m, "episodeTitle"), getStr(m, "playUrl"),
		getInt(m, "position"), getInt(m, "duration")); err != nil {
		writeJSON(w, 200, fail("保存历史记录失败"))
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true})
}

// ---- favorites ----

func (s *Server) handleGetFavorites(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	if u == nil {
		writeJSON(w, 200, map[string]any{"isSuccess": true, "data": []any{}})
		return
	}
	favs, err := s.getFavorites(&u.ID)
	if err != nil {
		writeJSON(w, 200, fail("获取收藏失败"))
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": favs})
}

func (s *Server) handleSaveFavorite(w http.ResponseWriter, r *http.Request) {
	m, err := readJSONMap(w, r)
	if err != nil {
		writeJSON(w, 200, fail("请求格式错误"))
		return
	}
	u := s.currentUser(r)
	if u == nil {
		writeJSON(w, 200, fail("请先登录"))
		return
	}
	if err := s.saveFavorite(&u.ID, getStr(m, "title"), getStr(m, "cover"),
		getStr(m, "sourceName"), getStr(m, "detailUrl")); err != nil {
		writeJSON(w, 200, fail("收藏失败"))
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true})
}

func (s *Server) handleRemoveFavorite(w http.ResponseWriter, r *http.Request) {
	m, err := readJSONMap(w, r)
	if err != nil {
		writeJSON(w, 200, fail("请求格式错误"))
		return
	}
	u := s.currentUser(r)
	var uid *int64
	if u != nil {
		uid = &u.ID
	}
	if err := s.removeFavorite(uid, getStr(m, "detailUrl")); err != nil {
		writeJSON(w, 200, fail("取消收藏失败"))
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true})
}

// ---- live ----

func (s *Server) handleGetLiveChannels(w http.ResponseWriter, r *http.Request) {
	group := r.URL.Query().Get("group")
	ch, err := s.getLiveChannels(group)
	if err != nil {
		writeJSON(w, 200, fail("获取直播失败"))
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": ch})
}

func parseInt64(s string) (int64, error) {
	var v int64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &v)
	return v, err
}
