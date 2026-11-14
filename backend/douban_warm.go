package backend

// douban_warm.go — 榜单预 warming：首次启动后台把全部分类数据拉进
// SQLite（douban_list 表）；此后客户端首页请求毫秒级命中数据库。
//
// 刷新策略（用户要求 + 补齐）：
//   - 启动后 30 秒首次巡检：只刷新超过 3 小时未更新的分类；
//     3 小时内重复打开应用不重复请求豆瓣。
//   - 之后每 30 分钟巡检一次：把超过 TTL 的分类拉新。
//   - 刷新串行 + 单飞合并（fetchDoubanList 自带），对上游压力恒定。
//   - 分类集合 = 客户端豆瓣体系全部分类 + TMDB 体系全部分类
//     （切到 TMDB 优先时同样秒开）。

import (
	"sync"
	"time"
)

// 首页分类全集（与客户端 app/home.go 的分类表对齐；客户端改分类
// 表时这里同步）。
type warmCategory struct {
	typ  string // tv | movie
	tag  string
	sort string
}

var warmCategories = buildWarmCategories()

func buildWarmCategories() []warmCategory {
	var cs []warmCategory
	// 豆瓣体系（movieTabs / tvTabs）。
	doubanTV := []warmCategory{
		{"tv", "热门", ""}, {"tv", "国产剧", ""}, {"tv", "美剧", ""},
		{"tv", "日剧", ""}, {"tv", "韩剧", ""}, {"tv", "动漫", ""},
		{"tv", "纪录片", ""},
		{"tv", "综艺", ""}, {"tv", "综艺", "time"},
	}
	doubanMovie := []warmCategory{
		{"movie", "热门", ""}, {"movie", "华语", ""}, {"movie", "欧美", ""},
		{"movie", "韩国", ""}, {"movie", "日本", ""},
		{"movie", "最新", ""}, {"movie", "豆瓣高分", ""}, {"movie", "冷门佳片", ""},
	}
	cs = append(cs, doubanTV...)
	cs = append(cs, doubanMovie...)
	// TMDB 体系（tmdbTVTabs / tmdbMovieTabs；编码 g{id} / trend tag）。
	tmdbTV := []warmCategory{
		{"tv", "tvtrend", ""}, {"tv", "g16", ""}, {"tv", "g35", ""},
		{"tv", "g80", ""}, {"tv", "g99", ""}, {"tv", "g10765", ""},
		{"tv", "g18", ""},
	}
	tmdbMovie := []warmCategory{
		{"movie", "movietrend", ""}, {"movie", "g28", ""}, {"movie", "g35", ""},
		{"movie", "g878", ""}, {"movie", "g53", ""}, {"movie", "g16", ""},
	}
	cs = append(cs, tmdbTV...)
	cs = append(cs, tmdbMovie...)
	return cs
}

const (
	// warmStartDelay 启动后延迟首次巡检（用户要求 30s：先让应用界面
	// 起来，再在后台静默预热）。
	warmStartDelay = 30 * time.Second
	// warmInterval 巡检间隔。
	warmInterval = 30 * time.Minute
	// warmLimit 每分类拉取条数（与客户端首屏 pageLimit 一致）。
	warmLimit = "30"
)

// startDoubanWarmLoop 启动预热/巡检 goroutine（bind 成功后调用一次）。
func (s *Server) startDoubanWarmLoop() {
	go func() {
		time.Sleep(warmStartDelay)
		s.warmPass()
		t := time.NewTicker(warmInterval)
		defer t.Stop()
		for range t.C {
			s.warmPass()
		}
	}()
}

// warmPass 一轮巡检：预热缺失的分类，刷新超过 TTL 的分类。
// 全部串行：豆瓣对并发敏感，一轮最坏 20 个分类 × ~1s ≈ 20s，30 分钟
// 巡检间隔完全放得下。
func (s *Server) warmPass() {
	// 1) 预热：库中没有的分类（首次启动）。
	for _, c := range warmCategories {
		key := c.typ + "|" + c.tag + "|" + c.sort + "|" + warmLimit + "|0"
		if _, ok := s.doubanStoreGet(key); ok {
			continue
		}
		s.refreshDoubanCategory(c.typ, c.tag, c.sort, warmLimit, "0", "")
	}
	// 2) 刷新：超过 3 小时未更新的分类。
	for _, key := range s.doubanStoreStaleKeys() {
		var typ, tag, sort, limit, start string
		if !parseWarmKey(key, &typ, &tag, &sort, &limit, &start) {
			continue
		}
		s.refreshDoubanCategory(typ, tag, sort, limit, start, "")
	}
}

// parseWarmKey 拆 cache_key（typ|tag|sort|limit|start；tag/sort 可能含 |）。
func parseWarmKey(key string, typ, tag, sort, limit, start *string) bool {
	// tag 不会包含 |（豆瓣 tag 是中文，TMDB tag 是 g{id}/trend），
	// 但防御式解析：固定 5 段才接受。
	n := 0
	var parts [5]string
	for _, r := range key {
		if r == '|' {
			n++
			if n >= 5 {
				return false
			}
			continue
		}
		parts[n] += string(r)
	}
	if n != 4 {
		return false
	}
	*typ, *tag, *sort, *limit, *start = parts[0], parts[1], parts[2], parts[3], parts[4]
	return true
}

// warmOnce 同一时刻只跑一轮巡检（startDoubanWarmLoop 是唯一入口，但
// 测试/未来多处触发时防重入）。
var warmOnce sync.Mutex

// refreshDoubanCategory 拉一分类数据并写入两级缓存。返回是否拿到数据。
func (s *Server) refreshDoubanCategory(dType, tag, sort, limit, start, prefer string) bool {
	warmOnce.Lock()
	defer warmOnce.Unlock()
	res := fetchDoubanListDirect(dType, tag, sort, limit, start)
	if len(res.list) == 0 {
		logInfo("douban warm: %s/%s 拉取失败", dType, tag)
		return false
	}
	key := dType + "|" + tag + "|" + sort + "|" + limit + "|" + start
	doubanListCachePut(key, res.list)
	s.doubanStorePut(key, res.list)
	logInfo("douban warm: %s/%s %d 条 source=%q", dType, tag, len(res.list), res.source)
	return true
}
