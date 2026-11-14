package backend

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// availabilityCheckEnabled toggles the pre-display source availability gate.
// It is on by default; pass --availability-check=false to disable (the search
// stream then behaves exactly as before, showing every searchable source).
var availabilityCheckEnabled = true

// mediaProbeResult reports whether a resolved media URL is usable for playback
// and for offline download. Both are derived from the same underlying URL (the
// app downloads through /api/proxy/media using the very URL /api/play returns),
// so a reachable, non-empty media resource is both playable and downloadable.
// For HLS (m3u8) we additionally probe the first segment to validate the
// download path specifically; a reachable playlist with an unreachable segment
// is still considered playable (the player may adapt) but flagged undownloadable.
type mediaProbeResult struct {
	Playable     bool
	Downloadable bool
	Info         string
	Metrics      mediaMetrics
}

// speedProbeBytes bounds how much of a media resource we pull when measuring
// throughput. 192KB smooths out TCP ramp-up while keeping per-source traffic
// and latency modest; results are cached for availabilitySuccessTTL so
// repeated searches reuse the same measurement.
const speedProbeBytes = 192 * 1024

// mediaMetrics 是一次媒体探测测得的网络质量指标，用作「智能排序」依据：
//   - TTFBMs 首字节延迟（影响起播快慢）
//   - SpeedKBps 实测吞吐（直接影响播放是否卡顿）
//   - BitrateKBps 媒体声明码率（m3u8 的 BANDWIDTH）；配合 SpeedKBps 可得
//     「速度余量」= SpeedKBps/BitrateKBps，余量 > 1 才不会持续缓冲
type mediaMetrics struct {
	TTFBMs      int `json:"ttfbMs"`
	SpeedKBps   int `json:"speedKbps"`
	BitrateKBps int `json:"bitrateKbps"`
}

// Empty 表示这次探测没采到任何可用于排序的指标（如 iframe 源）。
func (m mediaMetrics) Empty() bool { return m.SpeedKBps <= 0 && m.TTFBMs <= 0 }

// availResult 是一次站源可用性检查的完整结论，含供「智能排序」使用的
// 网络质量指标（Metrics 取探测过程中表现最好的一次测量）。
type availResult struct {
	Available    bool
	Playable     bool
	Downloadable bool
	Reason       string
	Metrics      mediaMetrics
}

// parseBandwidthKbps 从 m3u8 文本提取媒体声明码率（Kbps）。优先取第一档
// #EXT-X-STREAM-INF 的 BANDWIDTH（即默认起播档），无 STREAM-INF 时用
// #EXT-X-BITRATE 兜底；都取不到返回 0。
func parseBandwidthKbps(body string) int {
	best := 0
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF"):
			if v := bandwidthFromAttrs(line); v > 0 {
				// 第一档即默认起播档，直接采用
				return v
			}
		case strings.HasPrefix(line, "#EXT-X-BITRATE"):
			if _, v, ok := strings.Cut(line, ":"); ok {
				if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
					best = n // 单位已是 Kbps
				}
			}
		}
	}
	return best
}

// bandwidthFromAttrs 从 #EXT-X-STREAM-INF 行里取 BANDWIDTH=<bps> 并换算 Kbps。
func bandwidthFromAttrs(line string) int {
	_, attrs, ok := strings.Cut(line, ":")
	if !ok {
		return 0
	}
	for _, kv := range strings.Split(attrs, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "BANDWIDTH") {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n / 1000
		}
	}
	return 0
}

