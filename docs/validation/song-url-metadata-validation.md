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

开发行为登记为 [LA-042](../live-acceptance-backlog.md)，包含实际解析入口、正文尚未下载、共享资源、重启持久化、迟到/非法观察、映射冲突、取消后继续冷加载及故障恢复证据。联合 LA-010/019 回归，不启动本轮游戏验收，不改 hosts。自动结果不能替代真实客户端和控制台验收。实现、自动测试、本文及台账作为同一变更纳入版本控制。

## 定时探测复用播放地址

2026-10-15：运行期 Monitor 增加 `OnPlaybackURL`，在普通及多候选播放 API 探测成功后、资源采样前交付原始 URL、歌曲 ID、规范 API/节点及请求起止时间。Console 在现有存储生命周期锁下调用 `ObserveSongURL`，复用其校验、事务、排序与存储故障处理。自动与手动检测共享该路径；只更新实际抽样歌曲，不增加 API 请求或媒体下载，也不把资源采样计为播放。引导独立网络诊断未注册此回调。

回调在 Monitor 锁外运行，可跨通道并发；取消/退休批次不交付，退出取消并等待回调。迟到查询按原查询起始时间排序，不因较晚写库变成新证据。持久化失败记录脱敏 `monitor_song_url_sync_failed`，与 API 可用性及探测耗时分离，完整签名 URL 不进入状态快照。

| 测试 | 验证内容 |
| --- | --- |
| `TestPlaybackURLDeliveryReusesProbeResponses` | 普通/双候选的播放专项分别只有 3/6 次原请求、2/4 次回调、零资源请求；完整签名 URL 保留，歌曲 ID 不误用资源路径 ID，API 无查询参数，节点 cf/nya，时间取原请求，回调可读取快照且私密 URL 不外泄 |
| `TestPlaybackURLDeliveryRejectsInvalidCanceledAndRetiredResponses` | 非法大小、非重定向响应、已取消/退休批次不交付；配置替换后新批次仍按原机制重试 |
| `TestPlaybackURLDeliveryPrecedesResourceProbe` | 普通/多候选在正文采样前完成地址回调；资源 503 不丢弃已有地址证据 |
| `TestPlaybackURLDeliveryCloseCancelsAndWaits` | Close 取消回调上下文，等待回调释放后才结束 |
| `TestMonitorLifecycleConsentAndStatus` | 真正启动自动监测，原 14 次请求不变；两节点各留一条可复用 URL，共享资源大小已补齐，不产生播放/请求统计，条款门禁及状态查询仍有效 |
| `TestMonitorPlaybackURLPersistsMetadataWithoutDownload` | Console 无额外网络请求或媒体文件；更新大小/来源路径，旧请求迟到不覆盖新值，保存原请求时间，共享 MD5，保留冲突时的清单映射 |
| `TestMonitorPlaybackURLCanceledOrClosingDoesNotOpenStorage` | 取消/退出时不初始化数据库 |
| `TestMonitorPlaybackURLStorageFailureRollsBack` | 无清单的播放专项可初始化库；注入资源写入失败后地址一起回滚，并保留本地存储故障 |

本次测试使用隔离临时库和内存上游，原始自动日志保存于忽略目录 `artifacts/monitor-song-url-20261015/`。实机范围补充至 LA-042，未操作日常数据库、未启动游戏或修改 hosts。

自动执行记录（Windows amd64、Go 1.27.1、`CGO_ENABLED=0`）：

- 定向 `go test -tags=no_easytier ./internal/upstreamstate ./internal/console -run 'Test(PlaybackURLDelivery|MonitorPlaybackURL|MonitorLifecycleConsentAndStatus)' -count=1 -timeout=90s` 在常规本机权限下通过，见 `focused-host.log`。沙箱内控制台初始化遇到已有的 Windows `renameat` 权限/无效句柄限制，改用仓库内隔离 TEMP 仍复现，保留 `focused.log`；没有修改产品文件操作实现来绕过限制。
- 首轮三个包完整回归见 `packages-host.jsonl`：upstreamstate 160 个测试/子测试通过；cacheproxy 494 通过、8 跳过，既有 `TestCanceledSeekReleasesWorkerWithoutCancelingSharedReaders` 出现 `new seek stalled`；console 582 通过、5 跳过，既有 `TestQueueLatestSnapshotDedupAndRetry` 报 `latest queue was not prepared`。本次新增测试和扩展的自动监测集成测试全部通过，首轮失败记录保留。
- Seek 失败测试在相同隔离环境定向连续 5 次通过，见 `seek-repeat.log`；随后使用 `go test -tags=no_easytier -p=1 ./internal/cacheproxy ./internal/console -count=1 -timeout=180s -json` 串行复核，cacheproxy 495 通过、8 跳过；console 582 通过、5 跳过，仍只有 `TestQueueLatestSnapshotDedupAndRetry` 失败，本轮报 `future queue entry published early`，见 `packages-recheck-host.jsonl`。该结果不抹去首轮时序失败，也不等同实机通过。
- 对未修改 Git 基线 `eed5e7b98f22bb4f3cef00c5b6caadadb4636873` 导出的独立副本运行 console 整包，同一队列测试在 `queue_test.go:108` 复现 `future queue entry published early`；579 通过、5 跳过、1 失败，见 `console-baseline-full.jsonl`。基线单项一次及连续 10 次曾通过，当前工作区单项重复也有失败，分别见 `queue-baseline-complete.log`、`queue-baseline-repeat.log`、`queue-current-repeat.log`，不能用单项通过覆盖整包失败。未将这一已有队列问题纳入本次实现修复，完整 console 回归仍未全绿。
- `go vet -tags=no_easytier ./internal/upstreamstate ./internal/cacheproxy ./internal/console` 通过，见 `vet.log`；变更 Go 文件格式及 Git 空白检查通过。本次未运行 race 检查。

