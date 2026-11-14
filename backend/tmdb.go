package backend

// tmdb.go — TMDB（The Movie Database）第二数据源。
//
// 定位：豆瓣被域名级风控（j/search_subjects 与 rexxar 同时不可用）时的
// 最终兜底。数据与豆瓣评分体系不同（TMDB 10 分制），响应带 source=tmdb
// 标记，客户端弹窗提示用户。
//
// key 配置：环境变量 TMDB_API_KEY > 启动参数 -tmdb-key。未配置则本通道
// 禁用（fallback 链跳过）。
// 域名：api.tmdb.org（国内直连可用；api.themoviedb.org 常被墙）。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	tmdbAPIKey     string // 启动时注入（环境变量/参数），空则 TMDB 通道禁用
	tmdbAPIKeyOnce sync.Once
)

// SetTMDBKey 由 cmd/kanju 的启动参数或环境变量调用。
func SetTMDBKey(key string) {
	tmdbAPIKey = strings.TrimSpace(key)
}

// tmdbEnabled 报告 TMDB 通道是否可用。
func tmdbEnabled() bool { return tmdbAPIKey != "" }

// tmdbCollection 映射豆瓣 tag → TMDB discover 参数。
// TMDB 用 discover 接口按条件检索，比 j/search_subjects 的 tag 更灵活。
func tmdbDiscover(dType, tag string) (path string, query url.Values) {
	isTV := dType == "tv"
	path = "/3/discover/tv"
	if !isTV {
		path = "/3/discover/movie"
	}
	q := url.Values{}
	q.Set("language", "zh-CN")
	q.Set("sort_by", "popularity.desc")
	q.Set("include_adult", "false")

	switch tag {
	case "国产剧", "华语":
		q.Set("with_origin_country", "CN")
	case "美剧", "欧美":
		q.Set("with_origin_country", "US")
	case "日剧", "日本":
		q.Set("with_origin_country", "JP")
	case "韩剧", "韩国":
		q.Set("with_origin_country", "KR")
	case "动画", "日本动画", "动漫":
		// 动画：TV 用 genre 16（Animation），电影同样。
		q.Set("with_genres", "16")
		if !isTV {
			q.Set("with_origin_country", "JP")
		}
	case "纪录片":
		q.Set("with_genres", "99")
	case "综艺":
		q.Set("with_genres", "10767") // Talk + Reality 组合近似
	case "最新":
		// 最新：按日期倒排。
		q.Set("sort_by", "primary_release_date.desc")
		if isTV {
			q.Set("sort_by", "first_air_date.desc")
		}
	case "豆瓣高分", "经典", "冷门佳片":
		// 高分：按评分倒排 + 最低票数（TMDB 无豆瓣评分体系）。
		q.Set("sort_by", "vote_average.desc")
		q.Set("vote_count.gte", "100")
	}
	return path, q
}

// tmdbList 从 TMDB 拉取榜单，字段与豆瓣卡片对齐
// （title/cover/url/rate/episodesInfo）。ok=false 表示未配置 key 或请求失败。
//
// TMDB 每页固定 20 条，而客户端请求的 limit/start 是条数偏移；一次
// 请求可能横跨多页（如 start=30&limit=30 → 第 2、3 页各取一部分），
// 这里自动拼接补齐。结果不足 limit 说明榜单真的到头了。
func tmdbList(dType, tag, limit, start string) ([]map[string]any, bool) {
	if !tmdbEnabled() {
		return nil, false
	}
	// 热门 tag 走 trending 接口（全局趋势）。
	trendPath := ""
	if tag == "tvtrend" {
		trendPath = "/3/trending/tv/week"
	} else if tag == "movietrend" {
		trendPath = "/3/trending/movie/week"
	}
	if trendPath != "" {
		return tmdbTrending(trendPath, dType, limit, start)
	}
	path, q := tmdbDiscover(dType, tag)
	s, _ := strconv.Atoi(start)
	if s < 0 {
		s = 0
	}
	n, _ := strconv.Atoi(limit)
	if n <= 0 || n > 60 {
		n = 20
	}
	// 条数偏移 → TMDB 页区间（每页 20）。
	firstPage := s/20 + 1
	lastPage := (s + n + 19) / 20
	if lastPage-firstPage > 4 {
		lastPage = firstPage + 4 // 防御：最多拉 5 页
	}

	var list []map[string]any
	for page := firstPage; page <= lastPage && len(list) < n; page++ {
		items, ok := tmdbDiscoverPage(path, q, page, dType)
		if !ok {
			break // 上游失败：返回已拿到的部分
		}
		if len(items) == 0 {
			break // 榜单到头
		}
		list = append(list, items...)
	}
	// 裁掉 firstPage 里 start 之前的部分，超出 limit 的尾部。
	if skip := s - (firstPage-1)*20; skip > 0 && len(list) > skip {
		list = list[skip:]
	}
	if len(list) > n {
		list = list[:n]
	}
	logPrint("tmdb list: tag=%q start=%d %d items", tag, s, len(list))
	return list, len(list) > 0
}

