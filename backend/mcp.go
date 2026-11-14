package backend

// mcp.go — kanju 的 MCP (Model Context Protocol) stdio 服务端。
//
// 目的：让 WorkBuddy / Claude 等 MCP 客户端能直接对 kanju 做本机调试体检，
// 无需人工拼 curl + sqlite 命令。零第三方协议依赖：stdio 上逐行 JSON-RPC。
//
// 协议纪律（重要，改动前必读）：
//   - stdout 只允许出现 JSON-RPC 响应。任何日志/调试输出一律走 stderr，
//     否则会污染协议流导致客户端解析失败。
//   - 工具执行失败通过 content{isError:true} 返回，不用 JSON-RPC error，
//     模型能读到错误信息并自我纠正（MCP 规范推荐做法）。
//   - 本服务端全部工具为只读诊断（不写库、不改状态），探针目标固定
//     127.0.0.1，防止被当作内网横跳的 SSRF 跳板。
//
// 诊断纪律（来自实战教训）：
//   - 探针自身与被诊断的 kanju 服务可能不是同一个二进制（本文件由
//     `go build -o ~/.workbuddy/bin/kanju-mcp ./backend` 单独构建）。
//     因此版本对比永远以磁盘上 dist/kanju -v 的输出为「真实来源」，
//     探针自身版本单独标注 probe_self，不允许混为一谈。
//   - 输出必须带 summary 结论与可直接执行的建议命令。

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const mcpProtocolVersion = "2024-11-05"

// ---- JSON-RPC wire types ----

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpResponse struct {
	JSONRPC string       `json:"jsonrpc"`
	ID      any          `json:"id"`
	Result  any          `json:"result,omitempty"`
	Error   *mcpRPCError `json:"error,omitempty"`
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpToolDef struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(args map[string]any) (string, error)
}

// mcpLogf 只写 stderr，绝不碰 stdout。
func mcpLogf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[kanju-mcp] "+format+"\n", args...)
}

// mcpServe 阻塞读取逐行 JSON-RPC 请求并写响应，EOF 时正常返回。
// out 必须是真实 os.Stdout（协议通道），不要注入测试 writer 后再当生产用。
// MCPServe 以 stdio JSON-RPC 提供 MCP 诊断服务（原 mcpServe，导出供 cmd/kanju 调用）。
func MCPServe(in io.Reader, out io.Writer) error {
	var mu sync.Mutex // 保护 out 的并发写
	write := func(v any) {
		b, err := json.Marshal(v)
		if err != nil {
			mcpLogf("marshal response: %v", err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if _, err := out.Write(append(b, '\n')); err != nil {
			mcpLogf("write response: %v", err)
		}
	}

	tools := mcpToolTable()
	toolsByName := make(map[string]mcpToolDef, len(tools))
	for _, t := range tools {
		toolsByName[t.Name] = t
	}

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req mcpRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			write(mcpResponse{JSONRPC: "2.0", ID: nil,
				Error: &mcpRPCError{Code: -32700, Message: "parse error: invalid JSON"}})
			continue
		}
		// 通知（无 id）：不回响应。initialized 等通知直接吞掉。
		if len(req.ID) == 0 {
			mcpLogf("notification: %s", req.Method)
			continue
		}
		var id any
		_ = json.Unmarshal(req.ID, &id)

		switch req.Method {
		case "initialize":
			write(mcpResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{
				"protocolVersion": mcpProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{
					"name":    "kanju-mcp",
					"version": gitCommit,
				},
			}})
		case "ping":
			write(mcpResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{}})
		case "tools/list":
			list := make([]map[string]any, 0, len(tools))
			for _, t := range tools {
				list = append(list, map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"inputSchema": t.InputSchema,
				})
			}
			write(mcpResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{"tools": list}})
		case "resources/list":
			write(mcpResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{"resources": []any{}}})
		case "prompts/list":
			write(mcpResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{"prompts": []any{}}})
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				write(mcpResponse{JSONRPC: "2.0", ID: id,
					Error: &mcpRPCError{Code: -32602, Message: "invalid params: " + err.Error()}})
				continue
			}
			tool, ok := toolsByName[p.Name]
			if !ok {
				// 未知工具：按 MCP 推荐用 isError content 返回，模型可自我纠正。
				write(mcpResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{
					"content": []map[string]any{{"type": "text",
						"text": fmt.Sprintf("unknown tool %q; call tools/list for available tools", p.Name)}},
					"isError": true,
				}})
				continue
			}
			if p.Arguments == nil {
				p.Arguments = map[string]any{}
			}
			text, err := tool.Handler(p.Arguments)
			result := map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
			}
			if err != nil {
				result["isError"] = true
				result["content"] = []map[string]any{{"type": "text",
					"text": "tool error: " + err.Error()}}
			}
			write(mcpResponse{JSONRPC: "2.0", ID: id, Result: result})
		default:
			write(mcpResponse{JSONRPC: "2.0", ID: id,
				Error: &mcpRPCError{Code: -32601, Message: "method not found: " + req.Method}})
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("stdin scan: %w", err)
	}
	return nil // EOF：正常退出
}