// probeMedia validates that a resolved media URL actually serves playable /
// downloadable content. It is SSRF-safe (reuses isURLSafeForFetch) and never
// downloads more than a small preview of the resource.
func (s *Server) probeMedia(rawURL, mediaType string) mediaProbeResult {
	start := time.Now()
	status := 0
	var ct string

	finish := func(r mediaProbeResult) mediaProbeResult {
		logInfo("avail: probe url=%s type=%s status=%d ct=%q dur=%s -> play=%v down=%v info=%q",
			rawURL, mediaType, status, ct, time.Since(start), r.Playable, r.Downloadable, r.Info)
		return r
	}

	if rawURL == "" {
		return finish(mediaProbeResult{Info: "empty media url"})
	}
	if !isURLSafeForFetch(rawURL) {
		return finish(mediaProbeResult{Info: "blocked by security policy"})
	}
	client := safeHTTPClientWithRedirect(probeMediaTimeout)
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return finish(mediaProbeResult{Info: "bad url"})
	}
	req.Header.Set("User-Agent", desktopUA)
	req.Header.Set("Referer", getBaseURL(rawURL))
	// 直链媒体带 Range 请求一小段：既不会拉整个文件，又有足够字节
	// 完成吞吐实测（供智能排序）。不能请求 bytes=0-0——那只会返回
	// 1 个字节，后续测速循环读不到数据，SpeedKBps 恒为 0，导致
	// mp4/flv 源在「智能排序」中被系统性排到最后。
	if mediaType == "mp4" || mediaType == "flv" || mediaType == "mpd" {
		req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", speedProbeBytes-1))
	}
	resp, err := client.Do(req)
	if err != nil {
		return finish(mediaProbeResult{Info: "fetch failed: " + err.Error()})
	}
	// 响应头到达即近似首字节时刻：TTFB 是「起播快慢」的排序依据之一。
	firstByteAt := time.Now()
	defer resp.Body.Close()
	status = resp.StatusCode
	ct = resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return finish(mediaProbeResult{Info: fmt.Sprintf("status %d", resp.StatusCode)})
	}

	switch mediaType {
	case "m3u8":
		body, err := io.ReadAll(io.LimitReader(resp.Body, availabilityMediaProbeBytes))
		if err != nil {
			return finish(mediaProbeResult{Info: "playlist read failed"})
		}
		if !strings.Contains(string(body), "#EXTM3U") {
			// Not a real playlist (often an HTML error / challenge page).
			return finish(mediaProbeResult{Info: "not a valid m3u8 playlist"})
		}
		downloadable := true
		// Follow the playlist chain: a master playlist (#EXT-X-STREAM-INF)
		// only references child playlists, so we must descend into the child
		// and resolve the first real media segment there. Probing that segment
		// is what actually proves the source is playable/downloaded — a live
		// master with a dead child or segment tier would otherwise slip through.
		metrics := mediaMetrics{BitrateKBps: parseBandwidthKbps(string(body))}
		if seg := s.resolveFirstSegment(rawURL, string(body), 0); seg != "" {
			ok, segMetrics := s.probeSegmentSpeed(seg)
			if !ok {
				downloadable = false
			}
			// 分片实测优先于播放列表声明：TTFB/吞吐来自真实媒体流量
			metrics.TTFBMs = segMetrics.TTFBMs
			metrics.SpeedKBps = segMetrics.SpeedKBps
		} else {
			return finish(mediaProbeResult{Playable: false, Downloadable: false, Info: "no media segment resolvable from playlist chain"})
		}
		return finish(mediaProbeResult{Playable: true, Downloadable: downloadable, Info: "m3u8 ok", Metrics: metrics})
	case "iframe":
		// Server side cannot verify an iframe player's playback, so we never
		// filter iframe-type sources (avoiding false negatives).
		return finish(mediaProbeResult{Playable: true, Downloadable: true, Info: "iframe (unverified)"})
	default:
		// mp4 / flv / mpd / unknown direct media: 用一次带 Range 的实测下载
		// 同时验证可达性与吞吐（供智能排序使用）。
		total := 0
		buf := make([]byte, 32*1024)
		for total < speedProbeBytes {
			n, rerr := resp.Body.Read(buf)
			total += n
			if rerr != nil {
				break
			}
		}
		if total == 0 {
			return finish(mediaProbeResult{Info: "media read failed"})
		}
		m := mediaMetrics{TTFBMs: int(firstByteAt.Sub(start).Milliseconds())}
		// 吞吐按「扣除 TTFB 后的传输时间」计算，与 m3u8 分片测速
		// （probeSegmentSpeed）同一口径——否则直链源含 TCP 握手时间
		// 会被系统性低估，智能排序里两类源不可比。
		transfer := time.Since(start) - time.Duration(m.TTFBMs)*time.Millisecond
		if transfer <= 0 {
			transfer = time.Since(start)
		}
		m.SpeedKBps = int(float64(total) / transfer.Seconds() / 1024)
		return finish(mediaProbeResult{Playable: true, Downloadable: true, Info: "media reachable", Metrics: m})
	}
}

