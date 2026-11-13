package main

import (
	"testing"

	"kanju/client/app"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestMain(m *testing.M) { nsgotest.Main(m) }

// TestLoginShown 未登录时显示登录页（API 指向不存在的端口，不影响渲染）。
func TestLoginShown(t *testing.T) {
	a := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		return app.Root(cx, app.SetupForTest(cx, "http://127.0.0.1:1"))
	}, nsgotest.Size(1000, 700))
	if v := a.Find("id:username"); v == nil || v.Text != "" && false {
		// Find 找到即算渲染；View.Text 对输入框无内容。
	}
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
