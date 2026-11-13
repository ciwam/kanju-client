package app

import (
	"kanju/client/api"
	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// refreshFavState 查当前详情是否已收藏。
func (a *App) refreshFavState(cx *core.Scope, u string) {
	client := a.API
	go func() {
		favs, err := client.Favorites()
		cx.Post(func() {
			if err != nil {
				return
			}
			for _, f := range favs {
				if f.DetailURL == u {
					a.DetailFav.Set(true)
					return
				}
			}
			a.DetailFav.Set(false)
		})
	}()
}

// ToggleFav 收藏/取消收藏当前详情。
func (a *App) ToggleFav(cx *core.Scope) {
	u := a.DetailURL.Get()
	ClientLog("[detail] 切换收藏 url=%s 当前=%v", u, a.DetailFav.Get())
	title := a.DetailTitle.Get()
	cover := a.DetailCover.Get()
	client := a.API
	if a.DetailFav.Get() {
		go func() {
			if err := client.RemoveFavorite(u); err != nil {
				ClientLog("[detail] unfav error: %v", err)
			}
			cx.Post(func() { a.DetailFav.Set(false) })
		}()
		return
	}
	go func() {
		err := client.AddFavorite(api.Favorite{Title: title, Cover: cover, DetailURL: u})
		cx.Post(func() {
			if err != nil {
				ClientLog("[detail] fav error: %v", err)
				return
			}
			a.DetailFav.Set(true)
		})
	}()
}

// LoadDetail 并行拉豆瓣详情与站源聚合，各自先到先显示：
// 影片名称/海报/简介通常几秒内出来，线路选集（各站源逐个抓取，约
// 20 秒）后到再追加。状态放 App 而非页面局部：页面函数会被 shell 的
// Dynamic 重复调用，局部信号分裂成多实例时，请求回调写旧实例导致白屏。
func (a *App) LoadDetail(cx *core.Scope, u, title, cover string) {
	a.DetailLoading.Set(true)
	a.DetailErr.Set("")
	a.DetailSubject.Set(nil)
	a.DetailData.Set(nil)
	client := a.API
	doubanID := ""
	if contains(u, "movie.douban.com/subject/") {
		doubanID = segmentAfter(u, "/subject/")
	}

	// 豆瓣头部信息：独立加载、独立显示。
	go func() {
		if doubanID == "" {
			return
		}
		v, err := client.DoubanSubject(doubanID, cover)
		cx.Post(func() {
			if err != nil {
				ClientLog("[detail] douban subject error: %v", err)
				return
			}
			a.DetailSubject.Set(&v)
			ClientLog("[detail] subject ready %q", v.Title)
		})
	}()

	// 站源聚合：完成前 DetailLoading 让页面显示"正在聚合站源…"。
	go func() {
		dv, err := client.Detail(u, "", title)
		cx.Post(func() {
			a.DetailLoading.Set(false)
			if err != nil {
				a.DetailErr.Set(err.Error())
				ClientLog("[detail] aggregate error: %v", err)
				return
			}
			a.DetailData.Set(&dv)
			ClientLog("[detail] aggregated %d channels", len(dv.Channels))
		})
	}()
}

// SubjectDetail 详情页：豆瓣信息 + 各站源线路/选集聚合。纯渲染——
// 数据在 App 的全局信号里，进入页签时由 Root 的导航 effect 触发加载。
func SubjectDetail(cx *core.Scope, a *App) core.Node {
	// 每次切到详情页签时重新加载 + 查收藏状态。
	cx.Effect(func() {
		u := a.DetailURL.Get()
		if a.Tab.Get() != "detail" || u == "" {
			return
		}
		core.Untracked(cx, func() bool {
			a.LoadDetail(cx, u, a.DetailTitle.Get(), a.DetailCover.Get())
			a.refreshFavState(cx, u)
			return true
		})
	})

	play := func(ep api.Episode, ch api.Channel) {
		if a.DetailPlayBusy.Get() != "" {
			return
		}
		ClientLog("[detail] 点选集 %q line=%q", ep.Title, ch.Name)
		a.DetailPlayBusy.Set("解析播放地址…")
		client := a.API
		go func() {
			r, err := client.Play(ep.URL)
			cx.Post(func() {
				a.DetailPlayBusy.Set("")
				if err != nil {
					a.DetailErr.Set(err.Error())
					return
				}
				if r.Type == "iframe" {
					a.DetailErr.Set("该集只能网页播放，请换线路或选集")
					return
				}
				a.DetailNowPlaying.Set(ep.URL)
				a.OpenPlayer(a.DetailTitle.Get()+" · "+ep.Title, r.URL, ch.SourceName, a.DetailURL.Get())
			})
		}()
	}

	return core.View{
		StyleFunc: func() core.Style {
			return theme.Fill().Merge(core.Style{Padding: core.Symmetric(16, 20), Gap: 12, Overflow: core.Hidden})
		},
		Children: []core.Node{core.Dynamic(func(*core.Scope) core.Node {
			if u := a.DetailURL.Get(); u == "" {
				return theme.Empty(cx, "从首页或搜索选择一部影片")
			}
			s := a.DetailSubject.Get()
			d := a.DetailData.Get()
			// 豆瓣失败时用聚合响应的 cover/intro 兜底，保证标题下有介绍。
			var head []core.Node
			if s != nil {
				if s.Intro == "" && d != nil && d.Intro != "" {
					s.Intro = d.Intro
				}
				if s.Cover == "" && d != nil && d.Cover != "" {
					s.Cover = d.Cover
				}
				head = append(head, subjectHead(cx, a, s)...)
			} else if d != nil && (d.Intro != "" || d.Cover != "") {
				// 豆瓣没数据但聚合带了 cover/intro：渲染纯文字头部 + 简介。
				cover := d.Cover
				if cover == "" {
					cover = a.DetailCover.Get()
				}
				head = append(head, fallbackHead(cx, a, a.DetailTitle.Get(), cover, d.Intro))
			} else {
				head = append(head, theme.Title(a.DetailTitle.Get(), 20, ui.Semibold))
			}
			children := []core.Node{}
			children = append(children, head...)

			children = append(children,
				core.Dynamic(func(*core.Scope) core.Node {
					switch {
					case a.DetailLoading.Get():
						return theme.Row(8, ui.Spinner{}, ui.Text("正在聚合站源…", 13, ui.Regular, func() core.Color { return cx.Palette().TextMuted }))
					case a.DetailErr.Get() != "":
						return ui.Text(a.DetailErr.Get(), 13, ui.Regular, func() core.Color { return cx.Palette().Danger })
					case a.DetailPlayBusy.Get() != "":
						return theme.Row(8, ui.Spinner{}, ui.Text(a.DetailPlayBusy.Get(), 13, ui.Regular, func() core.Color { return cx.Palette().TextMuted }))
					default:
						return core.View{}
					}
				}),
			)

			if d != nil {
				if len(d.Channels) > 0 {
					for _, ch := range mergeChannels(d.Channels) {
						ch := ch
						children = append(children, channelSection(cx, a, ch, play))
					}
				} else if !a.DetailLoading.Get() {
					// 聚合完成但没有可播资源：给搜索兜底入口。
					children = append(children, theme.Card(cx,
						theme.Title("未找到播放资源", 14, ui.Semibold),
						ui.Text("各站源都没有收录这部影片，试试直接搜索片名", 12, ui.Regular, func() core.Color { return cx.Palette().TextMuted }),
						ui.Button{Text: "去搜索", Variant: ui.ButtonSecondary,
							OnPress: func() { a.Tab.Set("search") }},
					))
				}
			}
			return core.Scroll{Style: core.Style{FlexGrow: 1, FlexShrink: 1}, Children: []core.Node{
				core.View{Style: core.Style{Gap: 14, Padding: core.All(0)}, Children: children},
			}}
		})},
	}
}

// favButton 收藏/已收藏切换按钮（状态在 App.DetailFav）。
func favButton(cx *core.Scope, a *App) core.Node {
	return core.Dynamic(func(*core.Scope) core.Node {
		if a.Embedded.Get() {
			return core.View{} // 内嵌模式历史数据按 URL 键，收藏语义留给外部服务器
		}
		if a.DetailFav.Get() {
			return ui.Button{Text: "已收藏", Variant: ui.ButtonSecondary, Size: ui.SizeSmall,
				OnPress: func() { a.ToggleFav(cx) }}
		}
		return ui.Button{Text: "收藏", Variant: ui.ButtonSecondary, Size: ui.SizeSmall,
			OnPress: func() { a.ToggleFav(cx) }}
	})
}

// fallbackHead 豆瓣详情不可用时的头部：标题 + 海报 + 聚合响应带来的简介。
func fallbackHead(cx *core.Scope, a *App, title, cover, intro string) core.View {
	var info []core.Node
	info = append(info, theme.Title(title, 20, ui.Semibold))
	if intro != "" {
		info = append(info, core.Text{Content: intro, Style: theme.TypeBody.Merge(core.Style{Color: cx.Palette().Text, LineHeight: 22})})
	}
	children := []core.Node{
		core.View{Style: core.Style{FlexGrow: 1, FlexShrink: 1, Gap: 8}, Children: info},
	}
	if cover != "" {
		children = append([]core.Node{theme.Poster(a.API.ImageURL(cover), title, 130, 182)}, children...)
	}
	return core.View{
		StyleFunc: func() core.Style { return core.Style{Direction: core.Row, Gap: 16, AlignItems: core.AlignStart} },
		Children:  children,
	}
}

// subjectHead 海报 + 信息头。
func subjectHead(cx *core.Scope, a *App, s *api.Subject) []core.Node {
	coverURL := s.Cover
	if coverURL == "" {
		coverURL = a.DetailCover.Get()
	}
	info := []core.Node{core.View{
		StyleFunc: func() core.Style { return core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 10} },
		Children: []core.Node{
			theme.Title(s.Title, 20, ui.Semibold),
			favButton(cx, a),
		},
	}}
	meta := s.Year
	if s.Rating != "" {
		if meta != "" {
			meta += " · ★ " + s.Rating
		} else {
			meta = "★ " + s.Rating
		}
	}
	if s.Region != "" {
		meta += " · " + s.Region
	}
	if s.Episodes != "" {
		meta += " · " + s.Episodes + "集"
	}
	if meta != "" {
		info = append(info, ui.Text(meta, 13, ui.Regular, func() core.Color { return cx.Palette().TextMuted }))
	}
	if len(s.Genres) > 0 {
		info = append(info, ui.Text(joinNonEmpty(s.Genres, " / "), 12, ui.Regular, func() core.Color { return cx.Palette().TextMuted }))
	}
	if len(s.Directors) > 0 {
		info = append(info, ui.Text("导演："+joinNonEmpty(s.Directors, " / "), 12, ui.Regular, func() core.Color { return cx.Palette().TextMuted }))
	}
	if s.Intro != "" {
		info = append(info, core.Text{Content: s.Intro, Style: theme.TypeBody.Merge(core.Style{Color: cx.Palette().Text, LineHeight: 22})})
	}
	return []core.Node{core.View{
		StyleFunc: func() core.Style { return core.Style{Direction: core.Row, Gap: 16, AlignItems: core.AlignStart} },
		Children: []core.Node{
			theme.Poster(a.API.ImageURL(coverURL), s.Title, 130, 182),
			core.View{Style: core.Style{FlexGrow: 1, FlexShrink: 1, Gap: 8}, Children: info},
		},
	}}
}

