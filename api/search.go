package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ---- 搜索 SSE ----

// SearchMeta 是搜索开始时的元信息。
type SearchMeta struct {
	Total   int    `json:"total"`
	Keyword string `json:"keyword"`
}

// SearchItem 是一个搜索结果。
type SearchItem struct {
	Title      string `json:"title"`
	Cover      string `json:"cover"`
	URL        string `json:"url"`
	SourceName string `json:"sourceName"`
}

// SearchSource 是一个站源的完成事件。
type SearchSource struct {
	SourceName  string       `json:"sourceName"`
	Dur         int64        `json:"dur"`
	Matched     string       `json:"matched"`
	Items       []SearchItem `json:"items"`
	Error       string       `json:"error"`
	Unavailable bool         `json:"unavailable"`
	Playable    bool         `json:"playable"`
	Reason      string       `json:"reason"`
	SpeedKbps   float64      `json:"speedKbps"`
	TTFBMs      int64        `json:"ttfbMs"`
}

// SearchStream 发起 SSE 搜索并阻塞消费到 done。回调都在调用 goroutine 上执行。
// stop 通道关闭时中断连接。
func (c *Client) SearchStream(keyword, fallback string, stop <-chan struct{},
	onMeta func(SearchMeta), onSource func(SearchSource), onDone func()) error {
	q := url.Values{}
	q.Set("keyword", keyword)
	if fallback != "" {
		q.Set("fallback", fallback)
	}
	req, err := c.newRequest(http.MethodGet, "/api/search/stream?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	// SSE 不走全局超时。
	hc := &http.Client{Timeout: 0}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 限流等业务失败会以普通 JSON 返回。
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return &Err{Status: resp.StatusCode, Msg: firstString(raw, fmt.Sprintf("搜索失败 (HTTP %d)", resp.StatusCode))}
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return &Err{Status: resp.StatusCode, Msg: firstString(raw, "搜索失败")}
	}

	stopped := false
	go func() {
		select {
		case <-stop:
			stopped = true
			resp.Body.Close()
		case <-time.After(2 * time.Minute):
			resp.Body.Close()
		}
	}()

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	event := ""
	for sc.Scan() {
		if stopped {
			return nil
		}
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, ":"):
			// 心跳注释
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			switch event {
			case "meta":
				if onMeta != nil {
					var m SearchMeta
					if json.Unmarshal([]byte(data), &m) == nil {
						onMeta(m)
					}
				}
			case "search":
				if onSource != nil {
					var s SearchSource
					if json.Unmarshal([]byte(data), &s) == nil {
						onSource(s)
					}
				}
			case "done":
				if onDone != nil {
					onDone()
				}
				return nil
			}
			event = ""
		}
	}
	return sc.Err()
}