// ---- 工具参数辅助 ----

func argString(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func argInt(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

// resolveMCPDBPath 解析数据库路径：显式参数 > KANJU_DB 环境变量 >
// cwd/dist/data/kanju.db > cwd/data/kanju.db。返回 (路径, 来源说明, 候选列表)。
func resolveMCPDBPath(explicit string) (string, string, []string) {
	candidates := []string{}
	if explicit != "" {
		return filepath.Clean(explicit), "tool argument", candidates
	}
	if env := os.Getenv("KANJU_DB"); env != "" {
		return filepath.Clean(env), "env KANJU_DB", candidates
	}
	cwd, _ := os.Getwd()
	for _, rel := range []string{"dist/data/kanju.db", "data/kanju.db"} {
		p := filepath.Join(cwd, rel)
		candidates = append(candidates, p)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, "auto-discovered (" + rel + " under cwd)", candidates
		}
	}
	return "", "not found", candidates
}

// probeSelfVersion 是探针（kanju-mcp）自身的构建版本，与被诊断服务严格区分。
func probeSelfVersion() map[string]any {
	return map[string]any{
		"note":       "probe binary itself (kanju-mcp), NOT the running kanju service",
		"build_time": buildTime,
		"git_commit": gitCommit,
	}
}

// probeBinVersion 执行磁盘上被诊断二进制的 `-v`，这是版本的真实来源（磁盘产物）。
func probeBinVersion(binPath string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binPath, "-v").Output()
	if err != nil {
		return nil, err
	}
	info := map[string]any{"path": binPath, "output": strings.TrimSpace(string(out))}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if bt, ok := strings.CutPrefix(line, "构建时间:"); ok {
			info["build_time"] = strings.TrimSpace(bt)
		}
		if gc, ok := strings.CutPrefix(line, "Git 提交:"); ok {
			info["git_commit"] = strings.TrimSpace(gc)
		}
	}
	return info, nil
}

// httpProbe 对 127.0.0.1 做一次 HTTP 探针，返回状态码/耗时/头/截断 body。
// host 固定本机：MCP 诊断工具不允许被用于探测任意内网地址。
func httpProbe(port int, path, method, body string, timeout time.Duration) map[string]any {
	return httpProbeJar(port, path, method, body, timeout, nil)
}

