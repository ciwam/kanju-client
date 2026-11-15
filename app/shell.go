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
			st := theme.Fill().Merge(core.Style{Overflow: core.Hidden})
			// 手机 edge-to-edge：内容铺到系统栏下，根容器消费安全区
			// （状态栏 top + 三键导航栏 bottom + 刘海左右），否则系统
			// UI 盖住顶栏与底部导航。SafeArea 是响应式 Edges，读取在
			// StyleFunc（样式提交，不销毁子树）。
			if theme.IsAndroid() {
				sa := cx.Env().SafeArea()
				st.Padding = core.Edges{Top: sa.Top, Bottom: sa.Bottom, Left: sa.Left, Right: sa.Right}
			}
			return st
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
			// 手机底部导航：首页/历史/收藏/我的（设置收进「我的」）。
			// 桌面不渲染（顶栏入口已够）；全屏时隐藏（沉浸）。
			core.Dynamic(func(*core.Scope) core.Node {
				if !theme.IsAndroid() || desktop.FullScreen(cx) {
					return core.View{}
				}
				return bottomNav(cx, a)
			}),
		},
	}
}

// bottomNav 手机底部导航条：四个等宽入口（首页/历史/收藏/我的），
// 当前项主色高亮。「我的」页签打开设置页（设置收在其中）。
func bottomNav(cx *core.Scope, a *App) core.Node {
	items := []struct {
		id, label string
		icon      func() *core.SVG
		press     func()
	}{
		{"home", "首页", lucide.House, func() { a.GoHome() }},
		{"history", "历史", lucide.Clock, func() { a.Tab.Set("home"); a.HomeView.Set("history") }},
		{"favorites", "收藏", lucide.Heart, func() { a.Tab.Set("home"); a.HomeView.Set("favorites") }},
		{"me", "我的", lucide.CircleUser, func() { a.Navigate("settings") }},
	}
	current := func(it struct {
		id, label string
		icon      func() *core.SVG
		press     func()
	}) bool {
		switch it.id {
		case "home":
			return a.Tab.Get() == "home" && a.HomeView.Get() == ""
		case "history":
			return a.Tab.Get() == "home" && a.HomeView.Get() == "history"
		case "favorites":
			return a.Tab.Get() == "home" && a.HomeView.Get() == "favorites"
		default:
			return a.Tab.Get() == "settings"
		}
	}
	row := core.View{
		StyleFunc: func() core.Style {
			p := cx.Palette()
			return core.Style{
				Direction: core.Row, AlignItems: core.AlignCenter, FlexShrink: 0,
				Padding:     core.Symmetric(4, 8),
				Background:  p.Surface,
				Borders:     core.Edges{Top: 1},
				BorderColors: core.EdgeColors{Top: p.Border},
				BorderStyle: core.BorderSolid,
			}
		},
	}
	for _, it := range items {
		it := it
		row.Children = append(row.Children, core.View{
			StyleFunc: func() core.Style {
				p := cx.Palette()
				st := core.Style{
					FlexGrow: 1, FlexShrink: 1, AlignItems: core.AlignCenter, Gap: 2,
					Padding: core.Symmetric(6, 4), CornerRadius: 10,
				}
				if current(it) {
					st.Color = p.Accent
				} else {
					st.Color = p.TextMuted
				}
				return st
			},
			Children: []core.Node{
				ui.Icon(it.icon, 20, func() core.Color {
					if current(it) {
						return cx.Palette().Accent
					}
					return cx.Palette().TextMuted
				}),
				core.Text{Content: it.label, Style: core.Style{FontSize: 11}},
			},
			OnPress: func(core.PressEvent) { it.press() },
		})
	}
	return row
}

// appBarStyle 顶栏容器样式：白底卡片浮于浅灰页面之上。
func appBarStyle(cx *core.Scope) core.Style {
	p := cx.Palette()
	return core.Style{
		Direction: core.Row, AlignItems: core.AlignCenter, Gap: 16,
		Padding:     theme.PagePadding(10),
		Background:  p.Surface,
		Borders:     core.Edges{Bottom: 1},
		BorderColors: core.EdgeColors{Bottom: p.Border},
		BorderStyle: core.BorderSolid,
		FlexShrink:  0,
	}
}

// appTopBar 全局顶栏：桌面 B 站形态（返回+logo、搜索框、历史/收藏/
// 设置图标）；手机（Android）形态为 左小太阳 logo、中搜索框、右主题
// 切换按钮（导航收进底部栏，见 bottomNav）。
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
	// 返回按钮前插一个占宽垫片（Android 无红绿灯，垫片为 0）。
	tb := desktop.TitleBar(cx)
	lightPad := core.View{Style: core.Style{Width: core.Pt(max(core.Points(tb.Left)-8, 0))}}

	// logo：手机用小太阳图标，桌面保留 Kanju 标题（保底导航一致）。
	logo := core.View{
		StyleFunc: func() core.Style {
			return core.Style{Hover: &core.Style{Opacity: core.Alpha(0.7)}, CornerRadius: 6, Padding: core.Symmetric(2, 6)}
		},
		Children: []core.Node{theme.Title("Kanju", 17, core.FontWeightSemibold)},
		OnPress:  func(core.PressEvent) { a.GoHome() },
	}
	if theme.IsAndroid() {
		logo = core.View{
			StyleFunc: func() core.Style {
				return core.Style{Padding: core.Symmetric(4, 6), Hover: &core.Style{Opacity: core.Alpha(0.7)}}
			},
			Children: []core.Node{ui.Icon(lucide.Sun, 22, func() core.Color { return cx.Palette().Accent })},
			OnPress:  func(core.PressEvent) { a.GoHome() },
		}
	}

	// 手机：导航收进底部栏，顶栏右侧只剩主题切换；桌面保持三图标。
	if theme.IsAndroid() {
		return theme.AppBar(cx,
			// left：logo
			[]core.Node{logo},
			// center：全局搜索框
			[]core.Node{
				core.View{
					Style:    core.Style{Direction: core.Row, Gap: 8, FlexGrow: 1, AlignItems: core.AlignCenter},
					Desktop:  core.DesktopViewProps{WindowDrag: core.WindowDragOff},
					Children: []core.Node{searchInput, searchBtn},
				},
			},
			// right：主题切换（亮/暗）。
			[]core.Node{themeToggleButton(cx, a)},
		)
	}

	return theme.AppBar(cx,
		// left：红绿灯垫片 + 返回 + 标题
		[]core.Node{
			lightPad,
			backVisible,
			logo,
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

// themeToggleButton 亮/暗切换：图标随当前外观（亮显示月亮=切暗，
// 暗显示太阳=切亮），偏好持久化到 Storage（App.ThemePref）。
func themeToggleButton(cx *core.Scope, a *App) core.Node {
	return core.Dynamic(func(*core.Scope) core.Node {
		dark := cx.Env().Dark()
		icon := lucide.Moon
		if dark {
			icon = lucide.Sun
		}
		return ui.Button{Variant: ui.ButtonGhost, Icon: icon, Label: "主题",
			TestID: "appbar-theme",
			OnPress: func() {
				switch a.ThemePref.Get() {
				case "dark":
					cx.SetAppearance(core.AppearanceLight)
					a.ThemePref.Set("light")
				case "light":
					cx.SetAppearance(core.AppearanceSystem)
					a.ThemePref.Set("")
				default: // 跟随系统 → 强制暗
					cx.SetAppearance(core.AppearanceDark)
					a.ThemePref.Set("dark")
				}
			}}
	})
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