// channelSection 一条线路的选集列表。
func channelSection(cx *core.Scope, a *App, ch api.Channel, play func(api.Episode, api.Channel)) core.View {
	name := ch.Name
	if ch.SourceName != "" && ch.SourceName != name {
		name += "（" + ch.SourceName + "）"
	}
	nowPlaying := a.DetailNowPlaying.Get()
	epRow := core.View{Style: core.Style{Direction: core.Row, Gap: 6, Wrap: core.Wrap}}
	for _, ep := range ch.Episodes {
		ep := ep
		current := ep.URL != "" && ep.URL == nowPlaying
		epRow.Children = append(epRow.Children, core.View{
			StyleFunc: func() core.Style {
				p := cx.Palette()
				st := core.Style{
					Padding: core.Symmetric(3, 10), CornerRadius: 6, FontSize: 12,
					Background: p.Background, Color: p.Text,
					BorderColor: p.Border, BorderWidth: 1, BorderStyle: core.BorderSolid,
				}
				if current {
					st.Background = p.Accent
					st.Color = p.AccentText
					st.BorderColor = p.Accent
				}
				return st
			},
			Children: []core.Node{core.Text{Content: ep.Title}},
			OnPress:  func(core.PressEvent) { play(ep, ch) },
		})
	}
	return theme.Card(cx,
		theme.Title(name, 14, ui.Semibold),
		epRow,
	)
}

// mergeChannels 同名线路合并为一组（各站源对同一线路常有重复命名），
// 组内选集按 URL 去重。
func mergeChannels(channels []api.Channel) []api.Channel {
	out := []api.Channel{}
	index := map[string]int{}
	seen := map[string]bool{}
	for _, ch := range channels {
		name := ch.Name
		if name == "" {
			name = ch.SourceName
		}
		if name == "" {
			name = "线路"
		}
		i, ok := index[name]
		if !ok {
			i = len(out)
			index[name] = i
			out = append(out, api.Channel{Name: name, SourceName: ch.SourceName})
		}
		for _, ep := range ch.Episodes {
			key := ep.URL
			if key == "" {
				key = name + "|" + ep.Title
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out[i].Episodes = append(out[i].Episodes, ep)
		}
	}
	return out
}
