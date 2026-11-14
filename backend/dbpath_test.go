package backend

import (
	"path/filepath"
	"testing"
)

// TestResolveDBPathExplicit 验证显式 --db 参数按原样使用（清洗后）。
func TestResolveDBPathExplicit(t *testing.T) {
	if got := resolveDBPath("/var/data/custom.db"); got != "/var/data/custom.db" {
		t.Fatalf("explicit path = %q, want as-is", got)
	}
	if got := resolveDBPath("relative/path.db"); got != "relative/path.db" {
		t.Fatalf("relative path = %q, want cleaned as-is", got)
	}
}

// TestResolveDBPathFromExeDir 验证默认路径恒为 <可执行文件目录>/data/kanju.db，
// 与程序所在目录绑定（不再有 dist 目录的上一级特例）。
func TestResolveDBPathFromExeDir(t *testing.T) {
	cases := []struct {
		name string
		exe  string
		want string
	}{
		{"普通目录", "/opt/kanju/kanju", "/opt/kanju/data/kanju.db"},
		{"dist 目录（曾经的上一级特例，现统一为程序所在目录）", "/opt/kanju/dist/kanju", "/opt/kanju/dist/data/kanju.db"},
		{"项目根 dist", "/Users/dev/kanju/dist/kanju", "/Users/dev/kanju/dist/data/kanju.db"},
	}
	for _, c := range cases {
		got := dbPathForExe(c.exe)
		if got != c.want {
			t.Errorf("%s: dbPathForExe(%q) = %q, want %q", c.name, c.exe, got, c.want)
		}
	}
}

// TestResolveDBPathFromExeDir 行为等价于 dbPathForExe(os.Executable())。
func TestResolveDBPathEquivalence(t *testing.T) {
	exe := "/some/dir/kanju"
	want := filepath.Join(filepath.Dir(exe), "data", "kanju.db")
	if got := dbPathForExe(exe); got != want {
		t.Fatalf("dbPathForExe(%q) = %q, want %q", exe, got, want)
	}
}
