// Package app 组织 Kanju 客户端的应用状态与页面。
package app

import (
	"context"
	"log"
	"strconv"
	"sync"

	"kanju/client/api"
	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
)

// App 持有全局状态：API 客户端、用户、服务器地址、当前页签。
type App struct {
	API *api.Client

	Server *core.Signal[string]
	User   *core.Signal[*api.User]
	Tab    *core.Signal[string]
	// HomeView 首页内的视图切换：""=分类浏览，"search"=搜索面板，
	// "history"=历史面板，"favorites"=收藏面板。
	HomeView *core.Signal[string]
	// SearchKeyword 预填搜索框的关键词（跨页面进入搜索面板时设置）。
	SearchKeyword *core.Signal[string]
	// Embedded 标记当前连接的是进程内嵌后端（免登录）。
	Embedded *core.Signal[bool]
	// prevTab 上一页签（返回按钮用）；非信号，导航时序由 UI 线程保证。
	prevTab string

	// HomeCache 首页分类数据缓存（跨页签保留）。
	HomeCache homeCache
	// 首页分类信号（Home 挂载时注册，用于状态查询/测试）。
	homeKind  *core.Signal[string]
	homeSub   *core.Signal[int]
	homeChild *core.Signal[int]
	// PreferSourceSignal 影片数据源偏好（"douban"|"tmdb"），持久化在 Storage。
	PreferSourceSignal *core.Signal[string]
	// TestSetHomeItems 测试挂钩：Home 挂载时注册直接驱动网格数据的函数。
	TestSetHomeItems func([]api.DoubanItem)

	// 打开详情页时携带的上下文。
	DetailTitle *core.Signal[string]
	DetailCover *core.Signal[string]
	DetailURL   *core.Signal[string]
	// 详情页数据：全局单例——页面函数会被 shell 的 Dynamic 重复调用，
	// 局部信号会分裂成多实例，请求回调写到旧实例导致白屏。
	DetailSubject  *core.Signal[*api.Subject]
	DetailData     *core.Signal[*api.DetailResp]
	DetailLoading  *core.Signal[bool]
	DetailErr      *core.Signal[string]
	DetailPlayBusy *core.Signal[string]
	// DetailFav 当前详情是否已收藏；DetailNowPlaying 当前正在播的选集 URL（高亮用）。
	DetailFav        *core.Signal[bool]
	DetailNowPlaying *core.Signal[string]

	// 播放器入参。PlayURL 是交给播放器的（代理改写后）地址；
	// PlayEpisodeURL 是原始选集 URL，作为历史/续播的稳定匹配键。
	PlayTitle      *core.Signal[string]
	PlayURL        *core.Signal[string]
	PlayEpisodeURL *core.Signal[string]
	PlaySource     *core.Signal[string]
	PlaySourceURL  *core.Signal[string]
}

// Tab 定义侧栏页签。
type Tab struct {
	ID    string
	Text  string
	Icon  func() *core.SVG
	Admin bool
}

// homeCache 首页分类数据缓存：key 为 kind|tag|sort。
type homeCache struct {
	mu sync.Mutex
	m  map[string][]api.DoubanItem
}

const homeCacheMax = 60

func (c *homeCache) Get(key string) ([]api.DoubanItem, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	return v, ok
}

func (c *homeCache) Put(key string, v []api.DoubanItem) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string][]api.DoubanItem{}
	}
	if len(c.m) >= homeCacheMax {
		// 简单淘汰：删任意一个（map 遍历顺序不定即可）。
		for k := range c.m {
			delete(c.m, k)
			break
		}
	}
	c.m[key] = v
}

var tabs = []Tab{
	{ID: "home", Text: "首页", Icon: lucide.House},
}

// 嵌入后端的生命周期见 backend_embed.go（native）与 backend_web.go（wasm）。

