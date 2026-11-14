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

// tmdbTVTabs / tmdbMovieTabs：TMDB 优先时的分类体系（genre id 编码为 g{id}）。
var tmdbTVTabs = []tabGroup{
	{Label: "热门剧集", Tag: "tvtrend"},
	{Label: "动画", Tag: "g16"},
	{Label: "喜剧", Tag: "g35"},
	{Label: "犯罪", Tag: "g80"},
	{Label: "纪录片", Tag: "g99"},
	{Label: "科幻奇幻", Tag: "g10765"},
	{Label: "剧情", Tag: "g18"},
}

var tmdbMovieTabs = []tabGroup{
	{Label: "热门电影", Tag: "movietrend"},
	{Label: "动作", Tag: "g28"},
	{Label: "喜剧", Tag: "g35"},
	{Label: "科幻", Tag: "g878"},
	{Label: "惊悚", Tag: "g53"},
	{Label: "动画", Tag: "g16"},
}

type tabOpt struct {
	Label string
	Tag   string
}

// currentCategoryLabel 返回当前选中分类的中文名（chips 上的 Label）。
func currentCategoryLabel(kind *core.Signal[string], sub, child *core.Signal[int], tmdb bool) string {
	k := kind.Get()
	var gs []tabGroup
	if tmdb {
		if k == "tv" {
			gs = tmdbTVTabs
		} else {
			gs = tmdbMovieTabs
		}
	} else if k == "tv" {
		gs = tvTabs
	} else {
		gs = movieTabs
	}
	si := sub.Get()
	if si < 0 || si >= len(gs) {
		return "热门"
	}
	g := gs[si]
	if len(g.Children) == 0 {
		return g.Label
	}
	ci := child.Get()
	if ci < 0 || ci >= len(g.Children) {
		ci = 0
	}
	return g.Children[ci].Label
}

