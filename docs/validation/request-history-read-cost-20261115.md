# 请求历史读取成本验证（2026-11-15）

> 适用范围：本文保留先前计数分离的实现与证据。当前精确计数及非时间排序已移除，统计改为[有限近期读取](history-bounded-reads-20261115.md)；不将本记录的旧接口作为当前契约。

现行接口与界面规则见[历史查询范围](../webui-design.md#历史查询范围)及[请求监控](../console.md#最近请求监控)。本记录只覆盖本轮 Windows 合成数据、隔离 SQLite／Console 和 Edge，不代表真实用户数据库或游戏性能。

## 问题与边界

原 `ReadRequestHistory` 在同一事务里先对筛选后的全部 GET 执行 `count(*)`，再读一页。默认相邻中断过滤还要为候选中断查找下一条 GET；每次翻页和 SSE 刷新都重新计数。计数耗尽内部 2 秒预算时，列表也返回失败。

本轮在修复前运行：

```powershell
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run '^$' -bench '^BenchmarkRequestHistoryRetained$' -benchtime=1x -count=1
```

合成数据为同一资源、按收到时间递增、每秒一条 GET，`canceled`／成功 `completed` 交替，相邻中断过滤隐藏一半；每页 50 条。建库与写入不计入查询计时。修复前 1 万条的一页为 11.45 ms，100 万条为 1.024 s，300 万条返回 0 行并报 `context deadline exceeded`。另一个不带大库准备的合同回归 `TestHistoryPageDoesNotCountAllRequests` 在旧实现因页面仍返回全量 `total` 而失败。

修复后，在其他回归结束后独立运行同一基准，参数改为 `-benchtime=3x -count=1 -v`，每项查询执行三次，记录每次平均耗时：

| 保留 GET 数量 | 第一页 | 下一页（真实游标） | 独立精确计数 | 原强制索引计数 SQL |
| --- | --- | --- | --- | --- |
| 10,000 | 2.75 ms | 2.60 ms | 8.93 ms | 10.17 ms |
| 1,000,000 | 3.08 ms | 2.80 ms | 739.84 ms | 842.59 ms |
| 3,000,000 | 4.33 ms | 2.97 ms | 2,257.65 ms | 2,583.38 ms |

两种计数均每次打开、关闭只读连接，采用相同筛选 SQL，强制索引项只用于比较原计划，不受旧列表的 2 秒预算限制。查询计划为 `SCAN request_events`，下一条 GET 的关联仍为 `SEARCH next USING INDEX request_events_time (requested_at>? AND requested_at<?)`。完整输出位于忽略目录 `artifacts/history-read-cost/retained-benchmark-final.log`。早期与回归并行的单次探测曾测得 1 万条页面约 173 ms，不用于性能结论；另一次三次复测保存在 `retained-benchmark.log`，其强制索引项复用了写连接，最终已统一只读连接条件，以本表为准。计数仍为线性读取，本次收益主要来自将它移出列表路径及同筛选复用。

## 实施

- 列表只读取过滤后的 `limit+1` 条，用真实 `hasMore`／`nextCursor` 继续翻页；保持范围边界、相邻规则、排序和歌曲身份不变。
- 独立 `/api/history/count` 用同一过滤条件返回精确数量与统计开始时间。取消对外层计数强制使用时间索引；全范围可顺序扫描表，带时间范围时由 SQLite 选择索引，关联下一条 GET 仍使用时间索引。计数独立限时 30 秒，列表维持 2 秒。
- 前端复用现有资源读取机制，按数据库和已应用筛选保留一份统计结果；同筛选的排序、翻页和实时列表更新不重新计数。显式查询／筛选变化及手动刷新才重新统计，重新进入历史页也重新读取。没有增加数据库字段、汇总表、后台统计任务或跨筛选缓存。
- 总数显示「统计时共 N 条」，提示统计时间；分页只显示当前页码，不用旧统计推算实时总页数。统计慢或失败不影响列表；同筛选更新失败保留旧统计并说明，筛选变化丢弃旧结果及迟到响应。

## 自动与浏览器检查

```powershell
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy ./internal/console -count=1
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs vet -tags=no_easytier ./internal/cacheproxy ./internal/console
./scripts/with-test-temp.ps1 pnpm --dir webui test e2e/request-timeline.spec.ts e2e/history-refresh.spec.ts --workers=2
./scripts/with-test-temp.ps1 pnpm --dir webui test e2e/data-tables.spec.ts e2e/recovery.spec.ts e2e/console.spec.ts e2e/history-refresh.spec.ts --workers=2
pnpm --dir webui check
```

两个 Go 包整包通过（约 203／194 秒），vet、类型检查与嵌入产物重建一致性通过。Go 回归覆盖过滤数量、空库、时间含／不含边界、跨筛选边界的相邻关系、全部排序及游标、只读与鉴权、不启动引擎、取消统计后仍能读新页。

第一轮 Edge 23 项通过；追加一轮 22 项中 21 项通过，历史排序用例因仍断言旧「第 2/2 页 · 共 54 条」失败，改为当前页码后定向补跑通过。覆盖慢统计／失败与分页独立、筛选变化和迟到统计、实时事件不重算、手动刷新、键盘焦点、后台刷新、断线恢复、320／390／1280px 及深浅／高对比度布局。新增的总数刷新按钮也通过加载旋转、重复操作抑制、焦点和 reduced-motion 检查。

真实联调：设置 `SWD_HISTORY_BROWSER_EVIDENCE=artifacts/history-read-cost/browser` 的绝对路径，再运行 `TestRequestHistoryBrowser`。通过 601 条真实受控请求、关闭重开 SQLite、13 页浏览、排序、新请求 SSE、手动计数更新和相邻中断过滤；断言翻页／排序／实时更新不发起额外计数。最终 606 条持久化记录、无页面运行错误。桌面和 390px 暗色截图已人工查看。首次联调被驱动中已失效的来源标题 Tooltip 断言阻断；同时同步了驱动中旧 HTTP 提示位置断言，产品提示保持原样，随后完整联调通过。结果及截图保存在忽略目录 `artifacts/history-read-cost/browser/`。

## 环境限制和剩余成本

最初沙箱内的三个 Console 定向测试报原子重命名 `The handle is invalid`，pnpm 报创建项目符号链接 `EPERM`；按[开发约定](../development.md#开发命令)在沙箱外普通用户权限下复验通过，仍使用临时目录入口，没有修改系统配置、hosts 或启动游戏。首次尝试的 Python 启动别名不可用，文本调整改用已安装 Node.js 完成。

精确计数仍与筛选范围大小有关；本轮复用是页面内同筛选的统计快照，不承诺跨标签页或不同筛选共享计数。性能基准覆盖默认时间降序及相邻中断过滤；没有将这些结果外推至歌曲／资源全范围排序、极密集 HEAD 或其他硬件。本次没有新增必须依赖 VRC 的剩余验收项。

## 计数与业务写入隔离补充修复

本节保留提交 `9b8034f1` 的阶段记录；下述仅隔离计数的实现及复跑命令已由[完整历史长读隔离与批次恢复](request-history-write-isolation-20261115.md)扩展，当前实现与验证以该记录及存储契约为准。

上表中的独立计数仍在业务库的只读连接上执行；页面内复用结果没有隔离 SQLite 长读锁。300 万条约 2.26 秒的扫描仍可能超过默认 1 秒写锁等待，令收藏或请求批次提交失败。已有事务清理只保证失败连接不污染后续写入，不能挽回 `usage.run` 已丢弃的批次。

新增 `TestHistoryCountAllowsPersistentBusinessWrites` 直接调用真实计数入口，在该次只读连接上用 TEMP view／SQLite 标量函数暂停扫描，保留原业务库结构与真实 SQLite 锁。写连接设 `busy_timeout=0`，将超时竞争缩成确定性断言；请求记录和热度均经过实际 `startGET`／`record`／`flush` 工作线程。修复前运行该测试，收藏报 `SQLITE_BUSY`，worker 的 3 项批次失败；同会话与关闭重开后，收藏、请求明细及歌曲需求均缺失，1.23 秒稳定复现。

计数现与诊断导出共用 `temporaryStorageSnapshot`：在线备份完成并关闭源连接后，才在临时副本上扫描。复制与扫描共用原 30 秒预算，`storageID` 仍属于源库，`countedAt` 在复制开始前记录；不改筛选、计数 SQL、生产锁等待或业务库日志模式。临时空间和清理以[存储契约](../storage.md)为准。

本次验证：

- 最终并发回归连续三次通过（合计 1.353 秒），分别覆盖正常计数和受控 SQL 扫描失败。扫描保持暂停时，收藏、请求明细和 `demand_count=1`／`demand_score=1` 均提交成功，并在扫描结束前关闭重开确认持久化；正常计数仍只返回原快照的 1 条 GET，两个分支均清理整个临时副本目录。
- 历史筛选、分页、排序、原身份、只读／取消、WAL 快照、导出与事务清理的 cacheproxy 定向回归通过（37.034 秒）；最终并发回归及相同存储／导出路径的 Windows race 通过（36.024 秒）。Console 历史 API 与事件回归首次被沙箱的 `The handle is invalid` 原子重命名错误阻断，沙箱外沿用隔离目录入口复验通过（1.145 秒）。两包 vet 通过；本次没有界面修改，未重复浏览器验收。
- 独立复跑 `BenchmarkRequestHistoryRetained/3000000/count`，一次完整计数（含在线复制和扫描）约 **3.561 秒**，数量为 1,500,000。此单次合成样本只确认大库路径可完成，不与上表三次均值作严格性能对照，也不证明任意磁盘条件下无锁等待；并发写入由上述受控真实 SQLite 回归判断。
- 探索时单行 UDF 内立即取消再释放的探针曾因驱动中断与返回竞争收到成功结果，改用确定的 SQL 错误分支检查失败清理；前置取消继续由已有检查覆盖。该探针输出与原始失败均保留，没有扩大产品取消语义。

复跑入口：

```powershell
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run '^TestHistoryCountAllowsPersistentBusinessWrites$' -count=3 -v
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run '^$' -bench '^BenchmarkRequestHistoryRetained$/^3000000$/^count$' -benchtime=1x -count=1 -v
```

原始输出位于忽略目录 `artifacts/history-count-writes/`（`before.log`、`after-final.log`、`regression.log`、`console-outside-sandbox.log`、`race.log`、`retained-3m.log` 和探索日志）。本次不新增 VRC 待验项，未推送。