// SetupForTest 构造不读持久化的 App（nsgotest 用）。
func SetupForTest(cx *core.Scope, base string) *App {
	a := &App{
		API:                api.New(base),
		Server:             core.NewSignal(cx, base),
		User:               core.NewSignal(cx, (*api.User)(nil)),
		Tab:                core.NewSignal(cx, "home"),
		HomeView:           core.NewSignal(cx, ""),
		SearchKeyword:      core.NewSignal(cx, ""),
		Embedded:           core.NewSignal(cx, false),
		PreferSourceSignal: core.NewSignal(cx, "douban"),
		DetailTitle:        core.NewSignal(cx, ""),
		DetailCover:        core.NewSignal(cx, ""),
		DetailURL:          core.NewSignal(cx, ""),
		DetailSubject:      core.NewSignal(cx, (*api.Subject)(nil)),
		DetailData:         core.NewSignal(cx, (*api.DetailResp)(nil)),
		DetailLoading:      core.NewSignal(cx, false),
		DetailErr:          core.NewSignal(cx, ""),
		DetailPlayBusy:     core.NewSignal(cx, ""),
		DetailFav:          core.NewSignal(cx, false),
		DetailNowPlaying:   core.NewSignal(cx, ""),
		PlayTitle:          core.NewSignal(cx, ""),
		PlayEpisodeURL:     core.NewSignal(cx, ""),
		PlayURL:            core.NewSignal(cx, ""),
		PlaySource:         core.NewSignal(cx, ""),
		PlaySourceURL:      core.NewSignal(cx, ""),
	}
	return a
}

// Setup 在根 scope 上初始化信号与持久化状态。
func Setup(cx *core.Scope) *App {
	// 日志落盘到应用数据目录 logs/（在所有其它初始化之前，保证启动
	// 段的日志也留痕）。InitFileLogs 幂等，空 dir 时静默跳过。
	InitFileLogs(cx.DataDir())

	// 优先启动进程内嵌后端：开箱即用，无需外部服务，也无需登录
	// （NoAuth 虚拟 admin，见 backend_embed.go）。
	// wasm 下 EmbeddedBackend 返回空地址：后端由跟随应用分发的
	// kanju-web 原生程序提供，UI 走同源相对路径（空 base）。
	defaultBase := "http://127.0.0.1:28088"
	embedded := false
	if addr, err := EmbeddedBackend(cx); err == nil {
		if addr == "" {
			// wasm：同源直连，/api/* 由托管站点自身提供。
			defaultBase = ""
		} else {
			defaultBase = addr
			embedded = true
		}
	} else {
		log.Printf("embedded backend failed to start: %v", err)
	}

	// 优雅退出：关停内嵌后端（SQLite checkpoint）。
	cx.OnQuit(func(ctx context.Context) {
		ClientLog("[app] 退出")
		if embeddedStop != nil {
			embeddedStop()
		}
	})

	a := SetupForTest(cx, defaultBase)

	// 恢复数据源偏好（douban | tmdb）。
	store := cx.Storage()
	if v, ok := store.GetItem("prefer_source"); ok && v == "tmdb" {
		a.PreferSourceSignal.Set("tmdb")
	}
	// 恢复海报网格列数（首帧初值）：网格挂载时宽度未知，用上次会话
	// 的列数渲染，测到真实宽度后不再跳变（消除 4→5 闪变）。
	if v, ok := store.GetItem("poster_cols"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			theme.InitialCols(n)
		}
	}
	cx.Effect(func() {
		v := a.PreferSourceSignal.Get()
		core.Untracked(cx, func() bool {
			store.SetItem("prefer_source", v)
			ClientLog("[data] 数据源偏好 = %s", v)
			return true
		})
	})

	// 全部 API 请求打进统一日志（排查用）。
	a.API.Logf = ClientLog
	// 401 全局处理：token 过期时清登录态回登录页（只触发一次）。
	a.API.OnUnauthorized = func() {
		cx.Post(func() {
			ClientLog("[auth] 收到 401，登录态失效，回到登录页")
			a.Logout(cx)
		})
	}
	startLogPump(cx)
	a.Embedded.Set(embedded)
	if server, ok := store.GetItem("server"); ok && server != "" {
		a.API.SetBase(server)
		a.Server.Set(server)
	}
	// 内嵌后端免登录：直接以虚拟 admin 进入主界面。
	// wasm 同理：kanju-web 后端程序单机运行，浏览器 UI 是唯一用户。
	if embedded || a.API.Base() == "" {
		a.User.Set(&api.User{ID: 0, Username: "本地", Role: "admin"})
		return a
	}
	// 外部服务器：走常规 token 流程。
	if token, ok := store.GetItem("token"); ok && token != "" {
		a.API.SetToken(token)
		// 启动时校验 token，失效则清除。
		go func() {
			u, err := a.API.Me()
			cx.Post(func() {
				if err != nil {
					a.API.SetToken("")
					store.RemoveItem("token")
				} else {
					a.User.Set(&u)
				}
			})
		}()
	}
	return a
}

