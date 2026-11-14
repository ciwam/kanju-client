package backend

import (
	"sync"
	"time"
)

// playURLCache 缓存播放地址解析结果：同一选集反复打开（暂停续播、切换线路、
// 从历史页再次进入）时，无需每次重新抓取上游播放页做正则解析，可大幅减少
// 对上游的重复请求。仅缓存成功结果，失败不缓存（上游可能瞬时抖动）。
var playURLCache = newPlayCache()

const (
	playCacheTTL     = 5 * time.Minute
	playCacheMaxSize = 500
)

type playCacheEntry struct {
	res map[string]any
	exp time.Time
}

type playCache struct {
	mu sync.Mutex
	m  map[string]playCacheEntry
}

func newPlayCache() *playCache {
	c := &playCache{m: map[string]playCacheEntry{}}
	go c.cleanupLoop()
	return c
}

func (c *playCache) get(key string) (map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || time.Now().After(e.exp) {
		if ok {
			delete(c.m, key)
		}
		return nil, false
	}
	return e.res, true
}

func (c *playCache) put(key string, res map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.m[key]; !exists && len(c.m) >= playCacheMaxSize {
		// 达到上限时逐出一条最旧缓存（map 迭代顺序随机，效果等同 LRU 近似）
		for k := range c.m {
			delete(c.m, k)
			break
		}
	}
	c.m[key] = playCacheEntry{res: res, exp: time.Now().Add(playCacheTTL)}
}

// cleanupLoop 定期清理过期条目，防止内存无界增长。
func (c *playCache) cleanupLoop() {
	t := time.NewTicker(1 * time.Minute)
	defer t.Stop()
	for range t.C {
		c.mu.Lock()
		now := time.Now()
		for k, e := range c.m {
			if now.After(e.exp) {
				delete(c.m, k)
			}
		}
		c.mu.Unlock()
	}
}
