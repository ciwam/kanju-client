package app

import (
	"fmt"
	"strings"
	"time"

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
	// 防重入：同 URL 已在加载时跳过（effect 会因页签重渲染被重复创建）。
	if a.DetailLoading.Get() && a.DetailURL.Get() == u {
		ClientLog("[detail] 跳过重复加载 %s", u)
		return
	}
	a.DetailLoading.Set(true)
	a.DetailErr.Set("")
	a.DetailSubject.Set(nil)
	a.DetailData.Set(nil)
	client := a.API
	doubanID := ""
	if contains(u, "movie.douban.com/subject/") {
		doubanID = segmentAfter(u, "/subject/")
	}

	// 豆瓣头部信息：独立加载、独立显示。SWR 两段式：第一段可能拿到
	// 缓存旧数据（stale=true），立即显示；第二段强制刷新拉最新，
	// revision 有变化 → 更新动画（DetailUpdating）→ 自动替换新信息
	// （缓存已于后端写库，下次直接是新的）。
	go func() {
		if doubanID == "" {
			return
		}
		v, err := client.DoubanSubject(doubanID, cover)
		if err != nil {
			ClientLog("[detail] douban subject error: %v", err)
			return
		}
		cx.Post(func() {
			a.DetailSubject.Set(&v)
			ClientLog("[detail] subject ready %q stale=%v", v.Title, v.Stale)
		})
		if !v.Stale {
			return
		}
		// 旧数据已显示；后台强制刷新，revision 变了才替换。
		nv, err := client.DoubanSubjectRefresh(doubanID, cover)
		if err != nil {
			return // 刷新失败静默保留旧数据（后端已重试过 SWR）
		}
		cx.Post(func() {
			if u := a.DetailURL.Get(); u == "" || segmentAfter(u, "/subject/") != doubanID {
				return // 已切到别的影片，丢弃
			}
			if nv.Revision == "" || nv.Revision == v.Revision {
				return // 内容没变
			}
			a.DetailUpdating.Set(true)
			ClientLog("[detail] subject updated %q (rev %s -> %s)", nv.Title, v.Revision, nv.Revision)
			// 更新动画停留 1.2 秒后替换新信息（用户要求：动画提示后
			// 自动替换，信息即时）。
			go func() {
				time.Sleep(1200 * time.Millisecond)
				cx.Post(func() {
					a.DetailSubject.Set(&nv)
					a.DetailUpdating.Set(false)
				})
			}()
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

	return core.View{
		StyleFunc: func() core.Style {
			return theme.Fill().Merge(core.Style{Padding: core.Symmetric(16, 20), Gap: 12, Overflow: core.Hidden})
		},
		Children: []core.Node{episodeWatchPage(cx, a)},
	}
}

// episodeWatchPage B 站视频页单页布局：上双栏（左播放器/封面、右选集
// 列表），下信息与线路卡片。数据与渲染快照日志同原 SubjectDetail。
func episodeWatchPage(cx *core.Scope, a *App) core.Node {
	// 结构常驻：播放器组件不进 Dynamic（重跑会销毁重建 Video 导致
	// 重播，见 docs/nsgo-bugs.md 限制节）；变化的部分（头部信息、
	// 线路列表、封面覆盖层）各自用 Dynamic 包裹。

	// 上双栏：左播放器 + 右选集列表。
	playerPane := core.View{
		StyleFunc: func() core.Style {
			return core.Style{FlexGrow: 1, FlexShrink: 1, Overflow: core.Hidden, CornerRadius: 8, Background: core.RGB(0x000000)}
		},
		Children: []core.Node{
			PlayerSurface(cx, a),
			// 未播放时的封面覆盖层（PlayURL 空且有封面时显示）。
			core.Dynamic(func(*core.Scope) core.Node {
				if a.PlayURL.Get() != "" {
					return core.View{}
				}
				cover := a.DetailCover.Get()
				if s := a.DetailSubject.Get(); s != nil && s.Cover != "" {
					cover = s.Cover
				}
				if cover == "" {
					return core.View{}
				}
				return core.View{
					Style: core.Style{Position: core.Absolute, Left: core.Pt(0), Right: core.Pt(0), Top: core.Pt(0), Bottom: core.Pt(0),
						AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter,
						Background: core.RGB(0x111111)},
					Children: []core.Node{
						// 封面铺满播放器区域（Cover 裁剪，不再用原始小尺寸）。
						core.Image{
							Source:     core.Src(a.API.ImageURL(cover, a.DetailTitle.Get())),
							ResizeMode: core.Cover,
							Label:      a.DetailTitle.Get(),
							Style:      core.Style{Width: core.Pct(100), Height: core.Pct(100)},
						},
					},
				}
			}),
		},
	}

	watchRow := core.View{
		StyleFunc: func() core.Style {
			return core.Style{Direction: core.Row, Gap: 12, FlexGrow: 1, FlexShrink: 1, Overflow: core.Hidden}
		},
		Children: []core.Node{
			playerPane,
			core.View{
				StyleFunc: func() core.Style {
					p := cx.Palette()
					return core.Style{
						Width: core.Pt(320), FlexShrink: 0, Padding: core.All(8), Gap: 4,
						Background: p.Background, CornerRadius: 8, Overflow: core.Hidden,
					}
				},
				Children: []core.Node{episodeSidebar(cx, a)},
			},
		},
	}

	// 下区：标题信息 + 状态 + 线路卡片（Dynamic：随聚合数据更新）。
	// 状态与站源选集全部在右栏（episodeSidebar），下方无重复内容。
	return core.View{
		Style:     theme.Fill().Merge(core.Style{Overflow: core.Hidden}),
		Children:  []core.Node{watchRow},
	}
}

// playFallback 换源重试：在其它线路里找可播的同集依次尝试解析。
// 线路整体失效（如量子资源播放域名全部 404）时，同集在其它站源
// 线路仍可播。全部失败返回最后一个错误。
//
// 匹配两级：
//  1. 同名选集——各站源集数命名一致时（第07集 → 第07集）；
//  2. 单选集线路——命名粒度不同（正片/HD/全集，通常整部一条流），
//     点哪集都是同一条源，选集数==1 的线路直接试。
//     区间型（第01-03集）不参与：无法确定目标集在其中，播放错集。
func playFallback(cx *core.Scope, a *App, client *api.Client, ep api.Episode, failed api.Channel) (api.PlayResult, error) {
	return playFallbackWith(cx, a, client.Play, ep, failed)
}

// normalizeEpisodeTitle 集数标题标准化：全/半角数字统一、去掉数字
// 前导零与分隔符（"第01集"≡"第1集"、"第 08 集"≡"第8集"）。各站源
// 对同一部剧的集数命名粒度/格式不同，换源匹配靠它。
func normalizeEpisodeTitle(t string) string {
	t = strings.TrimSpace(t)
	// 全角数字/拉丁转半角。
	var b strings.Builder
	for _, r := range t {
		switch {
		case r >= '０' && r <= '９':
			b.WriteRune(rune(r-'０') + '0')
		case r >= 'Ａ' && r <= 'Ｚ':
			b.WriteRune(rune(r-'Ａ') + 'A')
		case r >= 'ａ' && r <= 'ｚ':
			b.WriteRune(rune(r-'ａ') + 'a')
		default:
			b.WriteRune(r)
		}
	}
	t = b.String()
	// 提取「第 N 集/话/」中的 N 并归一为 第N集；不匹配结构的原样返回
	// （小写化以便 "EP01"/"ep1" 归一）。
	lower := strings.ToLower(t)
	if i := strings.Index(lower, "ep"); i >= 0 {
		if n := trimLeadingZeros(lower[i+2:]); n != "" {
			return "ep" + n
		}
	}
	if i := strings.Index(t, "第"); i >= 0 {
		rest := t[i+3:] // 跳过 "第"（3 字节）
		end := strings.IndexAny(rest, "集话")
		if end > 0 {
			if n := trimLeadingZeros(rest[:end]); n != "" {
				return "第" + n + "集"
			}
		}
	}
	return lower
}

// trimLeadingZeros 去掉纯数字串的前导零（"01"→"1"）；非纯数字返回空。
func trimLeadingZeros(s string) string {
	s = strings.TrimSpace(s)
	digits := strings.TrimLeft(s, "0")
	for _, r := range digits {
		if r < '0' || r > '9' {
			return ""
		}
	}
	if digits == "" {
		return "0"
	}
	return digits
}

// episodeTitleMatch 两集是否为同一集：标准化后相等。
func episodeTitleMatch(a, b string) bool {
	return normalizeEpisodeTitle(a) == normalizeEpisodeTitle(b) && normalizeEpisodeTitle(a) != ""
}

// playFallbackWith 是 playFallback 的可测形态：play 函数注入。
// 注意：在后台 goroutine 运行，信号 Set 一律经 cx.Post 投递到 UI
// 线程——直接 Set 与 UI 线程的渲染 effect 并发修改信号订阅者列表，
// 是快速连点集数时应用闪退的根因。
func playFallbackWith(cx *core.Scope, a *App, play func(string) (api.PlayResult, error), ep api.Episode, failed api.Channel) (api.PlayResult, error) {
	data := a.DetailData.Get()
	if data == nil {
		return api.PlayResult{}, fmt.Errorf("获取播放地址失败")
	}
	var lastErr error = fmt.Errorf("获取播放地址失败")
	merged := mergeChannels(data.Channels)
	for _, ch := range merged {
		if ch.Name == failed.Name && ch.SourceName == failed.SourceName {
			continue
		}
		for _, cand := range ch.Episodes {
			if !episodeTitleMatch(cand.Title, ep.Title) && len(ch.Episodes) != 1 {
				continue // 非同集且非单选集线路：不猜
			}
			ClientLog("[detail] 换源重试 %q line=%q(%s) url=%s", ep.Title, ch.Name, ch.SourceName, cand.URL)
			busy := "当前线路不可用，尝试 " + ch.Name + "…"
			if cx != nil {
				cx.Post(func() { a.DetailPlayBusy.Set(busy) })
			}
			r, err := play(cand.URL)
			if err == nil {
				ClientLog("[detail] 换源成功 line=%q", ch.Name)
				return r, nil
			}
			lastErr = err
		}
	}
	return api.PlayResult{}, lastErr
}

// favButton 收藏/已收藏切换按钮（状态在 App.DetailFav）。内嵌后端
// 的收藏接口同样可用（NoAuth 虚拟 admin），不再区分模式。
func favButton(cx *core.Scope, a *App) core.Node {
	return core.Dynamic(func(*core.Scope) core.Node {
		if a.DetailFav.Get() {
			// 已收藏：主色深底白字（状态更醒目）。
			return ui.Button{Text: "★ 已收藏", Variant: ui.ButtonPrimary, Size: ui.SizeSmall,
				OnPress: func() { a.ToggleFav(cx) }}
		}
		// 未收藏：主色浅底（12% 透出）+ 主色文字，深浅随收藏状态切换。
		return ui.Button{Text: "☆ 收藏", Variant: ui.ButtonSecondary, Size: ui.SizeSmall,
			Color:   func() core.Color { return core.RGBA(0x00a1d6, 0.12) },
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
			theme.Title(s.Title, 22, ui.Semibold),
			favButton(cx, a),
			// 影片信息后台刷新中（SWR stale→refresh 检测到变化）：
			// 旋转指示 + 文案，1.2 秒后新信息自动替换。
			core.Dynamic(func(*core.Scope) core.Node {
				if !a.DetailUpdating.Get() {
					return core.View{}
				}
				return theme.Row(6, ui.Spinner{},
					ui.Text("影片信息已更新，正在刷新…", 12, ui.Regular, func() core.Color { return cx.Palette().Accent }))
			}),
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

// epCollapseLimit 选集折叠阈值：超过则默认只渲染前 N 个。
// 142 集 × 8 线路一次全渲染会把主线程卡死甚至 OOM（闪退根因）。
const epCollapseLimit = 40

// epBatchSize 展开/追加时每批渲染的数量。
const epBatchSize = 80

// channelSection 一条线路的选集列表（超长折叠）。
func channelSection(cx *core.Scope, a *App, ch api.Channel, play func(api.Episode, api.Channel)) core.View {
	name := ch.Name
	if ch.SourceName != "" && ch.SourceName != name {
		name += "（" + ch.SourceName + "）"
	}
	nowPlaying := a.DetailNowPlaying.Get()
	// shown 当前渲染到的集数：初始折叠数，点「展开更多」每批追加。
	shown := core.NewSignal(cx, epCollapseLimit)
	body := core.Dynamic(func(*core.Scope) core.Node {
		eps := ch.Episodes
		limit := shown.Get()
		// 保证当前播放的集可见（截窗向前扩）。
		if nowPlaying != "" {
			for i, ep := range eps {
				if ep.URL == nowPlaying && i >= limit {
					limit = i + 1
					break
				}
			}
		}
		row := core.View{Style: core.Style{Direction: core.Row, Gap: 6, Wrap: core.Wrap}}
		for i := 0; i < limit && i < len(eps); i++ {
			ep := eps[i]
			current := ep.URL != "" && ep.URL == nowPlaying
			row.Children = append(row.Children, core.View{
				StyleFunc: func() core.Style {
					p := cx.Palette()
					st := core.Style{
						Padding: core.Symmetric(6, 14), CornerRadius: 6, FontSize: 13,
						AlignItems: core.AlignCenter, MinWidth: core.Pt(72),
						Background: p.Surface, Color: p.Text,
						BorderColor: p.Border, BorderWidth: 1, BorderStyle: core.BorderSolid,
						Hover: &core.Style{BorderColor: p.Accent, Color: p.Accent},
					}
					if current {
						st.Background = p.Accent
						st.Color = p.AccentText
						st.BorderColor = p.Accent
					}
					return st
				},
				Children: []core.Node{core.Text{Content: ep.Title}},
				OnPress:  func(core.PressEvent) { a.PlayEpisode(cx, ep, ch) },
			})
		}
		children := []core.Node{row}
		if remaining := len(eps) - limit; remaining > 0 {
			// 分批展开：点一次追加一批，避免一次渲染几百个按钮卡顿。
			batch := remaining
			if batch > epBatchSize {
				batch = epBatchSize
			}
			children = append(children, ui.Button{
				Text:    "展开更多（剩余 " + itoa(int64(remaining)) + " 集）",
				Variant: ui.ButtonGhost, Size: ui.SizeSmall,
				OnPress: func() { shown.Update(func(n int) int { return n + batch }) },
			})
		}
		return core.View{Style: core.Style{Gap: 8}, Children: children}
	})
	return theme.Card(cx,
		theme.Title(name, 14, ui.Semibold),
		body,
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

// PlayFallbackForTest 供外部测试驱动换源逻辑（play 函数注入）。
func PlayFallbackForTest(state *App, ep api.Episode, failed api.Channel, play func(string) (api.PlayResult, error)) (api.PlayResult, error) {
	return playFallbackWith(nil, state, play, ep, failed)
}

// EpisodeTitleMatchForTest 供外部测试集数标题匹配。
func EpisodeTitleMatchForTest(a, b string) bool { return episodeTitleMatch(a, b) }
