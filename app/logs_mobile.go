//go:build android || ios || js

package app

// 移动端 / web：没有"打开目录"概念（沙箱内文件无 Finder/资源管理器
// 可视）。日志用复制按钮带走；路径本身对用户不可见。

const openLogDirAvailable = false

func openLogDir(dir string) {}
