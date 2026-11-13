# Kanju Client

基于 [NSGo](https://github.com/nsgo-dev/nsgo)（Go 后端 + 各平台原生 UI）重写的 Kanju 视频聚合客户端。macOS 先行，同一套 Go 代码后续可直接构建 iOS / Android / Windows / Linux / Web 版本。

## 结构

```
kanju-client/
├── main.go        # nsgo.Run 入口
├── nsgo.yaml      # 应用配置（名称/ID/构建目标）
├── api/           # kanju 后端 HTTP 客户端（含 SSE 流式搜索）
├── app/           # 页面与全局状态（登录/首页/搜索/详情/播放器/历史/收藏/直播/设置/站源）
└── theme/         # 通用样式与小组件
```

## 开发

```sh
go tool nsgo dev      # 热重载运行 macOS 版
go tool nsgo build .  # 构建 .app / .dmg → dist/
```

后端地址默认 `http://127.0.0.1:28088`，可在登录页或设置页修改。

## 多端构建

`nsgo build` 的 target 与宿主要求见 `go tool nsgo doctor`：`macos_arm64` / `windows_x64` / `linux_x64` / `ios` / `android_arm64` / `web` 等，均由本仓库同一份代码产出。

## 与 Web 版的差异

- 认证走 `Authorization: Bearer`（token 从登录响应的 `kanju_token` Cookie 解析），持久化在应用 Storage。
- 搜索用 `/api/search/stream` SSE，逐站源追加结果。
- m3u8 播放地址改写为 `/api/proxy/m/<scheme>/<host>/<path>` 路径式代理，分片引用自动承接；mp4 等直连。
- 封面一律经 `/api/proxy/image` 代理加载。
