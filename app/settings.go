package app

import (
	"strconv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

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
			return theme.Fill().Merge(core.Style{Padding: theme.PagePadding(16), Gap: 14, Overflow: core.Hidden})
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
			versionCard(cx),
		},
	}
}

// versionCard 版本与检查更新卡片：显示当前版本，点击检查 GitHub
// Releases 最新版（tag 形如 v<version>-<时间戳>，比对 version 前缀
// + 时间戳）。有新版给出下载链接——走加速节点（见 ghProxyNodes），
// 打开前逐节点探测可用性，A 节点不行自动换 B 节点。
func versionCard(cx *core.Scope) core.Node {
	app := core.App()
	checking := core.NewSignal(cx, false)
	result := core.NewSignal(cx, "") // 检查结果文案
	downloadURL := core.NewSignal(cx, "")

	check := func() {
		if checking.Get() {
			return
		}
		checking.Set(true)
		result.Set("")
		downloadURL.Set("")
		go func() {
			latest, tag, err := fetchLatestRelease()
			cx.Post(func() {
				checking.Set(false)
				if err != nil {
					ClientLog("[update] 检查更新失败: %v", err)
					result.Set("检查失败：" + err.Error())
					return
				}
				ClientLog("[update] 本地 v%s build=%s 最新 name=%q tag=%s", app.Version, app.Build, latest, tag)
				// 发版固定 tag "latest"、版本号不再变化，新旧以 build 号
				// 判断：线上 Release 名称形如 "Kanju 0.1.0 (3)"，与本地
				// core.App().Build 比对。名称解析不出 build 时退回字符串
				// 不等即提示（保守方向，宁可误报不漏报）。
				remoteBuild := releaseBuildOf(latest)
				local, _ := strconv.Atoi(app.Build)
				if remoteBuild > 0 && remoteBuild <= local {
					result.Set("已是最新版本")
					return
				}
				if remoteBuild > 0 {
					result.Set(fmt.Sprintf("发现新版本（build %d > 本地 %s），点击下方链接下载", remoteBuild, app.Build))
				} else {
					result.Set("发现新版本，点击下方链接下载")
				}
				downloadURL.Set("https://github.com/" + releaseRepo + "/releases/latest")
			})
		}()
	}

	return theme.Card(cx,
		theme.Title("版本", 14, ui.Semibold),
		ui.Text("当前版本 v"+app.Version, 13, ui.Regular, func() core.Color { return cx.Palette().TextMuted }),
		core.Dynamic(func(*core.Scope) core.Node {
			row := []core.Node{ui.Button{
				Text: "检查更新", Variant: ui.ButtonSecondary, Size: ui.SizeSmall,
				OnPress: check,
			}}
			if checking.Get() {
				row = append(row, ui.Spinner{})
			}
			return theme.Row(8, row...)
		}),
		core.Dynamic(func(*core.Scope) core.Node {
			m := result.Get()
			if m == "" {
				return core.View{}
			}
			return ui.Text(m, 12, ui.Regular, func() core.Color { return cx.Palette().Accent })
		}),
		core.Dynamic(func(*core.Scope) core.Node {
			u := downloadURL.Get()
			if u == "" {
				return core.View{}
			}
			return ui.Button{Text: "前往下载", Variant: ui.ButtonPrimary, Size: ui.SizeSmall,
				OnPress: func() {
					// 加速下载：探测加速节点（直链经前缀代理），第一个
					// 能取到数据的节点打开；全挂则退回 GitHub 原链。
					go openAccelerated(u, cx)
				}}
		}),
	)
}

// ghProxyNodes GitHub 下载加速节点（前缀式代理：节点/完整 GitHub 链接）。
// 探测按序尝试，可用（Range GET 返回 206/200）即用；社区公益节点
// 随时可能失效，失效自动换下一个。gh-proxy.com 实测最稳放首位。
var ghProxyNodes = []string{
	"https://gh-proxy.com",
	"https://ghfast.top",
	"https://ghproxy.net",
	"https://github.moeyy.xyz",
}

