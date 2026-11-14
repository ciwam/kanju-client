package backend

// tmdb_poster.go — 按片名在 TMDB 检索海报（豆瓣图床全挂时的替代图源）。
// 与榜单兜底独立：这里不需要 key 也可尝试？——TMDB API 必须 key，无 key
// 则此通道不可用，返回 false 由调用方回退到站源检索（findPosterByTitle）。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// tmdbPosterByTitle 按片名搜 TMDB 返回 w300 海报直链。multi 搜索同时
// 覆盖电影与剧集，取第一个有海报的结果。
func tmdbPosterByTitle(title string) (string, bool) {
	if !tmdbEnabled() || title == "" {
		return "", false
	}
	u := "https://api.tmdb.org/3/search/multi?api_key=" + tmdbAPIKey +
		"&language=zh-CN&query=" + url.QueryEscape(title)
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := safeHTTPClient(5 * time.Second).Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", false
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxFetchBodyBytes))
	var obj struct {
		Results []struct {
			PosterPath string `json:"poster_path"`
			Title      string `json:"title"`
			Name       string `json:"name"`
		} `json:"results"`
	}
	if json.Unmarshal(b, &obj) != nil {
		return "", false
	}
	want := strings.ToLower(title)
	for _, v := range obj.Results {
		if v.PosterPath == "" {
			continue
		}
		// 标题对齐：搜索结果与请求片名一致（或请求词以结果开头，容忍
		// 副标题截断）才采用，避免同名异片。
		t := strings.ToLower(v.Title)
		if t == "" {
			t = strings.ToLower(v.Name)
		}
		if t == want || strings.HasPrefix(t, want) || strings.HasPrefix(want, t) {
			return "https://image.tmdb.org/t/p/w300" + v.PosterPath, true
		}
	}
	return "", false
}

// tmdbPosterCache 按片名缓存检索结果（含失败，避免风控期间反复打 TMDB）。
var tmdbPosterCache = struct {
	m map[string]tmdbPosterEntry
}{m: map[string]tmdbPosterEntry{}}

type tmdbPosterEntry struct {
	url  string
	exp  time.Time
	done bool // true=已检索过且无结果，缓存空值
}

func cachedTMDBPoster(title string) (string, bool) {
	e, ok := tmdbPosterCache.m[title]
	if ok && time.Now().Before(e.exp) {
		if e.done {
			return "", false
		}
		return e.url, true
	}
	if ok {
		delete(tmdbPosterCache.m, title)
	}
	return "", false
}

func putTMDBPosterCache(title, poster string) {
	u := strings.TrimSpace(poster)
	if len(tmdbPosterCache.m) > 500 {
		tmdbPosterCache.m = map[string]tmdbPosterEntry{}
	}
	tmdbPosterCache.m[title] = tmdbPosterEntry{
		url:  u,
		exp:  time.Now().Add(6 * time.Hour),
		done: u == "",
	}
}
