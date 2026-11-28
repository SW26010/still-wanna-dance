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

## 后续：播放消费与策略重算解耦（2026-11-15）

本节基线为 `cedc55c9`，补充上述扫描临界区修复之后仍存在的后台重复计算，不改变前文历史结果。现行规则以 [canonical 生效边界](../download-resource-design.md#生效边界与共享资源)为准。

普通 GET 消费会刷新本地保护并唤醒保留任务；旧任务每次重新查询所有已标记歌曲的关联策略，再遍历本地资源及正式库存。空库存、不限容量也承担查询与分配。`TestRetentionReusesUnchangedResourcePolicies` 在首次成功查询后占住唯一策略查询连接，再读取未变策略；隔离基线报 `unchanged policies were queried again context deadline exceeded`，修复后直接复用快照。另断言缓存命中仍遵循调用方取消语义，避免把“无需查询”变成忽略取消。基线结果见 `regression-before.log`。

实现只扩展现有保留模块：保护、最后引用及零预算写块按资源合并；偏好／映射提交同步使只读策略快照失效并请求全量检查，失败写入和重复记录已有映射不失效。缓存查询使用独立互斥，不占用播放保护锁；发布、导入及回收继续在 Store 决策互斥内取得当前有效快照。保护尾期增量维护最早可能到期时刻，到期或全量维护时重建；不为每个普通唤醒扫描全部保护记录。容量取舍仍独立按已维护字节数触发，零预算下可回收无关的未保护正式文件。

### 性能对照

Windows amd64、Go 1.27.3、Ryzen 7 9850X3D。`BenchmarkRetentionUnchangedPolicies` 创建真实隔离 SQLite，每首歌对应一个资源，收藏／屏蔽各半；预热后测量空库存、不限容量的完整保留决策。基线每组 20 次，最终每组计时 1 秒；首次查询及数据库准备不计时。

| 已标记歌曲 | 原实现／轮 | 最终／轮 | 分配／轮 |
| --- | --- | --- | --- |
| 1,000 | 1.542 ms | 80.83 ns | 182,240 B、5,052 次 → 0 |
| 10,000 | 14.202 ms | 88.35 ns | 1,595,546 B、50,139 次 → 0 |

`BenchmarkGETConsumptionRetention` 经过真实消费、保护更新和合并后的保留决策；无关正式库存与活动保护分别增加至 0／1,000／10,000 个，受影响资源固定为两个块，最终分别为 1,479／1,195／1,014 ns，每轮均分配 264 B、3 次。耗时包含本机并行检查带来的波动，没有随无关库存增加而增长。该基准不包含磁盘、网络和等待定时器，游戏帧时间未在此基准中采集；旧任务会合并唤醒，不能按每个 GET 块直接乘算完整策略查询成本。

复跑入口：

```powershell
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run '^$' -bench '^(BenchmarkRetentionUnchangedPolicies|BenchmarkGETConsumptionRetention)$' -benchtime=1s -count=1
```

原始数据保留在忽略目录 `artifacts/retention-work-20261115/benchmark-before.log`、`benchmark-after.log`、`focused-final.log`、`benchmark-final.log`；表中最终值包含收尾补上的缓存命中取消检查。

### 验证与失败归因

- 最终 60 个定向顶层用例通过，20.748 秒，覆盖保护窗口／交接／尾期、消费只检查相关资源、屏蔽清理与独立容量淘汰、策略失效与失败后保留快照、发布及删除互斥。新增真实后台定时器回归不发全量通知，确认消费尾期自动回收且未读块仍受保护。见 `final-context-tests.jsonl`；此前同范围结果保留在 `final-targeted.jsonl`。
- Windows 定向 race 首轮 13 个顶层用例通过，预览夹具失败，无竞态报告；修正夹具后补验预览及新增消费计时器，两项通过，8.607 秒。收尾补上缓存命中取消检查后，策略复用／重复映射／写入失效／失败写入四项再次通过，119.583 秒。合计 15 个不同顶层用例完成 race；GCC 16.2.0、`CGO_ENABLED=1`，未运行全包 race。见 `race-unrestricted.jsonl`、`race-final-supplement.jsonl`、`race-context-tests.jsonl`。
- Console 的预算、收藏设置重启生效与队列保护共 7 个顶层用例通过，2.633 秒；只用隔离配置和受控上游。见 `console-unrestricted.jsonl`。
- 首次完整 cacheproxy 回归为 580 个顶层通过、15 个失败、11 个跳过，370.397 秒，不记整包全绿。13 项失败为沙箱拒绝硬链接／目录联接或相应子进程，普通用户权限下全部复验通过；另两项为下述查询／预览夹具，修正后定向复验通过。可选大数据、跨卷、真实磁盘耗尽及需要符号链接权限的检查未启用或跳过，不计通过。见 `cacheproxy-tests.jsonl`、`filesystem-unrestricted.jsonl`、`filesystem-final-supplement.jsonl`。
- SQL 读冲突夹具原先假设发布必然查询策略；缓存生效后会跳过该读取，下载可在后续登记冲突前完成发布，导致其 MISS 假设失效。夹具改为先使策略快照失效，再施加既有真实 SQLite 排他读冲突，保留 SQLITE_BUSY 与恢复后冷下载断言。
- 预览夹具原先在两个 GET 之间无使用需求地保留后续块，较快的零预算回收可先清理它；第二次 GET 的提前补回与写入钩子再次清理相遇，出现同一 Range 两次请求。夹具显式登记前两个块的持续使用需求，只在既有写入钩子处制造缺块，保留首次零回源、第二次恰好一次回源及完整正文断言。修正后的 SQL／预览两项各连续三轮通过；中间只保护后续块的夹具版本又暴露前缀块缺少保护，失败保留在 `conflict-preview-final.jsonl`，最终结果见 `conflict-preview-corrected.jsonl`。
- cacheproxy／Console 的 vet、gofmt 及 `git diff --check` 通过。产品及永久测试中已移除临时 `[DEBUG-retention-work]` 诊断日志，隔离基线与原始诊断输出仅留在上述忽略目录。

本轮未运行浏览器或 VRC；后端性能、文件保护与互斥由上述检查覆盖，无新增必须依赖游戏的剩余场景，不追加实机台账。
