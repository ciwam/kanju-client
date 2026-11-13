package app

import (
	"kanju/client/api"
	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// Search 流式搜索页：输入关键词，SSE 逐站返回结果。
func Search(cx *core.Scope, a *App) core.Node {
	keyword := core.NewSignal(cx, "")
	running := core.NewSignal(cx, false)
	meta := core.NewSignal(cx, "")
	summary := core.NewSignal(cx, "")
	results := core.NewSignalFunc(cx, []api.SearchItem(nil), func(a, b []api.SearchItem) bool { return false })
	errMsg := core.NewSignal(cx, "")
	stopCh := make(chan struct{})

	submit := func() {
		kw := trimSpace(keyword.Get())
		if kw == "" || running.Get() {
			return
		}
		running.Set(true)
		errMsg.Set("")
		summary.Set("")
		meta.Set("")
		results.Set(nil)
		client := a.API
		go func() {
			err := client.SearchStream(kw, "", stopCh,
				func(m api.SearchMeta) {
					cx.Post(func() { meta.Set(itoa(int64(m.Total)) + " 个站源") })
				},
				func(s api.SearchSource) {
					cx.Post(func() {
						if s.Error != "" {
							summary.Set(summary.Get() + "〔" + s.SourceName + " 失败〕")
							return
						}
						if len(s.Items) > 0 {
							cur := append(results.Get(), s.Items...)
							results.Set(cur)
						}
					})
				},
				nil,
			)
			cx.Post(func() {
				running.Set(false)
				if err != nil {
					errMsg.Set(err.Error())
				}
			})
		}()
	}

	inputRow := core.View{
		StyleFunc: func() core.Style { return core.Style{Direction: core.Row, Gap: 8} },
		Children: []core.Node{
			ui.Input{Value: keyword.Get, OnChange: keyword.Set, Placeholder: "搜索片名…", TestID: "keyword",
				OnSubmit: func(string) { submit() }, Style: core.Style{FlexGrow: 1}},
			ui.Button{Text: "搜索", Variant: ui.ButtonPrimary, OnPress: func() { submit() }, TestID: "search"},
		},
	}

	return core.View{
		StyleFunc: func() core.Style {
			return theme.Fill().Merge(core.Style{Padding: core.Symmetric(16, 20), Gap: 12, Overflow: core.Hidden})
		},
		Children: []core.Node{
			inputRow,
			core.Dynamic(func(*core.Scope) core.Node {
				parts := ""
				if meta.Get() != "" {
					parts = meta.Get()
				}
				if running.Get() {
					if parts != "" {
						parts += " · "
					}
					parts += "搜索中…"
				}
				if summary.Get() != "" {
					parts += summary.Get()
				}
				if parts == "" {
					return core.View{}
				}
				return ui.Text(parts, 12, ui.Regular, func() core.Color { return cx.Palette().TextMuted })
			}),
			core.Dynamic(func(*core.Scope) core.Node {
				if e := errMsg.Get(); e != "" {
					return ui.Text(e, 13, ui.Regular, func() core.Color { return cx.Palette().Danger })
				}
				return core.View{}
			}),
			searchGrid(cx, a, results),
		},
	}
}

// searchGrid 搜索结果网格。
func searchGrid(cx *core.Scope, a *App, results *core.Signal[[]api.SearchItem]) core.Node {
	return core.Dynamic(func(*core.Scope) core.Node {
		list := results.Get()
		if len(list) == 0 {
			return theme.Empty(cx, "输入关键词开始搜索")
		}
		return core.List{
			Count:         func() int { return len(results.Get()) },
			Key:           func(i int) any { return results.Get()[i].URL },
			NumColumns:    4,
			EstimatedSize: 240,
			Viewport:      core.ListViewportParent,
			Row: func(cx *core.Scope, row core.ListRow) core.Node {
				it := results.Get()[row.Index()]
				return searchCell(cx, a, it)
			},
			Style: core.Style{FlexGrow: 1, FlexShrink: 1},
		}
	})
}

// searchCell 单个结果：点击直接进入详情聚合。
func searchCell(cx *core.Scope, a *App, it api.SearchItem) core.Node {
	return core.View{
		StyleFunc: func() core.Style { return core.Style{Gap: 6, Padding: core.Symmetric(0, 6)} },
		Children: []core.Node{
			theme.Poster(a.API.ImageURL(it.Cover), it.Title, 150, 210),
			theme.Title(it.Title, 13, ui.Medium),
			ui.Text(it.SourceName, 11, ui.Regular, func() core.Color { return cx.Palette().TextMuted }),
		},
		OnPress: func(core.PressEvent) {
			a.OpenDetail(it.Title, it.Cover, it.URL)
		},
	}
}
