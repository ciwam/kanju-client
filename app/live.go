package app

import (
	"kanju/client/api"
	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// Live 直播页：分组 + 频道列表，点击播放。
func Live(cx *core.Scope, a *App) core.Node {
	groups := core.NewSignalFunc(cx, []string(nil), func(a, b []string) bool { return false })
	current := core.NewSignal(cx, "")
	items := core.NewSignalFunc(cx, []api.LiveChannel(nil), func(a, b []api.LiveChannel) bool { return false })
	loading := core.NewSignal(cx, false)
	errMsg := core.NewSignal(cx, "")

	refresh := func() {
		loading.Set(true)
		errMsg.Set("")
		client := a.API
		go func() {
			all, err := client.LiveChannels("")
			cx.Post(func() {
				loading.Set(false)
				if err != nil {
					errMsg.Set(err.Error())
					return
				}
				gs := []string{}
				seen := map[string]bool{}
				for _, c := range all {
					if !seen[c.GroupName] {
						seen[c.GroupName] = true
						gs = append(gs, c.GroupName)
					}
				}
				groups.Set(gs)
				g := current.Get()
				if g == "" && len(gs) > 0 {
					g = gs[0]
				}
				current.Set(g)
				filtered := []api.LiveChannel{}
				for _, c := range all {
					if c.GroupName == g {
						filtered = append(filtered, c)
					}
				}
				items.Set(filtered)
			})
		}()
	}
	cx.Effect(func() {
		core.Untracked(cx, func() bool { refresh(); return true })
	})

	return core.View{
		StyleFunc: func() core.Style {
			return theme.Fill().Merge(core.Style{Padding: core.Symmetric(16, 20), Gap: 12, Overflow: core.Hidden})
		},
		Children: []core.Node{
			core.Dynamic(func(*core.Scope) core.Node {
				row := core.View{Style: core.Style{Direction: core.Row, Gap: 6, Wrap: core.Wrap}}
				for _, g := range groups.Get() {
					g := g
					row.Children = append(row.Children, chipButton(cx, g, current.Get() == g, func() {
						current.Set(g)
						refresh()
					}))
				}
				if len(groups.Get()) == 0 {
					return core.View{}
				}
				return row
			}),
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
					return theme.Empty(cx, "没有直播频道")
				}
				return core.List{
					Count:         func() int { return len(list) },
					Key:           func(i int) any { return list[i].ID },
					EstimatedSize: 44,
					Viewport:      core.ListViewportParent,
					Row: func(cx *core.Scope, r core.ListRow) core.Node {
						ch := list[r.Index()]
						return liveRow(cx, a, ch)
					},
					Style: core.Style{FlexGrow: 1, FlexShrink: 1},
				}
			}),
		},
	}
}

func liveRow(cx *core.Scope, a *App, ch api.LiveChannel) core.View {
	return core.View{
		StyleFunc: func() core.Style {
			return core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 10, Padding: core.Symmetric(6, 8)}
		},
		Children: []core.Node{
			ui.Text(ch.Name, 14, ui.Medium, nil),
		},
		OnPress: func(core.PressEvent) {
			a.OpenPlayer(ch.Name, ch.URL, "直播", "")
		},
	}
}
