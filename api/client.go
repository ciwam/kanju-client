// Package api 对接 kanju 后端（HTTP + SSE），所有请求走 net/http。
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Envelope 是后端统一响应信封。
type Envelope[T any] struct {
	IsSuccess bool   `json:"isSuccess"`
	ErrorMsg  string `json:"errorMsg"`
	Data      T      `json:"data"`
}

// User 是登录后的用户信息。
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"` // user | admin
}

// Client 是 kanju 后端的 HTTP 客户端。Logf 可注入请求日志钩子
// （客户端把它接到统一的日志缓冲）。
type Client struct {
	mu          sync.RWMutex
	base        string // 如 http://127.0.0.1:28088
	token       string
	unauthFired bool // 401 回调已触发（防抖）
	http        *http.Client
	// Logf 可注入请求日志钩子（客户端把它接到统一的日志缓冲）。
	// 并发安全由调用方保证。
	Logf func(format string, args ...any)
	// OnUnauthorized 收到 401 时回调（任意 goroutine，调用方自行切换线程）。
	// 用于全局把用户送回登录页。可为 nil。
	OnUnauthorized func()
}

func New(base string) *Client {
	// 不设 Client 级 Timeout：它会覆盖整个请求生命周期且无法被
	// per-request context 放宽（detail 聚合 90s 曾被 15s 全局上限掐断）。
	// 超时全部由 doJSONTimeout 的 context 控制，SSE 用独立 client。
	c := &Client{http: &http.Client{}}
	c.SetBase(base)
	return c
}

// SetBase 设置后端地址（自动去掉尾部斜杠）。
func (c *Client) SetBase(base string) {
	c.mu.Lock()
	c.base = strings.TrimRight(base, "/")
	c.mu.Unlock()
}

// Base 返回当前后端地址。
func (c *Client) Base() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.base
}

// logf 打一条请求日志（未设置 Logf 则丢弃）。
func (c *Client) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf("[api] "+format, args...)
	}
}

// fireUnauthorized 触发 401 回调（只触发一次，防连续请求连环回调）。
func (c *Client) fireUnauthorized() {
	c.mu.Lock()
	fired := c.unauthFired
	c.unauthFired = true
	cb := c.OnUnauthorized
	c.mu.Unlock()
	if cb != nil && !fired {
		cb()
	}
}

// ResetUnauthorized 重新武装 401 回调（登录成功后调用）。
func (c *Client) ResetUnauthorized() {
	c.mu.Lock()
	c.unauthFired = false
	c.mu.Unlock()
}

// SetToken 保存/清除 Bearer token。
func (c *Client) SetToken(t string) {
	c.mu.Lock()
	c.token = t
	c.mu.Unlock()
}

// Token 返回当前 token。
func (c *Client) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// Err 是业务错误：HTTP 非 2xx 或 isSuccess=false。
type Err struct {
	Status int
	Msg    string
}

func (e *Err) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return fmt.Sprintf("请求失败 (HTTP %d)", e.Status)
}

// Unauthorized 是否 401（token 失效，应回到登录页）。
func (e *Err) Unauthorized() bool { return e.Status == http.StatusUnauthorized }

// newTraceID 生成 6 字节 hex 短 trace id，与后端 trace.go 的格式一致。
func newTraceID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (c *Client) newRequest(method, path string, body any) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.Base()+path, rd)
	if err != nil {
		return nil, err
	}
	// 每请求一个 trace id：请求头带给后端（后端日志同前缀），
	// 存进 request context 供本端日志行取（见 traceOf）。
	tid := newTraceID()
	req.Header.Set("X-Trace-ID", tid)
	req = req.WithContext(context.WithValue(req.Context(), traceKey{}, tid))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if t := c.Token(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	return req, nil
}

// traceKey 是 request context 里 trace id 的键（private 类型防跨包冲突）。
type traceKey struct{}

// traceOf 从 request 取 trace id；无 request（组装失败）返回 "-"。
func traceOf(req *http.Request) string {
	if req == nil {
		return "-"
	}
	if v, ok := req.Context().Value(traceKey{}).(string); ok {
		return v
	}
	return "-"
}