// tmdbDiscoverPage 拉一页 discover 结果，解析成与豆瓣卡片对齐的条目。
func tmdbDiscoverPage(path string, q url.Values, page int, dType string) ([]map[string]any, bool) {
	qq := url.Values{}
	for k, v := range q {
		qq[k] = v
	}
	qq.Set("page", strconv.Itoa(page))
	u := "https://api.tmdb.org" + path + "?api_key=" + tmdbAPIKey + "&" + qq.Encode()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := safeHTTPClient(8 * time.Second).Do(req)
	if err != nil {
		logPrint("tmdb discover page %d: %v", page, err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		logPrint("tmdb discover page %d: status %d", page, resp.StatusCode)
		return nil, false
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxFetchBodyBytes))
	var obj struct {
		Results []struct {
			ID           int     `json:"id"`
			Title        string  `json:"title"`
			Name         string  `json:"name"`
			PosterPath   string  `json:"poster_path"`
			VoteAverage  float64 `json:"vote_average"`
			FirstAirDate string  `json:"first_air_date"`
			ReleaseDate  string  `json:"release_date"`
		} `json:"results"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		logPrint("tmdb discover page %d: 解析失败 %v", page, err)
		return nil, false
	}
	mediaType := "tv"
	if dType != "tv" {
		mediaType = "movie"
	}
	list := []map[string]any{}
	for _, v := range obj.Results {
		title := v.Title
		if title == "" {
			title = v.Name
		}
		if title == "" || v.PosterPath == "" {
			continue
		}
		list = append(list, map[string]any{
			"title": title,
			"cover": "https://image.tmdb.org/t/p/w500" + v.PosterPath,
			"url":   fmt.Sprintf("https://www.themoviedb.org/%s/%d", mediaType, v.ID),
			"rate":  strconv.FormatFloat(v.VoteAverage, 'f', 1, 64),
		})
	}
	return list, true
}

// tmdbTrending 拉取 TMDB 周趋势榜单（tvtrend/movietrend tag）。
func tmdbTrending(path, dType, limit, start string) ([]map[string]any, bool) {
	s, _ := strconv.Atoi(start)
	if s < 0 {
		s = 0
	}
	n, _ := strconv.Atoi(limit)
	if n <= 0 || n > 60 {
		n = 20
	}
	firstPage := s/20 + 1
	lastPage := (s + n + 19) / 20
	if lastPage-firstPage > 4 {
		lastPage = firstPage + 4
	}

	var list []map[string]any
	for page := firstPage; page <= lastPage && len(list) < n+20; page++ {
		items, ok := tmdbTrendingPage(path, page, dType)
		if !ok {
			break
		}
		if len(items) == 0 {
			break
		}
		list = append(list, items...)
	}
	if skip := s - (firstPage-1)*20; skip > 0 && len(list) > skip {
		list = list[skip:]
	}
	if len(list) > n {
		list = list[:n]
	}
	logPrint("tmdb trending: %s start=%d %d items", path, s, len(list))
	return list, len(list) > 0
}

// tmdbTrendingPage 拉一页周趋势结果。
func tmdbTrendingPage(path string, page int, dType string) ([]map[string]any, bool) {
	u := "https://api.tmdb.org" + path + "?api_key=" + tmdbAPIKey + "&language=zh-CN&page=" + strconv.Itoa(page)
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := safeHTTPClient(8 * time.Second).Do(req)
	if err != nil {
		logPrint("tmdb trending page %d: %v", page, err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		logPrint("tmdb trending page %d: status %d", page, resp.StatusCode)
		return nil, false
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxFetchBodyBytes))
	var obj struct {
		Results []struct {
			ID          int     `json:"id"`
			Title       string  `json:"title"`
			Name        string  `json:"name"`
			PosterPath  string  `json:"poster_path"`
			VoteAverage float64 `json:"vote_average"`
		} `json:"results"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, false
	}
	mediaType := "tv"
	if dType != "tv" {
		mediaType = "movie"
	}
	list := []map[string]any{}
	for _, v := range obj.Results {
		title := v.Title
		if title == "" {
			title = v.Name
		}
		if title == "" || v.PosterPath == "" {
			continue
		}
		rate := ""
		if v.VoteAverage > 0 {
			rate = strconv.FormatFloat(v.VoteAverage, 'f', 1, 64)
		}
		list = append(list, map[string]any{
			"title": title,
			"cover": "https://image.tmdb.org/t/p/w500" + v.PosterPath,
			"url":   fmt.Sprintf("https://www.themoviedb.org/%s/%d", mediaType, v.ID),
			"rate":  rate,
		})
	}
	return list, true
}
