package app

import (
	"kanju/client/api"
	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// SubjectDetail 详情页：豆瓣信息 + 各站源线路/选集聚合。
func SubjectDetail(cx *core.Scope, a *App) core.Node {
	subject := core.NewSignalFunc(cx, (*api.Subject)(nil), func(a, b *api.Subject) bool { return a != b })
	detail := core.NewSignalFunc(cx, (*api.DetailResp)(nil), func(a, b *api.DetailResp) bool { return a != b })
	loading := core.NewSignal(cx, false)
	playLoading := core.NewSignal(cx, "")
	errMsg := core.NewSignal(cx, "")

	// 每次进入页签且上下文变化时重新加载。
	cx.Effect(func() {
		u := a.DetailURL.Get()
		title := a.DetailTitle.Get()
		cover := a.DetailCover.Get()
		if u == "" {
			return
		}
		core.Untracked(cx, func() bool {
			loadDetail(cx, a, u, title, cover, subject, detail, loading, errMsg)
			return true
		})
	})

	play := func(ep api.Episode, ch api.Channel) {
		if playLoading.Get() != "" {
			return
		}
		playLoading.Set("解析播放地址…")
		client := a.API
		go func() {
			r, err := client.Play(ep.URL)
			cx.Post(func() {
				playLoading.Set("")
				if err != nil {
					errMsg.Set(err.Error())
					return
				}
				if r.Type == "iframe" {
					errMsg.Set("该集只能网页播放，请换线路或选集")
					return
				}
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
			s := subject.Get()
			d := detail.Get()
			var head []core.Node
			if s != nil {
				head = append(head, subjectHead(cx, a, s)...)
			} else {
				head = append(head, ui.Text(a.DetailTitle.Get(), 20, ui.Semibold, nil))
			}
			children := []core.Node{}
			children = append(children, head...)

			children = append(children,
				core.Dynamic(func(*core.Scope) core.Node {
					switch {
					case loading.Get():
						return theme.Row(8, ui.Spinner{}, ui.Text("正在聚合站源…", 13, ui.Regular, func() core.Color { return cx.Palette().TextMuted }))
					case errMsg.Get() != "":
						return ui.Text(errMsg.Get(), 13, ui.Regular, func() core.Color { return cx.Palette().Danger })
					case playLoading.Get() != "":
						return theme.Row(8, ui.Spinner{}, ui.Text(playLoading.Get(), 13, ui.Regular, func() core.Color { return cx.Palette().TextMuted }))
					default:
						return core.View{}
					}
				}),
			)

			if d != nil && len(d.Channels) > 0 {
				for _, ch := range d.Channels {
					ch := ch
					children = append(children, channelSection(cx, a, ch, play))
				}
			}
			return core.Scroll{Style: core.Style{FlexGrow: 1, FlexShrink: 1}, Children: []core.Node{
				core.View{Style: core.Style{Gap: 14, Padding: core.All(0)}, Children: children},
			}}
		})},
	}
}

// loadDetail 拉豆瓣详情 + 站源聚合。
func loadDetail(cx *core.Scope, a *App, u, title, cover string,
	subject *core.Signal[*api.Subject], detail *core.Signal[*api.DetailResp],
	loading *core.Signal[bool], errMsg *core.Signal[string]) {
	loading.Set(true)
	errMsg.Set("")
	subject.Set(nil)
	detail.Set(nil)
	client := a.API
	isDouban := contains(u, "movie.douban.com/subject/")
	var doubanID string
	if isDouban {
		doubanID = segmentAfter(u, "/subject/")
	}
	go func() {
		var (
			s   *api.Subject
			d   *api.DetailResp
			sEr error
			dEr error
		)
		if doubanID != "" {
			if v, err := client.DoubanSubject(doubanID, cover); err == nil {
				s = &v
			} else {
				sEr = err
			}
		}
		dv, err := client.Detail(u, "", title)
		if err == nil {
			d = &dv
		} else {
			dEr = err
		}
		cx.Post(func() {
			loading.Set(false)
			if s != nil {
				subject.Set(s)
			}
			if d != nil {
				detail.Set(d)
			}
			if dEr != nil && (s == nil || len(d.Channels) == 0) {
				msg := dEr.Error()
				if sEr != nil {
					msg += "；" + sEr.Error()
				}
				errMsg.Set(msg)
			}
		})
	}()
}

// subjectHead 海报 + 信息头。
func subjectHead(cx *core.Scope, a *App, s *api.Subject) []core.Node {
	coverURL := s.Cover
	if coverURL == "" {
		coverURL = a.DetailCover.Get()
	}
	info := []core.Node{ui.Text(s.Title, 20, ui.Semibold, nil)}
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
		intro := s.Intro
		info = append(info, core.Text{Content: intro, Style: theme.TypeBody.Merge(core.Style{Color: cx.Palette().Text, LineHeight: 22})})
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
	epRow := core.View{Style: core.Style{Direction: core.Row, Gap: 6, Wrap: core.Wrap}}
	for _, ep := range ch.Episodes {
		ep := ep
		epRow.Children = append(epRow.Children, core.View{
			StyleFunc: func() core.Style {
				p := cx.Palette()
				return core.Style{
					Padding: core.Symmetric(3, 10), CornerRadius: 6, FontSize: 12,
					Background: p.Background, Color: p.Text,
					BorderColor: p.Border, BorderWidth: 1, BorderStyle: core.BorderSolid,
				}
			},
			Children: []core.Node{core.Text{Content: ep.Title}},
			OnPress:  func(core.PressEvent) { play(ep, ch) },
		})
	}
	return theme.Card(cx,
		ui.Text(name, 14, ui.Semibold, nil),
		epRow,
	)
}

// ---- 小工具 ----

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// segmentAfter 取 s 中 sep 之后到下一个 / 的片段。
func segmentAfter(s, sep string) string {
	i := indexOf(s, sep)
	if i < 0 {
		return ""
	}
	rest := s[i+len(sep):]
	if j := indexOf(rest, "/"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func joinNonEmpty(items []string, sep string) string {
	out := ""
	for _, s := range items {
		if s == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += s
	}
	return out
}
