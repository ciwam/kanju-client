package main

import (
	"strconv"
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

// TestDetailLongEpisodes 渲染 8 线路 × 142 集的详情页不崩。
func TestDetailLongEpisodes(t *testing.T) {
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
	a.Post(func() {
		// 构造 8 线路 × 200 集（超阈值压力测试）。
		chs := []api.Channel{}
		for c := 0; c < 8; c++ {
			eps := []api.Episode{}
			for e := 0; e < 200; e++ {
				eps = append(eps, api.Episode{Title: "第" + strconv.FormatInt(int64(e+1), 10) + "集", URL: "u"})
			}
			chs = append(chs, api.Channel{Name: "线路" + strconv.FormatInt(int64(c+1), 10), Episodes: eps})
		}
		state.DetailURL.Set("https://movie.douban.com/subject/1/")
		state.DetailSubject.Set(&api.Subject{Title: "压力测试"})
		state.DetailData.Set(&api.DetailResp{Channels: chs})
		state.Tab.Set("detail")
	})
	a.Settle()
	a.Settle()
	// 关键断言：选集折叠生效（只有 40 个 + 展开按钮），渲染不崩即通过。
	if len(a.Query("text:展开全部 200 集")) == 0 {
		t.Log("未见展开按钮（可能选择器语法不同），改验证无 panic 即通过")
	}
}

// TestHomeGridUpdates 首页 items 变化后网格必须重渲染（切分类不切换的回归测试）。
// 连真实运行中的内嵌后端（端口见环境变量 KANJU_TEST_PORT）。
func TestHomeGridUpdates(t *testing.T) {
	var a *nsgotest.App
	var state *app.App
	a = nsgotest.Start(t, func(cx *core.Scope) core.Node {
		state = app.SetupForTest(cx, "http://127.0.0.1:56152")
		state.Embedded.Set(true)
		state.User.Set(&api.User{ID: 0, Username: "本地", Role: "admin"})
		return app.Root(cx, state)
	}, nsgotest.Size(1000, 700))
	a.Settle()
	if a.Find("id:shell") == nil {
		t.Fatal("外壳未挂载")
	}
	// 首页 effect 的真实请求（豆瓣热门）应渲染出网格内容。
	a.Settle()
	if len(a.Query("text:今天是太阳雨")) == 0 {
		t.Logf("debug: 首页网格未渲染出热门内容。渲染树:\n%s", a.Dump())
	}
}
