package app

import (
	"time"

	"kanju/client/api"
	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// historyPanel 首页的历史面板（替代原独立历史页签）。
func historyPanel(cx *core.Scope, a *App) core.Node {
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
	// 面板打开时刷新（常驻后 effect 只挂载一次，需 track 面板可见性）。
	cx.Effect(func() {
		if a.Tab.Get() == "home" && a.HomeView.Get() == "history" {
			core.Untracked(cx, func() bool { refresh(); return true })
		}
	})

	return panelFrame(cx, a, "观看历史", listPage(cx, a,
		func() int { return len(items.Get()) },
		func(i int) any { return items.Get()[i].ID },
		func(cx *core.Scope, i int) core.Node {
			h := items.Get()[i]
			return historyRow(cx, a, h.Title+" · "+h.EpisodeTitle, h.Cover, h.SourceName,
				relTime(h.CreatedAt), progressFraction(h.Position, h.Duration), fmtPos(h.Position),
				func() {
					if h.DetailURL != "" {
						// 进详情页（B 站历史点击行为），可看简介选集再播。
						a.OpenDetail(h.Title, h.Cover, h.DetailURL)
						return
					}
					a.OpenPlayer(h.Title+" · "+h.EpisodeTitle, h.PlayURL, h.PlayURL, h.SourceName, h.SourceURL)
				})
		},
		loading, errMsg, refresh, "还没有观看记录",
	))
}

// favoritesPanel 首页的收藏面板（替代原独立收藏页签）。
func favoritesPanel(cx *core.Scope, a *App) core.Node {
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
	// 面板打开时刷新（常驻后 effect 只挂载一次，需 track 面板可见性）。
	cx.Effect(func() {
		if a.Tab.Get() == "home" && a.HomeView.Get() == "favorites" {
			core.Untracked(cx, func() bool { refresh(); return true })
		}
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

	confirmRemove := func(f api.Favorite) {
		base.ShowAlertDialog(cx, base.AlertDialog{
			Title:       "取消收藏",
			Description: "确定取消收藏「" + f.Title + "」吗？",
			Actions: []core.AlertAction{
				{Label: "取消", Role: core.AlertCancel},
				{Label: "确定", Role: core.AlertDestructive},
			},
		}, func(label string) {
			if label == "确定" {
				remove(f)
			}
		})
	}

	return panelFrame(cx, a, "我的收藏", listPage(cx, a,
		func() int { return len(items.Get()) },
		func(i int) any { return items.Get()[i].ID },
		func(cx *core.Scope, i int) core.Node {
			f := items.Get()[i]
			return historyRow(cx, a, f.Title, f.Cover, f.SourceName,
				"收藏", 0, "", func() {
					a.OpenDetail(f.Title, f.Cover, f.DetailURL)
				}, func() { confirmRemove(f) })
		},
		loading, errMsg, refresh, "还没有收藏",
	))
}

// panelFrame 面板外壳：仅内容。返回与标题已上移全局顶栏
// （appTopBar；HomeView!="" 时显示返回，关闭面板回分类浏览）。
func panelFrame(cx *core.Scope, a *App, title string, content core.Node) core.View {
	_ = a
	_ = title
	v, _ := content.(core.View)
	return v
}

// backButton 详情/播放器/设置页的左上角返回。
func backButton(cx *core.Scope, a *App) core.Node {
	return ui.Button{Text: "返回", Variant: ui.ButtonGhost, Size: ui.SizeSmall, TestID: "back", OnPress: func() {
		a.Back()
	}}
}

// listPage 通用列表页骨架。
func listPage(cx *core.Scope, a *App,
	count func() int, key func(int) any, row func(*core.Scope, int) core.Node,
	loading *core.Signal[bool], errMsg *core.Signal[string], refresh func(), emptyText string) core.View {
	return core.View{
		StyleFunc: func() core.Style {
			return core.Style{Gap: 12, FlexGrow: 1, FlexShrink: 1, Overflow: core.Hidden}
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
					Viewport:      core.ListViewportOwn,
					// Row 内容响应式包一层 Dynamic：cell 跨数据复用时
					// 依赖 pos.index 变化重渲染（同 home/search）。
					Row: func(cx *core.Scope, r core.ListRow) core.Node {
						return core.Dynamic(func(*core.Scope) core.Node {
							return row(cx, r.Index())
						})
					},
					Style: core.Style{FlexGrow: 1, FlexShrink: 1},
				}
			}),
		},
	}
}

// historyRow 一条记录：海报 + 标题 + 进度条/相对时间，可选删除按钮。
func historyRow(cx *core.Scope, a *App, title, cover, source, sub string, fraction float64, progressText string, open func(), remove ...func()) core.View {
	p := cx.Palette()
	// 右侧信息列：副信息 + 进度。
	var rightChildren []core.Node
	rightChildren = append(rightChildren, ui.Text(sub, 11, ui.Regular, func() core.Color { return p.TextMuted }))
	if fraction > 0 && progressText != "" {
		// 可视化进度条（海报下方细条）+ 已看时间。
		bar := core.View{
			Style: core.Style{FlexGrow: 1, Height: core.Pt(3), CornerRadius: 2,
				Background: p.SurfaceHover, Overflow: core.Hidden},
			Children: []core.Node{core.View{
				Style: core.Style{Height: core.Pt(3), Background: p.Accent,
					Width: core.Pct(float32(fraction * 100))},
			}},
		}
		rightChildren = append(rightChildren, theme.Row(6, bar, ui.Text(progressText, 10, ui.Regular, func() core.Color { return p.TextMuted })))
	}

	children := []core.Node{
		theme.Poster(a.API.ImageURL(cover), title, 44, 60),
		core.View{
			StyleFunc: func() core.Style { return core.Style{FlexGrow: 1, FlexShrink: 1, Gap: 3} },
			Children: []core.Node{
				theme.Title(title, 14, ui.Medium),
				ui.Text(source, 12, ui.Regular, func() core.Color { return p.TextMuted }),
			},
		},
		core.View{Style: core.Style{Gap: 4}, Children: rightChildren},
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
				Hover: &core.Style{Background: p.SurfaceHover},
			}
		},
		Children: children,
		OnPress:  func(core.PressEvent) { open() },
	}
}

// progressFraction 播放进度比例（0-1），数据不全返回 0。
func progressFraction(position, duration int64) float64 {
	if duration <= 0 || position <= 0 {
		return 0
	}
	f := float64(position) / float64(duration)
	if f > 1 {
		f = 1
	}
	return f
}

// relTime 相对时间：刚刚/N分钟前/N小时前/昨天/日期。
func relTime(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		// 后端可能是 "2026-11-14 01:02:03"。
		if t2, err2 := time.ParseInLocation("2006-01-02 15:04:05", ts, time.Local); err2 == nil {
			t = t2
		} else {
			return ""
		}
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return itoa(int64(d.Minutes())) + "分钟前"
	case d < 24*time.Hour:
		return itoa(int64(d.Hours())) + "小时前"
	case d < 48*time.Hour:
		return "昨天"
	default:
		return t.Format("01-02")
	}
}
