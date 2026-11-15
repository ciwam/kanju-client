package app

// update.go — 应用内检查更新与自更新（Android）：
//
//	检查：查 GitHub Releases 最新版（加速节点链），比对 build 标识
//	      （发版时间戳 epoch 分钟，见 release.sh）；
//	下载：Go 原生 socket 逐加速节点拉 APK（不受 Android 明文限制），
//	      进度经信号驱动 Modal 弹窗；
//	安装：mobile.InstallPackage 调起系统安装器（用户确认）。
//
// 需 nsgo.yaml android.permissions 声明 REQUEST_INSTALL_PACKAGES。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kanju/client/theme"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/mobile"
	"github.com/nsgo-dev/nsgo/ui"
)

// updateState 更新流程状态（versionCard 的局部信号，进度弹窗读取）。
type updateState struct {
	// active 更新弹窗是否显示；phase 当前进度文案；pct 0-100。
	active *core.Signal[bool]
	phase  *core.Signal[string]
	pct    *core.Signal[int]
	// hasErr 结果为错误（弹窗文案变警示色，隐藏进度条）。
	hasErr *core.Signal[bool]
	// apkPath 下载完成的 APK 路径（空 = 还没下完）。
	apkPath *core.Signal[string]
	// openedAt 弹窗打开时刻；done 结果已出（最短保留计时用）。
	openedAt time.Time
	done     bool
}

func newUpdateState(cx *core.Scope) *updateState {
	return &updateState{
		active:  core.NewSignal(cx, false),
		phase:   core.NewSignal(cx, ""),
		pct:     core.NewSignal(cx, 0),
		hasErr:  core.NewSignal(cx, false),
		apkPath: core.NewSignal(cx, ""),
	}
}

// ghRelease 最新 Release（名称 + 资产列表）。
type ghRelease struct {
	Name   string `json:"name"`
	Assets []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// fetchLatestReleaseFull 查最新 Release（加速节点链，全挂退直连）。
func fetchLatestReleaseFull() (ghRelease, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	apiURL := "https://api.github.com/repos/" + releaseRepo + "/releases/latest"
	try := func(u string) (ghRelease, error) {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return ghRelease{}, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := client.Do(req)
		if err != nil {
			return ghRelease{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return ghRelease{}, fmt.Errorf("GitHub 返回 %d", resp.StatusCode)
		}
		var r ghRelease
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return ghRelease{}, err
		}
		return r, nil
	}
	for _, node := range ghProxyNodes {
		if r, err := try(node + "/" + apiURL); err == nil {
			ClientLog("[update] 查最新版经加速节点 %s", node)
			return r, nil
		}
	}
	r, err := try(apiURL)
	if err != nil {
		return ghRelease{}, err
	}
	ClientLog("[update] 查最新版直连 GitHub（加速节点均不可用）")
	return r, nil
}

// latestAPK 在 Release 资产里找 Android 安装包直链。
func latestAPK(r ghRelease) (url string) {
	for _, a := range r.Assets {
		if strings.HasSuffix(strings.ToLower(a.Name), ".apk") {
			return a.BrowserDownloadURL
		}
	}
	return ""
}

// downloadWithProgress 经加速节点下载到 destPath（进度回调 0-100）。
// 逐节点尝试：某节点中途失败换下一个重来（APK 不大，可接受）。
func downloadWithProgress(assetURL, destPath string, onProgress func(pct int)) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	var lastErr error
	for _, node := range ghProxyNodes {
		u := assetURL
		if node != "" {
			u = node + "/" + assetURL
		}
		err := tryDownload(client, u, destPath, onProgress)
		if err == nil {
			return nil
		}
		lastErr = err
		ClientLog("[update] 节点下载失败 %s: %v，换下一个", node, err)
		os.Remove(destPath + ".part")
	}
	return lastErr
}

