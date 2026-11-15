// Package theme 是 Kanju 客户端的通用样式与小组件。
package theme

import (
	"strconv"
	"strings"

	"github.com/nsgo-dev/nsgo/core"
)

// Type 是全局字体排印预设。
var (
	TypeHeading = core.Style{FontSize: 17, FontWeight: core.FontWeightSemibold}
	TypeBody    = core.Style{FontSize: 14}
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

// Title 无色覆盖的标题/正文文字（继承父视图颜色）。
func Title(content string, size core.Points, weight core.FontWeight) core.Text {
	return core.Text{Content: content, Style: core.Style{FontSize: size, FontWeight: weight}}
}

// SkeletonBox 加载骨架：灰底圆角块。
func SkeletonBox(cx *core.Scope, w, h core.Points, grow bool) core.View {
	st := core.Style{
		Width: core.Pt(w), Height: core.Pt(h), CornerRadius: 6,
		Background: cx.Palette().SurfaceHover,
	}
	if grow {
		st = st.Merge(core.Style{FlexGrow: 1})
	}
	return core.View{Style: st}
}

// SkeletonGrid 加载骨架网格（首页/搜索加载时显示，比 spinner 有内容感）。
func SkeletonGrid(cx *core.Scope, rows, cols int) core.View {
	grid := core.View{Style: core.Style{Gap: 16}}
	posterH := core.Points(160 / 0.7)
	for r := 0; r < rows; r++ {
		row := core.View{Style: core.Style{Direction: core.Row, Gap: 14}}
		for c := 0; c < cols; c++ {
			row.Children = append(row.Children, core.View{
				Style: core.Style{FlexGrow: 1, Gap: 6},
				Children: []core.Node{
					SkeletonBox(cx, 0, posterH, true),
					SkeletonBox(cx, 0, 14, false),
				},
			})
		}
		grid.Children = append(grid.Children, row)
	}
	return grid
}

// ErrorRetry 错误提示 + 重试按钮。
func ErrorRetry(cx *core.Scope, msg string, retry func()) core.View {
	return core.View{
		Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 10, Padding: core.Symmetric(8, 0)},
		Children: []core.Node{
			core.Text{Content: msg, Style: core.Style{FontSize: 13, Color: cx.Palette().Danger, FlexShrink: 1}},
			core.View{Style: core.Style{FlexGrow: 1}},
			retryButton(cx, retry),
		},
	}
}

// retryButton 重试按钮（ErrorRetry 内部用）。
func retryButton(cx *core.Scope, retry func()) core.View {
	return core.View{
		StyleFunc: func() core.Style {
			p := cx.Palette()
			return core.Style{
				Direction: core.Row, AlignItems: core.AlignCenter, Gap: 4,
				Padding: core.Symmetric(3, 10), CornerRadius: 6, FontSize: 12,
				BorderColor: p.Border, BorderWidth: 1, BorderStyle: core.BorderSolid,
				Hover: &core.Style{Background: p.SurfaceHover},
			}
		},
		Children: []core.Node{
			core.Text{Content: "重试"},
		},
		OnPress: func(core.PressEvent) { retry() },
	}
}

// RetryImage 可重试图片：框架对取消/失败的加载不自动重试（首页启动
// 时列数自适应导致 List 连续重建，第一批请求被取消后图片一直空白，
// 切分类再切回才恢复——重建触发重新加载）。OnError 时以递增 nonce
// 换源重发，最多 attempts 次。
func RetryImage(cx *core.Scope, uri, label string, attempts int, style core.Style, onError func(error)) core.Image {
	gen := core.NewSignal(cx, 0)
	src := func() core.ImageSource {
		g := gen.Get() // track：重试时换 nonce 触发重载
		if g == 0 {
			return core.Src(uri)
		}
		sep := "?"
		if strings.Contains(uri, "?") {
			sep = "&"
		}
		return core.Src(uri + sep + "retry=" + strconv.Itoa(g))
	}
	return core.Image{
		SourceFunc: src,
		ResizeMode: core.Cover,
		Label:      label,
		OnError: func(err error) {
			if onError != nil {
				onError(err)
			}
			if gen.Get() < attempts {
				gen.Update(func(n int) int { return n + 1 })
			}
		},
		Style: style,
	}
}
