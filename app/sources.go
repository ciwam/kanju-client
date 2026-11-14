package app

import (
	"kanju/client/api"
	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// SourcesCard 站源管理卡片（管理员）：列表 + 启停 + 删除 + 新增。
// 挂在设置页，不再是独立页签。
func SourcesCard(cx *core.Scope, a *App) core.Node {
	items := core.NewSignalFunc(cx, []api.SiteSource(nil), func(a, b []api.SiteSource) bool { return false })
	loading := core.NewSignal(cx, false)
	errMsg := core.NewSignal(cx, "")
	newName := core.NewSignal(cx, "")
	newBase := core.NewSignal(cx, "")

	refresh := func() {
		loading.Set(true)
		errMsg.Set("")
		client := a.API
		go func() {
			list, err := client.Sources()
			cx.Post(func() {
				loading.Set(false)
				if err != nil {
					errMsg.Set(err.Error())
				} else {
					items.Set(list)
				}
			})
		}()
	}
	cx.Effect(func() {
		core.Untracked(cx, func() bool { refresh(); return true })
	})

	toggle := func(s api.SiteSource) {
		client := a.API
		go func() {
			err := client.ToggleSource(s.BaseURL)
			cx.Post(func() {
				if err != nil {
					errMsg.Set(err.Error())
				} else {
					refresh()
				}
			})
		}()
	}
	del := func(s api.SiteSource) {
		client := a.API
		go func() {
			err := client.DeleteSource(s.BaseURL)
			cx.Post(func() {
				if err != nil {
					errMsg.Set(err.Error())
				} else {
					refresh()
				}
			})
		}()
	}
	add := func() {
		name, base := trimSpace(newName.Get()), trimSpace(newBase.Get())
		if base == "" {
			return
		}
		if name == "" {
			name = base
		}
		client := a.API
		go func() {
			err := client.AddSource(api.SiteSource{Name: name, BaseURL: base})
			cx.Post(func() {
				if err != nil {
					errMsg.Set(err.Error())
				} else {
					newName.Set("")
					newBase.Set("")
					refresh()
				}
			})
		}()
	}

	return theme.Card(cx,
		core.View{
			StyleFunc: func() core.Style { return core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8} },
			Children: []core.Node{
				theme.Title("站源管理", 14, ui.Semibold),
				core.View{
					StyleFunc: func() core.Style {
						return core.Style{FlexGrow: 1, FlexShrink: 1, Direction: core.Row, Gap: 8}
					},
					Children: []core.Node{
						ui.Input{Value: newName.Get, OnChange: newName.Set, Placeholder: "名称", Style: core.Style{Width: core.Pt(120)}},
						ui.Input{Value: newBase.Get, OnChange: newBase.Set, Placeholder: "https://…", Style: core.Style{FlexGrow: 1}},
						ui.Button{Text: "添加", Variant: ui.ButtonPrimary, Size: ui.SizeSmall, OnPress: add},
					},
				},
			},
		},
		core.Dynamic(func(*core.Scope) core.Node {
			switch {
			case loading.Get():
				return theme.Row(8, ui.Spinner{}, ui.Text("加载中…", 13, ui.Regular, func() core.Color { return cx.Palette().TextMuted }))
			case errMsg.Get() != "":
				return ui.Text(errMsg.Get(), 13, ui.Regular, func() core.Color { return cx.Palette().Danger })
			default:
				return core.View{}
			}
		}),
		core.Dynamic(func(*core.Scope) core.Node {
			list := items.Get()
			if len(list) == 0 {
				return theme.Empty(cx, "还没有站源")
			}
			return core.List{
				Count:         func() int { return len(list) },
				Key:           func(i int) any { return list[i].BaseURL },
				EstimatedSize: 52,
				Viewport:      core.ListViewportOwn,
				Row: func(cx *core.Scope, r core.ListRow) core.Node {
					// Dynamic 包裹：cell 复用时跟随 index 信号重渲染。
					return core.Dynamic(func(*core.Scope) core.Node {
						list := items.Get()
						if r.Index() >= len(list) {
							return core.View{}
						}
						return sourceRow(cx, list[r.Index()], toggle, del)
					})
				},
				Style: core.Style{FlexGrow: 1, FlexShrink: 1, Height: core.Pt(180)},
			}
		}),
	)
}

func sourceRow(cx *core.Scope, s api.SiteSource, toggle, del func(api.SiteSource)) core.View {
	status := "已启用"
	if !s.Enabled {
		status = "已停用"
	}
	return core.View{
		StyleFunc: func() core.Style {
			return core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 10, Padding: core.Symmetric(6, 8)}
		},
		Children: []core.Node{
			core.View{
				StyleFunc: func() core.Style { return core.Style{FlexGrow: 1, FlexShrink: 1, Gap: 2} },
				Children: []core.Node{
					theme.Title(s.Name, 14, ui.Medium),
					ui.Text(s.BaseURL, 12, ui.Regular, func() core.Color { return cx.Palette().TextMuted }),
				},
			},
			ui.Text(status, 12, ui.Regular, func() core.Color { return cx.Palette().TextMuted }),
			ui.Button{Text: "启停", Variant: ui.ButtonSecondary, Size: ui.SizeSmall, OnPress: func() { toggle(s) }},
			ui.Button{Text: "删除", Variant: ui.ButtonGhost, Size: ui.SizeSmall, OnPress: func() { del(s) }},
		},
	}
}
