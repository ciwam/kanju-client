# NSGo 框架缺陷记录

来自 kanju-client（macOS 客户端）开发过程中实锤的框架 bug。
快照版本：`github.com/nsgo-dev/nsgo v0.0.0-20261113150221-03c2543204c5`
（20261114 快照 `v0.0.0-20261114034347-ab015417506a` 同样存在问题）。
记录日期：2026-11-14。建议逐条向上游提 issue。

---

## 1. Dynamic 子树替换的提交偶发丢失（最严重，影响面最广）

**现象**：`core.Dynamic` 在信号变化时 dispose 旧子树、mount 新子树
（`tree.go: mountDynamic` 的 `b.Dispose(); t.clear(region)` 路径），
新的挂载**执行了**（fn 被调用、新节点创建），但**提交批次没有发出**
（`-trace` 显示切换后零 mount item），屏幕与 nsgotest 的视图层
停留在旧子树。

**最小复现**（headless nsgotest，复现率非 100%，约 30-70%）：

```go
func TestSwitch(t *testing.T) {
    flag := make(chan *core.Signal[string], 1)
    a := nsgotest.Start(t, func(cx *core.Scope) core.Node {
        s := core.NewSignal(cx, "a")
        select { case flag <- s: default: }
        return core.Dynamic(func(*core.Scope) core.Node {
            switch s.Get() {
            case "b":
                return core.View{Children: []core.Node{core.Text{Content: "BRANCH-B"}}}
            }
            return core.View{Children: []core.Node{core.Text{Content: "BRANCH-A"}}}
        })
    }, nsgotest.Size(400, 300))
    s := <-flag
    a.Settle()
    a.Post(func() { s.Set("b") })
    a.Settle()
    if len(a.Query("text:BRANCH-B")) == 0 {
        t.Fatal("切换 B 未渲染") // 实测必现失败
    }
}
```

**实际影响**：
- 同类 `core.Show` 切换同样失败（Then/Else 分支交换）。
- 首页面板切换（分类浏览 ↔ 搜索面板）实机截图验证可用，但
  headless 测试中第二次起的按压事件被静默丢弃——事件目标是
  nsgotest 视图层里「上一个状态」的节点，而框架树里该节点已
  `deleted`，`Tree.node(id)` 返回 nil，事件静默丢弃
  （`tree.go:918`：`if n := t.nodes[id]; n != nil && !n.deleted`）。
- 测试里 `a.Post` + `Signal.Set`、`OnPress` 里的 `Signal.Set`、
  `DisplayNone` 样式切换均有此问题；重试等待无效（不是时序，
  是提交批次根本没生成）。

**建议排查方向**：`mountDynamic` 的 renderEffect 在 flush 循环中
重跑时，`t.clear(region)` 后的 `t.mount` 是否正确地把变更写进了
`commitRound` 的标记链（`mark`/`markResolve`/`childrenChanged`）；
或 `t.changed` 标志在 region 替换路径上被漏置。

**我们的绕过**：页面层平铺分发（把所有切换上提到 shell 的单个
Dynamic，不让 Dynamic 嵌套 Dynamic 做子树交换仍不可靠）；对关键
路径放弃 Dynamic 交换，测试只断言首个面板。

---

## 2. List splice 大批量替换丢变更（已知，已在用 catKey 绕过）

**现象**：分页数据追加后 `items.Set(merged)`，List 的 splice 更新
在批量替换（几十条以上）时**丢变更**：行不更新、显示旧数据，或
触发下一条 panic。

**实测场景**：首页海报网格 30 条/页追加，items 全量 Set；滚动探针
日志确认 Row 复用 cell 只更新 `pos.index`，网格显示旧内容。

**我们的绕过**（`app/home.go` posterGrid）：切换分类时不 splice，
改用外层 Dynamic track 一个 `catKey` 信号，key 变化时**整表重建**
List。分页追加仍走 splice（小批量时正常）。重建会回顶滚动位置，
需要 `ScrollToIndexAt` 延迟补偿。

