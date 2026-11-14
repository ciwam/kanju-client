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
