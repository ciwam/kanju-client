package backend

// subject_cache.go — 影片信息（豆瓣详情）SQLite 缓存：第一次搜索/
// 打开详情时抓取并落库，之后同影片直接读缓存秒出；缓存超过 TTL 时
// 仍先返回缓存（旧信息即刻可读），后台静默刷新，刷新结果写回库并
// 附加 revision（内容指纹）——客户端据此判断"信息有更新"并提示替换。

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// subjectTTL 影片信息被视为新鲜（免刷新）的时长。影片信息上线后
// 几乎不变，7 天足够；过期仍先返回旧数据（SWR），后台刷新。
const subjectTTL = 7 * 24 * time.Hour

// ensureSubjectSchema 建表（migrate 路径调用，幂等）。
func ensureSubjectSchema(db *sql.DB) {
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS douban_subject (
		douban_id  TEXT PRIMARY KEY,
		title      TEXT,
		payload    TEXT NOT NULL,
		revision   TEXT,
		fetched_at TEXT NOT NULL
	)`)
}

// subjectRevision 内容指纹：payload 字段集的 sha256 前 16 hex。
// 客户端对比新旧 revision 判断"信息有更新"。
func subjectRevision(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])[:16]
}

// subjectCacheGet 读缓存：命中返回 payload 与新鲜度。
func (s *Server) subjectCacheGet(id string) (payload []byte, fresh bool, ok bool) {
	var raw, fetchedAt string
	err := s.db.QueryRow(
		"SELECT payload, fetched_at FROM douban_subject WHERE douban_id = ?", id,
	).Scan(&raw, &fetchedAt)
	if err != nil {
		return nil, false, false
	}
	t, e := time.Parse(time.RFC3339, fetchedAt)
	if e != nil {
		return nil, false, false
	}
	return []byte(raw), time.Since(t) < subjectTTL, true
}

// subjectCachePut 写缓存（含 revision 计算）。
func (s *Server) subjectCachePut(id string, payload []byte) string {
	rev := subjectRevision(payload)
	var title string
	var data map[string]any
	if json.Unmarshal(payload, &data) == nil {
		title, _ = data["title"].(string)
	}
	_, _ = s.db.Exec(
		`INSERT INTO douban_subject (douban_id, title, payload, revision, fetched_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(douban_id) DO UPDATE SET title = excluded.title, payload = excluded.payload, revision = excluded.revision, fetched_at = excluded.fetched_at`,
		id, title, string(payload), rev, time.Now().Format(time.RFC3339))
	return rev
}

// subjectCacheRevision 只查 revision（SWR 刷新后客户端对比用）。
func (s *Server) subjectCacheRevision(id string) string {
	var rev string
	_ = s.db.QueryRow("SELECT revision FROM douban_subject WHERE douban_id = ?", id).Scan(&rev)
	return rev
}

// subjectRefreshInFlight 同影片刷新单飞：SWR 并发触发去重
// （与 douban warm 的 refreshInFlight 同模式）。
var subjectRefreshInFlight sync.Map

// subjectStoreGetAll 供清理循环遍历。
func (s *Server) subjectCacheCount() int {
	var n int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM douban_subject").Scan(&n)
	return n
}

// marshalStable 序列化为紧凑 JSON（revision 以此为准，键序稳定）。
func marshalStable(v map[string]any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// cleanupSubjectCache 容量巡检：超 5000 条按 fetched_at 从旧到新删除。
// 每日清理循环调用（cleanup.go）。
func (s *Server) cleanupSubjectCache() {
	const maxRows = 5000
	res, err := s.db.Exec(
		`DELETE FROM douban_subject WHERE douban_id IN (
			SELECT douban_id FROM douban_subject ORDER BY fetched_at DESC LIMIT -1 OFFSET ?)`,
		maxRows)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		logPrint("cleanup: 已清理 %d 条过期影片信息缓存", n)
	}
}