// doJSON 执行请求并解析信封（默认 10 秒超时）。
func (c *Client) doJSON(method, path string, body, out any) error {
	return c.doJSONTimeout(method, path, body, out, 10*time.Second)
}

// doJSONTimeout 同 doJSON，但用调用方给的超时。聚合类接口（detail）
// 要逐站抓取，需要远超普通请求的时限。
func (c *Client) doJSONTimeout(method, path string, body, out any, timeout time.Duration) error {
	req, err := c.newRequest(method, path, body)
	if err != nil {
		c.logf("[trace_id=-] %s %s 组装失败: %v", method, path, err)
		return err
	}
	tid := traceOf(req)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req = req.WithContext(ctx)
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.logf("[trace_id=%s] %s %s 网络错误(%dms): %v", tid, method, path, time.Since(start).Milliseconds(), err)
		return err
	}
	defer resp.Body.Close()
	elapsed := time.Since(start).Milliseconds()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		c.logf("[trace_id=%s] %s %s 读响应失败(%dms): %v", tid, method, path, elapsed, err)
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		c.logf("[trace_id=%s] %s %s -> 401 未登录(%dms)", tid, method, path, elapsed)
		c.fireUnauthorized()
		return &Err{Status: resp.StatusCode, Msg: "未登录或登录已过期"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.logf("[trace_id=%s] %s %s -> HTTP %d(%dms): %s", tid, method, path, resp.StatusCode, elapsed, firstString(raw, ""))
		return &Err{Status: resp.StatusCode, Msg: firstString(raw, resp.Status)}
	}
	var env Envelope[json.RawMessage]
	if err := json.Unmarshal(raw, &env); err != nil {
		c.logf("%s %s -> %d 解析失败(%dms)", method, path, resp.StatusCode, elapsed)
		return &Err{Status: resp.StatusCode, Msg: "响应解析失败"}
	}
	if !env.IsSuccess {
		c.logf("[trace_id=%s] %s %s -> 业务失败(%dms): %s", tid, method, path, elapsed, env.ErrorMsg)
		return &Err{Status: resp.StatusCode, Msg: env.ErrorMsg}
	}
	if out != nil {
		// /api/detail 等端点的字段在顶层（channels/cover 与 isSuccess 并列），
		// data 为空时整体再解一次，两种形态都兼容。
		if len(env.Data) > 0 {
			if err := json.Unmarshal(env.Data, out); err != nil {
				c.logf("[trace_id=%s] %s %s -> %d data 解析失败(%dms)", tid, method, path, resp.StatusCode, elapsed)
				return &Err{Status: resp.StatusCode, Msg: "数据解析失败"}
			}
		} else if err := json.Unmarshal(raw, out); err != nil {
			c.logf("[trace_id=%s] %s %s -> %d 顶层解析失败(%dms)", tid, method, path, resp.StatusCode, elapsed)
			return &Err{Status: resp.StatusCode, Msg: "数据解析失败"}
		}
	}
	c.logf("[trace_id=%s] %s %s -> 200 OK(%dms)", tid, method, path, elapsed)
	return nil
}

