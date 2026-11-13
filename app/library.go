package app

import (
	"kanju/client/api"
	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// History 观看历史。
func History(cx *core.Scope, a *App) core.Node {
	items := core.NewSignalFunc(cx, []api.History(nil), func(a, b []api.History) bool { return false })
	loading := core.NewSignal(cx, false)
	errMsg := core.NewSignal(cx, "")

	refresh := func() {
		loading.Set(true)
		errMsg.Set("")
		client := a.API
		go func() {
			list, err := client.Histories()
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

	return listPage(cx, a,
		func() int { return len(items.Get()) },
		func(i int) any { return items.Get()[i].ID },
		func(cx *core.Scope, i int) core.Node {
			h := items.Get()[i]
			progress := ""
			if h.Duration > 0 && h.Position > 0 {
				progress = fmtPos(h.Position) + " / " + fmtPos(h.Duration)
			}
			return historyRow(cx, a, h.Title+" · "+h.EpisodeTitle, h.Cover, h.SourceName, progress, func() {
				a.OpenPlayer(h.Title+" · "+h.EpisodeTitle, h.PlayURL, h.SourceName, h.SourceURL)
			})
		},
		loading, errMsg, refresh, "还没有观看记录",
	)
}

// Favorites 收藏。
func Favorites(cx *core.Scope, a *App) core.Node {
	items := core.NewSignalFunc(cx, []api.Favorite(nil), func(a, b []api.Favorite) bool { return false })
	loading := core.NewSignal(cx, false)
	errMsg := core.NewSignal(cx, "")

	refresh := func() {
		loading.Set(true)
		errMsg.Set("")
		client := a.API
		go func() {
			list, err := client.Favorites()
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

		remove := func(f api.Favorite) {
			client := a.API
			go func() {
				err := client.RemoveFavorite(f.DetailURL)
				cx.Post(func() {
					if err != nil {
						errMsg.Set(err.Error())
					} else {
						refresh()
					}
				})
			}()
		}

	return listPage(cx, a,
		func() int { return len(items.Get()) },
		func(i int) any { return items.Get()[i].ID },
		func(cx *core.Scope, i int) core.Node {
			f := items.Get()[i]
			return historyRow(cx, a, f.Title, f.Cover, f.SourceName, "收藏", func() {
				a.OpenDetail(f.Title, f.Cover, f.DetailURL)
			}, func() { remove(f) })
		},
		loading, errMsg, refresh, "还没有收藏",
	)
}

// listPage 通用列表页骨架。
func listPage(cx *core.Scope, a *App,
	count func() int, key func(int) any, row func(*core.Scope, int) core.Node,
	loading *core.Signal[bool], errMsg *core.Signal[string], refresh func(), emptyText string) core.View {
	return core.View{
		StyleFunc: func() core.Style {
			return theme.Fill().Merge(core.Style{Padding: core.Symmetric(16, 20), Gap: 12, Overflow: core.Hidden})
		},
		Children: []core.Node{
			theme.Row(8,
				ui.Button{Text: "刷新", Variant: ui.ButtonSecondary, OnPress: refresh},
				core.Dynamic(func(*core.Scope) core.Node {
					switch {
					case loading.Get():
						return ui.Text("加载中…", 12, ui.Regular, func() core.Color { return cx.Palette().TextMuted })
					case errMsg.Get() != "":
						return ui.Text(errMsg.Get(), 12, ui.Regular, func() core.Color { return cx.Palette().Danger })
					default:
						return core.View{}
					}
				}),
			),
			core.Dynamic(func(*core.Scope) core.Node {
				if count() == 0 {
					if loading.Get() {
						return core.View{}
					}
					return theme.Empty(cx, emptyText)
				}
				return core.List{
					Count:         count,
					Key:           key,
					EstimatedSize: 76,
					Viewport:      core.ListViewportParent,
					Row: func(cx *core.Scope, r core.ListRow) core.Node {
						return row(cx, r.Index())
					},
					Style: core.Style{FlexGrow: 1, FlexShrink: 1},
				}
			}),
		},
	}
}

// historyRow 一条记录：海报 + 标题 + 副信息，可选删除按钮。
func historyRow(cx *core.Scope, a *App, title, cover, source, sub string, open func(), remove ...func()) core.View {
	children := []core.Node{
		theme.Poster(a.API.ImageURL(cover), title, 44, 60),
		core.View{
			StyleFunc: func() core.Style { return core.Style{FlexGrow: 1, FlexShrink: 1, Gap: 3} },
			Children: []core.Node{
				theme.Title(title, 14, ui.Medium),
				ui.Text(source, 12, ui.Regular, func() core.Color { return cx.Palette().TextMuted }),
			},
		},
		ui.Text(sub, 12, ui.Regular, func() core.Color { return cx.Palette().TextMuted }),
	}
	if len(remove) > 0 {
		r := remove[0]
		children = append(children, ui.Button{Text: "删除", Variant: ui.ButtonGhost, Size: ui.SizeSmall,
			OnPress: func() { r() }})
	}
	return core.View{
		StyleFunc: func() core.Style {
			return core.Style{
				Direction: core.Row, AlignItems: core.AlignCenter, Gap: 12,
				Padding: core.Symmetric(8, 8), CornerRadius: 8,
			}
		},
		Children: children,
		OnPress:  func(core.PressEvent) { open() },
	}
}
