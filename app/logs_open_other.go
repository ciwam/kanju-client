//go:build !android && !ios && !js

package app

// 各桌面的文件管理器打开方式。macOS 主用 osascript reveal（Finder），
// openFileManager 是目录回退；Windows explorer /select, 选中文件；
// Linux xdg-open 只能开目录。

import (
	"os/exec"
	"runtime"
)

func openFileManager(path string) error {
	switch runtime.GOOS {
	case "windows":
		// explorer /select, 需要反斜杠路径。
		return exec.Command("explorer", "/select,", path).Start()
	case "darwin":
		return exec.Command("open", "-R", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}