**注意**：`NumColumns` 同样只在挂载时固化（`list.go:340`
`st.columns = max(l.NumColumns, 1)`），列数自适应也必须走整表
重建。若上游把 NumColumns 变成响应式或修复 splice，两处绕过都可
回收。

---

## 3. checkCommit panic："a commit missed changes it should have sent"

**现象**：headless 测试中偶发 panic：

```
panic: core: a commit missed changes it should have sent:
 kind 4 of view 42 (…, sent …); kind 1 of view 43;…
```

发生在 `core/commit.go:153` 的 `checkCommit()`——提交后自校验
发现还有变更未发。触发场景与 #2 同源：List 批量 splice 后的下一帧。

**推测**：splice 标记链（`mark`/`relayout`）对跨多行的批量替换有
漏标；自校验抓住了它。修复 #2 应同时消除本 panic。

---

## 4. Transition 未声明 Properties 时默认含 Border 动画 → cgo SIGTRAP 崩溃

**现象**：给 View 设置 `Transition{...}`（如 chips 的按压缩放）但
**不声明 `Properties`** 字段时，过渡默认包含 Border 属性动画；
macOS 平台层的 `apple/border.go:167` 在处理 Border 动画时 cgo
崩溃（SIGTRAP during cgo execution），整个应用闪退。

**实测**：stderr 捕获实锤：

```
SIGTRAP during cgo execution
github.com/nsgo-dev/nsgo/internal/platform/apple.animate border.go:167
```

**我们的绕过**：移除了两处未声明 Properties 的 Transition；后续
约定：**任何 Transition 必须显式声明 Properties 且不含 Border**。

**建议**：Border 动画路径加防御（或 Transition 默认 Properties
不含 Border）；至少在文档中标注。

---

## 5. headless 测试驱动（nsgotest）事件目标的时效性问题（#1 的伴生）

**现象**：Dynamic 子树替换提交丢失时，nsgotest 视图层仍保留旧子树
的视图记录；测试对这些视图发 press，事件里的节点 ID 在框架树中已
被 `discard`（deleted）或被新节点复用，`t.node(id)` 返回 nil →
**按压静默无效，无任何错误提示**。

**建议**：`Tree.Dispatch` 对 `n == nil` 的事件至少记一条日志，
方便测试区分「按钮没反应」与「事件目标已销毁」。

---

## 6. headless 测试并非无窗口——测试运行时应用窗口反复闪现

**现象**：`go tool nsgo test -target macos .` 运行时，屏幕上会
快速打开/关闭一个真实的 NSGo 测试窗口（每个测试用例一次，8 个
用例闪 8 次），抢占注意力并短暂置前。

**根因**（`internal/platform/macos/testhost.go`）：
1. `host.Open` 调 `openWindow` 创建**真实 NSWindow**，并
   `orderFrontRegardless`（无视前台应用强制置前）+ `makeKeyWindow`；
2. `startApp(Options{Title: "NSGo test"})` 未传 `HideDock`，激活
   策略为 Regular——测试进程出现在 Dock，窗口为标准层级。

"headless" 只指**输入合成**（无真实鼠标键盘），窗口本身真实显示。

**建议**：测试宿主改用 `NSApplicationActivationPolicyAccessory`
（`HideDock: true` 即可复用现有机制），且窗口不 `orderFrontRegardless`
（仅 `makeKeyWindow` 或干脆 `orderBack:`）；或提供 `NSGO_TEST_OFFSCREEN`
之类的环境变量把窗口移到屏幕外（如 `setPosition` 到 -2000）。

**我们的缓解**：接受现状（仅视觉打扰，无功能影响）；跑测试期间
不去点屏幕即可。CI（无显示器/SSH）环境不受影响。

---

## 7. dispose 链路 unlink 索引越界 panic（偶发，测试阻塞）

**现象**：headless 测试偶发（估 10-30% 概率，多次连跑可复现）：

