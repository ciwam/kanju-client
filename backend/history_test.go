package backend

import (
	"path/filepath"
	"testing"
)

// newTestServer 打开一个临时目录下的 SQLite 库并完成迁移/种子，供测试使用。
func newTestServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{tokens: newTokenCache()}
	if err := s.openDB(filepath.Join(t.TempDir(), "test.db")); err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { s.db.Close() })
	return s
}

// TestSaveHistoryDedup 验证同一影片同一集重复保存会被合并为一条记录
// （DELETE 旧键 + INSERT 新记录的事务语义）。
func TestSaveHistoryDedup(t *testing.T) {
	s := newTestServer(t)
	uid := int64(1)
	if err := s.saveHistory(&uid, "测试影片", "", "量子资源",
		"https://example.com/subject/1/", "", "第1集", "https://example.com/play/1.m3u8", 10, 100); err != nil {
		t.Fatalf("save 1: %v", err)
	}
	if err := s.saveHistory(&uid, "测试影片", "", "量子资源",
		"https://example.com/subject/1/", "", "第1集", "https://example.com/play/1.m3u8", 42, 100); err != nil {
		t.Fatalf("save 2: %v", err)
	}
	hist, err := s.getHistory(&uid)
	if err != nil {
		t.Fatalf("getHistory: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("期望合并为 1 条记录，实际 %d 条", len(hist))
	}
	if hist[0].Position != 42 {
		t.Fatalf("应保留最新进度 42，实际 %d", hist[0].Position)
	}
}

// TestSaveHistoryRejectsBlankRecord 验证无标题且无来源地址的异常记录被丢弃。
func TestSaveHistoryRejectsBlankRecord(t *testing.T) {
	s := newTestServer(t)
	uid := int64(1)
	if err := s.saveHistory(&uid, "", "", "", "", "", "", "https://example.com/play/1.m3u8", 0, 0); err != nil {
		t.Fatalf("blank record should be dropped silently, got err %v", err)
	}
	hist, err := s.getHistory(&uid)
	if err != nil {
		t.Fatalf("getHistory: %v", err)
	}
	if len(hist) != 0 {
		t.Fatalf("空白记录应被丢弃，实际 %d 条", len(hist))
	}
}

// TestCleanupOldHistory 验证超过保留期的历史被清理、未超期的不受影响。
func TestCleanupOldHistory(t *testing.T) {
	s := newTestServer(t)
	uid := int64(1)
	if err := s.saveHistory(&uid, "新片", "", "源",
		"https://example.com/subject/new/", "", "第1集", "https://example.com/play/new.m3u8", 0, 0); err != nil {
		t.Fatalf("save new: %v", err)
	}
	// 直接插入一条"一年前"的旧记录
	if _, err := s.db.Exec(
		`INSERT INTO history (user_id, title, source_url, episode_title, play_url, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		uid, "旧片", "https://example.com/subject/old/", "第1集",
		"https://example.com/play/old.m3u8", "2000-01-01T00:00:00+08:00"); err != nil {
		t.Fatalf("insert old: %v", err)
	}
	s.cleanupOldHistory()
	hist, err := s.getHistory(&uid)
	if err != nil {
		t.Fatalf("getHistory: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("期望保留 1 条新记录，实际 %d 条", len(hist))
	}
	if hist[0].Title != "新片" {
		t.Fatalf("应保留新片，实际 %q", hist[0].Title)
	}
}
