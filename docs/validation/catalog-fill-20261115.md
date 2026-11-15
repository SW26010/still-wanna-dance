# 未变化清单的资源补缺验证

2026-11-15，基于 `41de6cec` 的工作区修改。现行规则见[清单同步与增改不删](../song-catalog-design.md#清单同步与增改不删)。

## 复现与修改

原 `SyncCatalog` 在判定同版或旧版后，仍为每首带 MD5 的歌曲执行三条 `INSERT … ON CONFLICT DO NOTHING`；10,575 首完整映射会尝试 31,725 次 SQL。`writeMapping` 在整个事务及变更通知期间持有映射／保留决策锁，收藏写入共用其中的保留决策锁。

`TestCatalogFillOnlyInsertsMissingRows` 通过真实 SQLite 的 `BEFORE INSERT` 触发器拒绝对已有主键的插入尝试。这种尝试即使最终冲突忽略，也会触发检查；单靠 `total_changes()` 无法观察。修改前，同版和旧版两个子测试均失败，报 `complete catalog attempted redundant inserts: constraint failed: redundant insert (1811)`。

本次将带 MD5 的 ID／MD5 候选在共同锁外编码，事务内用三条集合式 SQL 分别筛出并插入缺失的歌曲、资源、映射；缺失的共享资源去重。已有映射冲突时仍可新增备选 media，且保留已有映射。无 MD5 时不执行补缺 SQL。继续沿用 Store 接口、数据库 adapter、锁、版本比较、提交及变更通知机制，没有新增持久化字段或跨轮缓存。旧版仍在成功补缺后返回 `ErrCatalogOlder`，同版／旧版均不提前跳过补缺。

## 隔离基准

Windows amd64、Go 1.27.3、AMD Ryzen 7 9850X3D；使用真实 SQLite 和临时数据库，合成 10,575 首不同 ID／MD5，预先完成清单同步，确保映射完整。歌曲只含 ID／MD5，不代表真实上游正文及其全部资料字段。

`BenchmarkSyncCatalogComplete` 测量完整 `SyncCatalog`，包含防御性归一化、候选编码与提交，使用可取消的 benchmark context；不含建库、首次同步或网络／解析。`BenchmarkSyncCatalogFavoriteWait` 占住唯一数据库连接，使同步在已经取得共同锁后等待连接，再发起真实 `SetSongPolicy`，释放连接后测到收藏写入完成；该项排除同步前的归一化和候选编码，包含剩余同步事务与收藏写入本身。它用于观察共同锁竞争，不是普通用户操作的固定延迟。

每项取三轮中位数。修改前每轮 5 次，最终修改后每轮 20 次；较长复测用于降低短测波动，不设 CI 耗时阈值。

| 测量项 | 修改前 | 修改后 |
| --- | --- | --- |
| 同版同步 | 230.99 ms | 41.67 ms |
| 旧版同步 | 240.24 ms | 41.82 ms |
| 同版累计分配 | 12.61 MiB／359,222 次 | 6.13 MiB／10,767 次 |
| 旧版累计分配 | 12.59 MiB／359,179 次 | 6.13 MiB／10,767 次 |
| 受控竞争下的收藏写入等待 | 236.48 ms | 39.49 ms |

同版／旧版同步耗时分别约降低 82%／83%，累计分配约减半，收藏写入等待约降低 83%。补缺仍需遍历候选并检查数据库缺项，共同锁内工作量并非零。累计分配是临时申请总量，不是常驻内存或进程峰值。

修改前短测同版为 138.70–232.61 ms，最终修改后为 35.47–43.29 ms；首轮修改后的短测曾测得约 27 ms，本表采用最终较长复测。原始结果保留在忽略目录 `artifacts/catalog-fill-20261115/` 的 `before.log`、`after.log`、`after-final.log`、`after-long.log`。尝试通过额外 `-overlay` 加长历史基准时，补丁入口以 `Use only the reviewed repository Go patch inputs` 拒绝，记录在 `before-long.log`；未绕过入口，修改前采用先前已完成的原代码基线。

复跑最终基准：

```powershell
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run '^$' -bench '^BenchmarkSyncCatalog(Complete|FavoriteWait)$' -benchtime=20x -count=3 -benchmem
```

## 回归与边界

- 新增触发器回归通过：同版／旧版完整映射不尝试重复插入；可补当前未解析歌曲、历史占位歌曲、已有／共享新资源及缺失映射；冲突备选资源可入库，当前映射、成员及水位保持。
- 新增故障注入通过：在最后一类补缺（映射）失败时，前面的占位歌曲和 media 均回滚，不发布保留策略通知。同版／旧版真正补缺才通知，重复同步不通知。
- `cacheproxy` 曲库、Udon、提交失败及通知、映射／保留决策和偏好／发布并发定向回归通过（34.213 秒）；Console 曲库同步、初始化、备用补缺、失败／取消、通知定向回归通过（34.864 秒）。两包 vet 通过。

```powershell
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run 'Catalog|Udon|BusinessWrites|FailedBusinessWrites|RetentionDecisionExcludesMapping|MappingWriteSerializesWithRetention|PreferenceCommit|PreferenceFailure|PublicationDecisionExcludesPreference|PreferenceWriteSerializesWithPublication' -count=1
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run 'Catalog|MonitorUdon|MetadataNotificationsOnlyFollowCommittedChanges' -count=1
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs vet -tags=no_easytier ./internal/cacheproxy ./internal/console
```

Console 按开发说明在普通用户权限下运行，避开已确认的 Windows 沙箱重命名限制；其他检查在沙箱内运行，均使用既有临时目录和补丁入口。日志为同目录 `cacheproxy.log`、`console.log`、`vet.log`。未运行整包或 race。

本次结论限于隔离数据库路径及共同锁竞争，不将其描述为播放卡顿或播放体验改善。未访问真实上游、替换运行程序、启动 VRC 或修改系统 hosts；无需新增 VRC 实机项。