## 按保留分数优先补齐缺项

2026-10-15：新增 `SongMetadataCandidates` 读取已有歌曲映射与 URL/大小缺项，直接调用 `resourcePriorities`，与缓存容量淘汰及批量补齐使用同一评分。真实需求替换内置先验并按原 60 天半衰期衰减，共享 MD5 的所有歌曲分数相加；候选按资源分数、资源最大歌曲 ID、候选歌曲 ID 降序。只缺一侧节点地址也会入选，地址须匹配当前 MD5/API，非空且未标失败；大小须为正数。单纯超过播放复用期的 URL 仍算元数据已存在，避免高分歌曲每天重新填表而饿死低分缺项。

Monitor 在清单同步后、播放 API 请求前选择，每批仍用原请求探测一首。普通/候选通道与自动/手动检测共享逻辑；只测清单不选歌。未补齐候选在一遍内只试一次，全部剩余项尝试后再从最高分重试，失败/冲突歌曲不会一直挡住后续项；已齐项下一批自动跳过，全部齐备恢复原抽样。遍历记录仅在进程内，配置退休清空，重启按持久化缺项重排，无额外表或字段。空库/尚未建立引擎时仍走原抽样，不因选歌单独初始化库。

| 测试 | 验证内容 |
| --- | --- |
| `TestSongMetadataCandidatesUseRetentionPriority` | 共享资源分数相加、同分编号顺序、逐项完成后退出，已完成关联仍贡献共享分数 |
| `TestSongMetadataCandidatesUsePriorAndDecayedDemand` | 无需求时沿用内置先验，旧真实需求覆盖先验并衰减后改变顺序 |
| `TestSongMetadataCandidatesDetectMissingCurrentMetadata` | NULL/零大小、缺 cf/nya、空地址、失败地址、其他 API、界面节点名 hkg、旧 MD5 均缺项；齐备和单纯过期不缺，不新增需求/请求统计 |
| `TestPlaybackSampleUsesPriorityPassAndFallsBack` | 普通/双通道按 73→91→73 遍历失败项，再在齐备后回退原样本 42；HTTP 次数及样本缓存复用不变 |
| `TestPlaybackSampleSelectionAfterCatalogAndWithoutRemoteSample` | 清单交付后才选本地歌曲，远端清单全失败时已有本地候选仍能探测 |
| `TestPlaybackSampleSelectionCancellationAndCatalogOnly` | 取消/退休不选择，只测清单不查询，选择过程中取消不消耗遍历记录 |
| `TestMonitorMetadataBackfillFollowsRetentionOrder` | 通过 Console 实际启动自动监测，先补高先验 1981、下一轮补 5404、齐备后回抽样 42；两节点多通道 URL/大小逐步保存，总请求 14→18→22，后两次仅播放地址检测无新增正文采样 |
| `TestMonitorMetadataCandidatesDoNotOpenOrReviveStorage` | 无引擎时不建库，已打开库在取消/退出时不返回候选 |

自动日志保留在忽略目录 `artifacts/monitor-metadata-priority-20261015/`。本轮仍为开发验证，实机补充至 LA-042，未对日常库做回填、未启动游戏或修改 hosts。

执行记录（Windows amd64、Go 1.27.1、`CGO_ENABLED=0`，常规本机权限，隔离 TEMP 与模拟上游）：

- `go test -tags=no_easytier ./internal/upstreamstate ./internal/cacheproxy ./internal/console -run 'Test(PlaybackSample|SongMetadataCandidates|MonitorMetadata|MonitorLifecycleConsentAndStatus)' -count=1 -timeout=90s` 通过，见 `focused.log`。
- `go test -tags=no_easytier -p=1 ./internal/upstreamstate ./internal/cacheproxy ./internal/console -count=1 -timeout=180s -json`：upstreamstate 165 个测试/子测试通过；cacheproxy 508 通过、8 跳过，既有 `TestCanceledSeekReleasesWorkerWithoutCancelingSharedReaders` 再现 `new seek stalled`；console 584 通过、5 跳过，既有 `TestQueueLatestSnapshotDedupAndRetry` 再现 `latest queue was not prepared`。本次新增测试均通过，完整回归仍未全绿，见 `packages.jsonl`。沿用上节的 Seek 重复结果及队列未修改基线失败证据，本轮未修改两项行为，也未用重复运行隐藏失败。
- 同范围 `go vet -tags=no_easytier` 通过，见 `vet.log`；变更 Go 文件的 `gofmt`、`git diff --check` 通过，台账 43 个编号唯一。未运行 race；未执行 Git commit/push。
