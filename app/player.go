package app

import (
	"time"

	"kanju/client/api"
	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// Player 播放页：core.Video + 历史记录。
func Player(cx *core.Scope, a *App) core.Node {
	paused := core.NewSignal(cx, false)
	position := core.NewSignal(cx, time.Duration(0))
	duration := core.NewSignal(cx, time.Duration(0))
	buffering := core.NewSignal(cx, false)
	playErr := core.NewSignal(cx, "")
	ref := &core.Ref{}

	// 播放地址变化时重新装配播放器。
	cx.Effect(func() {
		src := a.PlayURL.Get()
		title := a.PlayTitle.Get()
		source := a.PlaySource.Get()
		sourceURL := a.PlaySourceURL.Get()
		if src == "" {
			return
		}
		core.Untracked(cx, func() bool {
			playErr.Set("")
			position.Set(0)
			duration.Set(0)
			// 写观看历史。
			client := a.API
			go func() {
				_ = client.SaveHistory(historyEntry(title, src, source, sourceURL))
			}()
			return true
		})
	})

	return core.View{
		StyleFunc: func() core.Style {
			return theme.Fill().Merge(core.Style{Padding: core.Symmetric(12, 16), Gap: 10, Overflow: core.Hidden})
		},
		Children: []core.Node{
			core.Dynamic(func(*core.Scope) core.Node {
				title := a.PlayTitle.Get()
				src := a.PlayURL.Get()
				if src == "" {
					return theme.Empty(cx, "从详情页选择一集开始播放")
				}
				children := []core.Node{
					core.Video{
						Ref: ref, Source: src,
						PausedFunc: paused.Get,
						ResizeMode: core.Contain,
						Controls:   true,
						Style:      core.Style{FlexGrow: 1, FlexShrink: 1, Width: core.Pct(100), CornerRadius: 8, Background: core.RGB(0x000000)},
						OnLoad:     func(i core.VideoInfo) { duration.Set(i.Duration) },
						OnProgress: func(pos, dur time.Duration) { position.Set(pos); duration.Set(dur) },
						OnBuffer:   func(b bool) { buffering.Set(b) },
						OnError:    func(err error) { playErr.Set(err.Error()) },
						TestID:     "video",
					},
				}
				row := theme.Row(10,
					theme.Title(title, 15, ui.Semibold),
				)
				status := core.Dynamic(func(*core.Scope) core.Node {
					msg := ""
					if buffering.Get() {
						msg = "缓冲中…"
					}
					if e := playErr.Get(); e != "" {
						msg = "播放失败：" + e
					}
					if msg == "" {
						return core.View{}
					}
					color := cx.Palette().TextMuted
					if playErr.Get() != "" {
						color = cx.Palette().Danger
					}
					return ui.Text(msg, 12, ui.Regular, func() core.Color { return color })
				})
				return core.View{Style: core.Style{Gap: 10, FlexGrow: 1, FlexShrink: 1}, Children: append([]core.Node{row, status}, children...)}
			}),
		},
	}
}

// historyEntry 组装历史条目。title 形如「片名 · 第x集」。
func historyEntry(title, playURL, sourceName, sourceURL string) api.History {
	t, ep := splitEpisodeTitle(title)
	return api.History{
		Title: t, EpisodeTitle: ep, PlayURL: playURL,
		SourceName: sourceName, SourceURL: sourceURL,
	}
}

// splitEpisodeTitle 按「 · 」拆分片名与集名。
func splitEpisodeTitle(s string) (title, episode string) {
	for i := 0; i+3 <= len(s); i++ {
		if s[i] == '·' && i > 0 && s[i-1] == ' ' && s[i+1] == ' ' {
			return trimSpace(s[:i-1]), trimSpace(s[i+2:])
		}
	}
	return s, ""
}
