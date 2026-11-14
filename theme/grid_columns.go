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

// lastCols 记住最近一次按真实宽度算出的列数（进程生命周期内）。
// 首帧宽度未知时用它作初值，避免「先 4 列渲染、测到宽度后重建为
// 5 列」的闪变。InitialCols 可注入跨启动持久化的初值（App 层从
// Storage 恢复），进程内首次测量后即由 lastCols 接管。
var (
	lastCols    int
	initialCols int
)

// InitialCols 设置首帧（宽度未知时）使用的列数初值，须在首次
// columnsForWidth 调用前设置（App 启动时从持久化恢复）。
func InitialCols(n int) {
	if n >= 2 && n <= 6 {
		initialCols = n
	}
}

// CurrentCols 返回最近一次按真实宽度算出的列数（App 层持久化用；
// 未测量过返回 0）。
func CurrentCols() int { return lastCols }

// columnsForWidth 按内容区宽度挑列数：以海报列宽 ~170-210pt 为准，
// 窄窗口少列（最小 2 列保可用），宽窗口多列。
func columnsForWidth(w core.Points) int {
	switch {
	case w <= 0:
		if lastCols > 0 {
			return lastCols // 同进程再挂载：沿用上次测量
		}
		if initialCols > 0 {
			return initialCols // 跨启动：沿用上次会话的列数
		}
		return 4 // 无任何历史：默认 4 列
	case w < 440:
		lastCols = 2
	case w < 660:
		lastCols = 3
	case w < 900:
		lastCols = 4
	case w < 1200:
		lastCols = 5
	default:
		lastCols = 6
	}
	return lastCols
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
