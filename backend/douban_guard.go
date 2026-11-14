package backend

// douban_guard.go — 豆瓣上游保护：熔断器 + 请求合并 + 长 TTL 缓存。
//
// 背景：豆瓣 j/search_subjects 对高频抓取做域名级封锁（本机实测：
// movie.douban.com 整域 10s+ 无响应，m.douban.com 独立限流正常）。
// 三道防线：
//  1. 熔断器：连续失败达阈值后「跳闸」，一段时间内所有请求直接走
//     rexxar 备用通道，不再试探主通道加剧封禁；半开状态放行一个
//     探测请求，成功则恢复。
//  2. 请求合并：同 key 的并发请求只发一次上游，其余等待共享结果。
//  3. 长 TTL：榜单内容变化慢（小时级），缓存 15 分钟足够新鲜。

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// doubanCircuit 豆瓣主通道熔断器（进程级单例）。
type doubanCircuit struct {
	mu        sync.Mutex
	failures  int           // 连续失败次数
	threshold int           // 跳闸阈值
	openFor   time.Duration // 跳闸后熔断打开时长
	openUntil time.Time     // 熔断打开到此时刻
	probing   bool          // 半开探测进行中（同一时刻一个）
}

func newDoubanCircuit(threshold int, openFor time.Duration) *doubanCircuit {
	return &doubanCircuit{threshold: threshold, openFor: openFor}
}

// Allow 报告主通道是否可用：关闭（正常）或半开（放行一次探测）。
func (c *doubanCircuit) Allow() (allowed bool, probing bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.openUntil) {
		// 熔断打开中：窗口过半后放行一次探测。
		if time.Since(c.openUntil) > c.openFor/2 && !c.probingSwap() {
			return true, true
		}
		return false, false
	}
	return true, false
}

// probingSwap 原子地占用探测名额（同一时刻只放一个探测请求）。
func (c *doubanCircuit) probingSwap() bool {
	if c.probing {
		return true // 已有人在探测，继续走备用
	}
	c.probing = true
	return false
}

// RecordSuccess 记录主通道成功：复位熔断。
func (c *doubanCircuit) RecordSuccess() {
	c.mu.Lock()
	c.failures = 0
	c.openUntil = time.Time{}
	c.probing = false
	c.mu.Unlock()
}

// RecordFailure 记录主通道失败：达阈值跳闸。
func (c *doubanCircuit) RecordFailure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.probing = false
	c.failures++
	if c.failures >= c.threshold {
		c.openUntil = time.Now().Add(c.openFor)
		slog.Info(fmt.Sprintf("douban circuit OPEN: %d 次连续失败，%v 内主通道停用", c.failures, c.openFor))
	}
}

// inflight 请求合并：同 key 并发请求共享一次上游调用。
type inflight struct {
	mu    sync.Mutex
	calls map[string]*inflightCall
}

type inflightCall struct {
	done chan struct{}
	list []map[string]any
	err  error
}

// Do 合并同 key 的并发调用：第一个执行 fn，其余等结果。
func (g *inflight) Do(key string, fn func() ([]map[string]any, error)) ([]map[string]any, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = map[string]*inflightCall{}
	}
	if c, ok := g.calls[key]; ok {
		g.mu.Unlock()
		<-c.done
		return c.list, c.err
	}
	c := &inflightCall{done: make(chan struct{})}
	g.calls[key] = c
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		delete(g.calls, key)
		g.mu.Unlock()
		close(c.done)
	}()

	c.list, c.err = fn()
	return c.list, c.err
}