// tryDownload 单节点整包下载（先写 .part，成功后原子改名）。
func tryDownload(client *http.Client, u, destPath string, onProgress func(pct int)) error {
	resp, err := client.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	total := resp.ContentLength
	if total <= 0 {
		return fmt.Errorf("未知长度（Content-Length=%d）", total)
	}
	tmp := destPath + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	buf := make([]byte, 64*1024)
	var done int64
	lastPct := -1
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				os.Remove(tmp)
				return werr
			}
			done += int64(n)
			if pct := int(done * 100 / total); pct != lastPct {
				lastPct = pct
				onProgress(pct)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(tmp)
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, destPath); err != nil {
		return err
	}
	ClientLog("[update] APK 下载完成 %s (%d bytes)", destPath, done)
	return nil
}

// minDialogVisibleMs 弹窗最短保留时长（毫秒）：检查很快时弹窗不再
// 一闪而过，结果文案在弹窗内停留到满时长再收起。
const minDialogVisibleMs = 1200

// startUpdate 检查更新入口（设置页「检查更新」按钮）：
// 查最新版 → 有新版 → 下载（进度弹窗）→ 调起安装器。
func (a *App) startUpdate(cx *core.Scope, u *updateState) {
	if u.active.Get() {
		return
	}
	// 安装权限先行：未授权时引导去系统设置，返回后用户再点一次。
	mobile.CanInstallPackages(cx, func(allowed bool) {
		if !allowed {
			ClientLog("[update] 无安装权限，打开系统设置引导授权")
			mobile.OpenInstallSettings(cx, func(opened bool) {
				cx.Post(func() {
					if opened {
						a.UpdateErr.Set("请先在系统设置中允许本应用安装，再点检查更新")
					} else {
						a.UpdateErr.Set("无法打开安装权限设置")
					}
				})
			})
			return
		}
		// 权限就绪，走完整流程。
		cx.Post(func() {
			u.active.Set(true)
			u.phase.Set("正在检查更新…")
			u.pct.Set(0)
			u.apkPath.Set("")
			u.done = false
			u.openedAt = time.Now()
			a.UpdateErr.Set("")
			go a.runUpdate(cx, u)
		})
	})
}

// closeDialog 关闭弹窗，保证自打开起至少 minDialogVisibleMs 可见：
// 提前完成时等满剩余时间再收（done 标记避免重复延时）。
func closeDialog(cx *core.Scope, u *updateState, close func()) {
	if u.done {
		close()
		return
	}
	u.done = true
	elapsed := time.Since(u.openedAt)
	wait := time.Duration(minDialogVisibleMs)*time.Millisecond - elapsed
	go func() {
		if wait > 0 {
			time.Sleep(wait)
		}
		cx.Post(close)
	}()
}

// showResult 在弹窗内显示最终结果文案（不再转 spinner），到最短保留
// 时间后关闭。
func showResult(cx *core.Scope, u *updateState, msg string, err bool) {
	cx.Post(func() {
		u.phase.Set(msg)
		u.hasErr.Set(err)
		closeDialog(cx, u, func() { u.active.Set(false) })
	})
}

// runUpdate 检查 + 下载 + 调安装器（后台 goroutine，UI 更新经 cx.Post）。
func (a *App) runUpdate(cx *core.Scope, u *updateState) {
	app := core.App()
	r, err := fetchLatestReleaseFull()
	if err != nil {
		showResult(cx, u, "检查失败："+err.Error(), true)
		return
	}
	remoteTS := releaseBuildOf(r.Name)
	if app.Build != "" && remoteTS != "" && app.Build >= remoteTS {
		showResult(cx, u, "已是最新版本", false)
		return
	}
	apkURL := latestAPK(r)
	if apkURL == "" {
		showResult(cx, u, "最新版没有 Android 安装包", true)
		return
	}
	ClientLog("[update] 发现新版本 %s（build %s > 本地 %s）", r.Name, remoteTS, app.Build)
	cx.Post(func() { u.phase.Set("正在下载新版本…") })

	dest := filepath.Join(cx.DataDir(), "update.apk")
	err = downloadWithProgress(apkURL, dest, func(pct int) {
		cx.Post(func() { u.pct.Set(pct) })
	})
	cx.Post(func() {
		if err != nil {
			showResult(cx, u, "下载失败："+err.Error(), true)
			return
		}
		u.phase.Set("下载完成，正在打开安装…")
		u.apkPath.Set(dest)
		mobile.InstallPackage(cx, dest, func(started bool, ierr error) {
			cx.Post(func() {
				u.active.Set(false)
				if ierr != nil {
					ClientLog("[update] 安装器启动失败: %v", ierr)
					a.UpdateErr.Set("无法启动安装：" + ierr.Error())
				} else {
					ClientLog("[update] 安装器已启动")
				}
			})
		})
	})
}

