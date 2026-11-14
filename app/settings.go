package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// Settings 设置页：服务器地址、账号信息、运行日志。
func Settings(cx *core.Scope, a *App) core.Node {
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
			return theme.Fill().Merge(core.Style{Padding: core.Symmetric(16, 20), Gap: 14, Overflow: core.Hidden})
		},
		Children: []core.Node{
			theme.Title("设置", 20, ui.Semibold),
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
			// 影片数据源选择（TMDB 优先需后端已配置 key）。
			dataSourceCard(cx, a),
			// 站源管理（仅管理员）。
			core.Dynamic(func(*core.Scope) core.Node {
				u := a.User.Get()
				if u == nil || u.Role != "admin" {
					return core.View{}
				}
				return SourcesCard(cx, a)
			}),
			LogCard(cx),
		},
	}
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

// dataSourceCard 影片数据源卡片：豆瓣优先 / TMDB 优先。
func dataSourceCard(cx *core.Scope, a *App) core.View {
	return theme.Card(cx,
		theme.Title("影片数据源", 14, ui.Semibold),
		core.Dynamic(func(*core.Scope) core.Node {
			p := cx.Palette()
			isTMDB := a.PreferSourceSignal.Get() == "tmdb"
			// 两个选项按钮：选中态 accent 底。
			makeOpt := func(label, val string, active bool) core.Node {
				st := core.Style{
					Padding: core.Symmetric(5, 14), CornerRadius: 7, FontSize: 13,
					BorderWidth: 1, BorderStyle: core.BorderSolid,
					Hover: &core.Style{Background: p.SurfaceHover},
				}
				if active {
					st.Background = p.Accent
					st.Color = p.AccentText
					st.BorderColor = p.Accent
				} else {
					st.Background = p.Background
					st.Color = p.Text
					st.BorderColor = p.Border
				}
				return core.View{
					Style:    st,
					Children: []core.Node{core.Text{Content: label}},
					OnPress:  func(core.PressEvent) { a.SetPreferSource(cx, val) },
				}
			}
			var note core.Node
			if isTMDB {
				note = ui.Text("TMDB 评分（10 分制），豆瓣不可用时也保持可用", 12, ui.Regular,
					func() core.Color { return p.TextMuted })
			} else {
				note = ui.Text("豆瓣评分（5 星制）；豆瓣不可用时自动切 TMDB", 12, ui.Regular,
					func() core.Color { return p.TextMuted })
			}
			return core.View{
				Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 10, Wrap: core.Wrap},
				Children: []core.Node{
					makeOpt("豆瓣优先", "douban", !isTMDB),
					makeOpt("TMDB 优先", "tmdb", isTMDB),
					note,
				},
			}
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
		// nsgo 的 OpenURL 拒绝 file:// 协议（refusedSchemes）。
		// open -R 在连续点击时会让 Finder 主队列 SIGABRT 崩溃（实测，
		// 见 DiagnosticReports/Finder-*.ips），osascript reveal 稳定。
		target := filepath.Join(dir, time.Now().Format("2006-01-02")+".log")
		if _, err := os.Stat(target); err != nil {
			// 当天文件还没生成时退回打开目录。
			script := fmt.Sprintf(`tell application "Finder" to open (POSIX file %q as alias)`, dir)
			if err := exec.Command("osascript", "-e", script).Start(); err != nil {
				ClientLog("[logs] 打开目录失败: %v", err)
			}
			return
		}
		script := fmt.Sprintf(`tell application "Finder"
  reveal (POSIX file %q as alias)
  activate
end tell`, target)
		if err := exec.Command("osascript", "-e", script).Start(); err != nil {
			ClientLog("[logs] 打开目录失败: %v", err)
		}
	}

	return theme.Card(cx,
		core.View{
			StyleFunc: func() core.Style { return core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8} },
			Children: []core.Node{
				core.View{
					StyleFunc: func() core.Style { return core.Style{FlexGrow: 1, FlexShrink: 1} },
					Children:  []core.Node{theme.Title("运行日志", 14, ui.Semibold)},
				},
				ui.Button{Text: "打开日志目录", Variant: ui.ButtonSecondary, Size: ui.SizeSmall, OnPress: openDir},
				ui.Button{Text: "复制", Variant: ui.ButtonSecondary, Size: ui.SizeSmall, OnPress: copyAll},
				ui.Button{Text: "清空", Variant: ui.ButtonGhost, Size: ui.SizeSmall, OnPress: clear},
			},
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
