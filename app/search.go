package app

import (
	"html"
	"time"

	"kanju/client/api"
	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// searchPanel 首页的搜索面板（替代原独立搜索页签）。keyword 初值
// 来自 App.SearchKeyword（其它页面进入搜索时预填）。
func searchPanel(cx *core.Scope, a *App) core.Node {
	keyword := core.NewSignal(cx, a.SearchKeyword.Get())
	running := core.NewSignal(cx, false)
	results := core.NewSignalFunc(cx, []api.DoubanItem(nil), func(a, b []api.DoubanItem) bool { return false })
	errMsg := core.NewSignal(cx, "")
	searched := core.NewSignal(cx, false) // 已搜过（区分未搜索的空态）

	// 竞态防护：关键词 key，过期响应丢弃。
	reqKey := core.NewSignal(cx, "")

	submit := func() {
		kw := trimSpace(keyword.Get())
		if kw == "" || running.Get() {
			return
		}
		reqKey.Set(kw)
		running.Set(true)
		errMsg.Set("")
		results.Set(nil)
		searched.Set(true)
		client := a.API
		go func() {
			list, err := client.DoubanSearch(kw)
			cx.Post(func() {
				running.Set(false)
				if reqKey.Get() != kw {
					return
				}
				if err != nil {
					errMsg.Set(err.Error())
					results.Set(nil)
				} else {
					results.Set(list)
				}
			})
		}()
	}

	inputRow := core.View{
		StyleFunc: func() core.Style { return core.Style{Direction: core.Row, Gap: 8, FlexGrow: 1, FlexShrink: 1} },
		Children: []core.Node{
			ui.Input{Value: keyword.Get, OnChange: keyword.Set, Placeholder: "搜索片名…", TestID: "keyword",
				OnSubmit: func(string) { submit() }, Style: core.Style{FlexGrow: 1}},
			ui.Button{Text: "搜索", Variant: ui.ButtonPrimary, OnPress: func() { submit() }, TestID: "search"},
		},
	}

	return core.View{
		StyleFunc: func() core.Style {
			return core.Style{Gap: 12, FlexGrow: 1, FlexShrink: 1, Overflow: core.Hidden}
		},
		Children: []core.Node{
			core.View{
				StyleFunc: func() core.Style {
					return core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 10}
				},
				Children: []core.Node{
					backButton(cx, a),
					theme.Title("搜索", 17, ui.Semibold),
					inputRow,
				},
			},
			core.Dynamic(func(*core.Scope) core.Node {
				// 状态行：搜索中 / 结果计数。
				var parts string
				if running.Get() {
					parts = "搜索中…"
				} else if n := len(results.Get()); n > 0 {
					parts = itoa(int64(n)) + " 个结果"
				}
				if parts == "" {
					return core.View{}
				}
				return ui.Text(parts, 12, ui.Regular, func() core.Color { return cx.Palette().TextMuted })
			}),
			core.Dynamic(func(*core.Scope) core.Node {
				if e := errMsg.Get(); e != "" {
					return theme.ErrorRetry(cx, e, func() { submit() })
				}
				return core.View{}
			}),
			core.Dynamic(func(*core.Scope) core.Node {
				// 首次搜索且无结果时显示骨架。
				if running.Get() && len(results.Get()) == 0 {
					return theme.SkeletonGrid(cx, 2, 4)
				}
				return core.View{}
			}),
			searchGrid(cx, a, results, searched),
		},
	}
}

// searchGrid 搜索结果网格。列表常驻，结果更新时保持滚动位置。
func searchGrid(cx *core.Scope, a *App, results *core.Signal[[]api.DoubanItem], searched *core.Signal[bool]) core.Node {
	const gap = core.Points(14)
	return core.List{
		Count:         func() int { return len(results.Get()) },
		Key:           func(i int) any { return results.Get()[i].URL },
		NumColumns:    4,
		EstimatedSize: core.Points(160/0.7) + 44,
		Viewport:      core.ListViewportOwn,
		Gap:           gap + 8,
		ColumnGap:     gap,
		Empty: core.Dynamic(func(*core.Scope) core.Node {
			if searched.Get() {
				return theme.Empty(cx, "未找到相关影片，试试其它关键词")
			}
			return theme.Empty(cx, "输入关键词开始搜索")
		}),
		Row: func(cx *core.Scope, row core.ListRow) core.Node {
			it := func() api.DoubanItem {
				list := results.Get()
				if i := row.Index(); i < len(list) {
					return list[i]
				}
				return api.DoubanItem{}
			}
			return core.Dynamic(func(*core.Scope) core.Node {
				return searchCell(cx, a, it())
			})
		},
		Style: core.Style{FlexGrow: 1, FlexShrink: 1, Padding: core.Symmetric(4, 0)},
	}
}

// searchCell 单个结果：点击进豆瓣详情（id 可用时）或原 URL。
func searchCell(cx *core.Scope, a *App, it api.DoubanItem) core.Node {
	// 豆瓣搜索结果常带 HTML 实体，解码后展示。
	title := html.UnescapeString(it.Title)
	return core.View{
		Style: core.Style{Gap: 6},
		Children: []core.Node{
			core.View{
				StyleFunc: func() core.Style {
					return core.Style{
						Width: core.Pct(100), AspectRatio: 0.7, CornerRadius: 6,
						Background: core.RGB(0xD9D9D9), Overflow: core.Hidden,
						Hover:  &core.Style{Background: core.RGB(0xC8C8C8)},
						Active: &core.Style{Transform: core.Transform{Scale: 0.98}},
					}
				},
				Children: []core.Node{
					core.Image{
						Source:       core.Src(a.API.ImageURL(it.Cover)),
						ResizeMode:   core.Cover,
						Label:        title,
						FadeDuration: time.Millisecond,
						Style:        core.Style{Width: core.Pct(100), Height: core.Pct(100)},
					},
					ratingBadge(cx, it.Rate),
				},
				OnPress: func(core.PressEvent) {
					if it.ID != "" {
						a.OpenDetail(title, it.Cover, "https://movie.douban.com/subject/"+it.ID+"/")
					} else {
						a.OpenDetail(title, it.Cover, it.URL)
					}
				},
			},
			theme.Title(title, 13, ui.Medium),
			posterSubtitle(cx, it),
		},
		OnPress: func(core.PressEvent) {
			if it.ID != "" {
				a.OpenDetail(title, it.Cover, "https://movie.douban.com/subject/"+it.ID+"/")
			} else {
				a.OpenDetail(title, it.Cover, it.URL)
			}
		},
	}
}
