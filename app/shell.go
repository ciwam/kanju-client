package app

import (
	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/desktop"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

// Shell 已登录的主外壳：单内容区（页内导航），设置走菜单栏。
func Shell(cx *core.Scope, a *App) core.Node {
	// B 站桌面版主题（主色蓝、浅灰页面底、卡片白）——整个外壳子树继承。
	cx.SetTheme(theme.BiliTheme())
	// 隐藏系统标题栏（B 站桌面版形态）：红绿灯保留在自绘顶栏内，
	// 顶栏即窗口拖动区。0 高度 = 平台默认值。
	desktop.SetTitleBar(cx, desktop.TitleBarHidden, 0)
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
		Children: []core.Node{
			// 全局顶栏常显（B 站桌面版样式，播放页也显示——返回保底）；
			// 全屏时隐藏（沉浸，样式提交不销毁子树）。
			core.View{
				StyleFunc: func() core.Style {
					st := appBarStyle(cx)
					if desktop.FullScreen(cx) {
						st.Display = core.DisplayNone
					}
					return st
				},
				Desktop:   core.DesktopViewProps{WindowDrag: core.WindowDragOn},
				Children:  []core.Node{appTopBar(cx, a)},
			},
			shellPages(cx, a),
		},
	}
}

// appBarStyle 顶栏容器样式：白底卡片浮于浅灰页面之上。
func appBarStyle(cx *core.Scope) core.Style {
	p := cx.Palette()
	return core.Style{
		Direction: core.Row, AlignItems: core.AlignCenter, Gap: 16,
		Padding:     core.Symmetric(10, 20),
		Background:  p.Surface,
		Borders:     core.Edges{Bottom: 1},
		BorderColors: core.EdgeColors{Bottom: p.Border},
		BorderStyle: core.BorderSolid,
		FlexShrink:  0,
	}
}

// appTopBar 全局顶栏（B 站桌面版）：左返回+logo、中全局搜索框、
// 右历史/收藏/设置图标入口。搜索提交复用 OpenSearch 链路（面板
// 自动发起搜索）；返回按钮条件显示（home 无面板时隐藏）。
func appTopBar(cx *core.Scope, a *App) core.Node {
	keyword := core.NewSignal(cx, "")
	searchInput := ui.Input{
		Value:       keyword.Get,
		OnChange:    keyword.Set,
		Placeholder: "搜索片名…",
		TestID:      "appbar-search",
		Style:       core.Style{FlexGrow: 1, MaxWidth: core.Pt(480)},
		OnSubmit:    func(string) { a.OpenSearch(keyword.Get()) },
	}
	searchBtn := ui.Button{Text: "搜索", Variant: ui.ButtonPrimary, TestID: "appbar-search-btn",
		OnPress: func() { a.OpenSearch(keyword.Get()) }}

	// 返回按钮：非 home 页签或 home 面板打开时显示。
	backVisible := core.Dynamic(func(*core.Scope) core.Node {
		show := a.Tab.Get() != "home" || a.HomeView.Get() != ""
		st := core.Style{}
		if !show {
			st.Display = core.DisplayNone
		}
		return core.View{Style: st, Children: []core.Node{backButton(cx, a)}}
	})

	// 右侧图标入口。
	entry := func(label, testID string, icon func() *core.SVG, press func()) core.Node {
		return ui.Button{Variant: ui.ButtonGhost, Icon: icon, Label: label, TestID: testID, OnPress: press}
	}

	// 红绿灯避让：隐藏标题栏后 TitleBar().Left 是系统控件占宽，
	// 返回按钮前插一个占宽垫片。
	tb := desktop.TitleBar(cx)
	lightPad := core.View{Style: core.Style{Width: core.Pt(max(core.Points(tb.Left)-8, 0))}}

	return theme.AppBar(cx,
		// left：红绿灯垫片 + 返回 + 标题
		[]core.Node{
			lightPad,
			backVisible,
			// 保底导航：任何页面点 Kanju 标题回首页（影片推荐）。
			core.View{
				StyleFunc: func() core.Style {
					return core.Style{Hover: &core.Style{Opacity: core.Alpha(0.7)}, CornerRadius: 6, Padding: core.Symmetric(2, 6)}
				},
				Children: []core.Node{theme.Title("Kanju", 17, core.FontWeightSemibold)},
				OnPress:  func(core.PressEvent) { a.GoHome() },
			},
		},
		// center：全局搜索框
		[]core.Node{
			core.View{
				Style:    core.Style{Direction: core.Row, Gap: 8, FlexGrow: 1, AlignItems: core.AlignCenter, MaxWidth: core.Pt(560)},
				Desktop:  core.DesktopViewProps{WindowDrag: core.WindowDragOff},
				Children: []core.Node{searchInput, searchBtn},
			},
		},
		// right：历史 / 收藏 / 设置
		[]core.Node{
			entry("历史", "appbar-history", lucide.Clock, func() { a.Tab.Set("home"); a.HomeView.Set("history") }),
			entry("收藏", "appbar-favorites", lucide.Heart, func() { a.Tab.Set("home"); a.HomeView.Set("favorites") }),
			entry("设置", "appbar-settings", lucide.Settings, func() { a.Navigate("settings") }),
		},
	)
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
	settingsPage := core.View{StyleFunc: pageStyle(func() bool { return tab.Get() == "settings" }), Children: []core.Node{Settings(cx, a)}}
	return core.View{
		Style:    core.Style{FlexGrow: 1, FlexShrink: 1, Overflow: core.Hidden},
		Children: []core.Node{homeBrowse, homeSearch, homeHistory, homeFavorites, detailPage, settingsPage},
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

