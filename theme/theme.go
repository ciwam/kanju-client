// Package theme 是 Kanju 客户端的通用样式与小组件。
package theme

import (
	"github.com/nsgo-dev/nsgo/core"
)

// Type 是全局字体排印预设。
var (
	TypeHeading = core.Style{FontSize: 17, FontWeight: core.FontWeightSemibold}
	TypeBody    = core.Style{FontSize: 14}
	TypeCaption = core.Style{FontSize: 12}
)

// Row 横向布局。
func Row(gap core.Points, children ...core.Node) core.View {
	return core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: gap}, Children: children}
}

// Col 纵向布局。
func Col(gap core.Points, children ...core.Node) core.View {
	return core.View{Style: core.Style{Gap: gap}, Children: children}
}

// Card 圆角卡片。
func Card(cx *core.Scope, children ...core.Node) core.View {
	return core.View{
		StyleFunc: func() core.Style {
			p := cx.Palette()
			return core.Style{
				Padding: core.All(16), Gap: 10,
				CornerRadius: 10,
				Background:   p.Background,
				BorderColor:  p.Border, BorderWidth: 1, BorderStyle: core.BorderSolid,
			}
		},
		Children: children,
	}
}

// Muted 次要文字颜色。
func Muted(cx *core.Scope) core.Color {
	return cx.Palette().TextMuted
}

// Fill 填满容器。
func Fill() core.Style {
	return core.Style{FlexGrow: 1, FlexShrink: 1, Width: core.Pct(100), Height: core.Pct(100)}
}

// Poster 封面图（URL 应已由调用方包装为代理地址）。
func Poster(uri, label string, w, h core.Points) core.Image {
	return core.Image{
		Source:     core.Src(uri),
		ResizeMode: core.Cover,
		Label:      label,
		Style:      core.Style{Width: core.Pt(w), Height: core.Pt(h), CornerRadius: 6, Background: core.RGB(0xD9D9D9)},
	}
}

// Empty 空态提示。
func Empty(cx *core.Scope, text string) core.View {
	return core.View{
		StyleFunc: func() core.Style {
			return core.Style{
				FlexGrow: 1, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter,
				Padding: core.All(32),
			}
		},
		Children: []core.Node{core.Text{
			Content: text,
			Style:   core.Style{FontSize: 13, Color: cx.Palette().TextMuted},
		}},
	}
}