// currentTagOf 从首页分类信号算出当前请求参数。tmdb=true 时用 TMDB 分类体系。
func currentTagOf(kind *core.Signal[string], sub, child *core.Signal[int], tmdb bool) (k, tag, sort string) {
	k = kind.Get()
	var gs []tabGroup
	if tmdb {
		if k == "tv" {
			gs = tmdbTVTabs
		} else {
			gs = tmdbMovieTabs
		}
	} else if k == "tv" {
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
	// 测试挂钩：外部直接驱动网格数据 + 分类信号暴露。
	a.TestSetHomeItems = items.Set
	a.homeKind, a.homeSub, a.homeChild = kind, sub, child
	loading := core.NewSignal(cx, false)
	errMsg := core.NewSignal(cx, "")

	// 分页：每页条数与加载中标记。
	const pageLimit = 30
	pageLoading := core.NewSignal(cx, false)
	noMore := core.NewSignal(cx, false)
	sectionTitle := core.NewSignal(cx, "热门")
	catKey := core.NewSignal(cx, "")

	// 请求竞态防护：每次请求带分类 key，响应回来 key 不一致就丢弃。
	reqKey := core.NewSignal(cx, "")
	categoryKey := func() string {
		k, tag, sort := currentTagOf(kind, sub, child, a.PreferSourceSignal.Get() == "tmdb")
		return k + "|" + tag + "|" + sort
	}

	// 分类缓存：切换分类即时命中，免白屏（跨页签保留在 App 上）。
	// stale-while-revalidate：未命中时保留旧分类内容继续显示（不清空
	// 导致白屏），新数据到达后整体替换。首次无旧内容才显示骨架。
	applyCache := func() {
		if v, ok := a.HomeCache.Get(categoryKey()); ok {
			items.Set(v)
			// 缓存命中后允许翻页续拉；短缓存（豆瓣去重/rexxar 18 条）
			// 不代表到底，翻页请求空页才终止。
			noMore.Set(false)
		} else {
			noMore.Set(false)
		}
	}

	tmdbMode := func() bool { return a.PreferSourceSignal.Get() == "tmdb" }
	groups := func() []tabGroup {
		if tmdbMode() {
			if kind.Get() == "tv" {
				return tmdbTVTabs
			}
			return tmdbMovieTabs
		}
		if kind.Get() == "tv" {
			return tvTabs
		}
		return movieTabs
	}

	currentTag := func() (tag, sort string) {
		_, t, s := currentTagOf(kind, sub, child, a.PreferSourceSignal.Get() == "tmdb")
		return t, s
	}

	load := func() {
		key := categoryKey()
		// 分类未变且已有数据时不重拉（chips 连点防抖）。
		if !loading.Get() && len(items.Get()) > 0 && reqKey.GetUntracked() == key {
			return
		}
		ClientLog("[home] 切换分类 %s", key)
		reqKey.Set(key)
		catKey.Set(key) // 重建网格
		tag, sort := currentTag()
		// 缓存数据先垫上（有则秒显），但总是重新请求豆瓣拿最新，
		// 期间显示加载覆盖层（用户要求的确定性加载体验）。
		applyCache()
		loading.Set(true)
		errMsg.Set("")
		ClientLog("[home] 请求发出 %s (垫底 %d 条)", key, len(items.Get()))
		client := a.API
		k := kind.Get()
		var attempt func(try int)
		attempt = func(try int) {
			list, source, err := client.DoubanListWithSource(k, tag, sort, pageLimit, 0, a.PreferTMDB())
			cx.Post(func() {
				if reqKey.Get() != key {
					ClientLog("[home] 响应过期丢弃 %s", key)
					return
				}
				if err != nil {
					// 自动重试一次（豆瓣偶发超时），仍失败才报错。
					if try == 0 {
						ClientLog("[home] 请求失败，3 秒后自动重试 %s: %v", key, err)
						go func() {
							time.Sleep(3 * time.Second)
							cx.Post(func() {
								if reqKey.Get() == key && !loading.Get() {
									attempt(1)
								}
							})
						}()
						return
					}
					ClientLog("[home] 请求失败 %s: %v", key, err)
					loading.Set(false)
					errMsg.Set(err.Error())
					if len(items.Get()) == 0 {
						items.Set(nil) // 无旧内容可显示时才清空进错误页
					}
					return
				}
				loading.Set(false)
				ClientLog("[home] 请求完成 %s: %d 条 source=%q", key, len(list), source)
				items.Set(list)
				a.HomeCache.Put(key, list)
				// 短页（豆瓣去重 29、rexxar 18）不是到底，允许翻页。
				noMore.Set(false)
			})
		}
		go attempt(0)
	}

	// 分页加载：滚近列表尾部时由 List 的 OnLoadMore 触发。
	loadMore := func() {
		if loading.Get() || pageLoading.Get() || noMore.Get() {
			return
		}
		cur := items.Get()
		if len(cur) == 0 {
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
			list, _, err := client.DoubanListWithSource(k, tag, sort, pageLimit, start, a.PreferTMDB())
			cx.Post(func() {
				pageLoading.Set(false)
				if reqKey.Get() != key || err != nil {
					return
				}
				if len(list) == 0 {
					noMore.Set(true)
					return
				}
				// 短页不置 noMore：豆瓣去重后 29/30、rexxar 部分
				// 榜单本身就只有十几条、TMDB 每页 20——不足一页
				// ≠ 到底。下次翻页后端返回空页才真正终止。
				// items 可能在请求飞行中被重置（同 key 重进），以当前值拼接而非闭包快照。
				now := items.Get()
				if len(now) != start {
					return // 基线变了，丢弃这页避免错位拼接
				}
				merged := append(append([]api.DoubanItem{}, now...), list...)
				items.Set(merged)
				a.HomeCache.Put(key, merged)
				// 网格重建会回顶：滚到新加载的第一行保持阅读位置。
				if a.OnPageLoaded != nil {
					a.OnPageLoaded(start)
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

	// 当前分类标签：让数据来源一目了然（豆瓣各分类内容天然重叠，
	// 显示当前分类的中文名避免误以为切换没生效）。
	cx.Effect(func() {
		kind.Get()
		sub.Get()
		child.Get()
		core.Untracked(cx, func() bool {
			sectionTitle.Set(currentCategoryLabel(kind, sub, child, a.PreferSourceSignal.Get() == "tmdb"))
			return true
		})
	})

	return core.View{
		StyleFunc: func() core.Style {
			return theme.Fill().Merge(core.Style{Padding: core.Symmetric(16, 20), Gap: 12, Overflow: core.Hidden})
		},
		Children: []core.Node{
			browseView(cx, a, kind, sub, child, groups, items, load, loadMore,
				pageLoading, noMore, catKey, sectionTitle, loading, errMsg),
		},
	}
}

// browseView 分类浏览视图（首页默认）：顶部搜索框 + 历史/收藏入口，
// 下面是原来的分类 chips 与海报网格。
func browseView(cx *core.Scope, a *App,
	kind *core.Signal[string], sub, child *core.Signal[int], groups func() []tabGroup,
	items *core.Signal[[]api.DoubanItem], load func(), loadMore func(),
	pageLoading, noMore *core.Signal[bool], catKey *core.Signal[string],
	sectionTitle *core.Signal[string], loading *core.Signal[bool], errMsg *core.Signal[string]) core.View {
	return core.View{
		StyleFunc: func() core.Style {
			return core.Style{Gap: 12, FlexGrow: 1, FlexShrink: 1, Overflow: core.Hidden}
		},
		Children: []core.Node{
			homeToolbar(cx, a),
			kindSwitcher(cx, a, kind, sub, child, groups),
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
				case loading.Get() && len(items.Get()) > 0:
					// 切换分类：旧内容继续显示，顶部一条加载状态。
					return theme.Row(8, ui.Spinner{}, ui.Text("正在加载「"+sectionTitle.Get()+"」…", 12, ui.Regular, func() core.Color { return cx.Palette().TextMuted }))
				case errMsg.Get() != "" && len(items.Get()) == 0:
					return theme.ErrorRetry(cx, errMsg.Get(), func() { load() })
				case errMsg.Get() != "" && len(items.Get()) > 0:
					return theme.ErrorRetry(cx, "刷新失败，显示的仍是上一个分类的内容", func() { load() })
				default:
					return core.View{}
				}
			}),
			posterGrid(cx, a, items, loadMore, pageLoading, noMore, catKey),
		},
	}
}

// homeToolbar 首页工具行：搜索框（回车进搜索面板）+ 历史/收藏入口。
func homeToolbar(cx *core.Scope, a *App) core.Node {
	keyword := core.NewSignal(cx, a.SearchKeyword.Get())
	return core.View{
		StyleFunc: func() core.Style {
			return core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8}
		},
		Children: []core.Node{
			ui.Input{
				Value:       keyword.Get,
				OnChange:    keyword.Set,
				Placeholder: "搜索片名，回车搜索…",
				TestID:      "home-keyword",
				OnSubmit:    func(string) { a.SearchKeyword.Set(keyword.Get()); a.HomeView.Set("search") },
				Style:       core.Style{FlexGrow: 1, FlexShrink: 1},
			},
			ui.Button{Text: "搜索", Variant: ui.ButtonPrimary, TestID: "home-search", OnPress: func() {
				a.SearchKeyword.Set(keyword.Get())
				a.HomeView.Set("search")
			}},
			ui.Button{Text: "历史", Variant: ui.ButtonSecondary, TestID: "home-history", OnPress: func() {
				a.HomeView.Set("history")
			}},
			ui.Button{Text: "收藏", Variant: ui.ButtonSecondary, TestID: "home-favorites", OnPress: func() {
				a.HomeView.Set("favorites")
			}},
		},
	}
}

// kindSwitcher 顶部：电影/剧集分段 + 分类 chips（base.Tabs 官方控件，
// 带键盘导航、hover 反馈与按下缩放）。
func kindSwitcher(cx *core.Scope, a *App, kind *core.Signal[string], sub, child *core.Signal[int], groups func() []tabGroup) core.Node {
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

		// 组 chips：显示分类的中文名（豆瓣无子项的组才回退 tag，
		// TMDB 单层分类恒用 Label——Tag 是 g{id} 编码不能直接显示）。
		chipRow := core.View{Style: core.Style{Direction: core.Row, Gap: 8, Wrap: core.Wrap}}
		for i, grp := range gs {
			i, grp := i, grp
			label := grp.Label
			if len(grp.Children) == 0 && a.PreferSourceSignal.Get() != "tmdb" {
				label = grp.Tag
			}
			chipRow.Children = append(chipRow.Children, chipButton(cx, label, si == i, func() {
				ClientLog("[home] 点分类组 %q", label)
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
			Active: &core.Style{Transform: core.Transform{Scale: 0.96}},
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

// posterGrid 封面网格。分两类更新走不同路径：
//   - 分页追加：List 常驻，Count/Key/Row 响应式（滚动位置保留）
//   - 切换分类：分类 key 变化时 Dynamic 重建整个 List（nsgo List 的
//     splice 在大批量替换时丢变更/不更新行——20261114 快照实锤，
//     等上游修复后可回收此绕过）
func posterGrid(cx *core.Scope, a *App, items *core.Signal[[]api.DoubanItem], loadMore func(), pageLoading, noMore *core.Signal[bool], catKey *core.Signal[string]) core.Node {
	const gap = core.Points(14)
	estimated := core.Points(160/0.7) + 44
	gridRef := &core.Ref{}
	// 分页完成后滚到新加载的第一行（Dynamic 重建 List 会回顶，这里补偿）。
	// 延迟两帧等新 List commit 完成，否则滚动命令作用在旧 List 上会丢。
	a.OnPageLoaded = func(firstNew int) {
		go func() {
			time.Sleep(120 * time.Millisecond)
			cx.Post(func() {
				gridRef.ScrollToIndexAt(firstNew, 0, 0, false)
				ClientLog("[home] 滚回第 %d 行（延迟补偿）", firstNew)
			})
		}()
	}
	// 列数随窗口宽度自适应（OnLayout 测量 → 列数变化重建 List，
	// NumColumns 挂载时固化的框架限制，与 catKey 同一重建模式）。
	cols, measure := theme.Measure(cx)
	return measure(core.Dynamic(func(*core.Scope) core.Node {
		_ = catKey.Get()   // track：切分类时重建 List
		n := cols()        // track：窗口宽度跨档时重建 List
		_ = items          // 注意：严禁 track items——分页 Set 会重建 List 回顶
		ClientLog("[home] posterGrid 列数 %d", n)
		return buildPosterGrid(cx, a, items, loadMore, pageLoading, noMore, n, gap, estimated, gridRef)
	}))
}

func buildPosterGrid(cx *core.Scope, a *App, items *core.Signal[[]api.DoubanItem], loadMore func(), pageLoading, noMore *core.Signal[bool], columns int, gap, estimated core.Points, gridRef *core.Ref) core.List {
	return core.List{
		Ref:               gridRef,
		Count:             func() int { return len(items.Get()) },
		Key:               func(i int) any { return items.Get()[i].URL },
		NumColumns:        columns,
		EstimatedSize:     estimated,
		Viewport:          core.ListViewportOwn,
		Gap:               gap + 8,
		ColumnGap:         gap,
		OnLoadMore:        loadMore,
		LoadMoreThreshold: 2.5, // 提前 2.5 屏预加载，滑到底部时下一页已在路上
		Row: func(cx *core.Scope, row core.ListRow) core.Node {
			// 官方模式：Row 返回的子树必须响应式——cell 会被跨数据复用
			// （bind 只更新 pos.index 信号），静态取值会让复用行永远显示
			// 旧内容（切分类不切换的根因）。
			// 性能关键：Dynamic 只 track row.Index()（cell 复用时变化）；
			// items 用 Untracked 读，避免分页追加把所有 cell 标脏全量重跑。
			it := func() api.DoubanItem {
				list := core.Untracked(cx, func() []api.DoubanItem { return items.Get() })
				if i := row.Index(); i >= 0 && i < len(list) {
					return list[i]
				}
				return api.DoubanItem{}
			}
			return core.Dynamic(func(*core.Scope) core.Node {
				return posterCell(cx, a, it())
			})
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

// posterCardStyle 海报卡片共享样式（含 hover/active 状态样式，包级单例
// 避免每个 cell 每次渲染新建闭包）。
var posterCardStyle = core.Style{
	Width: core.Pct(100), AspectRatio: 0.7, CornerRadius: 6,
	Background: core.RGB(0xD9D9D9), Overflow: core.Hidden,
	Hover:  &core.Style{Background: core.RGB(0xC8C8C8)},
	Active: &core.Style{Transform: core.Transform{Scale: 0.98}},
}

// posterCell 单个海报卡片：点击进豆瓣详情。
func posterCell(cx *core.Scope, a *App, it api.DoubanItem) core.Node {
	title := it.Title
	return core.View{
		Style: core.Style{Gap: 6},
		Children: []core.Node{
			core.View{
				Style: posterCardStyle,
				Children: []core.Node{
					core.Image{
						Source:       core.Src(a.API.ImageURL(it.Cover, title)),
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