// probeSegmentSpeed 对媒体分片做一次带 Range 的实测下载：既验证下载路径
// 可达（失败视为「下载未验证」而非硬失败——部分 CDN 对分片有 cookie 门槛），
// 同时测出 TTFB 与吞吐（KB/s）。这两个指标是「智能排序」按网络质量排序的
// 依据：吞吐相对码率的余量越大，播放越不容易卡顿。
func (s *Server) probeSegmentSpeed(rawURL string) (bool, mediaMetrics) {
	start := time.Now()
	var m mediaMetrics
	if !isURLSafeForFetch(rawURL) {
		logInfo("avail: segment blocked by policy url=%s dur=%s", rawURL, time.Since(start))
		return false, m
	}
	client := safeHTTPClient(probeMediaTimeout)
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		logInfo("avail: segment bad url url=%s err=%v dur=%s", rawURL, err, time.Since(start))
		return false, m
	}
	req.Header.Set("User-Agent", desktopUA)
	// 取一段真实媒体数据用于测速（而非只探可达的 bytes=0-0）。
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", speedProbeBytes-1))
	resp, err := client.Do(req)
	if err != nil {
		logInfo("avail: segment fetch failed url=%s err=%v dur=%s", rawURL, err, time.Since(start))
		return false, m
	}
	defer resp.Body.Close()
	m.TTFBMs = int(time.Since(start).Milliseconds())
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		logInfo("avail: segment url=%s status=%d dur=%s -> false", rawURL, resp.StatusCode, time.Since(start))
		return false, m
	}
	total := 0
	buf := make([]byte, 32*1024)
	for total < speedProbeBytes {
		n, rerr := resp.Body.Read(buf)
		total += n
		if rerr != nil {
			break
		}
	}
	// 吞吐按「扣掉首字节延迟后的传输时间」计算：TTFB 反映起播快慢，
	// 传输速率反映持续播放能力，两者混在一起会低估带宽（TCP 慢启动 +
	// 固定握手开销在小样本下占比很高）。TTFB 单独作为排序的次级指标。
	transfer := time.Since(start) - time.Duration(m.TTFBMs)*time.Millisecond
	if transfer <= 0 {
		transfer = time.Since(start)
	}
	if total > 0 && transfer > 0 {
		m.SpeedKBps = int(float64(total) / transfer.Seconds() / 1024)
	}
	ok := total > 0
	logInfo("avail: segment url=%s status=%d bytes=%d ttfb=%dms transfer=%s speed=%dKB/s -> %v",
		rawURL, resp.StatusCode, total, m.TTFBMs, transfer, m.SpeedKBps, ok)
	return ok, m
}

// resolveFirstSegment walks an HLS playlist chain and returns the absolute URL
// of the first real media segment (a .ts/.m4s/.aac/.mp4 file) that can be
// resolved, descending through master playlists (#EXT-X-STREAM-INF) into their
// child playlists. It returns "" when no media segment can be resolved — either
// because the chain has no media tier, or because a referenced child playlist
// could not be fetched (dead child tier). The depth guard bounds how far we
// follow nested master->master links.
func (s *Server) resolveFirstSegment(playlistURL, text string, depth int) string {
	if depth > 3 {
		return ""
	}
	if strings.Contains(text, "#EXT-X-STREAM-INF") {
		// Master playlist: try each variant's child playlist in turn.
		lines := strings.Split(text, "\n")
		for i := 0; i < len(lines); i++ {
			if !strings.Contains(lines[i], "#EXT-X-STREAM-INF") {
				continue
			}
			for j := i + 1; j < len(lines); j++ {
				cand := strings.TrimSpace(lines[j])
				if cand == "" || strings.HasPrefix(cand, "#") {
					continue
				}
				child := absURL(playlistURL, cand)
				childText, ok := s.fetchPlaylistText(child)
				if !ok {
					break // this variant's child is unreachable; try the next variant
				}
				if seg := s.resolveFirstSegment(child, childText, depth+1); seg != "" {
					return seg
				}
				break // child resolved but had no media segment; try next variant
			}
		}
		return ""
	}
	return firstSegmentIn(playlistURL, text)
}

// firstChildPlaylistURI returns the absolute URL of the first child playlist
// referenced by a master playlist body (the URI following the first
// #EXT-X-STREAM-INF), or ("", false) if the body is not a master or has none.
func firstChildPlaylistURI(masterBody, baseURL string) (string, bool) {
	lines := strings.Split(masterBody, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.Contains(lines[i], "#EXT-X-STREAM-INF") {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			cand := strings.TrimSpace(lines[j])
			if cand == "" || strings.HasPrefix(cand, "#") {
				continue
			}
			return absURL(baseURL, cand), true
		}
	}
	return "", false
}

