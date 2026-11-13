// api 包对真实 kanju 后端的冒烟测试。后端未运行时跳过。
package api

import (
	"os"
	"testing"
	"time"
)

func backendBase(t *testing.T) string {
	base := os.Getenv("KANJU_TEST_BASE")
	if base == "" {
		base = "http://127.0.0.1:28088"
	}
	c := New(base)
	req, _ := c.newRequest("GET", "/actuator/health", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Skipf("后端 %s 未运行，跳过", base)
	}
	resp.Body.Close()
	return base
}

func TestLiveLoginAndBrowse(t *testing.T) {
	base := backendBase(t)
	c := New(base)

	u, err := c.Login("admin", "admin")
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	if u.Username != "admin" {
		t.Fatalf("用户名不符: %+v", u)
	}
	if c.Token() == "" {
		t.Fatal("token 未从 Set-Cookie 解析出来")
	}
	if _, err := c.Me(); err != nil {
		t.Fatalf("auth/me 失败: %v", err)
	}

	if _, err := c.DoubanList("tv", "热门", "", 10, 0); err != nil {
		t.Logf("豆瓣榜单失败（网络环境原因可接受）: %v", err)
	}

	if _, err := c.Histories(); err != nil {
		t.Fatalf("历史失败: %v", err)
	}
	if _, err := c.Favorites(); err != nil {
		t.Fatalf("收藏失败: %v", err)
	}
	if _, err := c.LiveChannels(""); err != nil {
		t.Logf("直播失败（无种子数据可接受）: %v", err)
	}

	// 收藏增删。
	f := Favorite{Title: "冒烟测试", DetailURL: "smoke://test/" + time.Now().Format(time.RFC3339)}
	if err := c.AddFavorite(f); err != nil {
		t.Fatalf("收藏失败: %v", err)
	}
	if err := c.RemoveFavorite(f.DetailURL); err != nil {
		t.Fatalf("取消收藏失败: %v", err)
	}

	// 站源列表（admin 可见）。
	if _, err := c.Sources(); err != nil {
		t.Fatalf("站源列表失败: %v", err)
	}
}
