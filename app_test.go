package main

import (
	"testing"

	"kanju/client/api"
	"kanju/client/app"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestMain(m *testing.M) { nsgotest.Main(m) }

// TestLoginShown 外部服务器模式（Embedded=false）未登录时显示登录页。
func TestLoginShown(t *testing.T) {
	a := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		state := app.SetupForTest(cx, "http://127.0.0.1:1")
		return app.Root(cx, state)
	}, nsgotest.Size(1000, 700))
	if a.Find("id:username") == nil {
		t.Fatal("登录页未渲染出用户名输入框")
	}
	if a.Find("id:password") == nil {
		t.Fatal("登录页未渲染出密码输入框")
	}
	if a.Find("id:submit") == nil {
		t.Fatal("登录页未渲染出登录按钮")
	}
}

// TestEmbeddedSkipsLogin 内嵌后端模式（Embedded=true）默认用户已就位，直接进主壳。
func TestEmbeddedSkipsLogin(t *testing.T) {
	a := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		state := app.SetupForTest(cx, "http://127.0.0.1:1")
		state.Embedded.Set(true)
		state.User.Set(&api.User{ID: 0, Username: "本地", Role: "admin"})
		return app.Root(cx, state)
	}, nsgotest.Size(1000, 700))
	if len(a.Query("id:username")) != 0 {
		t.Fatal("内嵌模式不应出现登录页")
	}
	if a.Find("id:shell") == nil {
		t.Fatal("内嵌模式未直接进入主界面")
	}
}

// TestDetailRenders 打开详情上下文后，页面应显示标题而不是空白。
func TestDetailRenders(t *testing.T) {
	stateCh := make(chan *app.App, 1)
	a := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		state := app.SetupForTest(cx, "http://127.0.0.1:1")
		state.Embedded.Set(true)
		state.User.Set(&api.User{ID: 0, Username: "本地", Role: "admin"})
		select {
		case stateCh <- state:
		default:
		}
		return app.Root(cx, state)
	}, nsgotest.Size(1000, 700))
	state := <-stateCh
	a.Post(func() { state.OpenDetail("测试影片", "", "https://movie.douban.com/subject/123/") })
	a.Settle()
	// 标题应出现（豆瓣请求失败也没关系，fallback 标题一定渲染）。
	if len(a.Query("text:测试影片")) == 0 {
		t.Fatal("详情页未渲染出影片标题")
	}
}
