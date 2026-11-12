# 视频导入按项持久化验证（2026-11-12）

现行规则见[视频导入与恢复](../video-import.md)。本轮修复逐文件保存整批 JSON 导致的平方级写入和内存克隆／汇总；使用隔离合成清单及视频夹具，没有操作用户已有视频、启动 VRC 或修改系统 hosts。旧 [2026-10-30 恢复报告](video-import-recovery-20261030.md)证明其 JSON 版本和当时条件，不作为新状态格式的验证证据。

## 根因和实现边界

旧实现每处理一项（包括本轮新跳过项），调用整批 `json.Marshal`、原子 Write／Sync，再在 Console 状态锁内克隆和遍历所有条目；源文件清理同样如此。N 个条目处理 N 次导致 O(N²) 的序列化、逻辑写入、复制和统计，同步提交次数为 O(N)。

实际调用旧提交路径：32 项累计写入 165,904 字节，64 项 652,320 字节，约 3.93 倍；诊断对线性增长断言失败。另一组合成清单的一万项 JSON 从 1,200,140 增至 1,930,140 字节，按逐项序列化增量精确折算累计 15,651,765,000 字节；该一万项旧结果是逻辑字节折算，没有实际重复写入 15.65GB，也不是物理设备写入量。

新实现复用已有 SQLite 依赖，实例独立 `video-import.sqlite` 不进入核心曲库。完整清单仅在创建、枚举完成和旧格式迁移时原子发布；后续只按固定序号更新对应条目。成功回执和清理结果立即提交，跳过／普通失败最多 64 项提交，取消／结束时补齐；正式写入使用 WAL 和 FULL 同步。内存只更新已提交的条目及派生计数，完整克隆／汇总留在任务边界，完整操作移出状态锁。

SQLite 文件及伴随文件复用已有数据库物理文件身份校验，拒绝硬链接别名和重解析路径；配置实例锁仍负责独占。旧 JSON 启动只读，明确继续／清理时迁移；未知版本、损坏的新记录和孤立恢复文件均保留并提示，不回退旧 JSON。

## 写入与分配结果

`TestImportProgressWriteVolumeDoesNotGrowWithManifest` 关闭测试连接的自动 checkpoint，保留实际 WAL，分别对 1,000／10,000 项清单逐项提交相同的 128 次更新，以覆盖成功回执的强提交策略：

| 清单大小 | 128 次单项提交的 WAL 字节 |
| --- | ---: |
| 1,000 | 725,152 |
| 10,000 | 725,152 |

`TestImportSkippedBatchWriteVolumeScalesLinearly` 按产品的 64 项批次规则更新整份清单，同样关闭测试连接的自动 checkpoint：

| 跳过项目数 | 整轮进度 WAL 字节 |
| --- | ---: |
| 1,000 | 659,232 |
| 2,000 | 1,318,432 |
| 10,000 | 6,649,712 |

这两项测量为 SQLite 实际输出的 WAL 文件长度，不含初始清单数据库、最终 checkpoint、文件系统元数据、视频和设备内部写放大。测试限制单项写入预算与增长比例，使用宽松的页级预算而非耗时阈值；不是磁盘寿命或真实视频导入吞吐结论。

`BenchmarkImportItemPublication`（Windows amd64，200ms）在 1,000／10,000 项下分别为 43.61／44.52 ns/op，均为 160 B/op、1 alloc/op；分配来自展示详情字符串，不再克隆整批数组。耗时只作本次观测，不设测试门槛。

## 恢复和故障检查

- 固定清单、源文件移动／删除／修改／替换、重启仅恢复待继续状态、回执跨重启清理、暂停与正常退出、清理失败重新确认：通过。
- 成功回执立即提交，且同步模式为 FULL；跳过／失败结果按 64 项提交，成功回执捎带已有待提交结果，暂停时补齐尾批：通过。
- 批内中途注入 SQLite UPDATE 错误，整批回滚，未提交的条目和清理资格不进入 Console；暂停期间补存失败仍报告持久化错误：通过。
- 实际导入成功后注入保存失败，下一文件不处理；64 个跳过结果提交失败后，第 65 个可导入视频不发布：通过。
- 旧 JSON 启动不迁移、不启动引擎；占用新快照工作槽使迁移失败，旧记录不变且不删除源视频；解除占用后明确清理，迁移保留两笔回执，删除结果重启可读：通过。
- 新数据库未知版本、缺项、错序，即使旁边有有效旧 JSON 也不回退；硬链接别名拒绝打开，明确放弃只解除本状态链接；孤立 WAL 不被当成没有任务：通过。
- 子进程强杀：暂存中断、视频已发布而回执未存、已提交 WAL 回执但连接未关闭、源文件删除后结果未存：通过。新增 WAL 场景在重启后保留成功回执，手动继续正确处理剩余文件。
- 跨卷复制中断：在已授权的第二卷创建测试独占随机源目录，确认复制部分内容后强杀，重新继续完成两文件，验证目标为独立副本、重启后明确清理：通过；测试结束清理自身源目录。
- 启动工作槽清理包含新 SQLite 快照工作槽，保留正式数据库及 WAL／SHM／journal；未使用导入时不创建状态库，多配置路径隔离：通过。

