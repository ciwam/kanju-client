// kanju-web 是 Kanju 的 Web 应用宿主：一个原生二进制同时提供
// kanju 后端 API 和内嵌的 nsgo wasm UI 站点。跟随应用分发，单机
// 运行，不依赖任何远程服务。
//
// 构建流程（见 build.sh 的 web target）：
//  1. nsgo build -target web 产出 wasm UI 站点
//  2. 拷入 web/dist（go:embed 不支持 ..）
//  3. go build ./cmd/kanju-web
//
// 用法: kanju-web [-port 28088] [-db path]
package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"kanju/client/backend"
)

//go:embed web/dist
var webDist embed.FS

func main() {
	port := flag.String("port", "", "HTTP 监听端口（默认或 0 = 自动分配）")
	db := flag.String("db", "", "SQLite 路径（默认 可执行文件目录/data/kanju.db）")
	flag.Parse()
	if *port == "0" {
		*port = ""
	}

	// web/dist 挂到站点根：embed.FS 以 "web/dist" 为前缀，剥掉后交给
	// backend 的 StaticFS（SPA 回退 index.html 由 handleStatic 处理）。
	site, err := fs.Sub(webDist, "web/dist")
	if err != nil {
		log.Fatalf("读取内嵌 web 站点: %v", err)
	}

	if *db == "" {
		if exe, e := os.Executable(); e == nil {
			*db = filepath.Join(filepath.Dir(exe), "data", "kanju.db")
		}
	}
	if err := os.MkdirAll(filepath.Dir(*db), 0o755); err != nil {
		log.Fatalf("创建数据目录: %v", err)
	}

	opts := backend.ServeOptions{
		Port:     *port,
		DBPath:   *db,
		StaticFS: site,
		// 单机自托管：浏览器 UI 是唯一用户，免登录（虚拟 admin）。
		NoAuth:   true,
		Public:   true, // 监听 0.0.0.0，方便局域网其他设备访问
		TMDBKey:  os.Getenv("TMDB_API_KEY"),
		LogOutput: os.Stderr,
	}
	addr, stop, err := backend.Start(opts)
	if err != nil {
		log.Fatalf("启动 kanju 后端: %v", err)
	}
	log.Printf("kanju-web 已启动: http://%s （db: %s）", addr, *db)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println("收到退出信号，关停…")
	stop()
}