// httpProbeJar 同 httpProbe，可选携带 cookieJar（用于「先登录再探针」的会话链）。
func httpProbeJar(port int, path, method, body string, timeout time.Duration, jar *cookiejar.Jar) map[string]any {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	// 所有出口回显的 url 都过脱敏，防止 path/query 里带的凭据进入诊断输出。
	displayURL := maskSensitive(url)
	var bodyReader io.Reader
	if body != "" && method != "GET" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return map[string]any{"ok": false, "error": maskSensitive("build request: " + err.Error()), "url": displayURL}
	}
	req.Header.Set("User-Agent", "kanju-mcp/1.0 (local diagnostic probe)")
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	// Proxy 显式置空：诊断必须直连 127.0.0.1。默认 transport 会读
	// HTTP_PROXY 环境变量，开发机常驻代理时会把本机探针劫持到代理上，
	// 产生「服务明明在跑却探不通」的假阴性。
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil},
		// 不跟随重定向：诊断要看原始状态码。
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: timeout,
	}
	// 注意：不能写 client.Jar = jar —— typed nil 接口（非 nil interface 包着
	// nil *cookiejar.Jar）会让 http.Client 误判 Jar 存在并解引用 panic。
	if jar != nil {
		client.Jar = jar
	}
	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return map[string]any{"ok": false, "url": displayURL, "error": maskSensitive(err.Error()),
			"elapsed_ms": elapsed.Milliseconds(),
			"hint":       "connection refused 通常意味着该端口没有 kanju 服务在跑；用 kanju.sh / ./build.sh run 启动"}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	bodyText := string(raw)
	if len(raw) == 4096 {
		bodyText += "\n... (truncated at 4096 bytes)"
	}
	headers := map[string]string{}
	for k, vs := range resp.Header {
		joined := strings.Join(vs, "; ")
		// 敏感头打码：httpOnly 会话 cookie 不应出现在诊断输出里。
		if strings.EqualFold(k, "Set-Cookie") {
			joined = maskSensitive(joined)
		}
		headers[k] = joined
	}
	return map[string]any{
		"ok":         true,
		"url":        displayURL,
		"status":     resp.StatusCode,
		"elapsed_ms": elapsed.Milliseconds(),
		"headers":    headers,
		"body":       maskSensitive(bodyText),
		"note":       "probe target fixed to 127.0.0.1 (localhost-only diagnostic)",
	}
}

var sensitiveValRe = regexp.MustCompile(`(?i)("(?:token|password|secret|authorization)"\s*:\s*")[^"]*(")`)

// cookieRe 精确匹配 kanju 会话 cookie 名（kanju_token / session_id / sid），
// 只打码值部分，避免对普通 body 文本误伤。
var cookieRe = regexp.MustCompile(`(?i)\b((?:kanju[_-]?)?(?:token|session[_-]?id|sid))=([^;\s"]+)`)

// maskSensitive 对 JSON 值与 Set-Cookie 中的敏感片段打码，避免凭据进入对话上下文。
func maskSensitive(s string) string {
	s = sensitiveValRe.ReplaceAllString(s, `${1}<redacted>${2}`)
	return cookieRe.ReplaceAllString(s, `${1}=<redacted>`)
}

// openMCPDB 以与主程序一致的 WAL + busy_timeout 配置打开 SQLite（只读使用）。
func openMCPDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&mode=ro")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// sqliteTables 列出用户表（排除 sqlite_% 内部表）。
func sqliteTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		tables = append(tables, n)
	}
	return tables, rows.Err()
}

func tableRowCount(db *sql.DB, table string) (int64, error) {
	// 表名来自 sqlite_master 白名单查询，非用户输入，无注入面。
	var cnt int64
	err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&cnt)
	return cnt, err
}

// ---- 工具实现 ----

