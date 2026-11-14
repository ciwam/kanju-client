package app

import (
	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/desktop"
)

// Shell 已登录的主外壳：单内容区（页内导航），设置走菜单栏。
func Shell(cx *core.Scope, a *App) core.Node {
	// macOS 菜单栏：首项是 app 菜单；「设置…」⌘, 唤出设置页。
	cx.Effect(func() {
		core.Untracked(cx, func() bool {
			desktop.SetApplicationMenu(cx, []desktop.MenuItem{
				{Items: []desktop.MenuItem{
					{Role: desktop.MenuAbout},
					{Separator: true},
					{Role: desktop.MenuHide},
					{Role: desktop.MenuHideOthers},
					{Role: desktop.MenuShowAll},
					{Separator: true},
					{Role: desktop.MenuQuit},
				}},
				{Label: "编辑", Items: []desktop.MenuItem{
					{Role: desktop.MenuUndo},
					{Role: desktop.MenuRedo},
					{Separator: true},
					{Role: desktop.MenuCut},
					{Role: desktop.MenuCopy},
					{Role: desktop.MenuPaste},
					{Role: desktop.MenuSelectAll},
				}},
				{Label: "显示", Items: []desktop.MenuItem{
					{Role: desktop.MenuToggleFullScreen},
				}},
				{Label: "设置", Items: []desktop.MenuItem{
					{Label: "设置…", Shortcut: "mod+,", OnSelect: func() {
						a.Navigate("settings")
					}},
				}},
			})
			return true
		})
	})

	return core.View{
		TestID: "shell",
		StyleFunc: func() core.Style {
			return theme.Fill().Merge(core.Style{Overflow: core.Hidden})
		},
		Children: []core.Node{core.Dynamic(func(*core.Scope) core.Node {
			// 首页面板（搜索/历史/收藏）在 page 层分发：与 detail/player
			// 同一 Dynamic——嵌套 Dynamic 的区域替换踩框架 commit 丢变更
			// bug（列表 splice 同源），平铺分发可靠。
			tab := a.Tab.Get()
			if tab == "home" {
				switch a.HomeView.Get() {
				case "search":
					return searchPanel(cx, a)
				case "history":
					return historyPanel(cx, a)
				case "favorites":
					return favoritesPanel(cx, a)
				}
			}
			return page(cx, a, tab)
		})},
	}
}

func userName(a *App) string {
	if u := a.User.Get(); u != nil {
		return u.Username + "（" + u.Role + "）"
	}
	return ""
}

// pageTitle 非首页页签的标题（首页自己管理头部）。
func pageTitle(id string) string {
	switch id {
	case "detail":
		return "影片详情"
	case "player":
		return "正在播放"
	case "settings":
		return "设置"
	}
	return "Kanju"
}

// page 按页签分发页面视图，并记录页签挂载。
func page(cx *core.Scope, a *App, id string) core.Node {
	ClientLog("[nav] 页面挂载 %s", id)
	switch id {
	case "detail":
		return SubjectDetail(cx, a)
	case "player":
		return Player(cx, a)
	case "settings":
		return Settings(cx, a)
	default:
		return Home(cx, a)
	}
}
