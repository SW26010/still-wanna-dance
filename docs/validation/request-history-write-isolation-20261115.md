# 历史长读隔离与记录恢复（2026-11-15）

> 适用范围：本记录保留当时实现及历史失败。精确计数、全量来源／首正文统计、歌曲／资源排序和私有索引现已由[有限近期读取](history-bounded-reads-20261115.md)替代；现行规则以[存储契约](../storage.md)为准。导出隔离、事务及队列恢复的独立证据仍保留。

基线 `9b8034f1` 已隔离精确计数和诊断导出，但来源、首正文汇总与非时间排序仍直接读业务库。本轮补齐隔离、后台批次有界恢复及排序计划。现行规则以[存储契约](../storage.md)和[历史查询范围](../webui-design.md#历史查询范围)为准；全部资料来自隔离 SQLite、受控请求和合成历史，没有操作用户数据库、游戏或系统 hosts。

本文按阶段保留结果。末尾“大库持续刷新优化补充”替代前阶段的任意提交失效、无条件歌曲索引及首正文窗口排序实现；原失败和耗时仍为当时版本的证据。

## 复现与根因

用户提供的 `natural-sorted-read-writes-6m.log` 保留原 600 万条失败：歌曲／资源排序触发原 2 秒查询超时，同时 collector 的三项批次提交报 `SQLITE_BUSY`，关闭重开后新增请求和需求行均为 0。上述负载可复现漏写。

本轮先建立 `TestUsageRecoversLockedBatchAndPersistsAfterReopen`：保持生产 `busy_timeout=1000` 和单次写入 2 秒预算，独立 SQLite 事务持有真实读锁；只有日志确认实际锁冲突后才释放，不以固定 sleep 猜测失败点。修改前约 1.59 秒失败，关闭报告 BUSY，重开后请求明细为 0、需求行缺失。原 worker 在 `write(batch)` 失败后即释放该批次，后续事务即使恢复也不能补回它。

非时间排序原来强制走时间索引并对筛选范围排序，既扩大读取量，也将读锁延长到足以阻止写事务提交。仅正确清理失败事务或仅隔离计数不能解决这些剩余读路径。

## 实现与 interface

- Store 持有一个 `RequestHistory` module；Console 的历史页、计数、来源、首正文统计及诊断导出通过同一 `Store.History()` interface。模块统一处理读取隔离、复制合并、复用、取消、租约和查询计划。文件路径函数保留给独立离线读取，重复查询应使用 Store 持有的实例。
- 所有长扫描／排序／导出使用私有在线副本；时间分页仍直接读取有限窗口。备份每步 128 页，遇到 BUSY／LOCKED 时让业务提交先完成，再在原请求期限内重试。没有退回业务库长读，也没有改变业务库 journal 模式、版本、表或索引。
- 固定一条只做 `PRAGMA data_version` 的物理连接检测已提交变化；无变化时复用当前副本，有变化时合并并发复制，复制开始至少相隔 5 秒。等待可取消，不将旧统计伪装成刷新结果；监测连接失效时丢弃旧比较基准再重新建立。该比较必须留在同一连接，见 [SQLite 规则](https://www.sqlite.org/pragma.html#pragma_data_version)。
- 私有副本建立歌曲／时间索引，资源使用原有资源／时间索引。已知和未知身份分别按索引键集读取最多 `limit+1` 条，最后只对最多 102 条候选进行默认 50 条页的排序；未知始终排最后，时间和事件 ID 的排序方向、游标、过滤边界不变。复制期间若有提交，下一次读取保守地重建，不把不确定的新旧版本视作相同。
- 只保留一个当前缓存代；旧代有读取／导出租约时延迟清理，最后释放才关闭和删除。Store 关闭取消待复制请求并放弃缓存，已租用副本由读取者释放；未完成副本在复制／索引失败或取消时清理。没有按筛选建立无界缓存。
- collector 在现有队列和 128 项批次内保留原批次，仅对 BUSY／LOCKED 最多尝试三次，每次沿用原 2 秒预算，两次退避为 50／100 毫秒。重试前事务已经回滚或连接已淘汰，避免部分结果和重复计数；批次屏障等待最终结果，后续批次不越过它。恢复成功不锁存失败；约束、取消、超时及存储故障不盲目重放，最终失败仍进入日志和 `Store.Close` 错误，不增加无限恢复或持久化观察队列。

## 验证

| 检查 | 结果 |
| --- | --- |
| 最小真实锁冲突 | 修复后约 1.29 秒通过；释放读锁后同批恢复，重开保留 1 条明细与 `demand_count=1`／`demand_score=1` |
| 有界恢复与整批原子性 | 长期持锁只产生两次重试、第三次失败；没有部分明细／需求残留。释放后完整重放两笔需求与明细，顺序和数量正确；无效歌曲 ID 不重试 |
| 同一历史 interface 的 12 个并发分支 | 计数、来源、首正文、歌曲排序、资源排序及导出各覆盖正常／受控 SQL 错误。用 TEMP view／UDF 暂停真实私有库扫描；暂停期间收藏和实际 worker 的明细／需求均能提交，并在读取结束前关闭重开确认持久化 |
| 复制合并、刷新与取消 | 八个并发计数及其他五类读取只建立一个副本；新提交后 50 ms 等待取消不复制，也不返回旧数，等待间隔后只建立一份新副本并读到新数量 |
| 监测连接重建 | 关闭固定监测连接后原错误可见，旧比较基准失效；重连后的新计数器不能使旧副本误判为当前版本 |
| cacheproxy 定向回归 | 26.810 秒通过，含筛选、所有排序和未知分组游标、只读／取消、WAL、导出及事务失败清理；大库与真实 ZIP 环境用例在未设变量时明确 skip |
| 两包整包 | `internal/cacheproxy` 244.079 秒、`internal/console` 211.189 秒通过；两包 vet 通过 |
| Windows 定向 race | 使用现有 w64devkit，历史并发、复制复用、后台恢复、WAL／事务清理与输出隔离共 232.918 秒通过；监测连接重建的新增断言另行补跑 18.213 秒通过 |
| 真实 SQLite／Console／Edge | `TestRequestHistoryBrowser` 16.10 秒、`TestDiagnosticExportBrowser` 1.90 秒通过；覆盖分页、排序、SSE、统计刷新、原身份、ZIP 下载及 320px 行为 |

最终将诊断导出的 Store 指针纳入既有状态锁的快照读取后，补跑诊断、历史读取鉴权及管理准入回归，2.070 秒通过；前端嵌入产物一致性、gofmt 和差异检查通过。

### 600 万条独立批次

`TestHistoryLargeReadsPersistConcurrentObservations` 使用 600 万条带同毫秒到达、未知歌曲／资源及已冻结来源的合成 GET；开启默认隐藏过滤，生产锁等待与写事务超时保持不变。冷读取期间突发提交 32 个请求，明细与需求经过真实 `startGET`／`record`／`flush`，结束后关闭重开直接检查数据库：**新增明细 32 条、需求行 32 条，均保留，collector 关闭无错误**。

| 路径 | 本机观测 |
| --- | --- |
| 冷歌曲页（含在线复制与私有索引，且有并发写入） | 7.843 秒，返回 50 条 |
| 写入完成后下一次保守刷新（含重新复制／索引） | 7.489 秒，返回 50 条 |
| 复用后的歌曲／资源升降序及下一页 | 2.58–3.72 ms，每次 50 条 |
| 来源汇总（复用副本） | 735 ms |
| 默认过滤精确计数（复用副本） | 4.841 秒 |
| 首正文汇总（600 万个密集窗口样本） | 2.005 秒，原扫描预算返回 `context deadline exceeded` |

实际计划分别 `SEARCH ... USING INDEX request_events_history_song_time` 和 `request_events_resource_time`，关联下一条 GET 仍走时间索引；`USE TEMP B-TREE FOR ORDER BY` 只作用于两组有限候选，不再排序完整历史。这些耗时不设 CI 阈值。

首轮探索使用持续写入直到读取返回的负载，冷复制约 1.451 秒因备份步骤 BUSY 失败；随后恢复查询，重开仍保留该轮全部 111 条新增明细和需求。该失败促成本轮备份步骤的期限内重试；最终用有界 32 笔突发复验，两个负载不同，不作为严格性能配对。原始失败保存在 `large-6m-first.log`，通过批次在 `large-6m-final.log`，均位于忽略目录 `artifacts/history-write-isolation/`。SQLite 的独立连接持续写入可能令在线备份反复重启，官方也明确这一[完成边界](https://www.sqlite.org/backup.html#file_and_database_connection_locking)。

## 共享构建取消隔离补充

提交前跟进 P2：共享构建原先使用首个请求的上下文，等待者在 `build.done` 后直接返回其错误。冷构建期间首请求取消或超时，会使仍有效的来源／首正文汇总一起失败；不损坏数据，但需要重新操作。

`TestHistoryWaitersRecoverCanceledBuild` 通过真实 SQLite 连接钩子暂停首轮私有副本构建，并从同一历史 interface 发起来源及首正文等待者。`synctest.Wait` 确认两者均已等待；虚拟时间覆盖原 5 秒间隔，不以固定 sleep 猜测排队状态。修改前取消、超时两分支在约 0.79 秒内稳定失败，两个等待者都继承首请求错误，且只构建一次；真实复制错误和模块关闭对照通过，输出保存在 `waiter-cancel-before.log`。

修复只允许构建取消／超时后的等待者重新进入获取循环，仍检查各自上下文及原复制间隔；真实错误保持可见，关闭不启动新构建。五个分支覆盖首请求取消、首请求超时、单个等待者取消、真实复制错误和模块关闭。普通检查连续三轮通过（3.258 秒）；取消／超时后两个有效等待者共用第二次构建，其他分支保持一次构建。相关定向回归 24.695 秒、race 连续三轮 125.182 秒及两包 vet 均通过，输出在 `waiter-cancel-regression.log`、`waiter-cancel-race.log`；未重复不受此分支影响的大库或浏览器批次，复用上文证据。

## 写入故障后的历史可读性补充

跟进 P2：Console 的历史读取原先经 `businessStore()` 拒绝已锁存的写入错误，即使已提交数据可读、写入条件已恢复，所有历史读取仍返回 500。现将 Store 打开结果检查独立为 `openedBusinessStore()`，历史查询和事件订阅复用它；查询继续使用同一 `Store.History()`，由读取路径报告实际错误，其他业务操作仍保留原故障检查。

`TestHistoryReadsSurviveDatabaseWriteFailure` 复用现有回滚日志写入障碍，在真实 Store 上触发并锁存写入错误，再检查故障期间及恢复后两阶段的时间／歌曲分页、总数、来源、首正文统计和事件订阅。修复前五个查询分支在两阶段均返回 500（0.16 秒）；修复后全部通过（5.20 秒），确认已提交数据和原故障均保留，恢复后实际写事务能够成功。已有缺库用例补验四个读取接口仍拒绝未成功打开的 Store。

首次沙箱运行在条款夹具初始化时遇到 Windows 无效句柄，未进入被测逻辑；普通本机权限下完成上述失败对照与通过复验。相关 Console 读取、鉴权、事件流、数据库所有权及存储故障回归通过（14.397 秒）。

## 复跑与剩余边界

```powershell
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run 'TestHistory|TestRecent|TestUsage(Recovers|LockRecovery)|TestStorage(Failed|Canceled|Snapshot)|TestExportRequestHistory' -count=1
$env:SWD_HISTORY_LARGE_EVIDENCE='1'
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run '^TestHistoryLargeReadsPersistConcurrentObservations$' -count=1 -v
```

证据根目录 `artifacts/history-write-isolation/` 还包含 `targeted.log`、`packages.log`、`race.log`、`race-monitor-reconnect.log`、真实浏览器日志与截图。初次代码整理中的未使用局部变量及缺少 `time` 导入导致编译失败，修正声明与导入后定向检查通过。

本轮解决历史长读阻塞写入和短暂锁冲突的观察漏写，磁盘耗尽、永久锁持有、进程崩溃或队列已满时仍可能丢失观察记录。极大库首次复制／索引仍有磁盘、临时空间与响应成本；密集首正文统计仍可能超时，但失败只影响统计，未扩大本次范围去优化分位数算法。没有新增必须依赖 VRC 的验收项；本轮没有启动游戏或修改 hosts。

## 大库持续刷新优化补充

针对前阶段“任意提交都会重新复制整库、建立歌曲索引，30 秒统计刷新反复扫描”的规模问题，保留 `RequestHistory` 读取隔离 interface 和原有期限，调整副本失效、按需准备和结果复用。现行规则以[存储契约](../storage.md)为唯一依据，以下记录实测范围。

### 失败对照与实现

`TestHistoryRefreshReusesUnchangedDetails` 经现有临时目录入口执行，修改前 **0.23 秒失败**：来源查询建立了未使用的歌曲索引，收藏提交使副本换代；给已成功读取的私有连接安装拒绝扫描的 TEMP view 后，来源及向前移动 30 秒且没有样本跨界的首正文刷新均再次执行 SQL 并失败。修改后 **0.22 秒通过**，不使用耗时阈值代替“确实没有复制／扫描”的断言。

- Store 按已经成功提交的明细变化失效；包含 HEAD 和保留期清理，回滚及纯需求摘要不失效。沿用现有 usage 提交回调，同时保持 GET-only SSE 的外部行为。独立文件 reader 仍检测 `data_version`，原监测连接重建测试改用该入口，保留对任意外部提交和连接计数器重置的检查。
- 歌曲索引只在歌曲排序时准备；来源、计数、资源排序和导出不需要它。当前歌名按当页有限 ID 读取，不用旧副本中的名称冒充当前曲库。首正文单独准备有效样本的覆盖索引，以精确计数和有序排名查询取 P50/P95，保持原始筛选、最近 72 小时及 nearest-rank 语义；没有增加 2 秒扫描预算。
- 每代只缓存来源、最近一次计数条件和最近一次首正文窗口结果，合并并发计算；失败、取消不缓存。首正文按最早已纳入样本与下一未来样本判断窗口边界，回拨重新计算，不以 TTL 近似窗口。返回的分位数指针复制给调用方，避免调用方修改污染缓存。
- 私有库 WAL 允许租用中的扫描／导出与按需索引共存；只在私有库建索引，业务库的结构、日志模式、写入期限和复制的短步读锁保持不变。实际明细提交仍可能触发全库换代，本轮未实现增量复制，首次准备耗时随库规模和磁盘条件变化。

### 自动验证

新回归覆盖无关收藏／摘要提交、回滚、当前歌名、GET／HEAD 提交、清理后统计与 HEAD 导出、不同计数条件、失败后重试、样本跨窗口两端、时钟回拨、空窗口及奇偶样本的精确分位数。8 个并发来源请求只扫描一次；首请求在 SQLite UDF 中取消后，有效等待者共用第二次计算。该检查也暴露驱动可能在取消与扫描完成同时发生时返回成功，缓存层现在在发布成功结果前再次检查调用上下文。

暂停真实导出输出后，在同一副本并发准备歌曲和首正文索引，两个读取均在导出继续持有租约时完成；业务库 journal 模式和 schema 未改变（`TestHistoryPreparesIndexesDuringLeasedExport`，0.52 秒）。既有长读并发写入、等待者取消恢复、离线 `data_version`、筛选、排序、清理及导出定向回归通过（44.605 秒）。

Windows 定向 race 首批 292.222 秒未报告数据竞争，但新增取消测试的扫描次数断言失败：驱动替换取消后的物理连接时，单次安装的 TEMP view 探针随旧连接丢失。修正为在每条属于该副本的只读连接上安装同一探针后，该测试连续三轮 race 通过（65.923 秒）；其余原定向 race 分支无需因测试探针修改重复执行。按需索引与导出租约、当前名称读取另外通过 race（21.419 秒）。最终非时间页读取与当前名称查询共用原 2 秒预算，包含该路径的历史回归通过。

两包完整检查曾在沙箱中失败：cacheproxy 的硬链接／junction 夹具被拒绝，Console 的条款／配置文件替换报告无效句柄，原记录保存在 `packages.log`。cacheproxy 的全部失败项及最终全部 `TestHistory*` 在普通权限下复验通过（34.343 秒，`cacheproxy-native-recheck.log`）；Console 整包在普通权限下通过（216.719 秒，`console-native.log`）。没有为环境失败修改产品逻辑。两包最终 vet、gofmt 和差异检查通过。

真实 SQLite／Console／Edge：历史页 **16.28 秒**、诊断 ZIP **1.87 秒**通过，覆盖排序、分页、统计刷新、SSE、请求身份及导出。沙箱首次在条款夹具的 `renameat` 操作报告 Windows 无效句柄，未进入被测读取逻辑；原失败保存在 `browser.log`，普通本机权限的通过结果为 `browser-native.log`。

### 600 万条连续刷新

沿用 `TestHistoryLargeReadsPersistConcurrentObservations` 的 600 万条合成分布和实际 collector，增加无歌曲索引的冷来源读取、准备后首正文扫描、三次收藏提交后的连续刷新。刷新窗口分别前移 30／60／90 秒，不实际 sleep；所有样本仍在 72 小时窗口内。最终批次 **128.855 秒通过**：

| 路径 | 本机观测 |
| --- | --- |
| 冷来源读取，含完整在线复制及求和 | 3.147 秒；断言没有准备任何历史派生索引 |
| 已有副本上的首次歌曲排序，同时发生 collector 写入 | 5.083 秒；此项不再称为冷复制 |
| 明细写入后重新复制并准备歌曲索引 | 7.561 秒；这项成本仍存在 |
| 复用后的歌曲／资源升降序及下一页 | 3.90–7.21 ms |
| 首次首正文统计，含按需索引准备 | 5.843 秒，成功 |
| 已准备索引上的首正文实际 SQL | 1.675 秒，600 万样本，P50=0.499999 ms、P95=0.949999 ms |
| 来源首次汇总／默认过滤首次精确计数 | 713 ms／6.324 秒 |
| 三次无关提交后的来源＋首正文＋计数刷新 | 7.068／6.098／4.555 ms；均为同一副本、同样准确结果 |
| 并发业务写入及关闭重开 | 新增明细 32 条、需求 32 行，均保留，无 collector 错误 |

第一轮修改后大库检查亦通过（133.360 秒），首正文含准备 5.507 秒；其首次歌曲页仍包含冷复制，与最终批次的预先来源读取顺序不同，不作严格配对。原 7.843／7.489 秒和密集窗口超时的历史失败保持原样。以上为该机、该分布观测，不作为跨硬件 CI 阈值；其他规模、更多过期样本或持续真实明细写入仍可能耗时／超时。

本阶段原始日志位于忽略目录 `artifacts/history-refresh/`，包括 `targeted.log`、`index-export.log`、`large-first.log`、`large-final.log`、`packages.log`、`cacheproxy-native-recheck.log`、`race.log`、`race-refresh-retry.log`、`race-index-export.log`、`vet-final.log`、浏览器日志及截图。实现整理时曾有未使用导入／变量的编译失败，修正后完成回归；大库首轮日志目录不存在的命令未进入测试，随后建立隔离证据目录复跑。没有启动游戏、修改 hosts 或新增 VRC 验收项。

复跑新增回归与原有隔离检查使用上文相同 test 入口；大库仍设置 `SWD_HISTORY_LARGE_EVIDENCE=1`。浏览器分别设置 `SWD_HISTORY_BROWSER_EVIDENCE`、`SWD_DIAGNOSTIC_BROWSER_EVIDENCE` 为隔离输出目录并运行 `TestRequestHistoryBrowser|TestDiagnosticExportBrowser`。

## 状态提示与观测同步补充

本节保留 `c0870123` 的实现与验证；随后合并历史导入的有序 worker 操作时，通用 `synchronize(ctx, apply)` 已由 `flush(ctx)` 与共用控制入队替代，取消和提交结果边界以[#28 合并记录](issue-28-validation.md#2026-11-15合并后的观测同步与导入结果)及[现行存储契约](../storage.md#打开失败与未来升级)为准。下文的旧 interface 名称不作为当前实现要求。

跟进 P1：状态页为判断 VRCX 导入提示调用 `SongUsageCount`，原实现强制 `flush`。数据库连接占用且 1024 项观测队列满时，屏障发送持有 GET 也需要的互斥锁；已经取消的状态请求仍等待队列，新的 GET 观测随之阻塞。原探针在释放数据库后两者均返回，证明特定积压条件下的阻塞，日常发生频率与阻塞持续时间需在运行中观测。

`TestCanceledSongUsageCountDoesNotBlockGET` 固定占用真实应用数据库连接，等待实际 worker 的连接排队后填满观测队列，不替换 worker 或其锁。修复前连续两次失败：取消的状态查询和新 GET 观测都无法在数据库释放前返回，第二轮约 0.70 秒；原探针及回归失败分别保留在 `status-flush-probe.log`、`status-flush-regression-before.log`。

现行行为回到[存储契约](../storage.md#打开失败与未来升级)：状态／预览数量读已提交状态，导入、淘汰及元数据补齐评分继续同步。观测 module 的 `synchronize(ctx, apply)` interface 收拢屏障发送、取消、等待和有序替换；生命周期共享读锁保护通道关闭，单独的时间戳／入队锁只包围普通观测的非阻塞发送。导入屏障终止当前批次，在替换事务结束后再处理后续观测，GET 期间仍能入队或沿用满队列丢弃。没有改变队列上限、存储格式、BUSY 重试或评分算法。

新增四组回归连续三轮通过（4.810 秒）：

- 已取消的状态查询及新 GET 在数据库释放前返回。
- 普通同步和实际 `ImportSongUsage` 分别覆盖屏障已入队、队列已满两种等待；GET 到达与完成观测均不等待同步锁，取消无需释放数据库。
- 同一队列中的先到 GET、覆盖导入、后到 GET 按序落库：先到统计被替换，后到需求在导入后的计数为 2。
- 暂停 worker 后，状态读取仍返回已提交的空统计；放行并同步后读到新的需求，不强制等待未落库的提示数据。

两包整包回归在普通用户权限下通过：`internal/cacheproxy` 246.874 秒、`internal/console` 204.548 秒；两包 vet、gofmt 与差异检查通过。Windows 定向 race 使用现有 w64devkit GCC，覆盖新增回归、历史导入、需求去重与乱序完成、淘汰及元数据补齐评分，556.142 秒通过。未重跑需要另设环境变量的大库、磁盘耗尽或浏览器批次，也没有界面内容变更。

沙箱整包首轮的硬链接、目录联接与原子重命名被拒绝，race 首轮报 `ThreadSanitizer: failed to restore address`；两者均不计通过。沿用同一隔离临时目录入口在普通用户权限下复验，没有修改测试输入、断言或产品文件操作。首轮日志 `packages-after.log`、`race-after.log` 保留；最终通过结果为 `packages-native-after.log`、`race-native-after.log`，定向三轮为 `backpressure-regressions-after.log`。

复跑命令：

```powershell
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run '^(TestCanceledSongUsageCountDoesNotBlockGET|TestUsageSynchronizationCancellationDoesNotBlockGET|TestUsageImportAppliesBetweenEarlierAndLaterGET|TestSongUsageCountReadsCommittedStateWithoutFlushing)$' -count=3 -v
```

本阶段的首轮失败和复测结果均保存在 `artifacts/architecture-review-storage-20261115/`。测试使用隔离 SQLite 和受控请求。数据库查询仍可能等待连接，队列满时也可能丢失观测。上述回归覆盖的是统计处理与 GET 的隔离，本轮无需新增 VRC 实机项。
