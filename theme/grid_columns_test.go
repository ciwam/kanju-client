package theme

import (
	"testing"

	"github.com/nsgo-dev/nsgo/core"
)

// TestColumnsForWidth 列数随内容区宽度自适应。
func TestColumnsForWidth(t *testing.T) {
	cases := []struct {
		w    core.Points
		want int
	}{
		{0, 4},   // 未知宽度（首帧）默认
		{380, 2}, // 窄窗
		{500, 3},
		{700, 4}, // 常规窗口
		{1000, 5},
		{1400, 6}, // 全屏/外接屏
	}
	for _, c := range cases {
		if got := columnsForWidth(c.w); got != c.want {
			t.Errorf("columnsForWidth(%v) = %d, want %d", c.w, got, c.want)
		}
	}
}

// TestMeasureMemoStableCols 拉伸模拟：宽度每帧都变、列数不变时，
// Measure 的 cols（Memo）不得通知读者——否则网格 Dynamic 每帧重跑
// 重建 List，海报清屏重载（拉伸闪动的根因）。用 Memo 的 Get 两次
// 之间宽度微变、无跨档，断言 computation 不重算（值引用相等性由
// lastCols 间接观察：重算必写 lastCols）。
func TestMeasureMemoStableCols(t *testing.T) {
	lastCols = 0 // 复位包级状态（前一个测试写过）
	root := core.NewRoot(core.RootOptions{})
	defer root.Dispose()
	a := root.Child()
	// 直接构造与 Measure 内部一致的 memo，验证「宽度变、列数不变不重算」。
	w := core.NewSignal(a, core.Points(0))
	col := core.NewMemo(a, func() int { return columnsForWidth(w.Get()) })
	runs := 0
	// 读者用 Effect（与框架自身测试一致）：memo 值不变不通知，effect 不重跑。
	stop := a.Effect(func() {
		_ = col.Get()
		runs++
	})
	defer stop()
	// 首次 Set 跨档（未知宽度 4 列 -> 5 列），effect 应重跑一次。
	w.Set(1001)
	if runs != 2 {
		t.Fatalf("跨档应通知一次，读者重跑 %d 次", runs)
	}
	// 同档内微变（模拟拉伸的每一帧）：Memo 不应通知，effect 不重跑。
	for _, ww := range []core.Points{1002, 1010, 1050, 1100, 1199} {
		w.Set(ww)
	}
	if runs != 2 {
		t.Fatalf("同档微变应无通知，读者重跑 %d 次", runs)
	}
	// 跨档：5 -> 6，读者应重跑一次。
	w.Set(1200)
	if runs != 3 {
		t.Fatalf("跨档应通知一次，读者重跑 %d 次", runs)
	}
	if col.Get() != 6 {
		t.Fatalf("1200 宽应为 6 列，得 %d", col.Get())
	}
	lastCols = 0
}
