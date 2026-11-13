package app

import (
	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// Settings 设置页：服务器地址、账号信息。
func Settings(cx *core.Scope, a *App) core.Node {
	draft := core.NewSignal(cx, a.Server.Get())
	msg := core.NewSignal(cx, "")

	save := func() {
		s := trimSpace(draft.Get())
		if s == "" || s == a.Server.Get() {
			return
		}
		a.SetServer(cx, s)
		msg.Set("已切换服务器，请重新登录")
	}

	return core.View{
		StyleFunc: func() core.Style {
			return theme.Fill().Merge(core.Style{Padding: core.Symmetric(16, 20), Gap: 14})
		},
		Children: []core.Node{
			theme.Title("设置", 20, ui.Semibold),
			theme.Card(cx,
				theme.Title("服务器", 14, ui.Semibold),
				core.View{
					StyleFunc: func() core.Style { return core.Style{Direction: core.Row, Gap: 8} },
					Children: []core.Node{
						ui.Input{Value: draft.Get, OnChange: draft.Set, Placeholder: "http://…", TestID: "server",
							Style: core.Style{FlexGrow: 1}},
						ui.Button{Text: "保存", Variant: ui.ButtonPrimary, OnPress: save},
					},
				},
				core.Dynamic(func(*core.Scope) core.Node {
					m := msg.Get()
					if m == "" {
						return core.View{}
					}
					return ui.Text(m, 12, ui.Regular, func() core.Color { return cx.Palette().Danger })
				}),
			),
			core.Dynamic(func(*core.Scope) core.Node {
				// 内嵌模式无账号概念，隐藏账号卡片。
				if a.Embedded.Get() {
					return core.View{}
				}
				return theme.Card(cx,
					theme.Title("账号", 14, ui.Semibold),
					ui.Text(userName(a), 13, ui.Regular, func() core.Color { return cx.Palette().TextMuted }),
					ui.Button{Text: "退出登录", Variant: ui.ButtonSecondary, OnPress: func() { a.Logout(cx) }},
				)
			}),
		},
	}
}