// toolKanjuStatus 一站式体检：磁盘二进制版本、服务探活、DB 状态，一次调用出结论。
func toolKanjuStatus(args map[string]any) (string, error) {
	port := argInt(args, "port", 28088)
	report := map[string]any{
		"port":        port,
		"probe_self":  probeSelfVersion(),
		"checked_at":  time.Now().Format(time.RFC3339),
		"workdir_cwd": func() string { wd, _ := os.Getwd(); return wd }(),
	}

	// 1) 被诊断二进制（真实来源：磁盘产物 dist/kanju -v）
	binPath := argString(args, "bin", "")
	if binPath == "" {
		cwd, _ := os.Getwd()
		for _, rel := range []string{"dist/kanju", "kanju"} {
			if p := filepath.Join(cwd, rel); fileExists(p) {
				binPath = p
				break
			}
		}
	}
	if binPath != "" && fileExists(binPath) {
		if info, err := probeBinVersion(binPath); err == nil {
			report["kanju_binary"] = info
			// 以 gitCommit 为准判断代码漂移；构建时间秒级/分钟级差异不构成告警。
			// 探针自身版本未注入（unknown）时不判定漂移 —— 拿 unknown 与磁盘版本对比
			// 只会产出误导性告警，真实问题是探针构建漏了 -ldflags。
			binCommit, _ := info["git_commit"].(string)
			switch {
			case gitCommit == "unknown" || gitCommit == "":
				report["probe_version_warning"] =
					"kanju-mcp 探针自身未注入版本信息（probe_self=unknown），无法与磁盘二进制对比代码版本。" +
						"重建探针时请带 -ldflags 注入（make release-backend 或 go build -ldflags \"-X main.buildTime=... -X main.gitCommit=...\" -o ~/.workbuddy/bin/kanju-mcp ./backend）"
			case binCommit != "" && binCommit != gitCommit:
				report["version_drift_warning"] =
					"磁盘 kanju 二进制（" + shortCommit(binCommit) + "）与 kanju-mcp 探针（" + shortCommit(gitCommit) + "）代码版本不一致：" +
						"探针结论基于探针自身代码版本，可能与运行中的服务行为不符；请重新构建两者后重试"
			}
		} else {
			report["kanju_binary"] = map[string]any{"path": binPath, "error": err.Error()}
		}
	} else {
		report["kanju_binary"] = map[string]any{
			"found": false,
			"note":  "磁盘上未找到 kanju 二进制（探测 cwd/dist/kanju），无法对比版本；不影响服务探活",
		}
	}

	// 2) 服务探活：TCP + /actuator/health + 静态首页
	tcpStart := time.Now()
	conn, tcpErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	tcpElapsed := time.Since(tcpStart)
	if tcpErr == nil {
		conn.Close()
		report["tcp_listen"] = map[string]any{"open": true, "elapsed_ms": tcpElapsed.Milliseconds()}
		health := httpProbe(port, "/actuator/health", "GET", "", 5*time.Second)
		report["health_endpoint"] = health
		index := httpProbe(port, "/", "GET", "", 5*time.Second)
		// 首页 body 对诊断无用且可能很大，只留状态码与耗时
		delete(index, "body")
		delete(index, "headers")
		report["static_index"] = index
	} else {
		report["tcp_listen"] = map[string]any{
			"open": false, "error": tcpErr.Error(),
			"summary": fmt.Sprintf("端口 %d 无服务监听。启动命令: cd $(git rev-parse --show-toplevel) && ./build.sh run（或 ./kanju.sh）", port),
		}
	}

	// 3) DB 状态
	dbPath, dbSource, candidates := resolveMCPDBPath(argString(args, "db", ""))
	report["db"] = map[string]any{"resolved_path": dbPath, "resolved_from": dbSource, "candidates": candidates}
	if dbPath != "" && fileExists(dbPath) {
		st, _ := os.Stat(dbPath)
		dbInfo := map[string]any{
			"exists":        true,
			"resolved_path": dbPath,
			"resolved_from": dbSource,
			"size_bytes":    st.Size(),
			"size_human":    humanBytes(st.Size()),
			"wal_sidecar":   map[string]bool{"wal": fileExists(dbPath + "-wal"), "shm": fileExists(dbPath + "-shm")},
			"note":          "WAL/SHM 侧车文件存在说明有进程以 WAL 模式打开过此库（通常是运行中的 kanju）",
		}
		if db, err := openMCPDB(dbPath); err == nil {
			if tables, terr := sqliteTables(db); terr == nil {
				dbInfo["table_count"] = len(tables)
				dbInfo["tables"] = tables
			}
			var srcTotal, srcEnabled int64
			_ = db.QueryRow("SELECT COUNT(*) FROM site_source").Scan(&srcTotal)
			_ = db.QueryRow("SELECT COUNT(*) FROM site_source WHERE is_disable = 0").Scan(&srcEnabled)
			dbInfo["site_sources"] = map[string]int64{"total": srcTotal, "enabled": srcEnabled, "disabled": srcTotal - srcEnabled}
			var userCount int64
			_ = db.QueryRow("SELECT COUNT(*) FROM app_user").Scan(&userCount)
			dbInfo["app_users"] = userCount
			db.Close()
		} else {
			dbInfo["open_error"] = err.Error()
		}
		report["db"] = dbInfo
	} else {
		report["db"] = map[string]any{
			"exists": false, "resolved_path": dbPath, "resolved_from": dbSource, "candidates": candidates,
			"summary": "未找到 kanju.db。首次启动 kanju 后会在 dist/data/kanju.db 生成；也可显式传 db 参数或设置 KANJU_DB 环境变量",
		}
	}

	// 4) 汇总结论
	summaryLines := []string{}
	if tcpErr != nil {
		summaryLines = append(summaryLines, fmt.Sprintf("❌ 服务未运行：127.0.0.1:%d 无监听。执行 ./build.sh run 启动后再调试 API。", port))
	} else {
		summaryLines = append(summaryLines, fmt.Sprintf("✅ 服务在跑：127.0.0.1:%d 可达（health=%v）。", port, healthStatusOf(report)))
	}
	if dbm, ok := report["db"].(map[string]any); ok && dbm["exists"] == true {
		summaryLines = append(summaryLines, fmt.Sprintf("✅ 数据库就绪：%s（%s）", dbm["resolved_path"], dbm["size_human"]))
	} else {
		summaryLines = append(summaryLines, "⚠️ 数据库未找到：服务未启动过或路径非默认。")
	}
	if w, ok := report["version_drift_warning"]; ok {
		summaryLines = append(summaryLines, "⚠️ "+w.(string))
	}
	if w, ok := report["probe_version_warning"]; ok {
		summaryLines = append(summaryLines, "⚠️ "+w.(string))
	}
	report["summary"] = strings.Join(summaryLines, "\n")

	return marshalPretty(report), nil
}

