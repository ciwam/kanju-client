package app

import (
	"kanju/client/api"
	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// Root 是应用根视图：未登录显示登录页，已登录显示外壳。
func Root(cx *core.Scope, a *App) core.Node {
	return core.Dynamic(func(*core.Scope) core.Node {
		if a.User.Get() == nil {
			return Login(cx, a)
		}
		return Shell(cx, a)
	})
}

// Login 登录/注册页。
func Login(cx *core.Scope, a *App) core.Node {
	mode := core.NewSignal(cx, "login") // login | register
	username := core.NewSignal(cx, "")
	password := core.NewSignal(cx, "")
	password2 := core.NewSignal(cx, "")
	busy := core.NewSignal(cx, false)
	msg := core.NewSignal(cx, "")

	submit := func() {
		if busy.Get() {
			return
		}
		name := trimSpace(username.Get())
		pass := password.Get()
		if name == "" || pass == "" {
			msg.Set("请输入用户名和密码")
			return
		}
		busy.Set(true)
		msg.Set("")
		client := a.API
		register := mode.Get() == "register"
		go func() {
			var (
				u     api.User
				token string
				err   error
			)
			if register {
				u, err = client.Register(name, pass)
			} else {
				u, err = client.Login(name, pass)
			}
			if err == nil {
				token = client.Token()
			}
			cx.Post(func() {
				busy.Set(false)
				if err != nil {
					msg.Set(err.Error())
					return
				}
				a.SaveAuth(cx, token, &u)
			})
		}()
	}

	serverDraft := core.NewSignal(cx, a.Server.Get())
	saveServer := func() {
		if s := trimSpace(serverDraft.Get()); s != "" && s != a.Server.Get() {
			a.SetServer(cx, trimSpace(s))
		}
	}

	return centered(cx, theme.Col(14,
		ui.Text("Kanju", 34, ui.Bold, nil),
		ui.Text("视频聚合播放", 14, ui.Regular, func() core.Color { return cx.Palette().TextMuted }),
		theme.Col(6,
			serverRow(cx, serverDraft, saveServer),
			theme.Col(10,
				ui.Input{Value: username.Get, OnChange: username.Set, Placeholder: "用户名", TestID: "username"},
				ui.Input{Value: password.Get, OnChange: password.Set, Placeholder: "密码", Masked: true,
					OnSubmit: func(string) { submit() }, TestID: "password"},
				core.Show{When: func() bool { return mode.Get() == "register" }, Then: func(*core.Scope) core.Node {
					return ui.Input{Value: password2.Get, OnChange: password2.Set, Placeholder: "确认密码", Masked: true,
						OnSubmit: func(string) { submit() }, TestID: "password2"}
				}},
			),
			core.Dynamic(func(*core.Scope) core.Node {
				if m := msg.Get(); m != "" {
					return ui.Text(m, 12, ui.Regular, func() core.Color { return cx.Palette().Danger })
				}
				return core.View{}
			}),
			core.Dynamic(func(*core.Scope) core.Node {
				return ui.Button{Text: buttonText(mode.Get(), busy.Get()), Variant: ui.ButtonPrimary, Flex: 1,
					OnPress: submit, TestID: "submit"}
			}),
			core.Dynamic(func(*core.Scope) core.Node {
				return ui.Button{
					Text:   toggleText(mode.Get()),
					Variant: ui.ButtonGhost, Flex: 1,
					OnPress: func() {
						msg.Set("")
						mode.Set(swapMode(mode.Get()))
					}, TestID: "toggle",
				}
			}),
		),
	))
}

func buttonText(mode string, busy bool) string {
	if busy {
		return "请稍候…"
	}
	if mode == "register" {
		return "注册并登录"
	}
	return "登录"
}

func toggleText(mode string) string {
	if mode == "register" {
		return "已有账号？去登录"
	}
	return "没有账号？去注册"
}

func swapMode(mode string) string {
	if mode == "register" {
		return "login"
	}
	return "register"
}

// serverRow 是登录页上的服务器地址输入行。
func serverRow(cx *core.Scope, draft *core.Signal[string], save func()) core.View {
	return core.View{
		StyleFunc: func() core.Style { return core.Style{Direction: core.Row, Gap: 8, Width: core.Pct(100)} },
		Children: []core.Node{
			ui.Input{Value: draft.Get, OnChange: draft.Set, Placeholder: "服务器地址 http://…", TestID: "server",
				Style: core.Style{FlexGrow: 1}},
			ui.Button{Text: "连接", Variant: ui.ButtonSecondary, OnPress: save},
		},
	}
}

// centered 居中容器。
func centered(cx *core.Scope, children ...core.Node) core.View {
	return core.View{
		StyleFunc: func() core.Style {
			return core.Style{
				Width: core.Pct(100), Height: core.Pct(100),
				AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter,
				Padding: core.All(24), Background: cx.Palette().Background,
			}
		},
		Children: []core.Node{theme.Col(12, children...)},
	}
}