// firstString 尽力从错误 body 里抠出 errorMsg。
func firstString(raw []byte, fallback string) string {
	var e struct {
		ErrorMsg string `json:"errorMsg"`
		Error    string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil {
		if e.ErrorMsg != "" {
			return e.ErrorMsg
		}
		if e.Error != "" {
			return e.Error
		}
	}
	return fallback
}

// ---- 认证 ----

// Login 登录并从 Set-Cookie 里取 token。
func (c *Client) Login(username, password string) (User, error) {
	req, err := c.newRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"username": username, "password": password,
	})
	if err != nil {
		c.logf("[trace_id=-] POST /api/auth/login 组装失败: %v", err)
		return User{}, err
	}
	tid := traceOf(req)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	start := time.Now()
	resp, err := c.http.Do(req)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		c.logf("[trace_id=%s] POST /api/auth/login 网络错误(%dms): %v", tid, elapsed, err)
		return User{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		c.logf("[trace_id=%s] POST /api/auth/login -> HTTP %d(%dms): %s", tid, resp.StatusCode, elapsed, firstString(raw, ""))
		return User{}, &Err{Status: resp.StatusCode, Msg: firstString(raw, "登录失败")}
	}
	var env Envelope[User]
	if err := json.Unmarshal(raw, &env); err != nil {
		c.logf("[trace_id=%s] POST /api/auth/login -> %d 解析失败(%dms)", tid, resp.StatusCode, elapsed)
		return User{}, &Err{Status: resp.StatusCode, Msg: "响应解析失败"}
	}
	if !env.IsSuccess {
		c.logf("[trace_id=%s] POST /api/auth/login -> 业务失败(%dms): %s", tid, elapsed, env.ErrorMsg)
		return User{}, &Err{Status: resp.StatusCode, Msg: env.ErrorMsg}
	}
	token := ""
	for _, ck := range resp.Cookies() {
		if ck.Name == "kanju_token" {
			token = ck.Value
			break
		}
	}
	if token == "" {
		c.logf("[trace_id=%s] POST /api/auth/login -> 200 但缺少凭证(%dms)", tid, elapsed)
		return User{}, &Err{Status: resp.StatusCode, Msg: "登录响应缺少凭证"}
	}
	c.SetToken(token)
	c.logf("[trace_id=%s] POST /api/auth/login -> 200 OK(%dms) user=%s role=%s", tid, elapsed, env.Data.Username, env.Data.Role)
	return env.Data, nil
}

func (c *Client) Register(username, password string) (User, error) {
	var u User
	err := c.doJSON(http.MethodPost, "/api/auth/register", map[string]string{
		"username": username, "password": password,
	}, &u)
	return u, err
}

func (c *Client) Logout() error {
	return c.doJSON(http.MethodPost, "/api/auth/logout", nil, nil)
}

func (c *Client) Me() (User, error) {
	var u User
	err := c.doJSON(http.MethodGet, "/api/auth/me", nil, &u)
	return u, err
}

// ---- 首页（豆瓣） ----

// DoubanItem 是榜单/搜索条目。ID 仅搜索降级时有。
type DoubanItem struct {
	ID           string `json:"id,omitempty"`
	Title        string `json:"title"`
	Cover        string `json:"cover"`
	URL          string `json:"url"`
	Rate         string `json:"rate"`
	Year         string `json:"year,omitempty"`
	EpisodesInfo string `json:"episodesInfo,omitempty"`
	SourceName   string `json:"sourceName,omitempty"`
}

