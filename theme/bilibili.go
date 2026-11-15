// bilibili_theme.go — 哔哩哔哩桌面版视觉 token：主色 B 站蓝、页面
// 背景浅灰分层、AppBar 顶栏组件。应用根挂载 BiliTheme()（main.go
// SetTheme），全部页面继承。
package theme

import (
	"github.com/nsgo-dev/nsgo/core"
)

// B 站桌面版视觉常量。
var (
	// AccentBili 哔哩哔哩主色（粉蓝 #00a1d6）。
	AccentBili = core.RGB(0x00a1d6)
	// AccentBiliHover 主色 hover（加深一档）。
	AccentBiliHover = core.RGB(0x0090c0)
	// AccentBiliPressed 主色按压。
	AccentBiliPressed = core.RGB(0x0080aa)
	// BgPageBili 页面底色（浅灰，卡片浮在其上）。
	BgPageBili = core.RGB(0xf6f7f8)
)

// BiliTheme 返回对齐哔哩哔哩桌面版的主题：克隆默认主题，覆盖主色
// 三态与浅色页面底（深色背景保持框架默认的暗灰，B 站深色同为暗灰）。
func BiliTheme() *core.Theme {
	t := core.DefaultTheme().Clone()
	t.Light.Accent, t.Light.AccentHover, t.Light.AccentPressed = AccentBili, AccentBiliHover, AccentBiliPressed
	t.Light.Background = BgPageBili
	t.Light.Surface = core.RGB(0xffffff)
	t.Light.Border = core.RGB(0xe3e5e7)
	t.Dark.Accent, t.Dark.AccentHover, t.Dark.AccentPressed = AccentBili, AccentBiliHover, AccentBiliPressed
	return t
}

// AppBar 全局顶栏（B 站桌面版样式）：白底、底部 1px 分隔线、内容
// 左中右三段。调用方给 left/center/right 三组节点。
func AppBar(cx *core.Scope, left, center, right []core.Node) core.View {
	p := cx.Palette()
	return core.View{
		StyleFunc: func() core.Style {
			return core.Style{
				Direction: core.Row, AlignItems: core.AlignCenter, Gap: 16,
				Padding:     core.Symmetric(10, 20),
				Background:  p.Surface,
				Borders:     core.Edges{Bottom: 1},
				BorderColors: core.EdgeColors{Bottom: p.Border},
				BorderStyle: core.BorderSolid,
			}
		},
		Children: append(append(append([]core.Node{}, left...), center...), right...),
	}
}
