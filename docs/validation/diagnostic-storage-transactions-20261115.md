# 诊断导出与事务失败清理（2026-11-15）

现行规则见[存储契约](../storage.md)和[诊断收集](../logging.md#收集诊断信息)。本轮只操作隔离数据库和合成资料，不运行游戏，不修改系统 hosts。

## 故障与根因

用户提供的 `artifacts/architecture-storage-review-20261115/export-lock-real-300k.log` 保留原失败：30 万条请求导出到真实 ZIP 耗时约 1.53 秒，并发收藏提交报 `SQLITE_BUSY`；导出后重试报 `cannot start a transaction within a transaction`。当前会话能读到未提交收藏，重开后消失。该路径还影响请求历史记录。

在 Go 1.27.3 与真实 modernc.org/sqlite v1.40.1 上建立更小的回归：固定一个独立只读事务，在写连接仅将 `busy_timeout` 设为 0，确保失败点是 COMMIT 而非业务写入。修改前 `TestStorageFailedCommitReleasesTransaction` 稳定失败，断言发现未提交收藏、后续历史／偏好无法开启事务、重开结果缺失。

本机源码核对：SQLite COMMIT 遇到读锁可保留原事务；Go `database/sql` 先将 `sql.Tx` 标记 done，再调用驱动 Commit。失败后 defer 的 `tx.Rollback()` 只返回 `sql.ErrTxDone`，底层连接仍可归还池。清理需要在持有物理连接的阶段完成；重复 defer 或延长等待仍可能留下事务。

## 修复边界

- `storage_transaction.go` 统一固定事务物理连接；COMMIT 失败保留原错误，并通过 `sql.Conn.Raw` 返回 `driver.ErrBadConn` 淘汰该连接，让 SQLite 关闭并清理遗留事务。普通错误先回滚；回滚本身失败同样淘汰。连接清理在归还池之前完成。
- 偏好／映射、请求记录／清理、历史导入、建库及 v8→v9 迁移共用此机制。迁移保留其现有固定连接和外键恢复顺序；读取事务保持原边界。
- 导出先调用已有 adapter 的 SQLite [在线备份 API](https://www.sqlite.org/backup.html)，每步最多 128 页，在各步间释放业务库读锁；完成后关闭源连接，再从系统临时目录的一致副本查询、JSON 编码和 ZIP 压缩。复制期间仍允许短暂锁竞争，等待时间与实际磁盘条件有关。取消按步骤检查，复制失败按既有来源错误反馈。
- 在线备份可能复制整个业务库，临时空间随库大小增长；ZIP 仍只包含所选请求明细。副本的正常和失败清理覆盖其伴随文件，进程强杀后可能残留。没有改业务库 journal/WAL 模式、生产等待上限、数据库／配置格式或偏好通知语义。

## 已验证

- `TestStorageFailedCommitReleasesTransaction`：分别迫使真实偏好、请求历史 COMMIT 返回 BUSY，确认原 SQLite 错误保留、当前会话看不到失败变更、之后历史与偏好写入成功、重开结果一致。首次测试中空库夹具未开启历史保留，导致后续行数断言为 0；补齐夹具既定保留条件后通过，没有修改产品保留规则。
- `TestStorageCanceledWriteReleasesTransaction`：写入后取消上下文，确认失败数据不可见且新写入成功。
- `TestStorageFailedRollbackDiscardsConnection`：在真实 adapter 的事务上仅注入一次 rollback 失败，确认物理连接被淘汰、未提交数据消失、新事务成功。
- `TestExportRequestHistoryReleasesBusinessSnapshotBeforeZIP`：暂停 ZIP 条目输出。修改前偏好及历史均报 BUSY；修改后两者完成，导出仍只含原有两条记录，不混入快照后的新历史。
- `TestStorageSnapshotIncludesWALAndCleansUp`：保留实际未 checkpoint 的已提交 WAL，确认快照包含收藏；清理后整个临时目录消失，取消返回原上下文错误。
- `TestExportRequestHistoryLargeZIP`：30 万条合成明细导出到真实磁盘 ZIP，约 **1.782 秒**，ZIP **1,762,077 字节**；并发收藏约 **3.16 毫秒**，并发历史写入成功，Store 无终端故障。完整读取 ZIP 为 300,000 条；当前会话和关闭／重开后均保留收藏及总共 300,001 条历史。这是本机固定批次观察，不设时间阈值。
- 上述定向检查共 5.284 秒通过。真实 Console／Edge `TestDiagnosticExportBrowser` 及其他诊断测试通过（1.954 秒），下载留空／带说明 ZIP、取消焦点、时间联动、可选测速及 320px 布局，证据位于忽略目录 `artifacts/diagnostic-export-storage-fix/`。
- `internal/cacheproxy` 全包回归通过（202.722 秒），覆盖现有存储、迁移、偏好、映射、清理与下载行为。最终提交路径在清理完成后立即释放自有连接，保留原有提交后回调可用连接的时机；另对最终版本复验迁移、请求记录及历史导入。
- 定向 Windows race 检查 `Test(Storage(Failed|Canceled|Snapshot)|ExportRequestHistoryReleases)` 通过（47.148 秒），使用已有 w64devkit GCC，覆盖真实 SQLite 连接淘汰、取消、失败恢复和导出快照并发路径。

## 复跑

```powershell
$env:SWD_EXPORT_LARGE_EVIDENCE = '1'
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run '^Test(Storage(Failed|Canceled|Snapshot)|ExportRequestHistory)' -count=1 -v
```

不设置变量时，普通回归跳过 30 万条磁盘批次，保留确定性的读锁／事务断言。原始失败证据不覆盖。不需要新增游戏专属待验项。