// Navigate 记录来源页签并切换（详情/播放器/设置页的返回按钮用）。
func (a *App) Navigate(id string) {
	ClientLog("[nav] 切换页签 %s -> %s", a.Tab.Get(), id)
	a.prevTab = a.Tab.Get()
	a.Tab.Set(id)
}

// Back 返回上一页签；没有来源时回首页。直接 Tab.Set 而不经
// Navigate——返回不改写 prevTab，否则 detail→player→Back(detail)
// →Back 时 prevTab 已被覆盖成 player，形成 detail↔player 乒乓。
func (a *App) Back() {
	back := a.prevTab
	if back == "" || back == a.Tab.Get() {
		back = "home"
	}
	ClientLog("[nav] 返回 %s -> %s", a.Tab.Get(), back)
	a.Tab.Set(back)
}

// OpenSearch 进入首页的搜索面板并预填关键词。
func (a *App) OpenSearch(keyword string) {
	ClientLog("[nav] 打开搜索面板 %q", keyword)
	a.SearchKeyword.Set(keyword)
	a.HomeView.Set("search")
	a.Tab.Set("home")
}

// CategoryLabelOf 返回首页当前选中分类的中文名（测试与调试用）。
func (a *App) CategoryLabelOf() string {
	return currentCategoryLabel(a.homeKind, a.homeSub, a.homeChild, a.PreferSourceSignal.Get() == "tmdb")
}

// PreferTMDB 返回用户是否设置了 TMDB 优先。
func (a *App) PreferTMDB() string {
	if a.PreferSourceSignal.Get() == "tmdb" {
		return "tmdb"
	}
	return ""
}

// SaveAuth 持久化登录凭证。
func (a *App) SaveAuth(cx *core.Scope, token string, u *api.User) {
	a.API.ResetUnauthorized()
	a.API.SetToken(token)
	a.User.Set(u)
	cx.Storage().SetItem("token", token)
}

// Logout 清除登录态。
func (a *App) Logout(cx *core.Scope) {
	ClientLog("[nav] 退出登录")
	a.API.SetToken("")
	a.User.Set(nil)
	cx.Storage().RemoveItem("token")
}

// SetServer 切换后端地址并重置登录态。切回内嵌地址时免登录。
func (a *App) SetServer(cx *core.Scope, base string) {
	ClientLog("[nav] 切换服务器 -> %s", base)
	a.API.SetBase(base)
	a.Server.Set(base)
	cx.Storage().SetItem("server", base)
	a.Logout(cx)
	if addr, err := EmbeddedBackend(cx); err == nil && base == addr {
		a.Embedded.Set(true)
		a.User.Set(&api.User{ID: 0, Username: "本地", Role: "admin"})
	} else {
		a.Embedded.Set(false)
	}
}

// OpenDetail 记录上下文并跳到详情页签。
func (a *App) OpenDetail(title, cover, detailURL string) {
	ClientLog("[nav] 打开详情 %q url=%s", title, detailURL)
	a.prevTab = a.Tab.Get()
	a.DetailTitle.Set(title)
	a.DetailCover.Set(cover)
	a.DetailURL.Set(detailURL)
	a.Tab.Set("detail")
}

// OpenPlayer 记录播放上下文并跳到播放器。episodeURL 是原始选集
// 地址（历史/续播的稳定匹配键），playURL 是代理改写后的播放地址。
func (a *App) OpenPlayer(title, playURL, episodeURL, sourceName, sourceURL string) {
	ClientLog("[nav] 播放 %q url=%s 来源=%s", title, playURL, sourceName)
	a.prevTab = a.Tab.Get()
	a.PlayTitle.Set(title)
	a.PlayEpisodeURL.Set(episodeURL)
	a.PlayURL.Set(playURL)
	a.PlaySource.Set(sourceName)
	a.PlaySourceURL.Set(sourceURL)
	a.Tab.Set("player")
}
