//go:build !android && !ios && !js

package app

// 日志目录的桌面端打开方式：macOS 用 osascript reveal（Finder），
// Windows/Linux 用系统文件管理器。移动端（android/ios）没有"打开
// 目录"概念，此文件不参与构建，LogCard 不渲染该按钮（openLogDirAvailable）。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// openLogDirAvailable 报告当前平台能否"打开日志目录"。
const openLogDirAvailable = true

// openLogDir 在系统文件管理器中打开/定位当天日志文件。
func openLogDir(dir string) {
	target := filepath.Join(dir, time.Now().Format("2006-01-02")+".log")
	if _, err := os.Stat(target); err != nil {
		// 当天文件还没生成时退回打开目录。
		switch runtime.GOOS {
		case "darwin":
			script := fmt.Sprintf(`tell application "Finder" to open (POSIX file %q as alias)`, dir)
			if err := exec.Command("osascript", "-e", script).Start(); err != nil {
				ClientLog("[logs] 打开目录失败: %v", err)
			}
		default:
			if err := openFileManager(dir); err != nil {
				ClientLog("[logs] 打开目录失败: %v", err)
			}
		}
		return
	}
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf(`tell application "Finder"
  reveal (POSIX file %q as alias)
  activate
end tell`, target)
		if err := exec.Command("osascript", "-e", script).Start(); err != nil {
			ClientLog("[logs] 打开日志失败: %v", err)
		}
	default:
		if err := openFileManager(target); err != nil {
			ClientLog("[logs] 打开日志失败: %v", err)
		}
	}
}
