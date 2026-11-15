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
		Children: []core.Node{shellPages(cx, a)},
	}
}

// shellPages 页签容器。
//
// 为什么不用 core.Dynamic 按页签区域替换：框架缺陷 #1（docs/nsgo-bugs.md）
// ——短时间连续两次区域替换，第二次的提交会丢。实锤：detail 聚合
// 进行中点集数换源成功切 player，渲染树停在 detail/home（headless
// 可复现，渲染树显示停在 home）。
//
// 显隐方案尝试过 DisplayNone：nsgo 对 DisplayNone 子树**不发 mount**，
// 页面根本不存在，比区域替换更糟。最终方案：区域替换保留，但把
// 「切换瞬间与其它信号变更同批」解除——navGen 递增信号与 Tab 一起
// track，Dynamic 重跑时以 navGen 值为 key 判断是否真的换了页；同批
// 的其它信号变更（busy 清空等）不再与 tab 变更挤在一个替换里。
// 若 #1 仍偶发，后备方案是 cx.Post 分批投递 Tab.Set。
func shellPages(cx *core.Scope, a *App) core.Node {
	tab := core.NewMemo(cx, func() string { return a.Tab.Get() })
	homeView := core.NewMemo(cx, func() string {
		if tab.Get() == "home" {
			return a.HomeView.Get()
		}
		return "" // 非 home 页签时 home 面板固定回浏览视图
	})
	visible := func(active bool) core.Style {
		st := core.Style{FlexGrow: 1, FlexShrink: 1, FlexBasis: core.Pct(100)}
		if !active {
			st.Display = core.DisplayNone
		}
		return st
	}
	// 页面节点的显隐样式是响应式的——DisplayNone 的样式提交不销毁
	// 子树，页内状态（播放器进度、详情聚合结果、海报网格滚动位置）保留。
	pageStyle := func(active func() bool) func() core.Style {
		return func() core.Style { return visible(active()) }
	}
	// home 的四个形态也常驻显隐：面板切换（OpenSearch 等）同样走
	// HomeView 信号，区域替换会踩 #1。
	homeBrowse := core.View{StyleFunc: pageStyle(func() bool { return tab.Get() == "home" && homeView.Get() == "" }), Children: []core.Node{Home(cx, a)}}
	homeSearch := core.View{StyleFunc: pageStyle(func() bool { return tab.Get() == "home" && homeView.Get() == "search" }), Children: []core.Node{searchPanel(cx, a)}}
	homeHistory := core.View{StyleFunc: pageStyle(func() bool { return tab.Get() == "home" && homeView.Get() == "history" }), Children: []core.Node{historyPanel(cx, a)}}
	homeFavorites := core.View{StyleFunc: pageStyle(func() bool { return tab.Get() == "home" && homeView.Get() == "favorites" }), Children: []core.Node{favoritesPanel(cx, a)}}
	detailPage := core.View{StyleFunc: pageStyle(func() bool { return tab.Get() == "detail" }), Children: []core.Node{SubjectDetail(cx, a)}}
	playerPage := core.View{StyleFunc: pageStyle(func() bool { return tab.Get() == "player" }), Children: []core.Node{Player(cx, a)}}
	settingsPage := core.View{StyleFunc: pageStyle(func() bool { return tab.Get() == "settings" }), Children: []core.Node{Settings(cx, a)}}
	return core.View{
		Style:    core.Style{FlexGrow: 1, FlexShrink: 1, Overflow: core.Hidden},
		Children: []core.Node{homeBrowse, homeSearch, homeHistory, homeFavorites, detailPage, playerPage, settingsPage},
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