// firstSegmentIn returns the absolute URL of the first media segment line in a
// media (non-master) playlist body, or "" if none is found.
func firstSegmentIn(playlistURL, text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, ".ts") || strings.Contains(line, ".m4s") ||
			strings.Contains(line, ".aac") || strings.Contains(line, ".mp4") {
			return absURL(playlistURL, line)
		}
	}
	return ""
}

// absURL resolves a possibly-relative playlist/segment reference against a base
// playlist URL using RFC 3986 resolution (handles absolute, root-relative and
// relative paths).
func absURL(baseURL, ref string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return ref
	}
	if strings.HasPrefix(ref, "/") {
		return u.Scheme + "://" + u.Host + ref
	}
	dir := path.Dir(u.Path)
	joined := path.Join(dir, ref)
	return u.Scheme + "://" + u.Host + joined
}

// fetchPlaylistText performs an SSRF-safe, time-bounded GET of a playlist URL
// and returns its body if the response looks like a playlist (2xx + non-empty).
func (s *Server) fetchPlaylistText(rawURL string) (string, bool) {
	if !isURLSafeForFetch(rawURL) {
		return "", false
	}
	client := safeHTTPClientWithRedirect(probeMediaTimeout)
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", desktopUA)
	req.Header.Set("Referer", getBaseURL(rawURL))
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, availabilityMediaProbeBytes))
	if err != nil {
		return "", false
	}
	return string(body), true
}

// channelsOf normalizes the channels field (it may be []map[string]any in
// memory or []any after JSON round-trips).
func channelsOf(detail map[string]any) []map[string]any {
	out := []map[string]any{}
	switch ch := detail["channels"].(type) {
	case []map[string]any:
		out = append(out, ch...)
	case []any:
		for _, c := range ch {
			if cm, ok := c.(map[string]any); ok {
				out = append(out, cm)
			}
		}
	}
	return out
}

// episodesOf normalizes the episodes field of a channel.
func episodesOf(ch map[string]any) []map[string]any {
	out := []map[string]any{}
	switch eps := ch["episodes"].(type) {
	case []map[string]any:
		out = append(out, eps...)
	case []any:
		for _, e := range eps {
			if em, ok := e.(map[string]any); ok {
				out = append(out, em)
			}
		}
	}
	return out
}

