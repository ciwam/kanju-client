package main

import (
	"kanju/client/app"

	"github.com/nsgo-dev/nsgo"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/desktop"
)

func main() {
	nsgo.Run(func(cx *core.Scope) core.Node {
		a := app.Setup(cx)
		// 窗口约束参考哔哩哔哩桌面版：默认 1280×800（海报网格 5-6 列
		// 舒适区），最小 1000×600（窄窗 4 列仍可用），不设上限。
		desktop.SetWindowConstraints(cx, desktop.WindowConstraints{
			MinWidth:  1000,
			MinHeight: 600,
		})
		return app.Root(cx, a)
	}, nsgo.WindowSize(1280, 800))
}
