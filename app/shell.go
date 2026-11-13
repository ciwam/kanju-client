package app

import (
	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// Shell 已登录的主外壳：桌面侧栏 + 内容区。
func Shell(cx *core.Scope, a *App) core.Node {
	return core.Dynamic(func(*core.Scope) core.Node {
		var sections []ui.SidebarSection
		var items []ui.SidebarItem
		user := a.User.Get()
		for _, t := range tabs {
			if t.Admin && (user == nil || user.Role != "admin") {
				continue
			}
			items = append(items, ui.SidebarItem{ID: t.ID, Text: t.Text, Icon: t.Icon, TestID: "nav-" + t.ID})
		}
		sections = append(sections, ui.SidebarSection{Items: items})

		// 内嵌模式：不显示用户身份与退出；外部服务器才显示 footer。
		var footer []core.Node
		if !a.Embedded.Get() {
			footer = append(footer,
				ui.Text(userName(a), 12, ui.Regular, func() core.Color { return cx.Palette().TextMuted }),
				ui.Button{Text: "退出登录", Variant: ui.ButtonGhost, Size: ui.SizeSmall,
					OnPress: func() { a.Logout(cx) }, TestID: "logout"},
			)
		}

		return ui.SidebarShell{
			TestID:        "shell",
			Sidebar:       []core.Node{ui.Sidebar{Sections: sections, Selected: a.Tab.Get, OnSelect: a.Tab.Set}},
			SidebarFooter: footer,
			Header: []core.Node{core.Dynamic(func(*core.Scope) core.Node {
				return theme.Title(tabTitle(a.Tab.Get()), 15, ui.Semibold)
			})},
			Content: []core.Node{core.Dynamic(func(*core.Scope) core.Node {
				return page(cx, a, a.Tab.Get())
			})},
		}
	})
}

func userName(a *App) string {
	if u := a.User.Get(); u != nil {
		return u.Username + "（" + u.Role + "）"
	}
	return ""
}

func tabTitle(id string) string {
	for _, t := range tabs {
		if t.ID == id {
			return t.Text
		}
	}
	return "Kanju"
}

// page 按页签分发页面视图。
func page(cx *core.Scope, a *App, id string) core.Node {
	switch id {
	case "home":
		return Home(cx, a)
	case "search":
		return Search(cx, a)
	case "detail":
		return SubjectDetail(cx, a)
	case "player":
		return Player(cx, a)
	case "history":
		return History(cx, a)
	case "favorites":
		return Favorites(cx, a)
	case "live":
		return Live(cx, a)
	case "settings":
		return Settings(cx, a)
	case "sources":
		return Sources(cx, a)
	default:
		return Home(cx, a)
	}
}