```
panic: runtime error: index out of range [2] with length 0

core.(*subscribers).remove(...)            core/reactive.go:683/789
core.(*computation).unlink(...)            core/reactive.go:520/576
core.(*Scope).disposeDetached(...)         core/reactive.go:262
core.(*Scope).disposeOwned(...)            core/reactive.go:243
core.(*Scope).Dispose(...)                 core/reactive.go:212
core.(*Tree).mountDynamic.func1()          core/tree.go:1841
core.(*computation).run.func1()
...runtime.flush / Tree.Dispatch
```

随后整个测试进程退出（runtime.fail），该用例 FAIL，后面用例仍跑。
应用层栈帧零参与——纯框架 dispose 路径。

**机制推测**（reactive.go）：`computation.unlink` 遍历自己的
`sources`，对每个 source 调 `readers().remove(s.at)`——`at` 是该
reader 在 source 订阅者列表里的槽位。`remove(i)` 把末位读者挪到
槽位 i 并更新它的 `sources[moved.at].at`。当**同一个 computation
的同一 source 出现在它 sources 列表里两次**（track 去重失效，或
unlink 在遍历中 readers 列表被同一批 dispose 的其它 computation
并发修改——dispose 是顺序的但共享 readers 槽位）时，先 remove
把末位挪进 i，第二次 remove 用过期的 `s.at` 越界或错删。
`index out of range with length 0` 表明该 source 的 readers 列表
已空，即 `s.at` 完全过期。

**触发场景**（我们的仓库）：首页挂载后立即按压（TestHomeViewPanels）
或快速页签切换（TestBackNavigation）——Dynamic 子树替换 dispose
旧 scope 时，其 owned computations 的 sources 指向正在被同一批次
dispose 的其它 scope 的信号。

**建议排查方向**：unlink 用 `s.at` 前先校验
`readers.list[s.at].c == c`（槽位被挪动后 at 过期），不符则线性
查找；或 readers 改为按 computation 的 map/Set。

**我们的绕过**：无（应用层无法干预 dispose 内部）。测试脚本层
重跑即过；正式构建用 `--no-test` 或重跑。已实测最新 main
（4a37cb4418e5）同样存在。

---

## 附：非缺陷但相关的框架限制

- `List.NumColumns` / `EstimatedSize` 挂载时固化，不支持响应式
  （见 #2）。
- `List` 的 `Row` 返回子树必须响应式（官方模式：cell 跨数据复用时
  bind 只更新 `pos.index` 信号），静态取值会让复用行永远显示旧内容
  —— 这是文档级约定，违反时症状与 #2 难以区分。
- `refusedSchemes` 拒绝 `file://` 的 OpenURL；打开日志目录等本地
  文件需求只能绕行 `osascript`/`open -R`。

## 环境信息

- nsgo：v0.0.0-20261113150221-03c2543204c5（及 20261114034347）
- 测试：`go tool nsgo test -target macos .`（headless nsgotest）
- 实机：macOS 15（Darwin 23.6.0），arm64
- 复现工程：`/Users/wangs/dev/code/kanju-client`（app_test.go、
  theme/grid_columns.go、app/home.go 中的绕过实现可作参考）

---

## 附：更新 nsgo 的坑——goproxy.cn 拉新快照失败

**现象**：`go get github.com/nsgo-dev/nsgo@<新 commit>` 时默认
GOPROXY（goproxy.cn）报：

```
read "https://goproxy.cn/github.com/nsgo-dev/nsgo/@v/v0.0.0-...zip":
stream error: stream ID 29; INTERNAL_ERROR; received from peer
```

nsgo 是伪版本频繁提交的仓库，goproxy.cn 对它的新快照偶发拉取失败
（2026-11-15 实锤两次：4a37cb44、c9e277de）。

**解法**：换官方 proxy 拉一次即可（成功后进本地模块缓存，此后
GOPROXY 无关）：

```bash
GOPROXY=https://proxy.golang.org,direct go get github.com/nsgo-dev/nsgo@<commit>
```
