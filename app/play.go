package app

// play.go — 播放触发链路（App 级）：详情页选集与播放器右侧选集列表
// 共用。解析 + 流探测 + 换源重试 + 进播放器，全部在此。

import (
	"runtime/debug"

	"kanju/client/api"

	"github.com/nsgo-dev/nsgo/core"
)

// PlayEpisode 解析并播放一集（含流探测与换源重试），成功后进播放器。
// busy 信号共用 DetailPlayBusy（详情页与播放器侧栏互斥，防连点并发）。
func (a *App) PlayEpisode(cx *core.Scope, ep api.Episode, ch api.Channel) {
	if a.DetailPlayBusy.Get() != "" {
		return
	}
	ClientLog("[play] 点选集 %q line=%q", ep.Title, ch.Name)
	a.DetailPlayBusy.Set("解析播放地址…")
	client := a.API
	// playOnce 解析 + 探测代理流：站源解析"成功"但视频 CDN 间歇性
	// 404 时（nsgo Video 对坏流静默黑屏、不触发 OnError），探测失败
	// 返回错误走换源，杜绝"不报错也不播放"。
	playOnce := func(rawURL string) (api.PlayResult, error) {
		r, err := client.Play(rawURL)
		if err != nil {
			return r, err
		}
		if r.Type == "iframe" {
			return r, nil // iframe 交给播放器的网页播放提示
		}
		proxyURL := client.PlaybackURL(r.URL, r.Type)
		if perr := client.ProbeMediaURL(proxyURL); perr != nil {
			ClientLog("[play] 流探测失败 %s: %v", proxyURL, perr)
			return r, perr
		}
		return r, nil
	}
	go func() {
		// panic 兜底：换源/探测链路的任何 panic 落日志再抛
		// （不兜底则整个应用闪退，连现场都没有）。
		defer func() {
			if r := recover(); r != nil {
				ClientLog("[play] 播放链路 panic: %v\n%s", r, debug.Stack())
				panic(r)
			}
		}()
		r, err := playOnce(ep.URL)
		if err != nil {
			// 换源重试：当前线路的解析失败（地址失效/线路整体下线）时，
			// 在其它线路里找同名选集依次尝试（如量子资源播放域名整体
			// 失效，其它站源的同集仍可播）。
			r, err = playFallbackProbe(cx, a, client, ep, ch, playOnce)
		}
		cx.Post(func() {
			a.DetailPlayBusy.Set("")
			if err != nil {
				a.DetailErr.Set(err.Error())
				return
			}
			if r.Type == "iframe" {
				a.DetailErr.Set("该集只能网页播放，请换线路或选集")
				return
			}
			a.DetailNowPlaying.Set(ep.URL)
			// 直链按类型改写成代理地址（防盗链/Referer 由代理层处理）。
			playURL := client.PlaybackURL(r.URL, r.Type)
			ClientLog("[play] 播放直链 %s (%s) -> %s", r.URL, r.Type, playURL)
			a.OpenPlayer(a.DetailTitle.Get()+" · "+ep.Title, playURL, ep.URL, ch.SourceName, a.DetailURL.Get())
		})
	}()
}

// playFallbackProbe 带流探测的换源（见 playFallbackWith）。
func playFallbackProbe(cx *core.Scope, a *App, client *api.Client, ep api.Episode, failed api.Channel, playOnce func(string) (api.PlayResult, error)) (api.PlayResult, error) {
	return playFallbackWith(cx, a, playOnce, ep, failed)
}

