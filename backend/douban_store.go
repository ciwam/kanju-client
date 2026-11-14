package backend

// douban_store.go — 豆瓣/TMDB 榜单的 SQLite 持久缓存。
//
// 设计：应用首次启动后由预热循环（douban_warm.go）把全部分类数据拉进
// douban_list 表；此后客户端首页请求直接命中数据库（毫秒级，不再等
// 上游豆瓣/TMDB）。刷新由预热循环按 TTL 巡检完成，不在请求路径上。
//
// 行结构：一分类一页数据一行（key 含 limit/start，与请求 cacheKey 一致），
// items 存 JSON。cover 字段暂不冗余（双图床降级在图片代理层做）。

import (
	"database/sql"
	"encoding/json"
	"time"
)

// doubanListStoreTTL 持久缓存被视为「新鲜」的时长：之内不向上游刷新
// （3 小时内重复打开应用不重复请求豆瓣）。之外预热循环会刷新，但请求
// 路径上仍先返回旧数据（stale-while-revalidate，保证离线/风控时可用）。
const doubanListStoreTTL = 3 * time.Hour

func (s *Server) doubanStoreGet(key string) ([]map[string]any, bool) {
	var itemsJSON string
	var fetchedAt sql.NullString
	err := s.db.QueryRow(
		"SELECT items, fetched_at FROM douban_list WHERE cache_key = ?", key,
	).Scan(&itemsJSON, &fetchedAt)
	if err != nil {
		return nil, false
	}
	var items []map[string]any
	if json.Unmarshal([]byte(itemsJSON), &items) != nil || len(items) == 0 {
		return nil, false
	}
	_ = fetchedAt // 新鲜度由预热循环管理；查询路径只要数据在就返回
	return items, true
}

func (s *Server) doubanStoreGetFresh(key string) ([]map[string]any, bool) {
	var itemsJSON string
	var fetchedAt sql.NullString
	err := s.db.QueryRow(
		"SELECT items, fetched_at FROM douban_list WHERE cache_key = ?", key,
	).Scan(&itemsJSON, &fetchedAt)
	if err != nil {
		return nil, false
	}
	t, err := time.Parse(time.RFC3339, fetchedAt.String)
	if err != nil || time.Since(t) > doubanListStoreTTL {
		return nil, false
	}
	var items []map[string]any
	if json.Unmarshal([]byte(itemsJSON), &items) != nil || len(items) == 0 {
		return nil, false
	}
	return items, true
}

func (s *Server) doubanStorePut(key string, items []map[string]any) {
	b, err := json.Marshal(items)
	if err != nil || len(items) == 0 {
		return
	}
	_, err = s.db.Exec(
		`INSERT INTO douban_list (cache_key, items, fetched_at) VALUES (?, ?, ?)
		 ON CONFLICT(cache_key) DO UPDATE SET items = excluded.items, fetched_at = excluded.fetched_at`,
		key, string(b), time.Now().Format(time.RFC3339))
	if err != nil {
		logWarn("douban store put %q: %v", key, err)
	}
}

// doubanStoreStaleKeys 返回超过 TTL 未刷新的 cache_key（预热循环巡检用）。
func (s *Server) doubanStoreStaleKeys() []string {
	cutoff := time.Now().Add(-doubanListStoreTTL).Format(time.RFC3339)
	rows, err := s.db.Query(
		"SELECT cache_key FROM douban_list WHERE fetched_at < ? ORDER BY fetched_at", cutoff)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			keys = append(keys, k)
		}
	}
	return keys
}
