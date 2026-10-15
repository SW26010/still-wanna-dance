# 播放地址解析后的资源元数据写入验证

2026-10-15：`ObserveSongURL` 在接受有效的新 URL 观察时，即将 URL 的 `e/s` 解析为资源 MD5/字节数，并复用资源写入 SQL 更新 `media.byte_size/source_path`。URL 记录与资源属性在同一事务提交，任一写入失败则回滚；不需要视频正文下载，不新增字段或数据库迁移。同 MD5 的不同歌曲共享资源行，已有歌曲映射不因 URL 冲突而改变。

未知大小仍为 NULL。沿用原 `recordVideo` 的属性更新规则：已提供的有效属性写入，缺失值不清空已知值。新解析的大小属于上游元数据，并不表示视频存在、内容已校验或已播放。没有扫描或回填用户现有数据库；旧 URL 缓存的资源属性在下一次接受新观察时补齐。

## 自动回归覆盖

| 测试 | 验证内容 |
| --- | --- |
| `TestResolvePlaybackRecordsSharedMediaWithoutDownload` | 从本地 HTTP API 的 302 响应解析地址；清单中的 NULL 大小立即补齐。两个歌曲引用同一 MD5 时仅有一个 media，映射不变；两次 API 请求、零次视频正文请求，正式与临时视频目录均为空，不增加播放/请求统计，关闭重开后大小和来源路径保留 |
| `TestSongURLMetadataIgnoresOlderAndTiedObservations` | 新资源可独立创建；MD5/大小从 URL 解析，不采信调用者重复填写的字段；迟到旧结果和同刻不同资源均不更新元数据，不创建歌曲或关联 |
| `TestSongURLMetadataAndObservationRollbackTogether` | 对 media/song_urls 分别注入写入失败，各覆盖首次插入及已有观察更新。两表同时保持原状态，错误归类并保留为本地存储故障 |
| `TestSongURLInvalidMetadataDoesNotWrite` | 缺少大小、零大小、错误 MD5、重复大小参数等被校验拒绝；两表均不写入，不误记为存储故障 |
| `TestSongURLConflictPreservesMapping` | URL 指向另一 MD5 时，只为那个资源保存大小；既有歌曲关联及原资源未知大小保持不变 |
| `TestLocalStorageFailureStopsConsoleFallback` | 控制台解析时的 URL/资源写入失败均在视频正文请求前停止；只查询一次 API，不继续换节点或歌曲重试。视频临时文件写入失败仍按原下载阶段处理 |
| `TestSongURLCanceledTransactionDoesNotDisableColdLoads` | 限制为单连接，取消事务后重新获取该连接，以确定自动回滚先于 `Commit`，真实返回 `sql.ErrTxDone`；经 URL 写入路径的错误分类还原为取消，不保留存储故障，事务数据回滚，同一服务随后观察地址并完成一次冷加载 |
| `TestSongURLTransactionErrorClassification` | 覆盖超时、包装后的事务结束错误、取消后的成功返回；上下文有效时的 `sql.ErrTxDone` 及取消同时出现的其他存储错误仍保留为本地存储故障 |

沿用已有地址排序、失败失效、纳秒同刻/回拨、清单和加载链路回归。事务退出后才保留存储错误，避免持有数据库事务时进入服务错误状态锁。

取消错误仅在 `errors.Is(err, sql.ErrTxDone)` 且 `ctx.Err()!=nil` 时归还上下文错误，不将任意取消期间的存储错误吞掉。复现测试使用真实 SQLite 事务确定回滚先后，再调用生产路径共用的错误分类函数；未在 `ObserveSongURL` 内加入时序测试钩子。

## 执行记录

环境为 Windows amd64、Go 1.27.1，`CGO_ENABLED=0`。测试使用独立临时库和回环 HTTP 上游，不访问实际视频库或真实播放服务。原始日志仅在忽略目录 `artifacts/song-url-metadata-20261015/`。

- 定向测试：`go test ./internal/cacheproxy -run 'Test(SongURL|ResolvePlaybackRecordsSharedMedia|ObservationStorageFailure)' -count=1 -timeout=90s -json`，17 个测试/子测试通过，记录 `focused.jsonl`。
- 缓存包完整测试：`go test -tags=no_easytier ./internal/cacheproxy ./internal/console -count=1 -timeout=180s -json` 中 cacheproxy 通过（457 个测试/子测试通过，8 项条件跳过），记录 `packages-host.jsonl`。该轮 console 的资源索引故障测试仍期待一次正文请求，与本次提前写入的行为不符；断言已改为零次，并保留首次失败记录。
- 控制台最终完整回归：`go test -tags=no_easytier ./internal/console -count=1 -timeout=180s -json` 通过（563 个测试/子测试通过，5 项条件跳过，81.495 秒），记录 `console-final.jsonl`。包含更新后的存储故障/回退断言。
- 最终 `go vet -tags=no_easytier ./internal/cacheproxy ./internal/console` 通过，记录 `vet-final.log`；`gofmt` 与变更空白检查通过，台账 42 个编号唯一。本次未执行 race 检查。

首次包级测试在受限沙箱中出现硬链接/目录连接权限拒绝和 Windows `renameat` 无效句柄，记录 `packages.jsonl`；随后在常规本机权限下使用同一隔离临时目录运行。不能将沙箱失败当成产品回归通过，也没有为绕过环境限制修改产品文件操作实现。

P2 取消边界按先复现再修复验证：`go test ./internal/cacheproxy -run 'TestSongURL(CanceledTransaction|TransactionErrorClassification)' -count=1 -timeout=30s`。修复前确定得到 `sql.ErrTxDone`，复现持续存储错误，超时与包装错误分支也失败，见 `cancel-before-fix.log`；加入窄条件还原后全部通过，见 `cancel-after-fix.log`。该结果为本地自动复现，不是实机验收。

P2 修复后的最终完整回归：`go test -tags=no_easytier ./internal/cacheproxy ./internal/console -count=1 -timeout=180s -json` 通过，cacheproxy 为 464 个测试/子测试通过、8 项条件跳过，console 为 563 个通过、5 项条件跳过，记录 `packages-cancel-final.jsonl`。同范围 `go vet` 也通过，记录 `vet-cancel-final.log`；格式及空白检查通过。

## 待实机验收

开发行为登记为 [LA-042](live-acceptance-backlog.md)，包含实际解析入口、正文尚未下载、共享资源、重启持久化、迟到/非法观察、映射冲突、取消后继续冷加载及故障恢复证据。联合 LA-010/019 回归，不启动本轮游戏验收，不改 hosts。自动结果不能替代真实客户端和控制台验收。实现、自动测试、本文及台账作为同一变更纳入版本控制。
