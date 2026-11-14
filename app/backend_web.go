//go:build js

package app

import (
	"github.com/nsgo-dev/nsgo/core"
)

// EmbeddedBackend 的 wasm 版：浏览器里无法起 TCP 服务（modernc.org/sqlite
// 也不支持 wasm），后端由跟随应用分发的 kanju-web 原生程序提供，UI 走
// 同源相对路径（base 为空）访问它的 /api/*。
func EmbeddedBackend(cx *core.Scope) (string, error) {
	return "", nil
}

// embeddedStop wasm 下没有内嵌后端可关停。
var embeddedStop func()