func healthStatusOf(report map[string]any) string {
	if h, ok := report["health_endpoint"].(map[string]any); ok {
		switch s := h["status"].(type) {
		case int:
			return strconv.Itoa(s)
		case float64:
			return strconv.Itoa(int(s))
		}
		if e, ok := h["error"].(string); ok {
			return "error: " + e
		}
	}
	return "n/a"
}

// shortCommit 展示用的短提交号（注入的是完整 hash 时截断）。
func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func marshalPretty(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// toolKanjuDB 数据库概览：表清单+行数、用户（无敏感字段）、站源统计、settings。
func toolKanjuDB(args map[string]any) (string, error) {
	dbPath, dbSource, _ := resolveMCPDBPath(argString(args, "db", ""))
	if dbPath == "" || !fileExists(dbPath) {
		return "", fmt.Errorf("kanju.db 未找到（解析来源: %s）。请先启动 kanju 或显式传 db 参数 / 设置 KANJU_DB", dbSource)
	}
	db, err := openMCPDB(dbPath)
	if err != nil {
		return "", fmt.Errorf("打开数据库失败 %s: %w", dbPath, err)
	}
	defer db.Close()

	report := map[string]any{"db_path": dbPath, "resolved_from": dbSource}
	tables, err := sqliteTables(db)
	if err != nil {
		return "", fmt.Errorf("读取表清单: %w", err)
	}
	counts := map[string]int64{}
	for _, t := range tables {
		if c, err := tableRowCount(db, t); err == nil {
			counts[t] = c
		}
	}
	report["table_row_counts"] = counts

	// 用户：只暴露 username/role/created_at，绝不查询 password_hash/salt/token。
	users := []map[string]any{}
	if rows, err := db.Query("SELECT id, username, role, created_at FROM app_user ORDER BY id"); err == nil {
		for rows.Next() {
			var id int64
			var username, role string
			var createdAt sql.NullString
			if rows.Scan(&id, &username, &role, &createdAt) == nil {
				users = append(users, map[string]any{"id": id, "username": username, "role": role, "created_at": createdAt.String})
			}
		}
		rows.Close()
	}
	report["app_users"] = users
	report["privacy_note"] = "密码哈希/salt/token 列被刻意排除，不在诊断输出中出现"

	// 站源统计
	var total, enabled, jsonAPI, html int64
	_ = db.QueryRow("SELECT COUNT(*) FROM site_source").Scan(&total)
	_ = db.QueryRow("SELECT COUNT(*) FROM site_source WHERE is_disable = 0").Scan(&enabled)
	_ = db.QueryRow("SELECT COUNT(*) FROM site_source WHERE source_type = 'json_api'").Scan(&jsonAPI)
	_ = db.QueryRow("SELECT COUNT(*) FROM site_source WHERE source_type = 'html'").Scan(&html)
	report["site_sources"] = map[string]int64{
		"total": total, "enabled": enabled, "disabled": total - enabled,
		"json_api": jsonAPI, "html": html,
	}

	// settings
	settings := map[string]string{}
	if rows, err := db.Query("SELECT key, value FROM settings"); err == nil {
		for rows.Next() {
			var k, v string
			if rows.Scan(&k, &v) == nil {
				settings[k] = v
			}
		}
		rows.Close()
	}
	report["settings"] = settings

	// avail_cache 健康度
	var availTotal, availOK int64
	_ = db.QueryRow("SELECT COUNT(*) FROM avail_cache").Scan(&availTotal)
	_ = db.QueryRow("SELECT COUNT(*) FROM avail_cache WHERE available = 1").Scan(&availOK)
	report["avail_cache"] = map[string]int64{
		"total": availTotal, "available": availOK, "unavailable": availTotal - availOK,
	}

	return marshalPretty(report), nil
}

// toolKanjuSources 站源清单：id/name/base_url/source_type/enabled/backup。
func toolKanjuSources(args map[string]any) (string, error) {
	dbPath, _, _ := resolveMCPDBPath(argString(args, "db", ""))
	if dbPath == "" || !fileExists(dbPath) {
		return "", fmt.Errorf("kanju.db 未找到；请先启动 kanju 或传 db 参数 / 设置 KANJU_DB")
	}
	limit := argInt(args, "limit", 50)
	if limit < 1 || limit > 500 {
		limit = 50
	}
	offset := argInt(args, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	nameFilter := argString(args, "name", "")

	db, err := openMCPDB(dbPath)
	if err != nil {
		return "", fmt.Errorf("打开数据库失败: %w", err)
	}
	defer db.Close()

	var total int64
	_ = db.QueryRow("SELECT COUNT(*) FROM site_source").Scan(&total)

	query := "SELECT id, name, base_url, source_type, is_disable, backup_base_url, serial_number FROM site_source"
	var conds []string
	var params []any
	if nameFilter != "" {
		conds = append(conds, "name LIKE ?")
		params = append(params, "%"+nameFilter+"%")
	}
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += fmt.Sprintf(" ORDER BY serial_number, id LIMIT %d OFFSET %d", limit, offset)

	rows, err := db.Query(query, params...)
	if err != nil {
		return "", fmt.Errorf("查询站源: %w", err)
	}
	defer rows.Close()

	sources := []map[string]any{}
	for rows.Next() {
		var id, serial, disable sql.NullInt64
		var name, baseURL sql.NullString
		var sourceType, backup sql.NullString
		if err := rows.Scan(&id, &name, &baseURL, &sourceType, &disable, &backup, &serial); err != nil {
			continue
		}
		sources = append(sources, map[string]any{
			"id":          id.Int64,
			"name":        name.String,
			"base_url":    baseURL.String,
			"source_type": sourceType.String,
			"enabled":     disable.Int64 == 0,
			"backup_url":  backup.String,
			"serial":      serial.Int64,
		})
	}
	out := map[string]any{
		"total": total,
		"count": len(sources),
		"limit": limit, "offset": offset,
		"sources": sources,
	}
	if nameFilter != "" {
		out["name_filter"] = nameFilter
	}
	return marshalPretty(out), nil
}

// toolKanjuProbe 对本机运行中的 kanju API 做一次 HTTP 探针（GET/POST 均可）。
func toolKanjuProbe(args map[string]any) (string, error) {
	port := argInt(args, "port", 28088)
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("非法端口 %d（需 1-65535）", port)
	}
	path := argString(args, "path", "")
	if path == "" {
		return "", fmt.Errorf("缺少必填参数 path（例如 /actuator/health 或 /api/sources）")
	}
	method := strings.ToUpper(argString(args, "method", "GET"))
	if method != "GET" && method != "POST" && method != "HEAD" && method != "OPTIONS" {
		return "", fmt.Errorf("探针仅允许无副作用方法 GET/POST/HEAD/OPTIONS，收到 %s（诊断工具不做写操作）", method)
	}
	body := argString(args, "body", "")
	timeoutMs := argInt(args, "timeout_ms", 8000)
	if timeoutMs < 100 || timeoutMs > 30000 {
		timeoutMs = 8000
	}
	// 可选会话链：提供 login_username 时先用同一 cookie jar 登录，
	// 正式探针自动携带会话 cookie，一次调用即可调通受 authGate 保护的接口。
	var loginStep map[string]any
	if lu := argString(args, "login_username", ""); lu != "" {
		lp := argString(args, "login_password", "")
		jar, _ := cookiejar.New(nil)
		cred, _ := json.Marshal(map[string]string{"username": lu, "password": lp})
		loginStep = httpProbeJar(port, "/api/auth/login", "POST", string(cred), time.Duration(timeoutMs)*time.Millisecond, jar)
		// kanju 沿用 HTTP 200 + 业务体 isSuccess 约定：登录失败也是 HTTP 200，
		// 必须双条件校验，否则错误密码会被误判为登录成功。
		loginOK := false
		if code, _ := loginStep["status"].(int); code == 200 {
			var biz struct {
				IsSuccess bool `json:"isSuccess"`
			}
			if json.Unmarshal([]byte(fmt.Sprint(loginStep["body"])), &biz) == nil && biz.IsSuccess {
				loginOK = true
			}
		}
		if !loginOK {
			return marshalPretty(map[string]any{
				"ok":         false,
				"login_step": loginStep,
				"next_hint":  "登录失败（HTTP 200 但 isSuccess=false 或非 200）：确认用户名/密码，或检查该账号是否被禁用",
			}), nil
		}
		res := httpProbeJar(port, path, method, body, time.Duration(timeoutMs)*time.Millisecond, jar)
		return renderProbeResult(res, loginStep), nil
	}
	res := httpProbe(port, path, method, body, time.Duration(timeoutMs)*time.Millisecond)

	return renderProbeResult(res, nil), nil
}

// renderProbeResult 附加基于状态码的下一步建议（拿一次结果就能行动），
// 可选附带登录步骤摘要（仅状态码与耗时，cookie 永不回显）。
func renderProbeResult(res map[string]any, loginStep map[string]any) string {
	if loginStep != nil {
		res["login_step"] = map[string]any{
			"status":     loginStep["status"],
			"elapsed_ms": loginStep["elapsed_ms"],
			"note":       "POST /api/auth/login via shared cookie jar; cookie 值不回显",
		}
	}
	if m, ok := res["status"]; ok {
		switch code, _ := m.(int); {
		case code == 401:
			res["next_hint"] = "401 未登录：该接口受 authGate 保护。可在本次调用传 login_username + login_password 自动登录后再探，或用 --no-auth 启动 kanju"
		case code == 404:
			res["next_hint"] = "404：路径不存在或方法不匹配（Go 1.22 mux 按方法+路径精确匹配，如 /api/detail 只接受 POST）"
		case code == 405:
			res["next_hint"] = "405：方法不被该路由接受；对照 server.go routes() 中的注册方法"
		case code >= 500:
			res["next_hint"] = "5xx 服务端错误：查看 kanju 进程 stderr 日志定位堆栈"
		case code == 200:
			res["next_hint"] = "200 正常。若 body 内容异常，重点看业务字段而非传输层"
		}
	}
	return marshalPretty(res)
}

// ---- 工具表 ----

func mcpToolTable() []mcpToolDef {
	return []mcpToolDef{
		{
			Name:        "kanju_status",
			Description: "kanju 一站式体检：服务探活（TCP + /actuator/health + 静态首页）、磁盘 kanju 二进制版本对比（真实来源 dist/kanju -v，与探针自身版本区分）、SQLite 数据库状态。一次调用回答「服务在不在跑 / 版本对不对 / 数据库在不在」，全部只读。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"port": map[string]any{"type": "integer", "description": "kanju HTTP 端口，默认 28088", "default": 28088},
					"db":   map[string]any{"type": "string", "description": "SQLite 路径；默认按 参数 > env KANJU_DB > cwd/dist/data/kanju.db > cwd/data/kanju.db 探测"},
					"bin":  map[string]any{"type": "string", "description": "被诊断的 kanju 二进制路径；默认探测 cwd/dist/kanju"},
				},
			},
			Handler: toolKanjuStatus,
		},
		{
			Name:        "kanju_db",
			Description: "kanju SQLite 概览（只读）：所有表及行数、用户清单（刻意排除密码哈希/salt/token）、站源启用统计、settings、可用性缓存健康度。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"db": map[string]any{"type": "string", "description": "SQLite 路径；默认同 kanju_status 探测链"},
				},
			},
			Handler: toolKanjuDB,
		},
		{
			Name:        "kanju_sources",
			Description: "kanju 站源清单（只读）：从 site_source 表列出 id/name/base_url/source_type/enabled/backup_url，支持按名称过滤与分页，返回 total。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"db":     map[string]any{"type": "string", "description": "SQLite 路径；默认同 kanju_status 探测链"},
					"limit":  map[string]any{"type": "integer", "description": "返回条数，默认 50，上限 500", "default": 50},
					"offset": map[string]any{"type": "integer", "description": "跳过条数，默认 0", "default": 0},
					"name":   map[string]any{"type": "string", "description": "按站源名称模糊过滤"},
				},
			},
			Handler: toolKanjuSources,
		},
		{
			Name:        "kanju_probe",
			Description: "对本机 kanju HTTP API 做一次探针（host 固定 127.0.0.1，只允许 GET/POST/HEAD/OPTIONS 无副作用方法）。返回状态码、耗时、响应头（Set-Cookie 打码）、截断 body（敏感字段打码）与下一步调试建议。401 时提示登录或 --no-auth 调试模式。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":           map[string]any{"type": "string", "description": "API 路径，如 /actuator/health、/api/sources、/api/auth/me"},
					"port":           map[string]any{"type": "integer", "description": "kanju HTTP 端口，默认 28088", "default": 28088},
					"method":         map[string]any{"type": "string", "description": "GET/POST/HEAD/OPTIONS，默认 GET", "default": "GET"},
					"body":           map[string]any{"type": "string", "description": "POST 请求体（JSON 文本）"},
					"login_username": map[string]any{"type": "string", "description": "可选：先用该账号 POST /api/auth/login（同一 cookie jar），再对目标 path 发起探针；用于调通受 authGate 保护的接口"},
					"login_password": map[string]any{"type": "string", "description": "配套密码；仅用于登录请求，不回显到任何输出"},
					"timeout_ms":     map[string]any{"type": "integer", "description": "超时毫秒，默认 8000，上限 30000", "default": 8000},
				},
				"required": []string{"path"},
			},
			Handler: toolKanjuProbe,
		},
	}
}

// 排序引用守卫：确保导入都被使用（sort 用于未来的确定性输出扩展）。
var _ = sort.Strings