// updateDialog 下载进度弹窗（u.active 时由 versionCard 渲染）。
// Animation 用 ModalFade：默认的 ModalSlide 从底部滑入，检查在弹窗
// 展开动画期间就跑完了，短暂新版弹窗一闪而过看不出过程。
func updateDialog(cx *core.Scope, u *updateState) core.Node {
	return core.Modal{
		NoCloseOnBackdropPress: true,
		NoCloseOnEscape:        true,
		Animation:              core.ModalNone,
		StyleFunc: func() core.Style {
			p := cx.Palette()
			return core.Style{
				Position: core.Absolute, Left: core.Pct(15), Right: core.Pct(15),
				Top: core.Pct(35), Padding: core.All(20), CornerRadius: 12,
				Background: p.Surface, Gap: 12,
				AlignItems: core.AlignCenter,
			}
		},
		Children: []core.Node{
			phaseRow(cx, u),
			// 结果已出（成功/失败）只显示文案；下载中才显示进度条与
			// 百分比。
			core.Dynamic(func(*core.Scope) core.Node {
				if u.done {
					return core.View{}
				}
				return core.View{Style: core.Style{Gap: 8}, Children: []core.Node{
					progressTrack(cx, u),
					pctText(cx, u),
				}}
			}),
		},
	}
}

// phaseRow 阶段文案行：进行中 spinner+中性色；结果已出换图标
// （成功 ✓ 绿 / 失败 ✗ 红）+ 对应色。
func phaseRow(cx *core.Scope, u *updateState) core.Node {
	return core.Dynamic(func(*core.Scope) core.Node {
		color := cx.Palette().Text
		var icon core.Node
		switch {
		case u.done && u.hasErr.Get():
			color = cx.Palette().Danger
			icon = ui.Icon(lucide.CircleX, 18, func() core.Color { return cx.Palette().Danger })
		case u.done:
			color = core.RGB(0x2e9e5b)
			icon = ui.Icon(lucide.CircleCheck, 18, func() core.Color { return core.RGB(0x2e9e5b) })
		default:
			icon = ui.Spinner{}
		}
		return core.View{
			Style:    core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8},
			Children: []core.Node{
				icon,
				core.Text{ContentFunc: func() string { return u.phase.Get() }, Style: core.Style{FontSize: 14, Color: color}},
			},
		}
	})
}

// progressTrack 进度条（轨道 + 主色填充，宽度随 pct）。
func progressTrack(cx *core.Scope, u *updateState) core.Node {
	return core.View{
		Style: core.Style{Width: core.Pct(100), Height: core.Pt(6), CornerRadius: 3,
			Background: cx.Palette().SurfaceHover, Overflow: core.Hidden},
		Children: []core.Node{core.Dynamic(func(*core.Scope) core.Node {
			return core.View{
				Style: core.Style{
					Height: core.Pt(6), CornerRadius: 3,
					Background: theme.AccentBili,
					Width:      core.Pct(float32(u.pct.Get())),
				},
			}
		})},
	}
}

// pctText 百分比文案。
func pctText(cx *core.Scope, u *updateState) core.Node {
	return core.Dynamic(func(*core.Scope) core.Node {
		return core.Text{
			Content: itoa(int64(u.pct.Get())) + "%",
			Style:   core.Style{FontSize: 12, Color: cx.Palette().TextMuted},
		}
	})
}
