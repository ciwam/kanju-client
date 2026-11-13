package app

import (
	"strconv"
	"strings"
	"time"

	"kanju/client/api"
)

func trimSpace(s string) string { return strings.TrimSpace(s) }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// fmtPos 把秒数格式化为 mm:ss 或 h:mm:ss（fmtDuration 的秒数版本）。
func fmtPos(seconds int64) string {
	return fmtDuration(time.Duration(seconds) * time.Second)
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// segmentAfter 取 s 中 sep 之后到下一个 / 的片段。
func segmentAfter(s, sep string) string {
	i := strings.Index(s, sep)
	if i < 0 {
		return ""
	}
	rest := s[i+len(sep):]
	if j := strings.Index(rest, "/"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func joinNonEmpty(items []string, sep string) string {
	out := ""
	for _, s := range items {
		if s == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += s
	}
	return out
}

// historyEntry 组装历史条目。title 形如「片名 · 第x集」。
func historyEntry(title, playURL, sourceName, sourceURL string) api.History {
	t, ep := splitEpisodeTitle(title)
	return api.History{
		Title: t, EpisodeTitle: ep, PlayURL: playURL,
		SourceName: sourceName, SourceURL: sourceURL,
	}
}

// splitEpisodeTitle 按「 · 」拆分片名与集名。
func splitEpisodeTitle(s string) (title, episode string) {
	for i := 0; i+3 <= len(s); i++ {
		if s[i] == '·' && i > 0 && s[i-1] == ' ' && s[i+1] == ' ' {
			return trimSpace(s[:i-1]), trimSpace(s[i+2:])
		}
	}
	return s, ""
}
