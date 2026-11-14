package backend

import (
	"testing"
	"time"
)

// TestDoubanCircuitTrip 熔断器：达阈值跳闸，窗口过半放行探测。
func TestDoubanCircuitTrip(t *testing.T) {
	c := newDoubanCircuit(3, 4*time.Second)
	// 未跳闸前允许。
	if ok, _ := c.Allow(); !ok {
		t.Fatal("初始应允许")
	}
	// 两次失败未达阈值。
	c.RecordFailure()
	c.RecordFailure()
	if ok, _ := c.Allow(); !ok {
		t.Fatal("未达阈值不应跳闸")
	}
	// 第三次失败跳闸。
	c.RecordFailure()
	if ok, _ := c.Allow(); ok {
		t.Fatal("达阈值应跳闸")
	}
	// 成功记录复位。
	c.RecordSuccess()
	if ok, _ := c.Allow(); !ok {
		t.Fatal("成功后应复位")
	}
}

// TestDoubanInflightMerge 并发同 key 请求合并为一次调用。
func TestDoubanInflightMerge(t *testing.T) {
	var g inflight
	calls := 0
	fn := func() ([]map[string]any, error) {
		calls++
		time.Sleep(50 * time.Millisecond) // 模拟上游耗时
		return []map[string]any{{"k": "v"}}, nil
	}
	const n = 5
	results := make(chan int, n)
	for i := 0; i < n; i++ {
		go func() {
			r, err := g.Do("same-key", fn)
			if err != nil {
				results <- 0
				return
			}
			results <- len(r)
		}()
	}
	for i := 0; i < n; i++ {
		if got := <-results; got != 1 {
			t.Fatal("结果应为 1 条")
		}
	}
	if calls != 1 {
		t.Fatalf("并发 5 个请求应只执行 1 次上游调用，实际 %d", calls)
	}
}
