// Package app 组织 Kanju 客户端的应用状态与页面。
package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/nsgo-dev/nsgo/base"

	"kanju/client/api"

	"kanju/backend"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
)

// App 持有全局状态：API 客户端、用户、服务器地址、当前页签。
type App struct {
	API *api.Client

	Server *core.Signal[string]
	User   *core.Signal[*api.User]
	Tab    *core.Signal[string]
	// Embedded 标记当前连接的是进程内嵌后端（免登录）。
	Embedded *core.Signal[bool]

	// HomeCache 首页分类数据缓存（跨页签保留）。
	HomeCache homeCache
	// OnPageLoaded 分页数据到达后的滚动补偿回调（posterGrid 设置）。
	OnPageLoaded func(firstNew int)
	// tmdbNoticeShown TMDB 兜底弹窗只提示一次。
	tmdbNoticeShown bool
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
	{ID: "search", Text: "搜索", Icon: lucide.Search},
	{ID: "history", Text: "历史", Icon: lucide.Clock},
	{ID: "favorites", Text: "收藏", Icon: lucide.Star},
	{ID: "settings", Text: "设置", Icon: lucide.Settings},
	{ID: "sources", Text: "站源管理", Icon: lucide.Server, Admin: true},
}

// 嵌入后端的生命周期：整个进程一份，退出时随进程结束。
var (
	embedOnce sync.Once
	embedAddr string
	embedErr  error
)

// EmbeddedBackend 在进程内启动 kanju 服务（随机端口，数据库在应用数据目录）。
// 幂等；返回实际监听地址（如 http://127.0.0.1:54321）。
func EmbeddedBackend(cx *core.Scope) (string, error) {
	embedOnce.Do(func() {
		dir := cx.DataDir()
		if dir == "" {
			embedErr = fmt.Errorf("应用数据目录不可用")
			return
		}
		opts := backend.ServeOptions{
			// 空端口 = 自动分配，避免与用户可能在跑的外部 kanju 冲突。
			DBPath: dir + "/kanju.db",
			// 内嵌模式免登录：只绑 127.0.0.1，无对外暴露；鉴权全开时
			// 用户每次打开 app 都要先输账号密码，没有意义。
			NoAuth: true,
			// 后端日志收进客户端的统一日志缓冲（设置页可看）。
			LogOutput: BackendLogWriter(),
			// TMDB key：环境变量（豆瓣风控时的第二数据源）。
			TMDBKey: os.Getenv("TMDB_API_KEY"),
			// 开启后端 debug 日志（请求路由+全链路计时）。
			Debug: true,
		}
		addr, stop, err := backend.Start(opts)
		if err != nil {
			embedErr = err
			return
		}
		embedAddr = "http://" + addr
		// stop 闭包持有 srv/db；退出时由 OnQuit 调用优雅关停。
		embeddedStop = stop
		log.Printf("kanju embedded backend at %s (db: %s)", embedAddr, dir)
	})
	return embedAddr, embedErr
}

var embeddedStop func()

// SetupForTest 构造不读持久化的 App（nsgotest 用）。
func SetupForTest(cx *core.Scope, base string) *App {
	a := &App{
		API:                api.New(base),
		Server:             core.NewSignal(cx, base),
		User:               core.NewSignal(cx, (*api.User)(nil)),
		Tab:                core.NewSignal(cx, "home"),
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
		PlayURL:            core.NewSignal(cx, ""),
		PlaySource:         core.NewSignal(cx, ""),
		PlaySourceURL:      core.NewSignal(cx, ""),
	}
	return a
}

// Setup 在根 scope 上初始化信号与持久化状态。
func Setup(cx *core.Scope) *App {
	// 优先启动进程内嵌后端：开箱即用，无需外部服务，也无需登录
	// （NoAuth 虚拟 admin，见 EmbeddedBackend）。
	defaultBase := "http://127.0.0.1:28088"
	embedded := false
	if addr, err := EmbeddedBackend(cx); err == nil {
		defaultBase = addr
		embedded = true
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
	if embedded && a.API.Base() == defaultBase {
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

// PreferTMDB 返回用户是否设置了 TMDB 优先。
func (a *App) PreferTMDB() string {
	if a.PreferSourceSignal.Get() == "tmdb" {
		return "tmdb"
	}
	return ""
}

// SetPreferSource 切换影片数据源偏好。
func (a *App) SetPreferSource(cx *core.Scope, v string) {
	a.PreferSourceSignal.Set(v)
	ClientLog("[data] 数据源偏好切换为 %s", v)
}

// ShowTMDBNotice 弹窗告知当前数据来自 TMDB（豆瓣全通道不可用）。
func (a *App) ShowTMDBNotice(cx *core.Scope) {
	ClientLog("[data] 豆瓣不可用，当前使用 TMDB 数据源（评分体系不同）")
	base.ShowAlertDialog(cx, base.AlertDialog{
		Title:       "数据源已切换",
		Description: "豆瓣接口暂时不可用，当前显示的是 TMDB 的影片数据。评分与豆瓣不同，恢复后将自动切回。",
		Actions: []core.AlertAction{
			{Label: "知道了", Role: core.AlertCancel},
		},
	}, nil)
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
	a.DetailTitle.Set(title)
	a.DetailCover.Set(cover)
	a.DetailURL.Set(detailURL)
	a.Tab.Set("detail")
}

// OpenPlayer 记录播放上下文并跳到播放器。episodeURL 是原始选集
// 地址（历史/续播的稳定键），playURL 是代理改写后的播放地址。
func (a *App) OpenPlayer(title, playURL, episodeURL, sourceName, sourceURL string) {
	ClientLog("[nav] 播放 %q url=%s 来源=%s", title, playURL, sourceName)
	a.PlayTitle.Set(title)
	a.PlayEpisodeURL.Set(episodeURL)
	a.PlayURL.Set(playURL)
	a.PlaySource.Set(sourceName)
	a.PlaySourceURL.Set(sourceURL)
	a.Tab.Set("player")
}