// checkSourceAvailability samples one item's detail and, across the first few
// channels / episodes, resolves a play URL and probes it. A source is
// considered AVAILABLE if it can be played OR downloaded (per the requirement:
// filter out sources that can NEITHER play NOR download). The probe is bounded
// by availabilityCheckTimeout so a slow/dead source is filtered rather than
// blocking the search stream.
func (s *Server) checkSourceAvailability(source *SiteSource, items []map[string]any) availResult {
	if len(items) == 0 {
		// Nothing to prove or disprove; don't filter a source with no hits.
		logInfo("avail: source=%q items=0 -> skip (no items)", source.Name)
		return availResult{Available: true, Playable: true, Downloadable: true, Reason: "no items"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), availabilityCheckTimeout)
	defer cancel()

	var detailURL string
	for _, it := range items {
		if u := getMapStr(it, "url"); u != "" {
			detailURL = u
			break
		}
	}
	if detailURL == "" {
		logInfo("avail: source=%q no detail url in search results -> UNAVAILABLE", source.Name)
		return availResult{Reason: "no detail url in search results"}
	}

	logInfo("avail: source=%q start sampling detailURL=%s", source.Name, detailURL)
	dStart := time.Now()
	detail, err := s.getDetail(source, detailURL)
	dDur := time.Since(dStart)
	if err != nil {
		logInfo("avail: source=%q detail FAILED dur=%s err=%v -> UNAVAILABLE", source.Name, dDur, err)
		return availResult{Reason: "detail fetch failed: " + err.Error()}
	}
	channels := channelsOf(detail)
	logInfo("avail: source=%q detail dur=%s channels=%d", source.Name, dDur, len(channels))
	if len(channels) == 0 {
		logInfo("avail: source=%q detail returned no channels/episodes -> UNAVAILABLE", source.Name)
		return availResult{Reason: "detail returned no channels/episodes"}
	}

	played := false
	downed := false
	probed := 0
	lastInfo := ""
	best := mediaMetrics{} // 取速度最高的一次测量作为该源的质量代表
loop:
	for ci, ch := range channels {
		// 最多采样 maxChannelsToCheck 个线路（每线路取其首个选集），
		// 再叠加全局 maxProbeTotal 上限与每线路选集数上限。
		if ci >= maxChannelsToCheck {
			break
		}
		chName := getStr(ch, "name")
		eps := episodesOf(ch)
		probedInChannel := 0
		for _, ep := range eps {
			if probed >= maxProbeTotal || probedInChannel >= maxEpisodesPerChannelToCheck {
				break
			}
			epURL := getMapStr(ep, "url")
			epTitle := getStr(ep, "title")
			if epURL == "" {
				continue
			}
			if ctx.Err() != nil {
				logInfo("avail: source=%q ctx timeout after %d probes -> %v", source.Name, probed, played || downed)
				return availResult{Available: played || downed, Playable: played, Downloadable: downed,
					Reason: "availability check timed out", Metrics: best}
			}
			pStart := time.Now()
			pr, perr := s.getPlayUrl(epURL)
			if perr != nil {
				probed++
				probedInChannel++
				lastInfo = "play url resolve failed: " + perr.Error()
				logInfo("avail: source=%q ch=%q ep=%q epURL=%s resolve FAILED dur=%s err=%v",
					source.Name, chName, epTitle, epURL, time.Since(pStart), perr)
				continue
			}
			mediaURL := getMapStr(pr, "url")
			mediaType := getStr(pr, "type")
			probe := s.probeMedia(mediaURL, mediaType)
			probed++
			probedInChannel++
			lastInfo = probe.Info
			logInfo("avail: source=%q ch=%q ep=%q epURL=%s -> media=%s type=%s dur=%s play=%v down=%v info=%q",
				source.Name, chName, epTitle, epURL, mediaURL, mediaType, time.Since(pStart), probe.Playable, probe.Downloadable, probe.Info)
			if probe.Playable {
				played = true
			}
			if probe.Downloadable {
				downed = true
			}
			// 保留吞吐最好的一次测量（探测可能落在冷启动分片上）
			if probe.Metrics.SpeedKBps > best.SpeedKBps {
				best = probe.Metrics
			} else if best.SpeedKBps == 0 && best.TTFBMs == 0 {
				best = probe.Metrics
			}
			if played && downed {
				logInfo("avail: source=%q VERDICT available=true (probe %d) play=%v down=%v speed=%dKB/s ttfb=%dms bitrate=%dKbps",
					source.Name, probed, played, downed, best.SpeedKBps, best.TTFBMs, best.BitrateKBps)
				return availResult{Available: true, Playable: true, Downloadable: true, Reason: "ok", Metrics: best}
			}
			if probed >= maxProbeTotal {
				break loop
			}
		}
	}

	available := played || downed
	reason := "ok"
	if !available {
		reason = "unplayable & undownloadable: " + lastInfo
		logInfo("avail: source=%q VERDICT available=false play=%v down=%v reason=%q", source.Name, played, downed, reason)
	} else {
		logInfo("avail: source=%q VERDICT available=true (probe %d) play=%v down=%v speed=%dKB/s ttfb=%dms",
			source.Name, probed, played, downed, best.SpeedKBps, best.TTFBMs)
	}
	return availResult{Available: available, Playable: played, Downloadable: downed, Reason: reason, Metrics: best}
}

// ---- per-source result cache ----

type availCacheEntry struct {
	available    bool
	playable     bool
	downloadable bool
	reason       string
	metrics      mediaMetrics
	expires      time.Time
}

var (
	availCacheMu sync.Mutex
	availCache   = map[string]availCacheEntry{}
)

func init() {
	go func() {
		t := time.NewTicker(30 * time.Minute)
		defer t.Stop()
		for range t.C {
			availCacheMu.Lock()
			now := time.Now()
			for k, e := range availCache {
				if now.After(e.expires) {
					delete(availCache, k)
				}
			}
			availCacheMu.Unlock()
		}
	}()
}

// availCacheKey 是内存缓存键：源名 + 源地址，避免不同源同名误命中。
func availCacheKey(source *SiteSource) string {
	return source.Name + "\x00" + source.BaseURL
}

