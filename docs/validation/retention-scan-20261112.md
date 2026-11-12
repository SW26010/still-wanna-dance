# 库存扫描与保留决策临界区验证（2026-11-12）

本记录对应当日工作区的库存维护修复。现行规则见[视频保留](../cache-retention.md#执行与并发)和[下载需求与资源管理设计](../download-resource-design.md#生效边界与共享资源)；不改变预算、歌曲偏好、数据库格式或启动／五分钟校正的频率。

## 复现与原因

在真实 `runRetention(true)` 的目录枚举处使用临时通道屏障，枚举继续调用 `os.ReadDir`，逐文件检查继续调用 `os.Lstat`。扫描暂停后发起期限为 50ms 的 `SetSongPolicy`；原实现直到约 100ms 释放扫描屏障后才返回 `context deadline exceeded`，有限／不限容量分别为 100.901ms 和 100.6534ms。

暂停时 `retentionMu.TryLock()` 成功，业务数据库的 `SELECT 1` 正常完成，而 `Store.retentionRunMu.TryLock()` 失败。原因是 `runRetention` 将整个 `trimCachePass` 放进 `retentionDecision`，从策略读取开始一直覆盖目录扫描；`sync.Mutex` 等待本身不会响应请求 context。不限容量只跳过容量淘汰，仍经过同一持锁扫描。

复现命令为 `./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run '^TestRetentionScanAllowsPreferenceProbe$' -count=1 -v`。临时探针及文件 I/O 屏障仅用于本轮诊断，验证后移除；产品不新增可变测试钩子。源文件副本和原始结果保留在忽略目录 `artifacts/retention-scan-20261112/`，其中 `before.log`、`lock-probe-before.log` 保留修复前失败。

## 修复与一致性边界

- 目录枚举、逐文件检查、库存快照及字节总量构建不持有 Store 决策锁。单独的扫描互斥保护已有的 `retentionChanges`，防止两次扫描覆盖同一份增量记录。
- 复用发布／删除的增量覆盖扫描结果。最终库存合并短时使用 Store 决策锁，保留准入／发布决策过程中库存只能缩减的既有约束；字节数只在合并时修正受影响的资源，不在决策锁内重新遍历全量求和。
- 完成校正后，在决策锁内重新读取当前策略并执行回收。映射／偏好事务从提交到同步通知的锁范围，以及视频发布／导入提交的互斥保持原有保证。

## 已完成检查

| 检查 | 结果与证据 |
| --- | --- |
| 同一请求等待探针 | 最终方案在有限／不限容量下分别为 3.6132ms、3.1152ms 成功返回；枚举仍被屏障暂停，决策锁可获取。见 `scan-final-settlement-after.log` |
| 扫描期间修改策略 | 四种组合均通过：有限／不限容量 × 改为收藏／屏蔽；回收采用扫描期间提交的新策略。同上日志 |
| 扫描期间发布与删除 | 通过真实准入和发布提交新增 7 字节文件，空扫描快照未覆盖其库存；扫描读到旧文件后实际删除该文件，旧快照未将其重新计入。同上日志 |
| 永久回归 | `TestRetentionReconciliationFailureDoesNotWaitForDecision` 在原实现的两种容量模式均失败，修复后通过。见 `regression-before.log`、`scan-final-settlement-after.log`；既有并发发布测试改用扫描互斥等待当前扫描，避免另扫一轮掩盖丢账 |
| 最终源码普通回归 | 移除探针后，库存、歌曲策略、发布、导入、引用及写入通知相关的 64 个顶层用例通过，19.721 秒；最初仅跨卷检查因未设置环境变量跳过。见 `final-tests.jsonl` |
| 实际跨卷导入 | 使用授权工作区的另一个卷创建随机测试子目录，补跑 `TestImportCrossVolumeCopy` 通过，0.278 秒；验证目标文件元数据计量及无需重扫的实际淘汰。见 `cross-volume-import.log`。连同普通回归，共 65 个不同的顶层用例通过 |
| Windows 定向 race | 11 个顶层用例通过，无竞态报告，83.077 秒；包含两个扫描发布／删除探针、并发扫描与真实发布、偏好提交到通知的排斥、失败记录顺序、策略读取到回收／发布的排斥及发布前策略复核。见 `decision-race-unrestricted.log`；Go 1.27.3、GCC 16.2.0、`CGO_ENABLED=1`，未执行全包 race |
| 静态检查及清理 | 最终源码 `vet -tags=no_easytier ./internal/cacheproxy`、gofmt 和 `git diff --check` 通过；`final-vet.log`。产品源码已移除临时探针和全部 `[DEBUG-retention-scan]` 标记 |

首轮沙箱内定向 race 检查因真实硬链接 `Access is denied` 失败，并出现 `ThreadSanitizer: failed to restore address`；记录在 `targeted-race.log`，不计为通过，也未修改产品文件操作绕过限制。

普通用户环境的大范围定向 race 在五分钟整包期限结束时仍在 `initializeMediaSeed` 的 SQLite 执行中；当时刚进入新增发布探针的数据库初始化，尚未运行到扫描并发，不是锁等待。`targeted-race-unrestricted.log` 保留超时及堆栈；此范围未记为通过。后续定向 race 聚焦新增扫描增量和既有提交互斥边界，不扩大单次期限或修改数据库种子夹具。

以上耗时只描述受控屏障下的请求等待，不是实际磁盘扫描性能基准。本次不需要 VRC 才能判断的剩余场景，没有启动游戏或修改 hosts。
