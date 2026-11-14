//go:build !js

package app

import (
	"fmt"
	"log"
	"os"
	"sync"

	"kanju/client/backend"

	"github.com/nsgo-dev/nsgo/core"
)

// 嵌入后端的生命周期：整个进程一份，退出时随进程结束。
var (
	embedOnce sync.Once
	embedAddr string
	embedErr  error
)

// EmbeddedBackend 在进程内启动 kanju 服务（随机端口，数据库在应用数据目录）。
// 幂等；返回实际监听地址（如 http://127.0.0.1:54321）。
func EmbeddedBackend(cx *core.Scope) (string, error) {
	embedOnce.Do(func() {
		dir := cx.DataDir()
		if dir == "" {
			embedErr = fmt.Errorf("应用数据目录不可用")
			return
		}
		opts := backend.ServeOptions{
			// 空端口 = 自动分配，避免与用户可能在跑的外部 kanju 冲突。
			DBPath: dir + "/kanju.db",
			// 内嵌模式免登录：只绑 127.0.0.1，无对外暴露；鉴权全开时
			// 用户每次打开 app 都要先输账号密码，没有意义。
			NoAuth: true,
			// 后端日志收进客户端的统一日志缓冲（设置页可看）。
			LogOutput: BackendLogWriter(),
			// TMDB key：环境变量（豆瓣风控时的第二数据源）。
			TMDBKey: os.Getenv("TMDB_API_KEY"),
			// 开启后端 debug 日志（请求路由+全链路计时）。
			Debug: true,
		}
		addr, stop, err := backend.Start(opts)
		if err != nil {
			embedErr = err
			return
		}
		embedAddr = "http://" + addr
		// stop 闭包持有 srv/db；退出时由 OnQuit 调用优雅关停。
		embeddedStop = stop
		log.Printf("kanju embedded backend at %s (db: %s)", embedAddr, dir)
	})
	return embedAddr, embedErr
}

var embeddedStop func()