// openAccelerated 用第一个可用的加速节点打开下载链接。页面/目录类
// 链接（releases/latest）只代理资产直链有意义，这类链接直接原样打开。
func openAccelerated(rawURL string, cx *core.Scope) {
	if !strings.Contains(rawURL, "/releases/download/") {
		// 非资产直链（如 releases 页面），代理无法加速，原样打开。
		cx.OpenURL(rawURL, nil)
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	for _, node := range ghProxyNodes {
		proxied := node + "/" + rawURL
		req, err := http.NewRequest(http.MethodGet, proxied, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Range", "bytes=0-1023") // 只探 1KB，不拉全包
		resp, err := client.Do(req)
		if err != nil {
			ClientLog("[update] 加速节点不可用 %s: %v", node, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			ClientLog("[update] 加速节点不可用 %s: status %d", node, resp.StatusCode)
			continue
		}
		ClientLog("[update] 加速节点可用 %s，打开下载", node)
		cx.Post(func() { cx.OpenURL(proxied, nil) })
		return
	}
	// 全部节点失效：退回 GitHub 原链。
	ClientLog("[update] 加速节点全部不可用，退回 GitHub 原链")
	cx.Post(func() { cx.OpenURL(rawURL, nil) })
}

// releaseBuildOf 从 Release 名称解析 build 号（"Kanju 0.1.0 (3)" → 3）。
// 无括号 build（旧版名称）返回 0。
func releaseBuildOf(name string) int {
	i := strings.LastIndex(name, "(")
	j := strings.LastIndex(name, ")")
	if i < 0 || j <= i {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(name[i+1 : j]))
	if err != nil {
		return 0
	}
	return n
}

// releaseRepo 发布仓库（release.github，发布脚本与检查更新共用）。
const releaseRepo = "ciwam/kanju-client"

// fetchLatestRelease 查 GitHub Releases 最新版的 tag 与名称。国内直连
// api.github.com 常超时，走加速节点链（ghProxyNodes），A 节点失败换
// B 节点，全部失效退回直连。未认证限额 60 次/小时，检查更新频率足够。
func fetchLatestRelease() (name, tag string, err error) {
	client := &http.Client{Timeout: 10 * time.Second}
	apiURL := "https://api.github.com/repos/" + releaseRepo + "/releases/latest"
	try := func(u string) (string, string, error) {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return "", "", err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := client.Do(req)
		if err != nil {
			return "", "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", "", fmt.Errorf("GitHub 返回 %d", resp.StatusCode)
		}
		var r struct {
			TagName string `json:"tag_name"`
			Name    string `json:"name"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return "", "", err
		}
		return r.Name, r.TagName, nil
	}
	for _, node := range ghProxyNodes {
		if n, t, err := try(node + "/" + apiURL); err == nil {
			ClientLog("[update] 检查更新经加速节点 %s", node)
			return n, t, nil
		}
	}
	n, t, err := try(apiURL)
	if err != nil {
		return "", "", err
	}
	ClientLog("[update] 检查更新直连 GitHub（加速节点均不可用）")
	return n, t, nil
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

// settingsLogsPane 运行日志页签。FlexBasis 100% 把卡片撑到滚动区
// 可见高度——日志列表占满剩余空间而不是固定 220pt 高。
func settingsLogsPane(cx *core.Scope) core.Node {
	return core.View{
		Style:    core.Style{FlexGrow: 1, FlexBasis: core.Pct(100)},
		Children: []core.Node{LogCard(cx)},
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

	return core.View{
		// 撑满页签面板（settingsLogsPane 已给 100% basis），日志列表
		// FlexGrow 吃掉卡片内剩余高度。
		Style: core.Style{FlexGrow: 1, FlexShrink: 1},
		Children: []core.Node{theme.Card(cx,
			core.View{
				StyleFunc: func() core.Style { return core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8, FlexShrink: 0} },
				Children: append([]core.Node{
					core.View{
						StyleFunc: func() core.Style { return core.Style{FlexGrow: 1, FlexShrink: 1} },
						Children:  []core.Node{theme.Title("运行日志", 14, ui.Semibold)},
					},
				}, logButtons...),
			},
			logView(cx, version),
		)},
	}
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
		// 到顶/到底继续拖不再回弹跳动（日志区 rubber band 无意义）。
		Behavior: core.ScrollBehavior{NoBounces: true},
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
		Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinHeight: core.Pt(220), CornerRadius: 6,
			Background: cx.Palette().Background, BorderWidth: 1, BorderColor: cx.Palette().Border,
			BorderStyle: core.BorderSolid, Padding: core.All(8)},
	}
}
