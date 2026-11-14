package backend

// poster_cache.go — 海报磁盘缓存：write-through，代理层第一次取到图后
// 落盘，之后同 URL 直接读本地文件，零上游流量。
//
// key 是图片 URL 的 SHA-256 前 32 hex；同一影片的海报 URL 在豆瓣体系内
// 稳定，且任何来源（豆瓣轮换主机/TMDB 兜底）的图都按 URL 命中，无需
// 影片↔图片关联表。文件放 DB 同级 posters/ 下，哈希前 4 位两级分片防
// 单目录文件过多。容量由每日清理循环按 mtime LRU 淘汰。

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	posterCacheMaxBytes = 500 << 20 // 500MB：一屏 30 张 × ~30KB ≈ 1MB，够存数百屏
	posterCacheMaxAge   = 30 * 24 * time.Hour
)

// posterCacheDir 由 openDB 时确定（DB 同级 posters/）。
var (
	posterDirMu sync.RWMutex
	posterDir   string
)

func setPosterCacheDir(dbPath string) {
	dir := filepath.Join(filepath.Dir(dbPath), "posters")
	_ = os.MkdirAll(dir, 0o755)
	posterDirMu.Lock()
	posterDir = dir
	posterDirMu.Unlock()
}

func posterCacheRoot() string {
	posterDirMu.RLock()
	defer posterDirMu.RUnlock()
	return posterDir
}

// posterCachePath URL → 磁盘路径：posters/ab/cd/<hash32>.<ext>。
// ext 由 Content-Type 推导（未知类型不落盘，见 posterExtOf）。
func posterCachePath(rawURL, contentType string) string {
	sum := sha256.Sum256([]byte(rawURL))
	h := hex.EncodeToString(sum[:])[:32]
	ext := posterExtOf(contentType)
	if ext == "" {
		ext = ".img"
	}
	return filepath.Join(posterCacheRoot(), h[:2], h[2:4], h+ext)
}

func posterExtOf(ct string) string {
	switch {
	case strings.Contains(ct, "jpeg"), strings.Contains(ct, "jpg"):
		return ".jpg"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "gif"):
		return ".gif"
	case strings.Contains(ct, "avif"):
		return ".avif"
	}
	return ""
}

// contentTypeOfPath serve 时按扩展名回 Content-Type。
func contentTypeOfPath(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".avif":
		return "image/avif"
	}
	return "application/octet-stream"
}

// posterCacheGet 命中返回文件路径（供 http.ServeFile）。命中即触摸
// mtime（LRU 依据）。损坏（空文件）按未命中处理并删除。
func posterCacheGet(rawURL string) string {
	root := posterCacheRoot()
	if root == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(rawURL))
	h := hex.EncodeToString(sum[:])[:32]
	dir := filepath.Join(root, h[:2], h[2:4])
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), h) {
			p := filepath.Join(dir, e.Name())
			if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
				_ = os.Chtimes(p, time.Now(), time.Now()) // LRU 触摸
				return p
			}
			_ = os.Remove(p)
		}
	}
	return ""
}

// posterCachePut 原子写盘：同 URL 并发请求只有一个落盘成功，其余静默。
// tmp + rename 保证不会出现半张图被 serve。
func posterCachePut(rawURL, contentType string, body io.Reader) {
	root := posterCacheRoot()
	if root == "" || contentType == "" {
		return
	}
	p := posterCachePath(rawURL, contentType)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	n, err := io.Copy(tmp, io.LimitReader(body, 10<<20)) // 与下发上限一致
	if err == nil && n > 0 {
		_ = tmp.Close()
		if _, statErr := os.Stat(p); errors.Is(statErr, os.ErrNotExist) {
			_ = os.Rename(tmpName, p)
			return
		}
	} else {
		_ = tmp.Close()
	}
	_ = os.Remove(tmpName)
}

// cleanupPosterCache 容量巡检：先删超龄条目，仍超容量再按 mtime 从旧到
// 新淘汰。由每日清理循环调用（cleanup.go）。
func cleanupPosterCache() {
	root := posterCacheRoot()
	if root == "" {
		return
	}
	type entry struct {
		path  string
		size  int64
		mtime time.Time
	}
	var (
		total     int64
		entries   []entry
		agedPaths []string
	)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil // ignore: 上游，读失败按不存在处理
		}
		if !strings.HasSuffix(d.Name(), ".tmp-") && !strings.HasPrefix(d.Name(), ".tmp-") {
			if fi, err := d.Info(); err == nil {
				total += fi.Size()
				if time.Since(fi.ModTime()) > posterCacheMaxAge {
					agedPaths = append(agedPaths, p)
				} else {
					entries = append(entries, entry{p, fi.Size(), fi.ModTime()})
				}
			}
		}
		return nil
	})
	for _, p := range agedPaths {
		if fi, err := os.Stat(p); err == nil {
			total -= fi.Size()
		}
		_ = os.Remove(p)
	}
	if total <= posterCacheMaxBytes {
		return
	}
	// mtime 升序（最久未用先删）。
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].mtime.Before(entries[i].mtime) {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
	for _, e := range entries {
		if total <= posterCacheMaxBytes {
			return
		}
		if os.Remove(e.path) == nil {
			total -= e.size
		}
	}
}