// loadAvailCache 从磁盘读取持久化的可用性结论（跨重启复用）。
// 返回 (entry, true) 当记录存在且未过期。
func (s *Server) loadAvailCache(key string) (availCacheEntry, bool) {
	parts := strings.SplitN(key, "\x00", 2)
	if len(parts) != 2 {
		return availCacheEntry{}, false
	}
	var (
		available, playable, downloadable int
		reason, expiresAt                 string
		ttfbMs, speedKbps, bitrateKbps    int
	)
	err := s.db.QueryRow(
		`SELECT available, playable, downloadable, reason, expires_at,
		        COALESCE(ttfb_ms,0), COALESCE(speed_kbps,0), COALESCE(bitrate_kbps,0)
		 FROM avail_cache WHERE source_name = ? AND base_url = ?`,
		parts[0], parts[1]).Scan(&available, &playable, &downloadable, &reason, &expiresAt,
		&ttfbMs, &speedKbps, &bitrateKbps)
	if err != nil {
		return availCacheEntry{}, false
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil || time.Now().After(exp) {
		return availCacheEntry{}, false
	}
	metrics := mediaMetrics{TTFBMs: ttfbMs, SpeedKBps: speedKbps, BitrateKBps: bitrateKbps}
	return availCacheEntry{
		metrics:      metrics,
		available:    available == 1,
		playable:     playable == 1,
		downloadable: downloadable == 1,
		reason:       reason,
		expires:      exp,
	}, true
}

// storeAvailCache 把可用性结论持久化到 SQLite，服务重启后直接复用，
// 避免重启后首次搜索为每个站源重新探测（每个源最多 30s 的串行探测链）。
func (s *Server) storeAvailCache(key string, e availCacheEntry) {
	parts := strings.SplitN(key, "\x00", 2)
	if len(parts) != 2 {
		return
	}
	_, err := s.db.Exec(
		`INSERT INTO avail_cache (source_name, base_url, available, playable, downloadable, reason,
		                          ttfb_ms, speed_kbps, bitrate_kbps, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(source_name, base_url) DO UPDATE SET
		   available = excluded.available, playable = excluded.playable,
		   downloadable = excluded.downloadable, reason = excluded.reason,
		   ttfb_ms = excluded.ttfb_ms, speed_kbps = excluded.speed_kbps,
		   bitrate_kbps = excluded.bitrate_kbps,
		   expires_at = excluded.expires_at`,
		parts[0], parts[1], boolToInt(e.available), boolToInt(e.playable),
		boolToInt(e.downloadable), e.reason,
		e.metrics.TTFBMs, e.metrics.SpeedKBps, e.metrics.BitrateKBps,
		e.expires.Format(time.RFC3339))
	if err != nil {
		logWarn("avail: persist cache failed: %v", err)
	}
}

// cachedAvailability returns the (cached) availability verdict for a source,
// probing on a cache miss. Caching by source name means repeated searches
// across different films don't re-validate the same source's link structure,
// which is consistent per source and keeps upstream load and latency low.
func (s *Server) cachedAvailability(source *SiteSource, items []map[string]any) availResult {
	entryOf := func(e availCacheEntry) availResult {
		return availResult{Available: e.available, Playable: e.playable,
			Downloadable: e.downloadable, Reason: e.reason, Metrics: e.metrics}
	}
	key := availCacheKey(source)
	availCacheMu.Lock()
	if e, ok := availCache[key]; ok && time.Now().Before(e.expires) {
		availCacheMu.Unlock()
		return entryOf(e)
	}
	availCacheMu.Unlock()

	// 内存未命中：先查磁盘持久化结果（跨重启复用，避免重启后首搜逐个重新探测）
	if e, ok := s.loadAvailCache(key); ok {
		availCacheMu.Lock()
		availCache[key] = e
		availCacheMu.Unlock()
		return entryOf(e)
	}

	res := s.checkSourceAvailability(source, items)
	ttl := availabilitySuccessTTL
	if !res.Available {
		ttl = availabilityFailureTTL
	}
	entry := availCacheEntry{
		available:    res.Available,
		playable:     res.Playable,
		downloadable: res.Downloadable,
		reason:       res.Reason,
		metrics:      res.Metrics,
		expires:      time.Now().Add(ttl),
	}
	availCacheMu.Lock()
	availCache[key] = entry
	availCacheMu.Unlock()
	// 写回磁盘，供重启后复用
	s.storeAvailCache(key, entry)
	return res
}

// cleanupAvailCacheDB 清理磁盘缓存中已过期的条目（内存缓存由 init 的
// ticker 负责）。由 dbCleanupLoop 周期性调用。
func (s *Server) cleanupAvailCacheDB() {
	cutoff := time.Now().Format(time.RFC3339)
	if _, err := s.db.Exec("DELETE FROM avail_cache WHERE expires_at < ?", cutoff); err != nil {
		logWarn("avail: cleanup cache failed: %v", err)
	}
}
