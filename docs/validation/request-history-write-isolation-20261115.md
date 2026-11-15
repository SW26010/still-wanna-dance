# 历史长读隔离与记录恢复（2026-11-15）

基线 `9b8034f1` 已隔离精确计数和诊断导出，但来源、首正文汇总与非时间排序仍直接读业务库。本轮补齐隔离、后台批次有界恢复及排序计划。现行规则以[存储契约](../storage.md)和[历史查询范围](../webui-design.md#历史查询范围)为准；全部资料来自隔离 SQLite、受控请求和合成历史，没有操作用户数据库、游戏或系统 hosts。

## 复现与根因

用户提供的 `natural-sorted-read-writes-6m.log` 保留原 600 万条失败：歌曲／资源排序触发原 2 秒查询超时，同时 collector 的三项批次提交报 `SQLITE_BUSY`，关闭重开后新增请求和需求行均为 0。它证明固定负载下的漏写，不证明日常发生频率。

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

实际计划分别 `SEARCH ... USING INDEX request_events_history_song_time` 和 `request_events_resource_time`，关联下一条 GET 仍走时间索引；`USE TEMP B-TREE FOR ORDER BY` 只作用于两组有限候选，不再排序完整历史。这些耗时不设 CI 阈值，也不外推其他数据分布、硬件或日常发生频率。

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

本轮解决历史长读阻塞写入和短暂锁冲突的观察漏写，不承诺磁盘耗尽、永久锁持有、进程崩溃或满队列时仍能保存所有观察。极大库首次复制／索引仍有磁盘、临时空间与响应成本；密集首正文统计仍可能超时，但失败只影响统计，未扩大本次范围去优化分位数算法。没有新增必须依赖 VRC 的验收项；本轮没有启动游戏或修改 hosts。
