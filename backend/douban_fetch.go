package backend

// douban_fetch.go — 豆瓣榜单的上游抓取链路（供 HTTP 处理器与预热循环共用）。
//
// 链路：豆瓣 j/search_subjects → rexxar（m.douban.com）→ TMDB。
// 三道防护全部在这层生效：
//  1. 熔断器：连续 3 次失败跳闸 5 分钟，期间直接跳过主通道；
//  2. 请求合并：同 key 并发请求共享一次上游调用；
//  3. 结果由调用方决定进内存缓存 / SQLite 持久缓存。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"
)

// fetchResult 上游抓取结果：list 为空且 source 为空表示全链路失败。
type fetchResult struct {
	list   []map[string]any
	source string // ""=豆瓣, "rexxar", "tmdb"
}

// fetchDoubanList 抓一页榜单（带熔断 + 请求合并）。返回的 list 一定非 nil。
func fetchDoubanList(dType, tag, sort, limit, start string) []map[string]any {
	key := dType + "|" + tag + "|" + sort + "|" + limit + "|" + start
	merged, err := doubanMerge.Do(key, func() ([]map[string]any, error) {
		return fetchDoubanListDirect(dType, tag, sort, limit, start).list, nil
	})
	if err != nil || merged == nil {
		return []map[string]any{}
	}
	return merged
}

// fetchDoubanListDirect 顺序尝试各通道；不再返回 error——每级失败自然
// 降级到下一级，全失败返回空列表。
func fetchDoubanListDirect(dType, tag, sort, limit, start string) fetchResult {
	// 熔断器放行时走主通道。
	if allowed, _ := doubanBreaker.Allow(); allowed {
		res := fetchDoubanMain(dType, tag, sort, limit, start)
		if len(res.list) > 0 {
			doubanBreaker.RecordSuccess()
			return res
		}
		doubanBreaker.RecordFailure()
	}
	// rexxar 备用通道（m.douban.com 独立限流）。
	if list, ok := doubanRexxarList(dType, tag, limit, start); ok {
		return fetchResult{list: list, source: "rexxar"}
	}
	// TMDB 最终兜底（需已配置 key）。
	if list, ok := tmdbList(dType, tag, limit, start); ok {
		return fetchResult{list: list, source: "tmdb"}
	}
	return fetchResult{list: []map[string]any{}}
}

// fetchDoubanMain 豆瓣主通道（j/search_subjects）。
func fetchDoubanMain(dType, tag, sort, limit, start string) fetchResult {
	u := "https://movie.douban.com/j/search_subjects?type=" + url.QueryEscape(dType) +
		"&tag=" + url.QueryEscape(tag) + "&page_limit=" + limit + "&page_start=" + start
	if sort != "" {
		u += "&sort=" + url.QueryEscape(sort)
	}
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://movie.douban.com/")
	resp, err := safeHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		logInfo("douban main: %v", err)
		return fetchResult{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		logInfo("douban main: status %d", resp.StatusCode)
		return fetchResult{}
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxFetchBodyBytes))
	var obj struct {
		Subjects []map[string]any `json:"subjects"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		logInfo("douban main: 解析失败 %v", err)
		return fetchResult{}
	}
	list := []map[string]any{}
	for _, v := range obj.Subjects {
		list = append(list, map[string]any{
			"title":        getMapStr(v, "title"),
			"cover":        getMapStr(v, "cover"),
			"url":          getMapStr(v, "url"),
			"rate":         getMapStr(v, "rate"),
			"episodesInfo": getMapStr(v, "episodes_info"),
		})
	}
	return fetchResult{list: list}
}
