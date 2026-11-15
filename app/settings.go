package app

import (
	"strings"

	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// settingsTabs 设置页子页签。
type settingsTab struct {
	id   string
	text string
}

var settingsTabs = []settingsTab{
	{"general", "常规"},
	{"sources", "站源管理"},
	{"logs", "运行日志"},
}

// Settings 设置页：子页签分区（常规 / 站源管理 / 运行日志）——设置项
// 多起来后平铺一页太长，B 站设置页形态是左侧/顶部 tab 分区。
func Settings(cx *core.Scope, a *App) core.Node {
	tab := core.NewSignal(cx, "general")

	return core.View{
		StyleFunc: func() core.Style {
			return theme.Fill().Merge(core.Style{Padding: core.Symmetric(16, 20), Gap: 14, Overflow: core.Hidden})
		},
		Children: []core.Node{
			theme.Title("设置", 20, ui.Semibold),
			settingsTabBar(cx, tab),
			core.Scroll{
				Style: core.Style{FlexGrow: 1, FlexShrink: 1},
				Children: []core.Node{core.Dynamic(func(*core.Scope) core.Node {
					switch tab.Get() {
					case "sources":
						return settingsSourcesPane(cx, a)
					case "logs":
						return settingsLogsPane(cx)
					default:
						return settingsGeneralPane(cx, a)
					}
				})},
			},
		},
	}
}

// settingsTabBar 子页签行：B 站胶囊 tab（选中主色实底白字，未选灰底）。
func settingsTabBar(cx *core.Scope, tab *core.Signal[string]) core.Node {
	row := core.View{Style: core.Style{Direction: core.Row, Gap: 8, FlexShrink: 0}}
	for _, t := range settingsTabs {
		t := t
		row.Children = append(row.Children, core.View{
			StyleFunc: func() core.Style {
				p := cx.Palette()
				st := core.Style{
					Padding: core.Symmetric(6, 16), CornerRadius: 999, FontSize: 13,
					Background: p.Surface, Color: p.Text,
					Hover: &core.Style{Background: p.SurfaceHover},
				}
				if tab.Get() == t.id {
					st.Background = p.Accent
					st.Color = p.AccentText
				}
				return st
			},
			Children: []core.Node{core.Text{Content: t.text}},
			OnPress:  func(core.PressEvent) { tab.Set(t.id) },
		})
	}
	return row
}

// settingsGeneralPane 常规页签：服务器地址（外部模式）与账号。
func settingsGeneralPane(cx *core.Scope, a *App) core.Node {
	draft := core.NewSignal(cx, a.Server.Get())
	msg := core.NewSignal(cx, "")

	save := func() {
		s := trimSpace(draft.Get())
		if s == "" || s == a.Server.Get() {
			return
		}
		a.SetServer(cx, s)
		msg.Set("已切换服务器，请重新登录")
	}

	return core.View{
		StyleFunc: func() core.Style {
			return core.Style{FlexGrow: 1, FlexShrink: 1, Gap: 14}
		},
		Children: []core.Node{
			// 服务器卡片：内嵌模式下没有意义（地址是自动分配的本地端口），隐藏。
			core.Dynamic(func(*core.Scope) core.Node {
				if a.Embedded.Get() {
					return core.View{}
				}
				return serverCard(cx, a, draft, msg, save)
			}),
			core.Dynamic(func(*core.Scope) core.Node {
				// 内嵌模式无账号概念，隐藏账号卡片。
				if a.Embedded.Get() {
					return core.View{}
				}
				return theme.Card(cx,
					theme.Title("账号", 14, ui.Semibold),
					ui.Text(userName(a), 13, ui.Regular, func() core.Color { return cx.Palette().TextMuted }),
					ui.Button{Text: "退出登录", Variant: ui.ButtonSecondary, OnPress: func() { a.Logout(cx) }},
				)
			}),
		},
	}
}

// settingsSourcesPane 站源管理页签（仅管理员可见内容）。
func settingsSourcesPane(cx *core.Scope, a *App) core.Node {
	return core.Dynamic(func(*core.Scope) core.Node {
		u := a.User.Get()
		if u == nil || u.Role != "admin" {
			return theme.Card(cx,
				theme.Title("站源管理", 14, ui.Semibold),
				ui.Text("仅管理员可管理站源", 13, ui.Regular, func() core.Color { return cx.Palette().TextMuted }),
			)
		}
		return SourcesCard(cx, a)
	})
}

// settingsLogsPane 运行日志页签。
func settingsLogsPane(cx *core.Scope) core.Node {
	return LogCard(cx)
}

// serverCard 外部服务器地址卡片（内嵌模式不显示）。
func serverCard(cx *core.Scope, a *App, draft *core.Signal[string], msg *core.Signal[string], save func()) core.View {
	return theme.Card(cx,
		theme.Title("服务器", 14, ui.Semibold),
		core.View{
			StyleFunc: func() core.Style { return core.Style{Direction: core.Row, Gap: 8} },
			Children: []core.Node{
				ui.Input{Value: draft.Get, OnChange: draft.Set, Placeholder: "http://…", TestID: "server",
					Style: core.Style{FlexGrow: 1}},
				ui.Button{Text: "保存", Variant: ui.ButtonPrimary, OnPress: save},
			},
		},
		core.Dynamic(func(*core.Scope) core.Node {
			m := msg.Get()
			if m == "" {
				return core.View{}
			}
			return ui.Text(m, 12, ui.Regular, func() core.Color { return cx.Palette().Danger })
		}),
	)
}

// logVersion 全局日志版本号：新日志 +1，Setup 里启动唯一的监听 goroutine。
var logVersion *core.Signal[int]

// startLogPump 把日志通知泵到 logVersion（进程内只跑一次）。
func startLogPump(cx *core.Scope) {
	logVersion = core.NewSignal(cx, 0)
	tick := make(chan struct{}, 1)
	stop := WatchLogs(tick)
	cx.Cleanup(func() { stop() })
	go func() {
		for range tick {
			cx.Post(func() { logVersion.Update(func(n int) int { return n + 1 }) })
		}
	}()
}

// LogCard 运行日志卡片：客户端事件 + 内嵌后端日志合并展示。
func LogCard(cx *core.Scope) core.View {
	version := logVersion
	if version == nil {
		// SetupForTest 路径没启动日志泵；给个空信号避免崩。
		version = core.NewSignal(cx, 0)
	}

	clear := func() { ClearLogs(); version.Update(func(n int) int { return n + 1 }) }
	copyAll := func() { cx.SetClipboard(strings.Join(Logs(), "\n")) }
	openDir := func() {
		dir := logDirPath()
		if dir == "" {
			return
		}
		ClientLog("[logs] 打开日志目录 %s", dir)
		openLogDir(dir)
	}

	logButtons := []core.Node{
		ui.Button{Text: "复制", Variant: ui.ButtonSecondary, Size: ui.SizeSmall, OnPress: copyAll},
		ui.Button{Text: "清空", Variant: ui.ButtonGhost, Size: ui.SizeSmall, OnPress: clear},
	}
	if openLogDirAvailable {
		// 桌面端专属：移动端无文件管理器概念，日志用复制带走。
		logButtons = append([]core.Node{ui.Button{Text: "打开日志目录", Variant: ui.ButtonSecondary, Size: ui.SizeSmall, OnPress: openDir}}, logButtons...)
	}

	return theme.Card(cx,
		core.View{
			StyleFunc: func() core.Style { return core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8} },
			Children: append([]core.Node{
				core.View{
					StyleFunc: func() core.Style { return core.Style{FlexGrow: 1, FlexShrink: 1} },
					Children:  []core.Node{theme.Title("运行日志", 14, ui.Semibold)},
				},
			}, logButtons...),
		},
		logView(cx, version),
	)
}

// logView 等宽字体的日志文本区。Row 里不重复取快照，直接按行读。
func logView(cx *core.Scope, version *core.Signal[int]) core.Node {
	return core.List{
		Count: func() int {
			version.Get() // 订阅日志变化，驱动重渲染
			return len(Logs())
		},
		EstimatedSize: 18,
		Viewport:      core.ListViewportOwn,
		Row: func(cx *core.Scope, row core.ListRow) core.Node {
			lines := Logs()
			if row.Index() >= len(lines) {
				return core.View{}
			}
			line := lines[row.Index()]
			return core.Text{
				Content: line,
				Style: core.Style{
					FontSize: 11, FontFamily: core.FontMonospace,
					Color: cx.Palette().TextMuted,
				},
			}
		},
		Style: core.Style{FlexGrow: 1, FlexShrink: 1, Height: core.Pt(220), CornerRadius: 6,
			Background: cx.Palette().Background, BorderWidth: 1, BorderColor: cx.Palette().Border,
			BorderStyle: core.BorderSolid, Padding: core.All(8)},
	}
}
