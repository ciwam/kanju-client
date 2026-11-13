package app

import (
	"time"

	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/desktop"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

// playerState 是播放页的局部状态（每次进入播放页签新建；
// 数据源 PlayURL 等在 App 上，页面销毁重建不影响数据）。
type playerState struct {
	paused     *core.Signal[bool]
	position   *core.Signal[time.Duration]
	duration   *core.Signal[time.Duration]
	volume     *core.Signal[float64]
	muted      *core.Signal[bool]
	rate       *core.Signal[float32]
	buffering  *core.Signal[bool]
	playErr    *core.Signal[string]
	attempts   *core.Signal[int] // 同一集的重试次数，用于自动重试
	resumed    *core.Signal[bool]
	barVisible *core.Signal[bool] // 控制栏自动隐藏
	hideGen    *core.Signal[int]  // 隐藏定时代次，新显示使旧定时失效
	// lastSavedSec 上次写进度的秒数（OnProgress 一秒多次，去重用）。
	lastSavedSec int64
	ref          *core.Ref
}

func newPlayerState(cx *core.Scope) *playerState {
	return &playerState{
		paused:     core.NewSignal(cx, false),
		position:   core.NewSignal(cx, time.Duration(0)),
		duration:   core.NewSignal(cx, time.Duration(0)),
		volume:     core.NewSignal(cx, 1.0),
		muted:      core.NewSignal(cx, false),
		rate:       core.NewSignal(cx, float32(1.0)),
		buffering:  core.NewSignal(cx, false),
		playErr:    core.NewSignal(cx, ""),
		attempts:   core.NewSignal(cx, 0),
		barVisible: core.NewSignal(cx, true),
		hideGen:    core.NewSignal(cx, 0),
		ref:        &core.Ref{},
	}
}

// Player 播放页：自绘控制栏（播放/暂停、进度条、音量）、续播、错误重试、全屏。
func Player(cx *core.Scope, a *App) core.Node {
	st := newPlayerState(cx)

	// 播放地址变化：重置状态 + 续播 + 写观看历史。
	cx.Effect(func() {
		src := a.PlayURL.Get()
		title := a.PlayTitle.Get()
		source := a.PlaySource.Get()
		sourceURL := a.PlaySourceURL.Get()
		if src == "" {
			return
		}
		core.Untracked(cx, func() bool {
			st.playErr.Set("")
			st.position.Set(0)
			st.duration.Set(0)
			st.attempts.Set(0)
			st.paused.Set(false)
			// 续播：查历史里同一片源同一集的位置。
			client := a.API
			go func() {
				histories, err := client.Histories()
				if err != nil {
					return
				}
				for _, h := range histories {
					if h.PlayURL == src && h.Position > 5 && h.Duration > 0 && h.Position < h.Duration-10 {
						pos := time.Duration(h.Position) * time.Second
						cx.Post(func() {
							st.ref.Seek(pos)
							ClientLog("[player] resume at %s", fmtDuration(pos))
						})
						break
					}
				}
			}()
			// 写观看历史。
			go func() {
				_ = client.SaveHistory(historyEntry(title, src, source, sourceURL))
			}()
			return true
		})
	})

	// 播放中定期保存进度（每 10 秒）。
	cx.Effect(func() {
		// 周期 effect：由 position 驱动节流保存。
		pos := st.position.Get()
		src := a.PlayURL.Get()
		if src == "" || st.duration.Get() == 0 {
			return
		}
		core.Untracked(cx, func() bool {
			secs := int64(pos.Seconds())
			// 同一秒的多次 OnProgress 只写一次（OnProgress 约 250ms 一次）。
			if secs > 0 && secs%10 == 0 && secs != st.lastSavedSec {
				st.lastSavedSec = secs
				a.saveProgress(src, secs, int64(st.duration.Get().Seconds()))
			}
			return true
		})
	})

	// 错误自动重试：同一集最多重试 2 次。
	autoRetry := func(errMsg string) {
		n := st.attempts.Get()
		if n < 2 {
			st.attempts.Set(n + 1)
			ClientLog("[player] auto retry #%d after error: %s", n+1, errMsg)
			// 通过重设 paused 重新拉流：nsgo 的 Video 在 Error 后需要重挂源。
			src := a.PlayURL.Get()
			a.PlayURL.Set("") // 先清源卸载
			go func() {
				time.Sleep(800 * time.Millisecond)
				cx.Post(func() { a.PlayURL.Set(src) })
			}()
		}
	}

	// seek 相对跳转（快进快退），卡到 duration 边界。
	seek := func(delta time.Duration) {
		dur := st.duration.Get()
		if dur <= 0 {
			return
		}
		next := st.position.Get() + delta
		if next < 0 {
			next = 0
		}
		if next > dur {
			next = dur
		}
		st.ref.Seek(next)
		st.position.Set(next)
		st.showBar(cx)
	}

	// 键盘快捷键：空格暂停、←/→ ±10s、↑/↓ 音量、F 全屏。
	cx.OnKey("space", func() { st.paused.Update(func(v bool) bool { return !v }); st.showBar(cx) })
	cx.OnKey("left", func() { seek(-10 * time.Second) })
	cx.OnKey("right", func() { seek(10 * time.Second) })
	cx.OnKey("up", func() {
		st.volume.Update(func(v float64) float64 {
			v += 0.1
			if v > 1 {
				v = 1
			}
			st.muted.Set(false)
			return v
		})
		st.showBar(cx)
	})
	cx.OnKey("down", func() {
		st.volume.Update(func(v float64) float64 {
			v -= 0.1
			if v < 0 {
				v = 0
			}
			return v
		})
		st.showBar(cx)
	})
	cx.OnKey("f", func() { desktop.ToggleFullScreen(cx) })

	return core.View{
		StyleFunc: func() core.Style {
			return theme.Fill().Merge(core.Style{Gap: 0, Overflow: core.Hidden})
		},
		Children: []core.Node{core.Dynamic(func(*core.Scope) core.Node {
			title := a.PlayTitle.Get()
			src := a.PlayURL.Get()
			if src == "" {
				return theme.Empty(cx, "从详情页选择一集开始播放")
			}

			video := core.Video{
				Ref:        st.ref,
				Source:     src,
				PausedFunc: st.paused.Get,
				MutedFunc:  st.muted.Get,
				VolumeFunc: func() float32 { return float32(st.volume.Get()) },
				RateFunc:   st.rate.Get,
				ResizeMode: core.Contain,
				Controls:   false, // 自绘控制栏
				Style:      core.Style{FlexGrow: 1, FlexShrink: 1, Width: core.Pct(100), Background: core.RGB(0x000000)},
				OnLoad:     func(i core.VideoInfo) { st.duration.Set(i.Duration) },
				OnProgress: func(pos, dur time.Duration) { st.position.Set(pos); st.duration.Set(dur) },
				OnBuffer:   func(b bool) { st.buffering.Set(b) },
				OnError:    func(err error) { st.playErr.Set(err.Error()); autoRetry(err.Error()) },
				TestID:     "video",
				StyleFunc:  nil,
				OnPress: func(ev core.PressEvent) {
					st.showBar(cx)
					if ev.Clicks >= 2 {
						desktop.ToggleFullScreen(cx)
						return
					}
					st.paused.Update(func(p bool) bool { return !p })
				},
			}

			// 播放器区域 = 视频 + 覆盖其上的底部控制栏 + 状态角标。
			playerArea := core.View{
				StyleFunc: func() core.Style {
					return core.Style{FlexGrow: 1, FlexShrink: 1, Width: core.Pct(100), Overflow: core.Hidden}
				},
				Children: []core.Node{
					video,
					badge(cx, st),
					controlBar(cx, a, st),
				},
			}

			return core.View{
				Style: core.Style{Gap: 6, FlexGrow: 1, FlexShrink: 1, Padding: core.Symmetric(8, 12)},
				Children: []core.Node{
					theme.Row(10,
						theme.Title(title, 15, ui.Semibold),
					),
					playerArea,
				},
			}
		})},
	}
}

// badge 缓冲/错误角标：悬浮在视频中央上方。
func badge(cx *core.Scope, st *playerState) core.Node {
	return core.Dynamic(func(*core.Scope) core.Node {
		p := cx.Palette()
		switch {
		case st.playErr.Get() != "":
			n := st.attempts.Get()
			msg := "播放失败：" + st.playErr.Get()
			if n < 2 {
				msg += "（自动重试中…）"
			}
			return overlayBox(cx, p.Danger, msg)
		case st.buffering.Get():
			return overlayBox(cx, p.TextMuted, "缓冲中…")
		default:
			return core.View{}
		}
	})
}

func overlayBox(cx *core.Scope, color core.Color, text string) core.View {
	p := cx.Palette()
	return core.View{
		Style: core.Style{
			Position: core.Absolute, Top: core.Pt(12), Left: core.Pct(50),
			Transform: core.Transform{TranslateXPercent: -50},
			Padding:   core.Symmetric(4, 12), CornerRadius: 6,
			Background: p.Background, Color: color, FontSize: 12,
		},
		Children: []core.Node{core.Text{Content: text}},
	}
}

// controlBar 底部悬浮控制栏：播放/暂停、±10s、进度条、时间、倍速、音量、全屏。
// 无操作 3 秒自动隐藏，鼠标在播放器区域内移动/按压即恢复。
func controlBar(cx *core.Scope, a *App, st *playerState) core.Node {
	return core.Dynamic(func(*core.Scope) core.Node {
		visible := st.barVisible.Get()
		p := cx.Palette()
		if !visible {
			// 隐藏时保留一个透明热区，鼠标进入恢复。
			return core.View{
				Style: core.Style{
					Position: core.Absolute, Left: core.Pt(0), Right: core.Pt(0), Bottom: core.Pt(0),
					Height: core.Pt(36),
				},
				OnMouseEnter: func(core.MouseEvent) { st.showBar(cx) },
			}
		}
		bar := core.View{
			Style: core.Style{
				Position: core.Absolute, Left: core.Pt(0), Right: core.Pt(0), Bottom: core.Pt(0),
				Direction: core.Row, AlignItems: core.AlignCenter, Gap: 10,
				Padding:    core.Symmetric(6, 12),
				Background: p.Background,
			},
			Children: []core.Node{
				// 播放/暂停。
				core.View{
					Style:    core.Style{Width: core.Pt(28), Height: core.Pt(28), AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter},
					Children: []core.Node{ui.Icon(playIcon(st), 18, func() core.Color { return p.Text })},
					OnPress:  func(core.PressEvent) { st.paused.Update(func(v bool) bool { return !v }) },
				},
				// 快退 10s。
				core.View{
					Style:    core.Style{Width: core.Pt(26), Height: core.Pt(28), AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter},
					Children: []core.Node{ui.Text("-10s", 10, ui.Medium, func() core.Color { return p.Text })},
					OnPress:  func(core.PressEvent) { st.ref.Seek(maxDur(st.position.Get()-10*time.Second, 0)) },
				},
				// 快进 10s。
				core.View{
					Style:    core.Style{Width: core.Pt(30), Height: core.Pt(28), AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter},
					Children: []core.Node{ui.Text("+10s", 10, ui.Medium, func() core.Color { return p.Text })},
					OnPress: func(core.PressEvent) {
						d := st.duration.Get()
						next := st.position.Get() + 10*time.Second
						if d > 0 && next > d {
							next = d
						}
						st.ref.Seek(next)
					},
				},
				// 进度条。
				progressSlider(cx, st),
				// 时间。
				core.Text{
					ContentFunc: func() string {
						return fmtDuration(st.position.Get()) + " / " + fmtDuration(st.duration.Get())
					},
					Style: core.Style{FontSize: 11, Color: p.TextMuted},
				},
				// 倍速。
				core.View{
					Style: core.Style{Padding: core.Symmetric(2, 8), CornerRadius: 4},
					Children: []core.Node{core.Text{
						ContentFunc: func() string {
							r := st.rate.Get()
							if r == 1 {
								return "倍速"
							}
							return fmtRate(r)
						},
						Style: core.Style{FontSize: 12, Color: p.Text},
					}},
					OnPress: func(core.PressEvent) {
						st.rate.Update(func(r float32) float32 {
							switch r {
							case 1:
								return 1.25
							case 1.25:
								return 1.5
							case 1.5:
								return 2
							case 2:
								return 0.5
							default:
								return 1
							}
						})
					},
				},
				// 音量。
				volumeControl(cx, st),
				// 全屏。
				core.View{
					Style:    core.Style{Width: core.Pt(28), Height: core.Pt(28), AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter},
					Children: []core.Node{ui.Icon(lucide.Maximize, 16, func() core.Color { return p.Text })},
					OnPress:  func(core.PressEvent) { desktop.ToggleFullScreen(cx) },
				},
			},
			OnMouseEnter: func(core.MouseEvent) { st.showBar(cx) },
		}
		return bar
	})
}

func maxDur(a, b time.Duration) time.Duration {
	if a < b {
		return b
	}
	return a
}

func fmtRate(r float32) string {
	switch r {
	case 2:
		return "2.0x"
	case 0.5:
		return "0.5x"
	default:
		// 1.25/1.5 等
		s := itoa(int64(r * 100))
		return s[:len(s)-2] + "." + s[len(s)-2:] + "x"
	}
}

// showBar 显示控制栏并安排 3 秒后自动隐藏（暂停时不隐藏）。
// hideGen 递增使旧的定时 goroutine 失效。
func (st *playerState) showBar(cx *core.Scope) {
	st.showBar(cx)
	st.hideGen.Set(st.hideGen.Get() + 1)
	gen := st.hideGen.Get()
	go func() {
		time.Sleep(3 * time.Second)
		cx.Post(func() {
			if st.hideGen.Get() == gen && !st.paused.Get() {
				st.barVisible.Set(false)
			}
		})
	}()
}

func playIcon(st *playerState) func() *core.SVG {
	return func() *core.SVG {
		if st.paused.Get() {
			return lucide.Play()
		}
		return lucide.Pause()
	}
}

// progressSlider 进度条：拖动 seek，拖动中不跳变。
func progressSlider(cx *core.Scope, st *playerState) core.Node {
	dragging := core.NewSignal(cx, false)
	dragValue := core.NewSignal(cx, 0.0)

	return base.Slider{
		Value: func() base.SliderValue {
			if dragging.Get() {
				return base.SingleValue(dragValue.Get())
			}
			dur := st.duration.Get().Seconds()
			if dur <= 0 {
				return base.SingleValue(0)
			}
			return base.SingleValue(st.position.Get().Seconds() / dur * 100)
		},
		Min:   func() float64 { return 0 },
		Max:   func() float64 { return 100 },
		Step:  func() float64 { return 0.01 },
		Axis:  base.Horizontal,
		Label: "播放进度",
		Style: core.Style{FlexGrow: 1, FlexShrink: 1},
		OnChange: func(v base.SliderValue) {
			dragging.Set(true)
			dragValue.Set(v.End)
		},
		OnChangeEnd: func(v base.SliderValue) {
			dragging.Set(false)
			dur := st.duration.Get()
			if dur > 0 {
				st.ref.Seek(time.Duration(v.End / 100 * float64(dur)))
			}
		},
		TrackStyle:     core.Style{Height: core.Pt(4), CornerRadius: 2},
		IndicatorStyle: core.Style{CornerRadius: 2},
	}
}

// volumeControl 音量：图标点击静音，拖动调音量。
func volumeControl(cx *core.Scope, st *playerState) core.Node {
	return core.Dynamic(func(*core.Scope) core.Node {
		p := cx.Palette()
		icon := lucide.Volume2
		if st.muted.Get() || st.volume.Get() == 0 {
			icon = lucide.VolumeX
		} else if st.volume.Get() < 0.5 {
			icon = lucide.Volume1
		}
		return core.View{
			Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 4},
			Children: []core.Node{
				core.View{
					Style:    core.Style{Width: core.Pt(24), Height: core.Pt(24), AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter},
					Children: []core.Node{ui.Icon(icon, 16, func() core.Color { return p.Text })},
					OnPress:  func(core.PressEvent) { st.muted.Update(func(m bool) bool { return !m }) },
				},
				base.Slider{
					Value: func() base.SliderValue { return base.SingleValue(st.volume.Get() * 100) },
					Min:   func() float64 { return 0 },
					Max:   func() float64 { return 100 },
					Axis:  base.Horizontal,
					Label: "音量",
					Style: core.Style{Width: core.Pt(64)},
					OnChange: func(v base.SliderValue) {
						st.volume.Set(v.End / 100)
						st.muted.Set(false)
					},
					TrackStyle:     core.Style{Height: core.Pt(3), CornerRadius: 2},
					IndicatorStyle: core.Style{CornerRadius: 2},
				},
			},
		}
	})
}

// saveProgress 写播放进度到历史。
func (a *App) saveProgress(src string, position, duration int64) {
	title := a.PlayTitle.Get()
	source := a.PlaySource.Get()
	sourceURL := a.PlaySourceURL.Get()
	h := historyEntry(title, src, source, sourceURL)
	h.Position = position
	h.Duration = duration
	go func() { _ = a.API.SaveHistory(h) }()
}

// fmtDuration 把 duration 格式化为 mm:ss 或 h:mm:ss。
func fmtDuration(d time.Duration) string {
	secs := int64(d.Seconds())
	if secs <= 0 {
		return "0:00"
	}
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	if h > 0 {
		return itoa(h) + ":" + pad2(m) + ":" + pad2(s)
	}
	return itoa(m) + ":" + pad2(s)
}

func pad2(n int64) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}