// DoubanListWithSource 同 DoubanList，并返回响应的 source 标记
// （tmdb=TMDB 数据源；空=豆瓣/rexxar 正常通道）。prefer 传 "tmdb"
// 表示用户设置了 TMDB 优先（后端需已配置 key）。
func (c *Client) DoubanListWithSource(kind, tag, sort string, limit, start int, prefer string) ([]DoubanItem, string, error) {
	q := url.Values{}
	q.Set("type", kind)
	q.Set("tag", tag)
	if sort != "" {
		q.Set("sort", sort)
	}
	q.Set("limit", fmt.Sprint(limit))
	q.Set("start", fmt.Sprint(start))
	if prefer != "" {
		q.Set("prefer", prefer)
	}
	req, err := c.newRequest(http.MethodGet, "/api/douban?"+q.Encode(), nil)
	if err != nil {
		return nil, "", err
	}
	tid := traceOf(req)
	start0 := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.logf("[trace_id=%s] GET /api/douban 网络错误(%dms): %v", tid, time.Since(start0).Milliseconds(), err)
		return nil, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	elapsed := time.Since(start0).Milliseconds()
	if resp.StatusCode == http.StatusUnauthorized {
		c.logf("GET /api/douban -> 401 未登录(%dms)", elapsed)
		c.fireUnauthorized()
		return nil, "", &Err{Status: resp.StatusCode, Msg: "未登录或登录已过期"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.logf("GET /api/douban -> HTTP %d(%dms)", resp.StatusCode, elapsed)
		return nil, "", &Err{Status: resp.StatusCode, Msg: firstString(raw, resp.Status)}
	}
	// 响应可能是信封包 data 或扁平（data 与 source 并列），两种都兼容。
	var env struct {
		IsSuccess bool            `json:"isSuccess"`
		ErrorMsg  string          `json:"errorMsg"`
		Data      json.RawMessage `json:"data"`
		Source    string          `json:"source"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		c.logf("GET /api/douban -> 解析失败(%dms)", elapsed)
		return nil, "", &Err{Status: resp.StatusCode, Msg: "响应解析失败"}
	}
	if !env.IsSuccess {
		c.logf("GET /api/douban -> 业务失败(%dms): %s", elapsed, env.ErrorMsg)
		return nil, env.Source, &Err{Status: resp.StatusCode, Msg: env.ErrorMsg}
	}
	dataSrc := env.Data
	if len(env.Data) == 0 {
		// 扁平形态：data 字段不存在，源数据混在顶层；剔除 source 后整体取 data。
		var flat map[string]json.RawMessage
		if json.Unmarshal(raw, &flat) == nil {
			dataSrc = flat["data"]
		}
	}
	var items []DoubanItem
	if err := json.Unmarshal(dataSrc, &items); err != nil {
		c.logf("GET /api/douban -> %d 数据解析失败(%dms)", resp.StatusCode, elapsed)
		return nil, env.Source, &Err{Status: resp.StatusCode, Msg: "数据解析失败"}
	}
	c.logf("GET /api/douban -> 200 OK(%dms) %d 条 source=%q", elapsed, len(items), env.Source)
	return items, env.Source, nil
}

// DoubanList 兼容旧签名（不关心数据源的调用方）。
func (c *Client) DoubanList(kind, tag, sort string, limit, start int) ([]DoubanItem, error) {
	items, _, err := c.DoubanListWithSource(kind, tag, sort, limit, start, "")
	return items, err
}

func (c *Client) DoubanSearch(keyword string) ([]DoubanItem, error) {
	q := url.Values{}
	q.Set("q", keyword)
	var items []DoubanItem
	err := c.doJSON(http.MethodGet, "/api/douban?"+q.Encode(), nil, &items)
	return items, err
}

// Subject 是豆瓣影片详情。
type Subject struct {
	ID        string   `json:"id"`
	URL       string   `json:"url"`
	Title     string   `json:"title"`
	Year      string   `json:"year"`
	Cover     string   `json:"cover"`
	Rating    string   `json:"rating"`
	Directors []string `json:"directors"`
	Actors    []string `json:"actors"`
	Genres    []string `json:"genres"`
	Region    string   `json:"region"`
	Episodes  string   `json:"episodes"`
	Duration  string   `json:"duration"`
	Subtype   string   `json:"subtype"`
	Intro     string   `json:"intro"`
	// 缓存元信息：stale=true 表示这次是缓存旧数据（后台已在刷新）；
	// Revision 是内容指纹，客户端对比新旧判断"信息有更新"。
	Stale    bool   `json:"stale,omitempty"`
	Revision string `json:"revision,omitempty"`
}

func (c *Client) DoubanSubject(id, cover string) (Subject, error) {
	return c.doubanSubject(id, cover, false)
}

// DoubanSubjectRefresh 强制绕过缓存拉取最新（SWR 第二段：客户端收到
// stale 旧数据后调用，对比 Revision 判断是否更新）。
func (c *Client) DoubanSubjectRefresh(id, cover string) (Subject, error) {
	return c.doubanSubject(id, cover, true)
}

func (c *Client) doubanSubject(id, cover string, refresh bool) (Subject, error) {
	q := url.Values{}
	q.Set("id", id)
	if cover != "" {
		q.Set("cover", cover)
	}
	if refresh {
		q.Set("refresh", "1")
	}
	var s Subject
	err := c.doJSON(http.MethodGet, "/api/douban/subject?"+q.Encode(), nil, &s)
	return s, err
}

// Poster 兜底海报。
func (c *Client) Poster(title string) (string, error) {
	q := url.Values{}
	q.Set("title", title)
	var r struct {
		Poster string `json:"poster"`
	}
	err := c.doJSON(http.MethodGet, "/api/poster?"+q.Encode(), nil, &r)
	return r.Poster, err
}

// ---- 详情与播放 ----

// Episode 是一条选集。
type Episode struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Channel 是一条线路（站点）。SpeedKBps/TTFBMs 是聚合时对该站源第一集
// 实测的网速指标（0 表示未测到），站源 tab 展示并用于默认选中。
type Channel struct {
	Name       string    `json:"name"`
	Episodes   []Episode `json:"episodes"`
	SourceName string    `json:"_sourceName"`
	SpeedKBps  int       `json:"_speedKbps,omitempty"`
	TTFBMs     int       `json:"_ttfbMs,omitempty"`
}

// DetailResp 是聚合详情。
type DetailResp struct {
	Cover    string    `json:"cover,omitempty"`
	Intro    string    `json:"intro,omitempty"`
	Channels []Channel `json:"channels"`
}

func (c *Client) Detail(detailURL, sourceName, title string) (DetailResp, error) {
	body := map[string]string{"url": detailURL}
	if sourceName != "" {
		body["sourceName"] = sourceName
	}
	if title != "" {
		body["title"] = title
	}
	var d DetailResp
	// 聚合要逐站源抓取，实测可达 20-40 秒。
	err := c.doJSONTimeout(http.MethodPost, "/api/detail", body, &d, 90*time.Second)
	return d, err
}

// PlayResult 是播放解析结果。Type: m3u8|mp4|flv|mpd|iframe。
type PlayResult struct {
	URL  string `json:"url"`
	Type string `json:"type"`
}

func (c *Client) Play(epURL string) (PlayResult, error) {
	var r PlayResult
	err := c.doJSON(http.MethodPost, "/api/play", map[string]string{"url": epURL}, &r)
	return r, err
}

// ---- 历史 / 收藏 / 直播 ----

type History struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	Cover        string `json:"cover"`
	SourceName   string `json:"sourceName"`
	SourceURL    string `json:"sourceUrl"`
	DetailURL    string `json:"detailUrl"`
	EpisodeTitle string `json:"episodeTitle"`
	PlayURL      string `json:"playUrl"`
	Position     int64  `json:"position"`
	Duration     int64  `json:"duration"`
	CreatedAt    string `json:"createdAt"`
}

func (c *Client) Histories() ([]History, error) {
	var h []History
	err := c.doJSON(http.MethodGet, "/api/history", nil, &h)
	return h, err
}

func (c *Client) SaveHistory(h History) error {
	return c.doJSON(http.MethodPost, "/api/history", map[string]any{
		"title": h.Title, "cover": h.Cover, "sourceName": h.SourceName,
		"sourceUrl": h.SourceURL, "detailUrl": h.DetailURL, "episodeTitle": h.EpisodeTitle,
		"playUrl": h.PlayURL, "position": h.Position, "duration": h.Duration,
	}, nil)
}

type Favorite struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Cover      string `json:"cover"`
	SourceName string `json:"sourceName"`
	DetailURL  string `json:"detailUrl"`
	CreatedAt  string `json:"createdAt"`
}

func (c *Client) Favorites() ([]Favorite, error) {
	var f []Favorite
	err := c.doJSON(http.MethodGet, "/api/favorites", nil, &f)
	return f, err
}

func (c *Client) AddFavorite(f Favorite) error {
	return c.doJSON(http.MethodPost, "/api/favorites", map[string]string{
		"title": f.Title, "cover": f.Cover, "sourceName": f.SourceName, "detailUrl": f.DetailURL,
	}, nil)
}

func (c *Client) RemoveFavorite(detailURL string) error {
	return c.doJSON(http.MethodPost, "/api/favorites/delete", map[string]string{"detailUrl": detailURL}, nil)
}

type LiveChannel struct {
	ID        int64  `json:"id"`
	GroupName string `json:"groupName"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	SortOrder int    `json:"sortOrder"`
}

func (c *Client) LiveChannels(group string) ([]LiveChannel, error) {
	path := "/api/live"
	if group != "" {
		q := url.Values{}
		q.Set("group", group)
		path += "?" + q.Encode()
	}
	var l []LiveChannel
	err := c.doJSON(http.MethodGet, path, nil, &l)
	return l, err
}

// ---- 设置 ----

func (c *Client) RegistrationOpen() (bool, error) {
	var r struct {
		Open bool `json:"open"`
	}
	err := c.doJSON(http.MethodGet, "/api/settings/registration", nil, &r)
	return r.Open, err
}

// ---- 站源管理（admin） ----

// SiteSource 是一个站源（字段有省略，客户端只用到管理常用项）。
type SiteSource struct {
	ID       int64  `json:"id,omitempty"`
	Name     string `json:"name"`
	BaseURL  string `json:"baseUrl"`
	Enabled  bool   `json:"enabled"`
	Type     string `json:"sourceType,omitempty"`
	Disabled int    `json:"isDisable,omitempty"`
}

func (c *Client) Sources() ([]SiteSource, error) {
	var s []SiteSource
	err := c.doJSON(http.MethodGet, "/api/sources", nil, &s)
	return s, err
}

func (c *Client) AddSource(s SiteSource) error {
	return c.doJSON(http.MethodPost, "/api/sources", s, nil)
}

func (c *Client) DeleteSource(baseURL string) error {
	return c.doJSON(http.MethodPost, "/api/sources/delete", map[string]string{"baseUrl": baseURL}, nil)
}

func (c *Client) ToggleSource(baseURL string) error {
	return c.doJSON(http.MethodPost, "/api/sources/toggle", map[string]string{"baseUrl": baseURL}, nil)
}

// ---- URL 工具 ----

// ImageURL 把任意封面 URL 包装成图片代理地址。title 可选：豆瓣图床
// 全部失败时，代理层按片名到 TMDB 检索替代海报（第二地址降级）。
func (c *Client) ImageURL(raw string, title ...string) string {
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "/api/proxy/image") {
		return raw
	}
	u := "/api/proxy/image?url=" + url.QueryEscape(raw)
	if len(title) > 0 && title[0] != "" {
		u += "&t=" + url.QueryEscape(title[0])
	}
	return c.Base() + u
}

// MediaPathProxy 把 m3u8 等直链改写成路径式媒体代理，
// playlist 内的相对分片引用可自然解析回代理路径。
func (c *Client) MediaPathProxy(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	p := "/api/proxy/m/" + u.Scheme + "/" + u.Host + u.Path
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return c.Base() + p
}

// MediaQueryProxy 直链媒体代理（Range 透传，支持 seek）。
func (c *Client) MediaQueryProxy(raw string) string {
	return c.Base() + "/api/proxy/media?url=" + url.QueryEscape(raw)
}

// ProbeMediaURL 探测代理后的媒体流是否真的可取：GET 拉少量字节，
// 2xx 且非空才认为可播。站源播放页解析"成功"但视频 CDN 间歇性 404
// 时（如量子资源），nsgo 的 Video 对坏流静默黑屏不触发 OnError——
// 进入播放器前先验流，坏流走换源，杜绝"不报错也不播放"。
func (c *Client) ProbeMediaURL(playURL string) error {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(playURL)
	if err != nil {
		return fmt.Errorf("媒体流不可达: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("媒体流不可用 (HTTP %d)", resp.StatusCode)
	}
	// m3u8 内容非空即可（分片可达性交给播放器渐进报错）；
	// mp4/flv 依靠 Range，首字节能取到即认为流活着。
	n, err := io.ReadFull(resp.Body, make([]byte, 64))
	if err != nil && n == 0 {
		return fmt.Errorf("媒体流为空")
	}
	return nil
}

// PlaybackURL 按 /api/play 返回的 type 把直链改写成代理地址：
// m3u8 走路径式代理（playlist 内相对分片自动承接），其余走查询式
// 代理（Range 透传支持 seek）。iframe 类型调用方应已拦截。
// 内嵌后端（127.0.0.1 loopback）同样走代理：保持行为与外部服务器
// 一致，且代理层带 Referer/UA 处理，直连易被上游防盗链拒绝。
func (c *Client) PlaybackURL(raw, typ string) string {
	if raw == "" {
		return ""
	}
	switch typ {
	case "m3u8":
		return c.MediaPathProxy(raw)
	case "mp4", "flv", "mpd":
		return c.MediaQueryProxy(raw)
	default:
		// 未知类型：按扩展名猜，猜不出直连。
		l := strings.ToLower(raw)
		switch {
		case strings.Contains(l, ".m3u8"):
			return c.MediaPathProxy(raw)
		case strings.Contains(l, ".mp4"), strings.Contains(l, ".flv"):
			return c.MediaQueryProxy(raw)
		}
		return raw
	}
}
