# 网络多页面订阅阻塞修复（2026-11-15）

## 问题与方案

网络页原先为连通性和测速分别维持 SSE。三个同时可见页面在同一 Edge 会话中占用六条普通 HTTP 连接，使管理请求排队；仅合并为每页一条 SSE 仍会在更多窗口时再次触及限制。

首页上游摘要和网络检测改用页级 `diagnostic-stream`：连通性、测速共用一条 WebSocket，统一管理订阅者、可见性、重连及每类快照版本；操作意图、错误和历史结果保留原有视图语义。服务端直接复用 `networkDiagnostics` 的快照／变更通道，无轮询或额外检测任务。复用现有 `coder/websocket v1.8.12`，不新增依赖；生命周期通知的存活统计与隐藏页退出通知保持独立，Mihomo 的连接 adapter 也保留自身业务边界。现行规则见[上游检测与测速](../upstream-channels.md#上游检测与测速)。

## 真实 Console 与 Edge 对照

`TestRealtimeNetworkBrowsers` 通过隔离 Console、实际内嵌前端和 Edge `157.0.4341.60` 运行，不拦截 UI 管理接口；检测的上游传输受控，不访问公网。

| 场景 | 结果 |
| --- | --- |
| 修复前，三个可见网络页 | 普通请求约 1,816.1 ms 后超时；关闭一页后约 3.0 ms 完成，服务端计数为 1，表明此前超时请求未到达 |
| 修复后，三个／四个／五个／六个可见网络页 | 普通请求分别约 1.4／1.2／1.3／1.4 ms 完成 |
| 六个页面的服务端订阅 | 6 条网络 WebSocket、0 条旧诊断 SSE |
| 跨页开始与取消 | 一个页面开始检测，六页显示运行；另一页面取消，六页恢复可操作 |
| 关闭全部页面 | 网络订阅降为零；无页面异常 |

首次修复前复现、初版通过和最终产物结果分别保留在忽略目录 `artifacts/realtime-before/result.json`、`artifacts/realtime-after/result.json`、`artifacts/realtime-final/result.json`。失败证据不覆盖；上述时间仅说明本机这批请求是否排队，不作为性能基准。

可重复命令（PowerShell，证据目录应为绝对路径）：

```powershell
$env:SWD_REALTIME_BROWSER_EVIDENCE = Join-Path (Get-Location) 'artifacts/realtime-check'
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestRealtimeNetworkBrowsers$' -count=1 -v
```

## 回归范围

- Edge 内存夹具 **69 项通过**：`network-diagnostics`、`home-upstream`、`lifecycle`、`components`、`dashboard`、`recovery` 共 67 项，加新增 `diagnostic-stream` 2 项。包括每页一条连接、切页／隐藏／pagehide／退出释放、pageshow 恢复、断线重取两类快照及重置版本基线、旧事件拒绝、独立开始／取消、取消回执丢失和已有结果保留。
- Go 定向回归通过：`TestNetworkEvents*`、`TestDiagnosticEvents*`、`TestNetworkDiagnostics*`、`TestLifecycle*`。新 WebSocket 检查同源／同站点／条款准入、订阅不启动检测、重连初始快照、数据消息不能执行管理操作，以及关闭释放。
- `pnpm --dir webui check`、Console `go vet`、`gofmt`、`git diff --check` 通过。内嵌产物已重建，`app.js` 为 1,321,043 字节，LF SHA256 为 `621ebaee22a02c5a78cf8cf5df4396387c11c25b0a17fcc9caf3bd5ae5dcdb76`，构建清单与精确产物登记一致；当前产物分类无错误。历史产物检查无错误，保留既有大文本体积提醒。

首次沙箱内 Go 在创建隔离条款文件时因 Windows rename 句柄失败；Edge 夹具也在页面断言前退出，已停止该轮。改在普通权限下运行同一隔离命令后取得上述红／绿对照和回归结果；未为绕过环境问题修改产品代码。

## 边界

真实 Console 检查使用六个 `document.hidden=false` 的无头页面模拟多窗口，未把当前安装的桌面实例作为生产完整验收，也未覆盖所有浏览器、第三方代理面板的并发连接或任意数量窗口。首页块需求和历史事件仍保留各自已有 SSE，本次修复范围是网络诊断的双 SSE。未运行完整发布预检、完整 UI 套件或 race；不外推游戏播放体验。本轮未启动游戏、网关或修改 hosts，没有需要 VRC 判断的新增剩余项。以上验证在提交前的工作区完成；本轮不推送。
