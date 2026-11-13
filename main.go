package main

import (
	"kanju/client/app"

	"github.com/nsgo-dev/nsgo"
	"github.com/nsgo-dev/nsgo/core"
)

func main() {
	nsgo.Run(func(cx *core.Scope) core.Node {
		a := app.Setup(cx)
		return app.Root(cx, a)
	}, nsgo.WindowSize(1200, 780))
}
