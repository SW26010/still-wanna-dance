# 批量失败结果的观测成本验证（2026-11-15）

本轮处理失败越多时进度复制、公共状态传输和隐藏页面表格持续增长的问题。现行接口见[下载结果边界](../download-resource-design.md#5-容量接入准备回补与保留取舍)，展示规则见[WebUI 设计](../webui-design.md)，完整快照契约见[存储约定](../storage.md)。本报告只证明本轮实现及下述隔离环境。

## 复现与对照

`TestDownloadFillProgressDeliversFailuresOnce` 通过真实 `DownloadFill.Run` 处理三个失败歌曲（其中两个共用资源）。修复前回调累计交付 10 条失败，最终结果只有 3 条，断言失败；修复后每条只交付一次，修改保存的进度切片不会改变最终结果。

`TestDownloadFillFailureVolume` 通过相同入口处理 5,000 个独立资源，使用固定失败适配器；清单准备完成后才记录 `runtime.MemStats.TotalAlloc` 差值。为量化同一路径，在新接口中临时恢复原累计列表复制这一处行为运行一次，再恢复增量实现：

| 进度行为 | 交付的失败条目总数 | 累计内存分配 |
| --- | ---: | ---: |
| 累计列表复制 | 25,005,000 | 810.36 MiB |
| 仅本次新增 | 5,000 | 14.09 MiB |

这是累计分配而非峰值内存，包含 `Run` 的分组、数据库读取及任务分派；不代表真实公网下载耗时。累计复制版本因条目重复断言失败，增量版本通过。临时对照改动已移除。

复跑入口：

```powershell
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run '^TestDownloadFill(ProgressDeliversFailuresOnce|FailureVolume)$' -count=1 -v
```

`TestBatchStatusOmitsFailureDetails` 将当前与历史结果都注入 10,575 条失败，专门检查两个状态出口。修复前状态 1,358,002 字节、两个列表均被传输；修复后 5,454 字节、两个列表均省略，内存完整记录仍保留。此双列表压力夹具不等于普通成功历史结果的体积。

## 完整记录与行为

- `TestBatchFailurePagesPreserveCompleteSnapshot` 从缺少任务标识的版本 3 旧快照恢复 125 条完整失败；遍历三页，逐项验证顺序、内容及无遗漏、无重复。覆盖尾页、越界偏移、非法查询、旧任务、Token／Origin 拒绝，以及原有重新执行入口替换旧结果。
- `TestMD5BatchTriesSharedSongIDs` 的全失败场景继续验证完整结算与快照；随后恢复一个可用候选，重新执行同一任务入口，共用资源只下载一次，失败归零。已有预算、取消、屏蔽与候选重试回归继续使用真实引擎。
- `batch-results.spec.ts` 验证 10,575 条记录每页最多 50 行、翻页、离开曲库卸载及返回；另受控延迟旧任务第二页，确认迟到响应不能覆盖新任务。

## 真实 Console 与 Edge

`TestBatchResultsBrowser` 将 10,575 条完整失败写入任务快照，重启隔离 Console，再用实际嵌入产物及受鉴权的真实接口打开 Edge。结果：

| 项目 | 实际结果 |
| --- | --- |
| 浏览器 | Edge 157.0.4341.60 |
| 公共状态响应 | 5,149 字节，失败计数 10,575，无失败明细字段 |
| 曲库当前页 | 50 行，可读取第二页 |
| 切回首页 | 失败表格及其行均为 0 |
| 返回曲库 | 重新读取第一页 50 行 |
| 390px 视口 | 无横向溢出，分页与表格截图已检查 |
| 页面脚本错误 | 0 |

本机证据在忽略目录 `artifacts/batch-results/`：`result.json`、`failures-page-2.png`、`failures-mobile.png`。复跑使用[开发说明](../development.md)中的 `SWD_BATCH_BROWSER_EVIDENCE` 入口。结果由受控失败快照提供；未访问真实上游、启动视频服务、网关或游戏，也未修改系统 hosts。原有下载行为由前述引擎及 Console 回归验证。

## 检查及环境边界

Go 1.27.3／Node 24.22.1 下，`internal/cacheproxy`、`internal/console` 两包完整常规测试通过（分别 324.958 秒、284.653 秒），两包 `go vet` 通过；按需真实浏览器检查另已显式运行并通过。受影响的 `batch-results.spec.ts`、`library.spec.ts`、`task-errors.spec.ts` 共 59 项 Edge 用例通过；WebUI 类型检查、嵌入产物重建一致性、生成大文本登记、gofmt 及 `git diff --check` 通过。本轮未运行 race。

首次沙箱 Console 检查在条款夹具准备阶段报 `renameat ...: The handle is invalid`，属于仓库已记录的 Windows 沙箱文件句柄限制；普通用户权限下同一检查复现了状态明细问题，修复后通过。首次沙箱浏览器运行在启动阶段失败且清理未结束，已中断；后续普通用户权限的浏览器回归与真实联调通过。没有修改产品文件操作或测试断言绕开环境问题。

本轮不涉及 VRC 专属行为，无新增实机台账。完整失败记录仍占线性内存并在任务结束时一次性保存，未引入执行恢复或整个调度系统改造。
