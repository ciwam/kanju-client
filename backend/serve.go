package backend

// serve.go — 库入口：让宿主进程（macOS/iOS/Android 客户端、测试、其它 Go
// 程序）在进程内启动完整的 kanju HTTP 服务。命令行入口在 cmd/kanju/main.go，
// 它解析 flag 后最终也走这里的 Serve / Start。

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

// ServeOptions 描述一次进程内启动的全部可调项。零值即合理默认：
// 随机端口、可执行文件目录下的 data/kanju.db。
type ServeOptions struct {
	// Port 为空则绑定随机空闲端口（Start 会回填实际端口到 Addr）。
	Port string
	// DBPath 为空则用 <可执行文件目录>/data/kanju.db。
	DBPath string
	// StaticFS 覆盖内嵌前端（测试可注入内存 FS）；nil 用 embeddedStatic。
	StaticFS fs.FS
	// Debug 开启全链路计时日志。
	Debug bool
	// AvailabilityCheck 关闭站源可用性探测（默认开启，false 才生效语义由调用方决定）。
	AvailabilityCheck *bool
	// NoAuth 跳过鉴权（仅测试用）。
	NoAuth bool
	// TrustProxy 信任 X-Forwarded-For。
	TrustProxy bool
	// Public 使 Serve 监听全部接口（部署场景）；Start（内嵌）恒为 127.0.0.1。
	Public bool
	// TMDBKey TMDB API key（豆瓣风控时的第二数据源）；空则 TMDB 通道禁用。
	TMDBKey string
	// LogOutput 接收全部服务日志（横幅、错误、--debug 计时）；nil 用标准 stderr。
	// 进程内嵌场景传自定义 Writer 即可把日志收进宿主的缓冲。
	LogOutput io.Writer
}

// validate 检查端口合法性。
func (o *ServeOptions) validate() error {
	if o.Port != "" {
		n, err := strconv.Atoi(o.Port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("非法端口 %q，端口需为 1-65535 之间的整数", o.Port)
		}
	}
	return nil
}

func (o *ServeOptions) apply() {
	debugMode = o.Debug
	authBypass = o.NoAuth
	trustProxy = o.TrustProxy
	if o.AvailabilityCheck != nil {
		availabilityCheckEnabled = *o.AvailabilityCheck
	}
}

// Serve 以阻塞方式启动 HTTP 服务（等价原命令行行为的库形态）。
// 返回时说明服务已因错误退出。
func Serve(opts ServeOptions) error {
	r, err := bind(opts)
	if err != nil {
		return err
	}
	// 命令行场景监听 0.0.0.0（与历史部署一致）；内嵌场景用 Start（127.0.0.1）。
	if opts.Port == "" || opts.Public {
		pub, perr := net.Listen("tcp", ":"+strconv.Itoa(mustPort(r)))
		if perr != nil {
			r.stop()
			return fmt.Errorf("listen: %w", perr)
		}
		r.ln = pub
		r.addr = pub.Addr().String()
	}
	r.serve()
	return nil
}

// mustPort 从已绑定地址取端口。
func mustPort(r *running) int {
	_, port, _ := net.SplitHostPort(r.addr)
	n, _ := strconv.Atoi(port)
	return n
}

// Start 在后台 goroutine 启动服务并立即返回。
// Addr 是实际监听的 "127.0.0.1:<port>"（Port 为空时自动选端口）。
// Stop 优雅关停（关闭数据库）；重复调用安全。
func Start(opts ServeOptions) (addr string, stop func(), err error) {
	r, err := bind(opts)
	if err != nil {
		return "", func() {}, err
	}
	go r.serve()
	return r.addr, r.stop, nil
}

// running 是一次已绑定、待服务的实例。
type running struct {
	ln   net.Listener
	http *http.Server
	addr string
	db   *Server
	stop func()
}

// serve 开始 Accept 并阻塞到关停。
func (r *running) serve() {
	if err := r.http.Serve(r.ln); err != nil && err != http.ErrServerClosed {
		log.Printf("kanju server error: %v", err)
	}
}

// logWriterOf 把 nil Writer 还原为 stderr。
func logWriterOf(w io.Writer) io.Writer {
	if w == nil {
		return os.Stderr
	}
	return w
}

// bind 打开数据库、装配路由并绑定 127.0.0.1 监听（不开始 Accept）。
func bind(opts ServeOptions) (*running, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	opts.apply()
	if opts.TMDBKey != "" {
		SetTMDBKey(opts.TMDBKey)
	}
	if opts.LogOutput != nil {
		// 全部日志（logPrint/logInfo/logWarn/logError 走 defaultLogger）
		// 重定向到调用方的 Writer。并发场景由调用方保证 Writer 安全。
		defaultLogger = slog.New(slog.NewTextHandler(opts.LogOutput, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	logger := log.New(logWriterOf(opts.LogOutput), "", log.LstdFlags)

	staticFS := opts.StaticFS
	if staticFS == nil {
		// 无内嵌前端（本客户端用 nsgo 原生 UI）；StaticFS 留给
		// 需要独立部署 Web SPA 的场景注入。
		logger.Printf("提示: 未配置静态前端，/ 下请求将返回 404")
	}

	port := opts.Port
	if port == "" {
		port = "0" // 交给内核分配；实际端口以 Listener 地址为准
	}

	s := &Server{
		tokens:   newTokenCache(),
		rl:       newRateLimiter(),
		staticFS: staticFS,
		port:     port,
	}
	resolvedDB := resolveDBPath(opts.DBPath)
	if err := s.openDB(resolvedDB); err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s.startDBCleanupLoop()
	// 榜单预热：首次启动拉全分类进 SQLite，之后按 TTL 巡检刷新。
	s.startDoubanWarmLoop()

	mux := securityHeaders(gzipMiddleware(s.cors(traceMiddleware(s.authGate(s.routes())))))
	srv := &http.Server{
		Addr:           ":" + port,
		Handler:        mux,
		ReadTimeout:    15 * time.Second,
		WriteTimeout:   0, // 流式媒体下载无固定时长，靠 ctx 取消与字节上限终止
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	// 在独立 Listener 上监听，绑定 127.0.0.1：进程内嵌场景只服务本进程/本机。
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		_ = s.db.Close()
		return nil, fmt.Errorf("listen: %w", err)
	}
	listenAddr := ln.Addr().String()

	logger.Printf("============================================================")
	logger.Printf("Kanju API server is running!")
	logger.Printf("Local:   http://%s", listenAddr)
	logger.Printf("DB:      %s", resolvedDB)
	logger.Printf("============================================================")

	stopOnce := false
	r := &running{
		ln:   ln,
		http: srv,
		addr: listenAddr,
		db:   s,
		stop: func() {
			if stopOnce {
				return
			}
			stopOnce = true
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx)
			_ = s.db.Close()
		},
	}
	return r, nil
}
