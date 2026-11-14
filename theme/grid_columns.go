package theme

// grid_columns.go — 按容器宽度自适应的海报网格列数。
//
// List 的 NumColumns 在挂载时固化（mountList 只读一次 spec），列数
// 变化必须整表重建。用法：measureView 包住网格，OnLayout 把宽度写进
// 信号；网格 Dynamic track 列数信号，列数变化时重建 List。

import (
	"github.com/nsgo-dev/nsgo/core"
)

// posterColumnGap 海报网格列间距（与调用方的 ColumnGap 一致）。
const posterColumnGap = core.Points(14)

// columnsForWidth 按内容区宽度挑列数：以海报列宽 ~170-210pt 为准，
// 窄窗口少列（最小 2 列保可用），宽窗口多列。
func columnsForWidth(w core.Points) int {
	switch {
	case w <= 0:
		return 4 // 未知宽度（首帧）沿用默认
	case w < 440:
		return 2
	case w < 660:
		return 3
	case w < 900:
		return 4
	case w < 1200:
		return 5
	default:
		return 6
	}
}

// widthSignal 是一次容器宽度测量（OnLayout 写入）。
type widthSignal = core.Signal[core.Points]

// Measure 生成一个测量容器与它的宽度信号。把返回的 wrap 包住网格：
// 网格实际宽度（随窗口缩放、侧栏、边距变化）驱动 cols()。
func Measure(cx *core.Scope) (cols func() int, wrap func(child core.Node) core.Node) {
	width := core.NewSignal(cx, core.Points(0))
	cols = func() int { return columnsForWidth(width.Get()) }
	wrap = func(child core.Node) core.Node {
		return core.View{
			Style:    core.Style{FlexGrow: 1, FlexShrink: 1, FlexBasis: core.Pct(100)},
			OnLayout: func(b core.Bounds) { width.Set(b.Size.Width) },
			Children: []core.Node{child},
		}
	}
	return cols, wrap
}
