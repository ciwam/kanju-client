package backend

import (
	"time"
)

// historyRetentionDays 是历史记录保留天数。超过该时限的播放历史会被
// 定期清理，防止 history 表无限膨胀拖慢写入（列表查询本身有 LIMIT）。
const historyRetentionDays = 90

// startDBCleanupLoop 启动后台数据库清理任务：启动后立即执行一次，
// 之后每天执行一次。清理内容包括：
//   - 超过保留期的播放历史（favorite 是用户主动收藏，不自动删除）；
//   - 已过期的可用性探测持久化缓存（avail_cache）；
//   - 海报磁盘缓存的超龄/超容条目（posters/）。
func (s *Server) startDBCleanupLoop() {
	go func() {
		s.cleanupDB()
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for range t.C {
			s.cleanupDB()
		}
	}()
}

func (s *Server) cleanupDB() {
	s.cleanupOldHistory()
	s.cleanupAvailCacheDB()
	cleanupPosterCache()
	s.cleanupSubjectCache()
}

// cleanupOldHistory 删除超过保留期的历史记录。cutoff 用 Go 生成的
// RFC3339 字符串与数据库内存储格式（time.Now().Format(time.RFC3339)）
// 同格式比较，避免 SQLite datetime 解析差异。
func (s *Server) cleanupOldHistory() {
	cutoff := time.Now().AddDate(0, 0, -historyRetentionDays).Format(time.RFC3339)
	res, err := s.db.Exec("DELETE FROM history WHERE created_at < ?", cutoff)
	if err != nil {
		logWarn("cleanup: history purge failed: %v", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		logPrint("cleanup: 已清理 %d 条超过 %d 天的历史记录", n, historyRetentionDays)
	}
}
