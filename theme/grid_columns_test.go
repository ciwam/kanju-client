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
