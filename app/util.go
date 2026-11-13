package app

import (
	"fmt"
	"strconv"
	"strings"
)

func trimSpace(s string) string { return strings.TrimSpace(s) }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// fmtPos 把秒数格式化为 mm:ss 或 h:mm:ss。
func fmtPos(seconds int64) string {
	if seconds <= 0 {
		return "0:00"
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
