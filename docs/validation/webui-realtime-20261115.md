# 历史与块视图多窗口订阅修复（2026-11-15）

## 问题与实现

首次[网络诊断修复](network-realtime-20261115.md)只替换了网络双 SSE，历史和块视图仍占用普通 HTTP/1 连接。真实 Console／Edge 中，六个可见历史页或块视图会使普通请求约 1.8 秒超时；关闭一页立即恢复。探针的服务端计数表明超时请求未到达 Console，符合浏览器连接池耗尽，而非历史查询或管理处理函数执行缓慢。

历史、块概览和当前块详情改用原路径上的 WebSocket，与网络诊断共用 `webui/src/realtime-stream.ts`。模块负责连接、1 秒断线重连、可见性、pagehide／pageshow、取消重试、拒绝已释放连接的迟到消息，以及非法／终止事件停止；业务 module 只处理自己的快照、增量、版本和界面状态。网络两类诊断继续共享一条连接；块概览和详情各自订阅，没有引入跨窗口协调、协议多路复用或额外快照缓存。

历史 resource 只维护查询取消和通知合并，删除它原有的重复页面订阅生命周期。新增检查曾发现 pagehide／pageshow 会让已终止的历史订阅重新创建；集中规则后，该检查与原隐藏恢复、查询重试检查均通过。

服务端共用 `realtime_events.go` 的只读 WebSocket、读取消、5 秒写入截止和连接释放；复用现有 `coder/websocket`。原 SSE 及事件内容继续供既有观测工具使用，WebUI 不使用 SSE 回退。订阅不启动网络检测、下载引擎或网关；当前规则统一见[实时订阅](../webui-design.md#实时订阅)。

## 真实 Console／Edge 红绿对照

使用隔离存储、真实内嵌资源及 Edge `157.0.4341.60`。同一浏览器会话逐步打开六个 `document.hidden=false` 页面，普通请求通过页面 fetch 发出；连接计数通过独立测试请求读取，不拦截管理接口。网络检测的上游受控，不访问公网。

| 场景 | 修复前 | 最终产物 |
| --- | --- | --- |
| 六个可见历史页 | 约 1,804.1 ms 超时；关闭一页约 3.5 ms 恢复 | 约 3.1 ms；6 WebSocket、0 SSE |
| 六个可见块视图 | 约 1,812.4 ms 超时；关闭一页约 3.3 ms 恢复 | 约 2.3 ms；12 WebSocket、0 SSE（每页块概览＋上游摘要） |
| 六个可见网络页 | 首次失败见前序记录 | 约 5.6 ms；6 WebSocket、0 SSE；跨页开始／取消同步 |
| 关闭全部页面 | 关闭一页即可解除阻塞 | 三类场景的实时连接均归零，无 pageerror |

最终 3／4／5 页也均完成普通请求。时间只用于判断是否排队，不作为性能基准；最终检查与其他浏览器回归同时运行。历史和块的红色证据保留在忽略目录 `artifacts/realtime-subscriptions-repro/{history,blocks}/result.json`；初版绿色结果位于 `artifacts/realtime-subscriptions-after/`，最终结果位于 `artifacts/realtime-subscriptions-final/{network,history,blocks}/result.json`，未覆盖原始失败。

可重复命令（PowerShell）：

```powershell
$env:SWD_REALTIME_BROWSER_EVIDENCE = Join-Path (Get-Location) 'artifacts/realtime-subscriptions-check'
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestRealtime(Network|HistoryAndBlocks)Browsers$' -count=1 -v
```

## 关联验证

- Edge 内存夹具 **128 项通过**：历史、块视图、共用实时生命周期、诊断共享连接、网络诊断、首页上游、应用退出、组件、面板与恢复页。包括重连取得新快照、旧版本拒绝、读取期间合并通知、翻页忽略迟到响应、隐藏／切页／pagehide 释放、重连等待取消、非法／终止事件停止及重新查询。分两组执行，最终分别为 77／51 项。
- Go 定向回归通过：`TestRealtimeHistoryAndBlockSockets`、`TestNetworkEvents*`、`TestDiagnosticEvents*`、`TestNetworkDiagnostics*`、`TestLifecycle*`、`TestBlockEvents*`、`TestBlockEngineWaiter*`、`TestRequestHistory*`。覆盖同源／同站点／条款准入、只读连接拒绝数据命令、引擎创建前等待、同连接继续快照、重连及关闭释放；原 SSE 检查继续通过。
- 真实 `TestBlockTimelineBrowser` 通过：实际 Range 下载、块来源详情、离屏零订阅、固定详情恢复、3,145,765 字节实际缓存一致性、速度归零、无订阅时物理回收。证据在 `artifacts/realtime-block-timeline/`；该轮驱动的结果文字仍使用旧 SSE 标签，实际执行的是本轮 WebSocket 产物，驱动文字随后已同步。
- 真实 `TestRequestHistoryBrowser` 通过：SQLite 重开后 606 条历史记录、13 页读取、事件插入自动更新、总数快照复用、相邻中断过滤和终止结果；证据在 `artifacts/realtime-history/results.json` 及同目录截图。
- `pnpm --dir webui build/check`、Console `go vet`、`gofmt`、`git diff --check` 与产物策略检查通过。内嵌 `app.js` 为 1,321,120 字节，LF SHA256 为 `b4476c5a30f098f2fb2b329ac69250bdc914f62d4db24f4762d76795b9e42e04`，构建清单及精确产物登记已更新；策略检查没有错误，保留既有大文本提醒。

首次沙箱内 Go 在创建隔离条款文件时遇到 Windows rename 无效句柄，尚未进入业务断言；以普通本机权限运行相同隔离入口后取得上述红绿对照。未因该环境失败修改产品文件操作或放宽断言。

## 验证边界

六页采用可见无头页面模拟多窗口；未声称任意窗口数、其他浏览器或第三方代理面板的全部并发行为均已验证。未运行完整 UI 套件、完整发布预检或 race；真实下载和历史检查使用受控回环服务。本轮未启动游戏、修改系统 hosts 或读写用户资料，没有新增必须 VRC 判断的剩余项。以上为未提交工作区验证，未 commit／push。
