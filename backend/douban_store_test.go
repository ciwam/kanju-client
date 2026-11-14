package backend

import (
	"strings"
	"testing"
	"time"
)

// TestDoubanStoreRoundTrip SQLite 持久缓存读写与过期。
func TestDoubanStoreRoundTrip(t *testing.T) {
	s := newTestServer(t)
	key := "tv|热门||30|0"
	if _, ok := s.doubanStoreGet(key); ok {
		t.Fatal("空库不应命中")
	}
	s.doubanStorePut(key, []map[string]any{{"title": "测试", "cover": "https://x/1.jpg"}})
	got, ok := s.doubanStoreGet(key)
	if !ok || len(got) != 1 || got[0]["title"] != "测试" {
		t.Fatalf("写入后应命中: ok=%v got=%v", ok, got)
	}
	// 新鲜读：刚写入必命中。
	if _, ok := s.doubanStoreGetFresh(key); !ok {
		t.Fatal("新鲜读应命中")
	}
	// 把 fetched_at 改旧，fresh 读应失效，stale keys 应包含它。
	cutoff := time.Now().Add(-4 * time.Hour).Format(time.RFC3339)
	if _, err := s.db.Exec("UPDATE douban_list SET fetched_at = ? WHERE cache_key = ?", cutoff, key); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.doubanStoreGetFresh(key); ok {
		t.Fatal("超过 3h 的记录 fresh 读不应命中")
	}
	if _, ok := s.doubanStoreGet(key); !ok {
		t.Fatal("stale-while-revalidate：普通读仍应命中旧数据")
	}
	keys := s.doubanStoreStaleKeys()
	found := false
	for _, k := range keys {
		if k == key {
			found = true
		}
	}
	if !found {
		t.Fatalf("stale keys 应含 %q: %v", key, keys)
	}
}

// TestParseWarmKey cache_key 五段解析（tag 内无 |，防御式校验）。
func TestParseWarmKey(t *testing.T) {
	var typ, tag, sort, limit, start string
	if !parseWarmKey("movie|热门||30|0", &typ, &tag, &sort, &limit, &start) {
		t.Fatal("正常 key 应解析成功")
	}
	if typ != "movie" || tag != "热门" || sort != "" || limit != "30" || start != "0" {
		t.Fatalf("解析结果不对: %q %q %q %q %q", typ, tag, sort, limit, start)
	}
	if parseWarmKey("bad", &typ, &tag, &sort, &limit, &start) {
		t.Fatal("畸形 key 应拒绝")
	}
	if parseWarmKey("a|b|c|d|e|f", &typ, &tag, &sort, &limit, &start) {
		t.Fatal("超段 key 应拒绝")
	}
}

// TestWarmCategories 客户端分类表与后端预热集合对齐：TMDB 编码 tag 必须是
// g{数字} 或 *trend 形式，且 movie 与 tv 都有热门分类。
func TestWarmCategories(t *testing.T) {
	hasTV, hasMovie := false, false
	for _, c := range warmCategories {
		switch c.typ {
		case "tv":
			hasTV = true
		case "movie":
			hasMovie = true
		default:
			t.Fatalf("非法分类类型 %q", c.typ)
		}
		if strings.HasPrefix(c.tag, "g") && c.tag != "g" {
			for _, r := range c.tag[1:] {
				if r < '0' || r > '9' {
					t.Fatalf("TMDB 编码 tag %q 含非数字", c.tag)
				}
			}
		}
	}
	if !hasTV || !hasMovie {
		t.Fatalf("预热集合应覆盖 tv 与 movie（tv=%v movie=%v）", hasTV, hasMovie)
	}
}