## 命令和范围

全部 Windows 测试经 `scripts/with-test-temp.ps1` 隔离。首次沙箱运行在既有条款夹具的原子重命名处报 `The handle is invalid`，未到达导入断言；按[开发说明](../development.md)已知限制，在沙箱外使用相同临时目录入口验证，没有修改产品文件操作绕过。

```powershell
# 三个受影响包的完整回归：通过（Console 154.889s）。
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console ./internal/desktop ./internal/apppaths -count=1
# 补充身份检查与孤立文件后，全部导入／配置清理定向检查：通过。
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console ./internal/desktop ./internal/apppaths -run 'TestImport|TestConfigCleanup|TestPortableLayout|TestCustomConfigurations' -count=1
# 既有曲库文件身份与独立资源入口：通过。
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run 'TestStoreRejects.*(Alias|HardLink)|TestIndependent' -count=1
# 已有本地 w64devkit GCC，进程内设置 CGO_ENABLED=1、CC：通过（Console 120.270s）。
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -race -tags=no_easytier ./internal/console ./internal/desktop -run 'TestImport|TestConfigCleanup' -count=1 -timeout=5m
# 新增 WAL 强杀恢复与整轮一万项写入检查：通过。
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestImport(CrashRecovery|SkippedBatchWriteVolumeScalesLinearly)$' -count=1 -v
# 进程内将 SWD_IMPORT_TEST_VOLUME 指向已授权第二卷的测试根目录：通过，无跳过。
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestImportCrashRecovery/copy$' -count=1 -v
# 相关包静态检查及写入／分配测量：通过。
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs vet -tags=no_easytier ./internal/console ./internal/desktop ./internal/apppaths ./internal/cacheproxy
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestImportProgressWriteVolumeDoesNotGrowWithManifest$' -bench '^BenchmarkImportItemPublication$' -benchmem -benchtime=200ms -count=1 -v
```

## 同卷硬链接的哈希读取（同日补充）

`ImportSnapshotVideo` 原先分别哈希源文件和暂存文件，`RemoveImportedSource` 又分别哈希目标和源文件；即使两个路径是同一文件，也完整读取两遍。新增 `TestImportHardLinkReadVolume` 使用真实 5 MiB 文件及 Windows `GetProcessIoCounters`，先在原实现上复现并触发单阶段读取预算断言失败，再验证修复：

| 阶段 | 修复前进程读取字节 | 修复后进程读取字节 |
| --- | ---: | ---: |
| 导入 | 10,485,776 | 5,242,896 |
| 清理 | 10,485,760 | 5,242,880 |

视频每份为 5,242,880 字节，导入另有 16 字节的进程读取。此计数是进程读取传输量，包含文件系统缓存命中及少量其他读取，不代表物理设备 I/O 或耗时收益。回归预算允许少量其他读取，但拒绝再次完整读取一份视频，也要求清理重新读取完整内容。

修复复用 `sameVerifiedFile` 比较已打开句柄的文件身份与属性，仅合并同一阶段内的共享内容校验，不引入跨操作哈希缓存。跨卷或被替换为独立副本的目标仍分别计算 MD5，源文件前后快照、路径检查、发布保护和清理错误分类保持原规则。

缓存层全部 `TestImport` 通过，包含真实第二卷的复制／清理；新增检查确认大小、文件身份与修改时间保持不变时，硬链接内容损坏、独立源或目标损坏仍拒绝删除，独立正确副本允许清理。Console 的回执清理／确认、固定清单、取消／退出和强杀恢复定向回归通过，包含跨卷复制中断后恢复，无跳过。缓存层与 Console 的 vet 通过。读取量测试首次在沙箱内被硬链接权限拒绝，尚未到达预算断言；按开发说明在普通用户权限下复现和复验，未修改产品文件操作。

```powershell
# 进程内设置 SWD_IMPORT_TEST_VOLUME 为已授权的第二卷测试目录。
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run '^TestImport' -count=1 -v
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestImport(UsesAllKnownMediaWithoutNetworkOrMapping|Cleanup.*|MediaOnlyAndCancellation|EnumerationFreezesPathsAndExcludesStorage|RecoveryUsesFrozenManifestAndRestoresReceipts|PauseAndExitKeepCurrentFilePending|CrashRecovery)$' -count=1 -v
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs vet -tags=no_easytier ./internal/cacheproxy ./internal/console
```

原始复验日志保存在忽略目录 `artifacts/import-hardlink-md5/`。本次没有前端协议或操作流程变更，未重跑完整浏览器套件。强杀不等同于真实断电、磁盘耗尽或文件系统故障；没有据此声明这些环境通过，也没有必须依赖 VRC 的新增剩余项。
