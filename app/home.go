package app

import (
	"time"

	"kanju/client/api"
	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// tvTabs 与 movieTabs 复刻 Web 版首页的豆瓣分类。
var tvTabs = []tabGroup{
	{Label: "热门剧集", Children: []tabOpt{
		{Label: "综合", Tag: "热门"}, {Label: "国产剧", Tag: "国产剧"}, {Label: "欧美剧", Tag: "美剧"},
		{Label: "日剧", Tag: "日剧"}, {Label: "韩剧", Tag: "韩剧"}, {Label: "动画", Tag: "动漫"},
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

// currentTagOf 从首页分类信号算出当前请求参数。
func currentTagOf(kind *core.Signal[string], sub, child *core.Signal[int]) (k, tag, sort string) {
	k = kind.Get()
	var gs []tabGroup
	if k == "tv" {
		gs = tvTabs
	} else {
		gs = movieTabs
	}
	si := sub.Get()
	if si < 0 || si >= len(gs) {
		return k, "热门", ""
	}
	g := gs[si]
	if len(g.Children) == 0 {
		return k, g.Tag, ""
	}
	ci := child.Get()
	if ci < 0 || ci >= len(g.Children) {
		ci = 0
	}
	if k == "tv" && si == 1 && ci == 1 {
		return k, g.Children[ci].Tag, "time"
	}
	return k, g.Children[ci].Tag, ""
}

// Home 首页：电影/剧集分类 + 封面网格。
func Home(cx *core.Scope, a *App) core.Node {
	kind := core.NewSignal(cx, "tv") // tv | movie
	sub := core.NewSignal(cx, 0)
	child := core.NewSignal(cx, 0)
	items := core.NewSignalFunc(cx, []api.DoubanItem(nil), func(a, b []api.DoubanItem) bool { return false })
	loading := core.NewSignal(cx, false)
	errMsg := core.NewSignal(cx, "")

	// 分页：每页条数与加载中标记。
	const pageLimit = 30
	pageLoading := core.NewSignal(cx, false)
	noMore := core.NewSignal(cx, false)
	sectionTitle := core.NewSignal(cx, "热门")

	// 请求竞态防护：每次请求带分类 key，响应回来 key 不一致就丢弃。
	reqKey := core.NewSignal(cx, "")
	categoryKey := func() string {
		k, tag, sort := currentTagOf(kind, sub, child)
		return k + "|" + tag + "|" + sort
	}

	// 分类缓存：切换分类即时命中，免白屏（跨页签保留在 App 上）。
	applyCache := func() {
		if v, ok := a.HomeCache.Get(categoryKey()); ok {
			items.Set(v)
			noMore.Set(len(v) < pageLimit)
		} else {
			items.Set(nil)
			noMore.Set(false)
		}
	}

	groups := func() []tabGroup {
		if kind.Get() == "tv" {
			return tvTabs
		}
		return movieTabs
	}

	currentTag := func() (tag, sort string) {
		_, t, s := currentTagOf(kind, sub, child)
		return t, s
	}

	load := func() {
		key := categoryKey()
		// 分类未变且已有数据时不重拉（chips 连点防抖）。
		if !loading.Get() && len(items.Get()) > 0 && reqKey.GetUntracked() == key {
			return
		}
		reqKey.Set(key)
		tag, sort := currentTag()
		applyCache()
		if len(items.Get()) > 0 {
			return // 缓存命中，不闪白
		}
		loading.Set(true)
		errMsg.Set("")
		client := a.API
		k := kind.Get()
		go func() {
			list, err := client.DoubanList(k, tag, sort, pageLimit, 0)
			cx.Post(func() {
				loading.Set(false)
				if reqKey.Get() != key {
					return // 过期响应丢弃
				}
				if err != nil {
					errMsg.Set(err.Error())
					items.Set(nil)
				} else {
					items.Set(list)
					a.HomeCache.Put(key, list)
					noMore.Set(len(list) < pageLimit)
				}
			})
		}()
	}

	// 分页加载：滚近列表尾部时由 List 的 OnLoadMore 触发。
	loadMore := func() {
		if loading.Get() || pageLoading.Get() || noMore.Get() {
			return
		}
		cur := items.Get()
		if len(cur) == 0 || len(cur) < pageLimit { // 不足一页说明到底了
			noMore.Set(true)
			return
		}
		key := categoryKey()
		if reqKey.Get() != key {
			return // 分类已切换，丢弃旧分页
		}
		tag, sort := currentTag()
		k := kind.Get()
		start := len(cur)
		client := a.API
		pageLoading.Set(true)
		go func() {
			list, err := client.DoubanList(k, tag, sort, pageLimit, start)
			cx.Post(func() {
				pageLoading.Set(false)
				if reqKey.Get() != key || err != nil || len(list) == 0 {
					if len(list) < pageLimit {
						noMore.Set(true)
					}
					return
				}
				merged := append(cur, list...)
				items.Set(merged)
				a.HomeCache.Put(key, merged)
				noMore.Set(len(list) < pageLimit)
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

	// 当前分类标签：让数据来源一目了然（豆瓣各分类内容天然重叠，
	// 显示当前 tag 避免误以为切换没生效）。
	cx.Effect(func() {
		kind.Get()
		sub.Get()
		child.Get()
		core.Untracked(cx, func() bool {
			_, tag, _ := currentTagOf(kind, sub, child)
			sectionTitle.Set(tag)
			return true
		})
	})

	return core.View{
		StyleFunc: func() core.Style {
			return theme.Fill().Merge(core.Style{Padding: core.Symmetric(16, 20), Gap: 12, Overflow: core.Hidden})
		},
		Children: []core.Node{
			kindSwitcher(cx, kind, sub, child, groups),
			core.Dynamic(func(*core.Scope) core.Node {
				t := sectionTitle.Get()
				if t == "" {
					return core.View{}
				}
				return theme.Title(t, 15, ui.Semibold)
			}),
			core.Dynamic(func(*core.Scope) core.Node {
				switch {
				case loading.Get() && len(items.Get()) == 0:
					// 首屏骨架：比 spinner 有内容感。
					return theme.SkeletonGrid(cx, 2, 4)
				case errMsg.Get() != "" && len(items.Get()) == 0:
					return theme.ErrorRetry(cx, errMsg.Get(), func() { load() })
				default:
					return core.View{}
				}
			}),
			posterGrid(cx, a, items, loadMore, pageLoading, noMore),
		},
	}
}

// kindSwitcher 顶部：电影/剧集分段 + 分类 chips（base.Tabs 官方控件，
// 带键盘导航、hover 反馈与按下缩放）。
func kindSwitcher(cx *core.Scope, kind *core.Signal[string], sub, child *core.Signal[int], groups func() []tabGroup) core.Node {
	return core.Dynamic(func(*core.Scope) core.Node {
		gs := groups()
		si := sub.Get()
		if si >= len(gs) {
			si = 0
		}
		g := gs[si]

		// 电影/剧集分段：胶囊分段控件，选中片有浅色底。
		seg := base.Tabs{Label: "分类", Style: core.Style{Direction: core.Row, Gap: 4,
			Padding: core.Symmetric(3, 3), CornerRadius: 9, Background: cx.Palette().SurfaceHover}}
		seg.Children = append(seg.Children,
			segItem(cx, "剧集", kind.Get() == "tv", func() { resetKind(kind, sub, child, "tv") }),
			segItem(cx, "电影", kind.Get() != "tv", func() { resetKind(kind, sub, child, "movie") }),
		)

		// 组 chips（有子项的组显示组名，没有的组直接显示 tag）。
		chipRow := core.View{Style: core.Style{Direction: core.Row, Gap: 8, Wrap: core.Wrap}}
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
			childRow = core.View{Style: core.Style{Direction: core.Row, Gap: 8, Wrap: core.Wrap}}
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

// segItem 分段控件里的一片（剧集/电影）。
func segItem(cx *core.Scope, label string, active bool, press func()) core.Node {
	return base.Tab{Selected: func() bool { return active }, Label: label,
		OnPress: func(core.PressEvent) { press() },
		Style: core.Style{Padding: core.Symmetric(2, 2), CornerRadius: 7,
			Active: &core.Style{Transform: core.Transform{Scale: 0.97}}},
		Children: []core.Node{core.View{
			StyleFunc: func() core.Style {
				p := cx.Palette()
				st := core.Style{
					Height: core.Pt(26), Padding: core.Symmetric(0, 16),
					CornerRadius: 7, JustifyContent: core.JustifyCenter,
					Transition: core.Transition{Duration: 150 * time.Millisecond, Easing: core.EaseOut},
				}
				if active {
					st.Background = p.Background
					st.Color = p.Text
				} else {
					st.Color = p.TextMuted
					st.Hover = &core.Style{Color: p.Text}
				}
				return st
			},
			Children: []core.Node{core.Text{Content: label, StyleFunc: func() core.Style {
				p := cx.Palette()
				st := core.Style{FontSize: 13, FontWeight: ui.Medium}
				if active {
					st.Color = p.Text
				} else {
					st.Color = p.TextMuted
				}
				return st
			}}},
		}},
	}
}

// chipButton 分类胶囊：选中有 accent 底，未选有边框 + hover 反馈 + 按压缩放。
func chipButton(cx *core.Scope, label string, active bool, press func()) core.Node {
	return core.Dynamic(func(*core.Scope) core.Node {
		p := cx.Palette()
		st := core.Style{
			Padding: core.Symmetric(4, 12), CornerRadius: 999, FontSize: 12,
			Transition: core.Transition{Duration: 120 * time.Millisecond, Easing: core.EaseOut},
			Active:     &core.Style{Transform: core.Transform{Scale: 0.96}},
		}
		if active {
			st.Background = p.Accent
			st.Color = p.AccentText
			st.FontWeight = core.FontWeightMedium
		} else {
			st.Background = p.Background
			st.Color = p.TextMuted
			st.BorderColor = p.Border
			st.BorderWidth = 1
			st.BorderStyle = core.BorderSolid
			st.Hover = &core.Style{Color: p.Text, Background: p.SurfaceHover, CornerRadius: 999}
		}
		return core.View{Style: st, Children: []core.Node{core.Text{Content: label}}, OnPress: func(core.PressEvent) { press() }}
	})
}

// posterGrid 封面网格（虚拟化列表，按宽度自适应列数）。
// 注意不要在外层 core.Dynamic 里读 items——追加分页会整树重建 List
// 导致滚动位置归零；List 的 Count/Row 本身就响应数据变化。
func posterGrid(cx *core.Scope, a *App, items *core.Signal[[]api.DoubanItem], loadMore func(), pageLoading, noMore *core.Signal[bool]) core.Node {
	// 列宽估算：窗口内容区约 700-1100pt，4 列。行高 = 列宽/0.7（海报）+ 文字两行 + 间距。
	const columns = 4
	const gap = core.Points(14)
	estimated := core.Points(160/0.7) + 44
	return core.List{
		Count:         func() int { return len(items.Get()) },
		Key:           func(i int) any { return items.Get()[i].URL },
		NumColumns:    columns,
		EstimatedSize: estimated,
		Viewport:      core.ListViewportOwn,
		Gap:           gap + 8,
		ColumnGap:     gap,
		OnLoadMore:    loadMore,
		Row: func(cx *core.Scope, row core.ListRow) core.Node {
			it := items.Get()[row.Index()]
			return posterCell(cx, a, it)
		},
		Footer: core.Dynamic(func(*core.Scope) core.Node {
			p := cx.Palette()
			text := ""
			switch {
			case pageLoading.Get():
				text = "加载中…"
			case noMore.Get() && len(items.Get()) > 0:
				text = "已加载全部"
			}
			if text == "" {
				return core.View{}
			}
			return core.View{
				Style:    core.Style{AlignItems: core.AlignCenter, Padding: core.All(14)},
				Children: []core.Node{core.Text{Content: text, Style: core.Style{FontSize: 12, Color: p.TextMuted}}},
			}
		}),
		Style: core.Style{FlexGrow: 1, FlexShrink: 1, Padding: core.Symmetric(4, 0)},
	}
}

// posterCell 单个海报卡片：点击进豆瓣详情。
func posterCell(cx *core.Scope, a *App, it api.DoubanItem) core.Node {
	title := it.Title
	return core.View{
		Style: core.Style{Gap: 6},
		Children: []core.Node{
			core.View{
				StyleFunc: func() core.Style {
					return core.Style{
						Width: core.Pct(100), AspectRatio: 0.7, CornerRadius: 6,
						Background: core.RGB(0xD9D9D9), Overflow: core.Hidden,
						// 悬停微亮，按下有缩放反馈。
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
	}
}

// ratingBadge 海报左上角的半透明评分角标。
func ratingBadge(cx *core.Scope, rate string) core.Node {
	if rate == "" || rate == "0" {
		return core.View{}
	}
	return core.View{
		Style: core.Style{
			Position: core.Absolute, Top: core.Pt(6), Left: core.Pt(6),
			Padding: core.Symmetric(1, 6), CornerRadius: 4,
			Background: core.RGB(0x000000B8), Color: core.RGB(0xFFD166), FontSize: 11,
		},
		Children: []core.Node{core.Text{Content: "★ " + rate}},
	}
}

// posterSubtitle 副标题：集数信息（评分已上角标）。
func posterSubtitle(cx *core.Scope, it api.DoubanItem) core.Node {
	if it.EpisodesInfo == "" {
		return core.View{}
	}
	return ui.Text(it.EpisodesInfo, 11, ui.Regular, func() core.Color { return cx.Palette().TextMuted })
}
