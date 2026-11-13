package app

import (
	"kanju/client/api"
	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// tvTabs 与 movieTabs 复刻 Web 版首页的豆瓣分类。
var tvTabs = []tabGroup{
	{Label: "热门剧集", Children: []tabOpt{
		{Label: "综合", Tag: "热门"}, {Label: "国产剧", Tag: "国产剧"}, {Label: "欧美剧", Tag: "美剧"},
		{Label: "日剧", Tag: "日剧"}, {Label: "韩剧", Tag: "韩剧"}, {Label: "动画", Tag: "日本动画"},
		{Label: "纪录片", Tag: "纪录片"},
	}},
	{Label: "热门综艺", Children: []tabOpt{
		{Label: "综合", Tag: "综艺"}, {Label: "国内", Tag: "综艺"}, {Label: "国外", Tag: "综艺"},
	}},
}

var movieTabs = []tabGroup{
	{Label: "热门电影", Children: []tabOpt{
		{Label: "全部", Tag: "热门"}, {Label: "华语", Tag: "华语"}, {Label: "欧美", Tag: "欧美"},
		{Label: "韩国", Tag: "韩国"}, {Label: "日本", Tag: "日本"},
	}},
	{Label: "最新电影", Tag: "最新"},
	{Label: "豆瓣高分", Tag: "豆瓣高分"},
	{Label: "冷门佳片", Tag: "冷门佳片"},
}

type tabGroup struct {
	Label    string
	Tag      string
	Children []tabOpt
}

type tabOpt struct {
	Label string
	Tag   string
}

// Home 首页：电影/剧集分类 + 封面网格。
func Home(cx *core.Scope, a *App) core.Node {
	kind := core.NewSignal(cx, "tv") // tv | movie
	sub := core.NewSignal(cx, 0)
	child := core.NewSignal(cx, 0)
	items := core.NewSignalFunc(cx, []api.DoubanItem(nil), func(a, b []api.DoubanItem) bool { return false })
	loading := core.NewSignal(cx, false)
	errMsg := core.NewSignal(cx, "")

	groups := func() []tabGroup {
		if kind.Get() == "tv" {
			return tvTabs
		}
		return movieTabs
	}

	currentTag := func() (tag, sort string) {
		gs := groups()
		si := sub.Get()
		if si < 0 || si >= len(gs) {
			return "热门", ""
		}
		g := gs[si]
		if len(g.Children) == 0 {
			return g.Tag, ""
		}
		ci := child.Get()
		if ci < 0 || ci >= len(g.Children) {
			ci = 0
		}
		if kind.Get() == "tv" && si == 1 && ci == 1 {
			return g.Children[ci].Tag, "time"
		}
		return g.Children[ci].Tag, ""
	}

	load := func() {
		tag, sort := currentTag()
		loading.Set(true)
		errMsg.Set("")
		client := a.API
		k := kind.Get()
		go func() {
			list, err := client.DoubanList(k, tag, sort, 30, 0)
			cx.Post(func() {
				loading.Set(false)
				if err != nil {
					errMsg.Set(err.Error())
					items.Set(nil)
				} else {
					items.Set(list)
				}
			})
		}()
	}

	// 参数变化即重载；kind/sub/child 的读取被 track。
	cx.Effect(func() {
		kind.Get()
		sub.Get()
		child.Get()
		core.Untracked(cx, func() bool { load(); return true })
	})

	return core.View{
		StyleFunc: func() core.Style {
			return theme.Fill().Merge(core.Style{Padding: core.Symmetric(16, 20), Gap: 12, Overflow: core.Hidden})
		},
		Children: []core.Node{
			kindSwitcher(cx, kind, sub, child, groups),
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
			posterGrid(cx, a, items),
		},
	}
}

// kindSwitcher 顶部：电影/剧集分段 + 分类 chips。
func kindSwitcher(cx *core.Scope, kind *core.Signal[string], sub, child *core.Signal[int], groups func() []tabGroup) core.Node {
	return core.Dynamic(func(*core.Scope) core.Node {
		gs := groups()
		si := sub.Get()
		if si >= len(gs) {
			si = 0
		}
		g := gs[si]

		// 电影/剧集分段控制。
		seg := core.View{Style: core.Style{Direction: core.Row, Gap: 0, CornerRadius: 7,
			BorderColor: cx.Palette().Border, BorderWidth: 1, BorderStyle: core.BorderSolid, Overflow: core.Hidden}}
		seg.Children = append(seg.Children,
			segItem(cx, "剧集", kind.Get() == "tv", func() { resetKind(kind, sub, child, "tv") }),
			segItem(cx, "电影", kind.Get() != "tv", func() { resetKind(kind, sub, child, "movie") }),
		)

		// 组 chips（有子项的组显示组名，没有的组直接显示 tag）。
		chipRow := core.View{Style: core.Style{Direction: core.Row, Gap: 6, Wrap: core.Wrap}}
		for i, grp := range gs {
			i, grp := i, grp
			label := grp.Label
			if len(grp.Children) == 0 {
				label = grp.Tag
			}
			chipRow.Children = append(chipRow.Children, chipButton(cx, label, si == i, func() {
				sub.Set(i)
				child.Set(0)
			}))
		}

		// 子项 chips。
		var childRow core.View
		if len(g.Children) > 0 {
			childRow = core.View{Style: core.Style{Direction: core.Row, Gap: 6, Wrap: core.Wrap}}
			for i, opt := range g.Children {
				i, opt := i, opt
				childRow.Children = append(childRow.Children, chipButton(cx, opt.Label, child.Get() == i, func() {
					child.Set(i)
				}))
			}
		}

		children := []core.Node{seg, chipRow}
		if childRow.Children != nil {
			children = append(children, childRow)
		}
		return core.View{Style: core.Style{Gap: 8}, Children: children}
	})
}

func resetKind(kind *core.Signal[string], sub, child *core.Signal[int], k string) {
	if k == "tv" {
		kind.Set("tv")
	} else {
		kind.Set("movie")
	}
	sub.Set(0)
	child.Set(0)
}

func segItem(cx *core.Scope, label string, active bool, press func()) core.Node {
	p := cx.Palette()
	st := core.Style{Padding: core.Symmetric(4, 14), FontSize: 13}
	if active {
		st.Background = p.Accent
		st.Color = p.AccentText
	} else {
		st.Background = p.Background
		st.Color = p.Text
	}
	return core.View{Style: st, Children: []core.Node{core.Text{Content: label}}, OnPress: func(core.PressEvent) { press() }}
}

func chipButton(cx *core.Scope, label string, active bool, press func()) core.Node {
	return core.Dynamic(func(*core.Scope) core.Node {
		p := cx.Palette()
		st := core.Style{
			Padding: core.Symmetric(3, 10), CornerRadius: 999, FontSize: 12,
		}
		if active {
			st.Background = p.Accent
			st.Color = p.AccentText
		} else {
			st.Background = p.Background
			st.Color = p.Text
			st.BorderColor = p.Border
			st.BorderWidth = 1
			st.BorderStyle = core.BorderSolid
		}
		return core.View{Style: st, Children: []core.Node{core.Text{Content: label}}, OnPress: func(core.PressEvent) { press() }}
	})
}

// posterGrid 封面网格（虚拟化列表，4 列）。
func posterGrid(cx *core.Scope, a *App, items *core.Signal[[]api.DoubanItem]) core.Node {
	return core.Dynamic(func(*core.Scope) core.Node {
		list := items.Get()
		if len(list) == 0 {
			return core.View{}
		}
		return core.List{
			Count:         func() int { return len(items.Get()) },
			Key:           func(i int) any { return items.Get()[i].URL },
			NumColumns:    4,
			EstimatedSize: 240,
			Viewport:      core.ListViewportParent,
			Row: func(cx *core.Scope, row core.ListRow) core.Node {
				it := items.Get()[row.Index()]
				return posterCell(cx, a, it)
			},
			Style: core.Style{FlexGrow: 1, FlexShrink: 1},
		}
	})
}

// posterCell 单个海报卡片：点击进豆瓣详情。
func posterCell(cx *core.Scope, a *App, it api.DoubanItem) core.Node {
	title := it.Title
	subtitle := it.Rate
	if subtitle != "" {
		subtitle = "★ " + subtitle
	}
	if it.EpisodesInfo != "" {
		if subtitle != "" {
			subtitle += " · " + it.EpisodesInfo
		} else {
			subtitle = it.EpisodesInfo
		}
	}
	return core.View{
		StyleFunc: func() core.Style {
			return core.Style{Gap: 6, Padding: core.Symmetric(0, 6)}
		},
		Children: []core.Node{
			core.View{
				Style: core.Style{Width: core.Pct(100)},
				Children: []core.Node{
					theme.Poster(a.API.ImageURL(it.Cover), title, 150, 210),
				},
				OnPress: func(core.PressEvent) {
					if it.ID != "" {
						a.OpenDetail(title, it.Cover, "https://movie.douban.com/subject/"+it.ID+"/")
					} else {
						a.OpenDetail(title, it.Cover, it.URL)
					}
				},
			},
			ui.Text(title, 13, ui.Medium, nil),
			ui.Text(subtitle, 11, ui.Regular, func() core.Color { return cx.Palette().TextMuted }),
		},
	}
}
