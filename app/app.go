// Package app 组织 Kanju 客户端的应用状态与页面。
package app

import (
	"fmt"
	"log"
	"sync"

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
	Error  *core.Signal[string]

	// 打开详情页时携带的上下文。
	DetailTitle *core.Signal[string]
	DetailCover *core.Signal[string]
	DetailURL   *core.Signal[string]

	// 播放器入参。
	PlayTitle     *core.Signal[string]
	PlayURL       *core.Signal[string]
	PlaySource    *core.Signal[string]
	PlaySourceURL *core.Signal[string]
}

// Tab 定义侧栏页签。
type Tab struct {
	ID    string
	Text  string
	Icon  func() *core.SVG
	Admin bool
}

var tabs = []Tab{
	{ID: "home", Text: "首页", Icon: lucide.House},
	{ID: "search", Text: "搜索", Icon: lucide.Search},
	{ID: "history", Text: "历史", Icon: lucide.Clock},
	{ID: "favorites", Text: "收藏", Icon: lucide.Star},
	{ID: "live", Text: "直播", Icon: lucide.Radio},
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
		}
		addr, stop, err := backend.Start(opts)
		if err != nil {
			embedErr = err
			return
		}
		embedAddr = "http://" + addr
		// stop 闭包持有 srv/db；随进程退出即可，保引用防意外。
		embeddedStop = stop
		log.Printf("kanju embedded backend at %s (db: %s)", embedAddr, dir)
	})
	return embedAddr, embedErr
}

var embeddedStop func()

// SetupForTest 构造不读持久化的 App（nsgotest 用）。
func SetupForTest(cx *core.Scope, base string) *App {
	a := &App{
		API:           api.New(base),
		Server:        core.NewSignal(cx, base),
		User:          core.NewSignal(cx, (*api.User)(nil)),
		Tab:           core.NewSignal(cx, "home"),
		Error:         core.NewSignal(cx, ""),
		DetailTitle:   core.NewSignal(cx, ""),
		DetailCover:   core.NewSignal(cx, ""),
		DetailURL:     core.NewSignal(cx, ""),
		PlayTitle:     core.NewSignal(cx, ""),
		PlayURL:       core.NewSignal(cx, ""),
		PlaySource:    core.NewSignal(cx, ""),
		PlaySourceURL: core.NewSignal(cx, ""),
	}
	return a
}

// Setup 在根 scope 上初始化信号与持久化状态。
func Setup(cx *core.Scope) *App {
	// 优先启动进程内嵌后端：开箱即用，无需外部服务。
	defaultBase := "http://127.0.0.1:28088"
	if addr, err := EmbeddedBackend(cx); err == nil {
		defaultBase = addr
	} else {
		log.Printf("embedded backend failed to start: %v", err)
	}

	a := SetupForTest(cx, defaultBase)
	store := cx.Storage()
	if server, ok := store.GetItem("server"); ok && server != "" {
		a.API.Base = server
		a.Server.Set(server)
	}
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

// SaveAuth 持久化登录凭证。
func (a *App) SaveAuth(cx *core.Scope, token string, u *api.User) {
	a.API.SetToken(token)
	a.User.Set(u)
	cx.Storage().SetItem("token", token)
}

// Logout 清除登录态。
func (a *App) Logout(cx *core.Scope) {
	a.API.SetToken("")
	a.User.Set(nil)
	cx.Storage().RemoveItem("token")
}

// SetServer 切换后端地址并重置登录态。
func (a *App) SetServer(cx *core.Scope, base string) {
	a.API.Base = base
	a.Server.Set(base)
	cx.Storage().SetItem("server", base)
	a.Logout(cx)
}

// OpenDetail 记录上下文并跳到详情页签。
func (a *App) OpenDetail(title, cover, detailURL string) {
	a.DetailTitle.Set(title)
	a.DetailCover.Set(cover)
	a.DetailURL.Set(detailURL)
	a.Tab.Set("detail")
}

// OpenPlayer 记录播放上下文并跳到播放器。
func (a *App) OpenPlayer(title, playURL, sourceName, sourceURL string) {
	a.PlayTitle.Set(title)
	a.PlayURL.Set(playURL)
	a.PlaySource.Set(sourceName)
	a.PlaySourceURL.Set(sourceURL)
	a.Tab.Set("player")
}
