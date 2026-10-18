# 全局块调度开发记录

本页按实现先后保留开发与自动检查记录，最初依据会话「全局调度器设计」（`01a13b63-0ffa-7ad2-918a-fe93a9f25c70`）实施，后续按讨论继续收敛，分批本地提交，不推送。

**阅读当前设计请先看[设计说明](block-scheduler-design.md)与[运行规则](download-concurrency.md)。** 截至第 36 节，已采用统一 T+M 启动窗口、完成事件更新 T、按可启动积压缓增，初始 T=3 秒、M=500 秒，M 可在设置中调整；第 34 节补全独立补齐的提前准入。下文早期的目标并发反推、两秒追赶、500 ms 采样、无收益回退、竞争暂停和旧参数都是历史阶段，不是当前约定。各次测试只证明其当时覆盖的实现；实机仍待 LA-040 / LA-041 / LA-043。

第 38 节修正 GET 的覆盖范围并删除固定读取窗口，替代第 27/37 节中“越过起点的块立即到期”“GET 覆盖全部缺块”“当前块及后面两块独立登记”的历史规则。游戏剩余场景及状态以现行[台账](live-acceptance-backlog.md)为准。

## 1. 独立调度核心

`internal/blocksched` 不解释视频、GET、队列或交接来源。每个内容块共享执行任务，各需求独立保存 `Owner / ReadyBy / LeaseUntil / Blocking`；撤销和有限交接在一次事件中完成。过期或关闭句柄不能被迟到更新复活。完成期限只影响排序，租约才决定存亡。

一个事件循环维护任务堆、租约堆和执行额度；等待 channel 或最近租约定时器。网络和收尾在有界执行 goroutine 中完成，收尾返回前不归还额度。同等阻塞按不透明 Owner 轮转，重复块共享任务。排队任务、需求记录有硬上限。

本提交尚未接入产品路径，不改变产品行为，因此不新增实机项。后续接入与并发控制继续更新 LA-037/038/039，并单列新模型验收范围。

自动验证：`go test ./internal/blocksched` 通过；覆盖独立租约、迟到更新、原子交接、重复任务、阻塞公平、正文/进度收尾前不释放、期限迟到不取消以及关闭排空。确定性时序使用 Go 标准库 `testing/synctest`，无需真实网络或游戏。

## 2. 全局并发控制

核心支持逐路开放、已有额度立即复用、空闲复位、最多两路阻塞应急启动。成本估算使用去重剩余字节和绝对期限，迟到使用 2 秒追赶窗口；活跃下载每 500 ms 合并采样，排队/暂停不计执行时间。增路后吞吐无明显收益时降低目标并冷却 5 秒。阻塞持续竞争时可选择性暂停非阻塞块，保留即将完成的小尾部；收尾完成才释放，恢复有冷却且不反复对同一阻塞重新发起。

本批仍未接入产品，不改变产品行为、不新增实机项。自动测试增加增长间隔、额度复用、空闲无积攒、竞争暂停/恢复、成本估算与无收益回退。参数是保守初值，实际 CPU 峰值、吞吐增益和音画效果须在产品接入后集中验收。

## 3. 产品接入及需求转换

`scheduled_download.go` 负责有限窗口、来源、实际 Read 等待与交接；`scheduled_range.go` 执行分块并复用既有部分文件、通道候选、强 ETag 及最终 MD5 边界。首请求与顺序回退也通过同一个调度器。原视频等级、按视频抢占以及固定 3+1 worker 已删除，文件会话只负责初始化/校验/发布和存储边界。参数及未知媒体时长的启发式决定见[当前并发规则](download-concurrency.md)。

新需求测试覆盖 GET 不等于 Blocking、多读者独立撤销、有限窗口、Seek、取消不续期、队列重排、固定交接与来源互不覆盖。旧 `demand_test`、`playback_grace_test`、`range_resume_startup_test` 的视频等级/固定 worker 断言由上述模型测试和 blocksched 的租约/公平/调度测试替代；原真实响应校验、正文关闭、部分前缀、失败通道、重定向及文件清理回归继续执行。整会话暂停测试改为明确触发暂停，验证存储契约，不再以“抢视频席位”作为前提；Seek 竞争夹具显式维持读取阻塞后再检查让位，不要求瞬时无条件取消。

接入中发现并修正：取得共享流后提前释放需求；执行上下文遗漏下载总期限导致多通道无法分配超时预算；完成块的工作退出事件与最后需求撤销并发时误判无人需要；暂停收尾的存储错误被取消状态遮蔽。新增/保留对应回归。

已执行：核心普通测试与 5 轮 Windows race；块需求、实际 Range/Seek、暂停恢复、实体变化/错误 Range/损坏正文、弱 ETag、候选失败及重定向、关闭清理定向测试通过。四项接入回归连续 3 轮通过；完整 `go test -race ./internal/cacheproxy -count=1 -timeout=120s` 通过（27.249 s）。全仓检查在后续收尾记录中单列。

未启动游戏、未修改 hosts；LA-040 新增，LA-037/038/039 继续积攒。原始本地检查输出保留在忽略目录 `artifacts/block-scheduler/`，不将其当作实机证据。Windows 沙箱首次运行的默认临时目录重命名、硬链接/junction 权限错误属于测试环境限制；改用隔离临时目录并在正常权限下复查，不以修改产品代码绕过这些限制。

## 4. 资源边界与检查收尾

删除 HTTP 预算中的第二套优先级队列，只保留覆盖 Body.Close 的生命周期守卫。调度核心维护活动集合与需求计数，恢复冷却使用索引堆，成本汇总/排序每 500 ms 执行一次；清理变更来源留下的公平性记录。有限 Seek 窗口到期后回收任务，不在一个长连接内积累所有历史位置。资源窗口从已完成前缀向前推进，不再每次复制完整块表。实际执行复用有界分块缓冲；重试丢弃的样本不用于供数增长，顺序回退修正为全文件成本。

暂停检查点最多 32 个、文件长度合计 512 MiB，按最早暂停顺序淘汰；上限独立于正式缓存预算与活动文件。旧读取器仍通过引用计数持有文件。全仓集成检查发现队列取消测试仍要求换队首立刻取消旧视频，现改为确认来源替换/解析不等待交接、释放竞争后新队列完成；让位由块期限和持续竞争决定。同时修复有限交接可能将远期期限提前的问题：在原期限与当前时间的较晚者上一次延后，租约仍只保留退出后 2/20 秒；尚无字节的请求不误认作接近完成的小尾部，持续期限竞争后可以让位。

新增计数/来源清理、任务/需求容量、顺序成本、重试样本及暂停上限/旧读者保护回归。测试夹具中原 HTTP 优先级断言由块调度测试替代，保留请求取消、正文关闭和恰好一次释放的回归。历史可选 CPU 实验仅适配新的调度入口，本轮未重新运行该实验。

本机 Go 1.27.1、Windows amd64，单次需求更新基准（1 秒采样）如下；这是开发开销测量，不是游戏帧时间或真实吞吐结论：

| 排队块数 | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| 16 | 1047 | 233 | 4 |
| 1024 | 1065 | 233 | 4 |
| 4096 | 1047 | 233 | 4 |

检查范围使用受版本控制的 Go 源码根目录 `./cmd/... ./internal/... ./data/... ./experiments/...`，与干净工作区的全仓包范围对应。当前忽略目录 `artifacts/` 中有历史故障探针、旧源码副本和临时构建文件，直接 `./...` 会误编译它们；保留首次失败输出，随后按受控源码范围重跑。脚本测试需在正常权限的隔离目录创建临时 Git 仓库。

已通过 `go vet -tags=no_easytier`、`CGO_ENABLED=0 go build -tags=no_easytier`（上述全部源码目录），及 `node --test scripts/*.test.mjs` 的 246 项测试。全仓普通测试通过（`final-tests.log`）；全仓 Windows race 通过：17 个测试包、1,266 个测试/子测试通过，16 项按条件跳过，无 race 报告；原始记录为忽略目录内的 `final-race.jsonl`。收尾又清除了空闲期间残留的已排空采样累计量，避免它混入下一次启动的吞吐窗口；核心完整 race 追加 5 轮通过。所有游戏、实网吞吐/CPU 对照及发行打包仍延期；未改 hosts、未启动 VRChat、未 push。

来源规则复核：原讨论最后一段补充了 GET 对交接的属性覆盖、队首/普通队列不同撤销行为及撤销原因。本批基础接入尚未完整表达这三项，下一独立变更在需求管理层补齐，不能仅凭上述测试宣布整个来源策略完成。

## 5. 来源规则复核与最小交接状态

需求管理层现在明确区分 GET、队首、普通队列与补齐，独立保存来源和有效期。先应用 GET 对交接的调度属性覆盖，再汇总 Blocking、最早有限 ReadyBy 和存活上限；核心仍不解释来源。交接即使被覆盖，也按自己的 LeaseUntil 到期；有效队列需求仍独立参与。资源计时器按最近的单份租约到期重新汇总，不把摘要的最晚租约误作所有来源的期限。

撤销原因参与转换：GET 取消/关闭保留 2 秒，队首消失保留 20 秒；普通队列删除直接撤销，队首与普通项互换原地更新；读取推进/Seek、块完成和关闭引擎不生成交接。一次块更新先处理全部来源变化，再原子更新调度摘要，避免 GET 已到达时旧交接仍暂时贡献紧迫性。每块活跃来源与交接合计最多 128 份。

按用户最新澄清，交接运行时不保留原始来源。`handoffContribution` 所属集合本身表示 Source=交接，只保存已算好的需求时间和非阻塞属性；来源及原因在转换日志 `block_demand_handoff` 中记录。交接不产生交接、不按原始来源续期。`block_demand` 记录各活跃来源数量、交接覆盖状态、汇总属性和最近到期时间。

队列来源在连续成员关系内保持标识，重排更新属性；移除后重新加入或重置会生成新标识。开始异步地址解析前捕获该标识，迟到结果不能重新登记成新来源。新增回归覆盖 GET 覆盖但不删除/续长交接、独立队列期限、每份租约独立到期、不同来源/撤销原因、队列升降、迟到解析结果和来源容量上限。

定向 race 曾发现首次响应初始化 `ranges` 与撤销判断之间的并发访问；已通过初始化完成 channel 同步访问，保留首次失败日志 `source-final-focused.log`，修复后来源/交接/队列相关测试连续 20 轮通过（`source-final-focused-2.log`）。最终对全部受控 Go 源码目录运行 `go test -tags=no_easytier -race ... -count=1 -timeout=180s -json`：17 个测试包、1,279 个测试/子测试通过，16 项按条件跳过，无 race 报告（`source-final-race.jsonl`）；最终 `go vet` 和 `CGO_ENABLED=0 go build` 同范围通过。命令范围与本地日志存放规则同上一节；脚本未改，沿用已通过的 246 项脚本测试。

本批更新 LA-040 的来源转换、覆盖、固定到期和迟到结果验证步骤及证据要求。开发实现完成；真实游戏、实网吞吐/CPU 对照及发行打包仍待后续安排，没有将自动回归标记为实机通过。

## 6. 完成审计与职责收尾

重新读取原讨论及最新“交接不保存原始来源”的澄清，按当前代码核对以下要求。前三批核心曾提供 `Demand.Close(handoff)`，产品接入后已通过需求层汇总并原子 Update，未使用该分支；本批删除核心的交接参数及自动清除 Blocking 的策略，只保留通用 Close。交接创建、覆盖及转换日志全部归需求管理层，避免两处重复实现规则。队列重排回归还修正了旧字符串键读取零值的问题，现在明确检查来源存在、普通队列变为队首、ReadyBy 精确提前一个位置间隔。

| 要求 | 当前实现与自动验证依据 |
| --- | --- |
| 调度器不解释视频/来源；事件驱动、局部更新 | `scheduler.go` 的单事件循环、索引堆、Watch 通知和有界执行任务；`control.go` 仅按需设定时器与合并采样；`BenchmarkDemandUpdate` 记录更新成本 |
| 完成期限与存活分离、独立撤销、旧更新不能复活 | `TestIndependentLeasesAndLateUpdates`、`TestDeadlineDoesNotCancelAndCloseDrains`；需求管理层按最近独立租约到期重新汇总 |
| Blocking 只表示真实缺块等待；同块共享、多读者独立 | `stream.go` 的 Read 等待登记/释放；`TestBlockDemandFollowsActualWaitAndIndependentReaders`、`TestSharedExecutionFairBlockingAndReleaseAfterCleanup` |
| 来源过滤、最小交接状态与固定时限 | `block_demands.go`、`scheduled_download.go`；`TestSourceFilteringKeepsIndependentLeases`、`TestSourceAndWithdrawalReasonDetermineHandoff`、`TestQueuePromotionDemotionDoesNotCreateHandoff`、`TestReadProgressAndCompletionDoNotProduceHandoff`、`TestHandoffDefersFutureDeadlineWithoutExtendingValidity` |
| 队列只提供顺序，异步旧结果不能复活来源 | `demand.go`、`song_load.go` 的代次校验；`TestBlockQueueOrderAndFiniteHandoff`、`TestQueueSourceIdentityOnlySurvivesContinuousMembership`；时间估计的不确定性在当前规则中明确说明 |
| 有界渐增、额度复用、供数估算与无收益回退 | `control.go`；`TestRampReuseEmergencyAndNoIdleCredit`、`TestDeadlineCostEstimateAndNoGainRollback`、`TestRetriedBytesDoNotInflateSupply` |
| 竞争暂停、收尾后释放、恢复冷却及内容一致性 | `TestContentionPausesProgressWithoutCancelStorm`、`TestFirstReaderWaitYieldsExistingPrefetch`、`TestSeekYieldsAndResumesPartialBlocksOnTheirExistingChannels`、`TestSessionPauseResumesValidatedRanges`、`TestPreemptionDoesNotTrustFullBlockWithoutEOF`；响应校验与完整 MD5 发布仍由资源层执行 |
| 元数据、候选窗口、缓冲与暂停文件有界 | `TestResponseCostDiscoveryAndMetadataCapacity`、`TestBlockSourceMetadataIsBounded`、`TestPausedCheckpointBoundsPreserveReaderReferences`；32 个资源会话和最多 6 份执行缓冲由 Server 固定上限管理 |

本批删除的是产品未使用的核心接口分支，并加强已有回归，不新增产品运行行为；实机范围继续为 LA-040。用户补充的“队首消失至正式开播时长”另以纯文档提交登记 LA-041，区分队列事件、GET 到达与游戏实际开播，并要求报告轮询/日志时间误差。历史验收项与报告保留，没有因开发完成而结项。

审计核对第五批完整 race 日志，表内对应的 26 个测试入口均有通过记录。接口收尾后追加核心完整 race 20 轮通过（`audit-core-race.log`），缓存完整 race 通过（29.238 s，`audit-cache-race.log`），全部受控源码根目录的 `go vet -tags=no_easytier` 与 `CGO_ENABLED=0 go build -tags=no_easytier` 通过。LA-041 文档检查确认 41 个台账编号均唯一；未为文档变更重复运行产品测试。全部提交仅留本地，游戏、hosts、GitHub 远端均未操作。

## 7. 16 路软上限与 Blocking 立即放行

按用户新规则，核心配置改为 `SoftLimit`，产品取 16。非阻塞需求仍遵守开放额度、250 ms 缓升和无收益回退；Blocking 队列头直接启动，不检查软上限、开放额度或恢复冷却。超限执行不增加普通请求的开放额度，实际执行数超过开放额度时普通新请求等待回落，已有请求仍按生命周期收尾。移除旧独立后台上限及“最多两路应急”策略，保留任务/需求元数据容量限制与共享去重。

原 HTTP `requestBudget` 改为 `requestActivity`，只统计每次尝试从发起到 Body.Close 完成的生命周期，不再排队或限制并发，避免 Blocking 被第二层额度阻挡。重试与重定向仍在所属块执行内；失败尝试释放计数、部分恢复与响应校验规则保持。复用池最多保留 16 份缓冲；Blocking 超限执行按需分配额外缓冲，归还时不扩大池。历史可选 CPU 探针的并发参数现表示软上限，不把旧 6/4 的实验结论外推到新规则。

新增确定性回归覆盖 20 个 Blocking 在普通额度占满时同一时刻启动、同块重复读者去重、超限结束不留下额外普通额度、普通任务逐路扩至 16、已暂停任务改为 Blocking 不等冷却。真实本地 HTTP 回归由 20 个不同缺块读取和一个重复读取触发请求，确认 HTTP 活动数突破 16，底层没有硬额度。原公平性测试改为验证同等非阻塞期限；Blocking 不再排队争用固定名额。失败候选恢复测试改在下一通道等响应头时暂停，保留不重访已失败通道的断言；最初适配时发现错误包含暂停原因而非排队取消，修正断言后重跑，没有修改产品错误语义。

LA-040 同步更新超过 16 个缺块的立即启动、普通请求恢复、缓冲与 CPU 峰值证据要求；LA-041 的开播空挡观测继续积攒。未启动游戏、未调整 hosts，真实体验仍待集中验收。

自动验证：核心及缓存完整 race 通过（416 个测试/子测试，8 项条件跳过）；全受控源码目录 `go test -tags=no_easytier -race ./cmd/... ./internal/... ./data/... ./experiments/... -count=1 -timeout=180s -json` 通过，17 个测试包、1,283 个测试/子测试通过、16 项条件跳过，无 race 报告（`soft-limit-all-race.jsonl`）。最终边界及 HTTP/候选恢复定向测试连续 10 轮 race 通过（`soft-limit-final-focused.log`），覆盖最后调整的单 Blocking 场景峰值断言；`go vet` 与 `CGO_ENABLED=0 go build` 对同一全部源码范围通过。日志仍仅留忽略目录，自动检查不替代实机验收。

## 8. 完成事件后的初始化复核

核实当前 HEAD 仍会使用首轮初始化快照判断已完成任务；合法的“快照为 false → 206 初始化并完成首块 → 协调器观察 Done”会被当作顺序 200 已发布。将完成任务回收集中到 reapCompleted，并在 Done 之后重新读取初始化完成 channel，确保余块规划、整文件 MD5 与正式发布继续执行。没有新增执行状态或更改顺序 200 的发布流程。

确定性回归先在保留原判定的提取代码上失败（first_range_only 返回 published=true、initialized=false），修复后通过，并确认余块进入计划；顺序 200 对照仍返回已发布。完成判定与 Range/Stream 定向 race 连续 3 轮通过（review-completion-race.log），覆盖响应拒绝发布及读取错误路径。LA-040 增加快速首块、多块完整发布和整段播放的步骤/证据，真实游戏继续待验收。

## 9. 暂停判断读取即时有效进度

当前实现的 200 ms 竞争事件早于下一次 500 ms 供数采样，旧 remaining 的确会误取消小尾部及等待 EOF 的块。Progress 现在独立统计当前尝试的有效接收量；丢弃正文时清零，累计流量和无效样本标记保持原用途。选择暂停对象及同等紧迫性的剩余量比较读取即时值和实际响应大小，供数采样节奏不变。

回归先复现 small_tail、awaiting_eof、retry_tail 三项失败（review-progress-before.log）；修复后核心全部 race 连续 10 轮通过（review-progress-race.log）。另覆盖大尾部、顺序 200 成本发现、重试等待响应头可让位。LA-040 增补采样间快速接收、延迟 EOF、重试/大尾部对照及证据要求；真实播放尚未验收。

## 10. 队列共同时间基准与撤销时的来源

两项队列反馈在当前实现均成立。确定性虚拟时间回归复现运行 75 秒后的第三首旧期限 70 秒优于新队首 85 秒，以及普通项升为队首再消失漏交接、队首降为普通项再消失错误保留交接（review-queue-before.log）。

队列成员对象替代原位置/代次两张表，对象身份代表连续成员关系；实际队列变化统一更新所有成员的期限基准，相同快照不滑动。移除时冻结成员的最终位置和移除时刻，现有块需求通过已有引用完成转换，不建立事件日志队列或全局历史集合。交接以移除时刻计算 20 秒，延迟处理不延期；过期交接立即过滤，转换后仍只保存通用需求，原始来源只写转换日志。旧异步结果仍通过成员身份检查拒绝，移除重加/重置不会复活旧来源。

新回归同时核对未变动第三项重算、重复快照稳定、通知合并后的正反转换、移除时间起算、处理前过期及恰好一次交接。队列来源/升降/新回归及首块完成测试连续 20 轮 race 通过（review-queue-race.log）；缓存完整 race 通过（review-cache-console-race.jsonl）。LA-040 同步增加这些步骤与证据，LA-041 时长观测保留；真实游戏继续待验收。

## 11. 配置说明同步与审查修复收尾

确认 MaxDownloads 现仅用于 localChecks 池。保留兼容参数名，命令行帮助和配置注释明确其为本地文件验证并发；cache_engine_ready 改记 max_local_validations，并分别记录 max_resource_sessions=32、video_request_soft_limit=16。服务文档同步移除旧资源级 3/2 席位及抢占说明，引用当前全局块规则。直接运行 CLI -help 成功（review-cli-help.log），不会启动服务或游戏；LA-040 增补启动参数/日志核对，不新增编号。

首次缓存/控制台联合 race 中，缓存全部通过；控制台的 Portable 迁移测试一次遇到 Windows 临时目录重命名 Access is denied，未涉及队列或下载断言（review-cache-console-race.jsonl）。保留失败输出，未修改该产品路径或测试规避错误；同一测试隔离连续 10 轮 race 通过（review-console-move-recheck.log）。

最终全部受控 Go 源码目录 go test -tags=no_easytier -race ./cmd/... ./internal/... ./data/... ./experiments/... -count=1 -timeout=180s -json 通过：17 个测试包、1,298 个测试/子测试通过、16 项按条件跳过，无 race 报告（review-final-race.jsonl），包含上述 Portable 迁移场景。本轮最终日志也确认三个启动上限字段实际输出正确。全部同范围 go vet 与 CGO_ENABLED=0 go build 通过（review-final-vet.log、review-final-build.log）；脚本及前端未修改，不重复运行其历史已通过的测试。台账 41 项编号唯一，LA-040 更新、LA-041 保留待验收。四批修复均仅本地提交，未 push、未启动游戏或改 hosts。

## 12. 最后一块完成与空计划的交错

当前代码在生成计划前检查完整性，最后一块随后完成会被计划过滤，空计划分支误返回 errPreempted。受控需求上下文仅在测试中设置同步屏障，令真实块执行恰好在计划检查需求时完成；原实现确认 1 MiB 已下载却以 canceled 结束、读取截断且正式文件不存在（last-range-before.log）。同一交错下的损坏正文也被取消分支抢先结束，未进入整文件校验。

完成检查移到计划生成后、撤销块需求前，并重新观察初始化 channel。完整范围先等待执行任务结果收割，再进入原 MD5/发布流程；空计划只有在内容尚未完整且没有有效需求时才退出。未加运行时测试钩子或额外状态。合法正文及等长损坏正文的确定性交错、首块初始化回归、多块请求共享、冷流读取及响应拒绝发布回归，连续 20 轮 race 通过（last-range-race.log）。合法流完整读取并发布；损坏流明确返回完整性错误且无正式文件。LA-040 同步补充最后一块的步骤和证据，真实游戏继续待验收。

## 13. 首次请求的候选游标跨暂停保留

当前首次请求由 openUpstream 每次创建新 resourceAttempts，初始化前暂停后会从头重试。新增回归让次通道连续两次停在响应头等待阶段，原实现的已失败首通道被调用三次，重定向场景也重走三次（initial-open-before.log）。传输失败、503、无效 Range 响应和合法重定向四种前置条件均复现。

资源会话仅在首次响应头尚未被接受时持有 opening 游标；暂停复用现有 resume 规则，保留被暂停的通道及已验证地址，清除旧执行上下文，下一次执行重新绑定自己的上下文/期限。有效响应到达后由原 resourceBody/首块状态承接，会话释放该引用。成功重定向和拒绝的响应头明确消耗原通道，避免恰在转场时暂停又回退。未新增路由排名、独立重试机制或跨会话缓存。

新增首次请求回归及既有候选预算、失败通道跳过、响应头前暂停和 Seek 部分续传回归，连续 20 轮 race 通过（initial-open-race.log）。断言连续两次暂停后失败通道仍只调用一次，当前通道三次，成功重定向一次；首块字节正确，全部 HTTP 活动计数收尾归零。LA-040 联合 LA-038 增加相应步骤及证据要求，实机继续积攒。

两项修复后的最终全受控源码检查通过：go test -tags=no_easytier -race ./cmd/... ./internal/... ./data/... ./experiments/... -count=1 -timeout=180s -json，17 个测试包、1,306 个测试/子测试通过、16 项按条件跳过，无 race 报告（completion-open-final-race.jsonl）。同范围 go vet 与 CGO_ENABLED=0 go build 均通过（completion-open-final-vet.log、completion-open-final-build.log）。台账 41 个编号保持唯一；两项修复分开本地提交，未 push、未启动游戏或修改 hosts，LA-040/041 仍待实机验收。

## 14. 无需求时清除竞争计时

核实当前版本仍会在目标并发归零时提前返回，留下尚未触发的竞争时间。新增真实调度事件循环的虚拟时间回归：后台与 Blocking 需求建立竞争后，两份租约在 100 ms 同时到期；旧实现仍留下 100 ms 后的唤醒，越过原 200 ms 竞争时刻会反复使用过期时间。回归在越过该时刻前检查并明确失败，避免以测试超时替代诊断（`idle-contention-before.log`）。

目标并发归零时同步清除竞争计时，保留活动任务的正常收尾采样。修复后空闲调度器不再安排控制唤醒，跨过原竞争时刻仍保持空闲；核心完整 race 连续 20 轮通过（`idle-contention-race.log`）。LA-040 增补竞争后快速清空的 CPU 与唤醒观测，真实游戏继续待验收。

## 15. GET 交接从来源退出时刻计算

GET 与读取窗口此前只在协调器处理来源消失时计算交接，确实会把处理延迟加到 2 秒窗口。新增虚拟时间回归分别取消上下文或主动关闭 GET/读取器，延迟 1 秒、3 秒再合并需求；旧代码的八个场景全部失败，3 秒延迟处理时仍取得新的 2 秒交接（`get-withdrawal-before.log`）。

原需求和读取游标现在各自保存一次性的退出时刻：退出回调先记录再获取登记表锁，规划先观察到取消时也可记录；读取器 Close 同步释放游标，不依赖异步回调完成。重复清理不改写时刻。协调器据此计算 ReadyBy 和 LeaseUntil，处理前已经过期的交接直接过滤。队列原有移除时刻和来源判断保持；交接仍只保存通用需求，不增加原始来源或退出历史，转换日志增加 withdrawn_at 便于核对处理延迟。

GET 延迟处理、重复关闭、固定到期、队列升降/合并通知、正常读取推进与最后一块完成回归连续 20 轮 race 通过（`get-withdrawal-race.log`）。最后一块的定向交错仍明确触发并通过完整读取/MD5/正式发布及损坏正文拒绝发布断言。LA-040 同步补充退出和处理时刻、固定到期的步骤及证据；LA-041 的队首消失到正式开播观测保留，实机继续积攒。

两项修复后的最终全受控源码检查通过：`go test -tags=no_easytier -race ./cmd/... ./internal/... ./data/... ./experiments/... -count=1 -timeout=180s -json`，17 个测试包、1,316 个测试/子测试通过、16 项按条件跳过，无 race 报告（`idle-exit-final-race.jsonl`）。同范围 `go vet` 与 `CGO_ENABLED=0 go build` 通过（`idle-exit-final-vet.log`、`idle-exit-final-build.log`）。台账 41 个编号保持唯一，两项修复分别本地提交；未 push、未启动游戏或修改 hosts，LA-040/041 仍待实机验收。

## 16. 退出会话在收尾前停止接纳

在资源释放回调中延迟旧会话收尾，206 分块和 200 顺序回退均复现新 GET 附着旧会话、随后读取 0 字节并收到取消错误（flight-exit-before.log）。当前退出标志确实设置过晚。

需求合并先计算全部块的存活情况，再决定是否退出，最后执行任务撤销；空计划的决定在 Server.mu 下核对来源通知代次并设置既有 preempted 标志。新 GET 若已登记，旧快照重新规划；若退出已经确定，则沿用 obtain 的等待路径，收尾后才创建或恢复会话。顺序下载暂停也在正文关闭前停止接纳，退出清理仍保留本地故障优先级。没有新增交接来源字段或并行同资源会话。

新增收尾间隙与空计划后到达的确定性交错回归，并联测 GET 固定交接、队列转换、最后一块发布和完整会话续传，连续 3 轮 race 通过（flight-exit-race.log）。首次普通验证使用沙箱默认临时目录，正式文件重命名被 Windows 拒绝（flight-exit-focused.log）；改用工作区隔离临时目录后上述完整发布断言通过，没有修改产品文件操作规避错误。LA-040 联合 LA-038 增补接纳/等待边界和实际播放证据，实机继续积攒。

## 17. 未到期期限使用真实剩余时间

旧估算把未来不足 2 秒的期限也扩为追赶窗口，回归复现 4 MiB / 1 MiB/s 在剩余 1 秒时被估为 2 路，0.5 秒和 1.5 秒场景也被低估（deadline-horizon-before.log）。现在只对 ReadyBy 已到或已过的任务使用 2 秒追赶窗口，未来期限使用正的真实剩余时间；浮点需求先限于软上限再转整数，避免极近期限/大剩余量溢出后反而低估。

新增七个边界场景覆盖 0.5/1/1.5 秒、恰好到期、已超时、软上限与近零正时间的大剩余量；核心全部 race 连续 20 轮通过（deadline-horizon-race.log）。供数采样、普通请求缓升与无收益回退规则保持，估算目标不冒充吞吐保证。LA-040 补充目标估算与实际开放额度的对照证据，实机继续积攒。

## 18. 顺序暂停保留本地写入错误

新增确定性交错：200 顺序正文在 contextReader 已检查上下文之后触发暂停，等待取消传到请求，再返回字节并使本地文件写入失败。旧实现将写入错误替换为 errPreempted，StorageError 仍为空（sequential-pause-before.log）。

顺序执行现在只把 context.Canceled 或 blocksched.ErrPaused 错误链转换为会话暂停，保留同时发生的存储及完整性错误。原任务收尾的错误优先级与 prepare 的存储分类继续生效，不另建故障分类机制。回归确认原始 os.ErrClosed 和 ErrLocalStorage 同时保留，当前引擎熔断且后续冷下载不再发起上游请求；正常取消对照仍返回暂停，不误熔断，失败正文没有正式文件。新增交错及既有创建/写入/发布/上游/完整性错误分类回归连续 20 轮 race 通过（sequential-pause-race.log）。LA-040 联合 LA-010 补充故障重合、后续请求与实际体验证据，真实游戏继续待验收。

本轮三项修复后的全受控源码检查通过：go test -tags=no_easytier -race ./cmd/... ./internal/... ./data/... ./experiments/... -count=1 -timeout=180s -json，17 个测试包、1,331 个测试/子测试通过、16 项按条件跳过，无 race 报告（flight-deadline-pause-final-race.jsonl）。同范围 go vet 与 CGO_ENABLED=0 go build 通过（flight-deadline-pause-final-vet.log、flight-deadline-pause-final-build.log）。台账 41 个编号保持唯一。三项修复分开本地提交，未 push、未启动游戏或修改 hosts；LA-040 联合 LA-010/038 及原 LA-041 观测继续待实机验收。

## 19. 持续 Blocking 阻止后台补位

核实 dispatch 只检查开放额度，旧后台让位后新后台确实能补位。启用竞争让位时，普通请求准入同时检查是否仍有 Blocking；已有后台保留 200 ms 让位节奏和小尾部保护，Blocking 启动及晋升仍立即放行，最后一份 Blocking 撤销后恢复普通准入。不增加新的计时或竞争状态。

新增产品参数回归：16 路后台和 8 个排队块遇到持续 Blocking，1.2 秒内恰好暂停 6 块、没有启动任何替补后台，总在途降至 11；Blocking 撤销后普通请求恢复。核心全部 race 连续 10 轮通过（blocking-admission-race.log），涵盖 Blocking 突破软上限、晋升绕过冷却与普通渐增。LA-040 增加持续竞争期间的请求时间线和实际播放证据，实机继续积攒。

## 20. 下载末尾完整校验共用本地额度

分块下载完成后的 Seek 与整文件 MD5 现在由 checkDownloadedFile 取得 Server.localChecks 额度，和已缓存文件验证共用 MaxDownloads；校验结束立即归还，正式发布不占校验额度。排队响应上下文取消，成功、Seek 失败及校验失败均通过 defer 释放。

新增完整分块文件的收尾回归：占满全部额度时不能校验/发布，等待超时不能释放别人的额度；释放竞争额度后新会话可校验并发布完整内容，损坏正文拒绝发布且不泄漏额度。联测已有缓存验证取消和最后一块完成竞态，连续 10 轮 race 通过（final-validation-capacity-race-2.log）。提取时遗留未使用 import 的首次编译失败日志保留，移除后重跑通过。LA-040 增加校验并发、排队取消和播放尾段证据，实机仍待验收。

## 21. 完整校验区分上游完整性与本地读取故障

checkDownloadedFile 仅将 errInvalidCache（大小或 MD5 不匹配）标记为上游完整性失败，Seek/Stat/Read 的本地错误和上下文取消原样返回，由 prepare 的统一分类决定存储熔断。避免文件读取故障被 errUpstreamDownload 包装后绕过熔断。

确定性故障上下文在最终校验完成 Seek/Stat 后、第一次文件读取前关闭句柄，确认 os.ErrClosed 和 ErrLocalStorage 保留，StorageError 记录故障且拒绝后续冷下载；另覆盖 Seek 失败、等长损坏正文及读取前取消。全部路径拒绝发布且归还校验额度。联测原顺序暂停、发布健康与存储熔断回归，连续 10 轮 race 通过（final-validation-errors-race.log）。LA-040 联合 LA-010 增补本地故障及完整性对照，实机继续积攒。

## 22. 并行收尾优先保留存储故障

资源会话仍先撤销全部任务，再逐个等待收尾；提取 close 统一处理生命周期，并用同一存储错误判定为最终错误选定优先级。已发现的本地错误不能被后续上游失败或取消覆盖，后到的本地错误可替换上游失败；不把上游标记 Join 回本地错误链，防止调用方错误地允许线路回退。正常取消、超时及核心暂停/关闭均不单独触发存储熔断。

新增确定性错误顺序排列，覆盖本地错误先/后于上游、取消、超时、关闭、暂停和容量错误；另用真实分块读取在取消时保存 64 KiB 前缀，注入文件关闭故障，同时让另一个任务在收尾时返回上游失败。确认两种故障均发生，最终保留 os.ErrClosed、记录 ErrLocalStorage 并拒绝下一个冷下载。联测并行正文关闭、完整校验和顺序暂停故障，连续 10 轮 race 通过（cleanup-error-priority-race.log）。LA-040 联合 LA-010/038 增补重合故障证据，实机继续积攒。

本轮四项修复后的全受控源码检查通过：`go test -tags=no_easytier -race ./cmd/... ./internal/... ./data/... ./experiments/... -count=1 -timeout=180s -json`，17 个测试包、1,341 个测试/子测试通过、16 项按条件跳过，无 race 报告（admission-validation-cleanup-final-race.jsonl）；全部 10 个新增测试/子测试均有通过记录。同范围 `go vet` 与 `CGO_ENABLED=0 go build` 通过（admission-validation-cleanup-final-vet.log、admission-validation-cleanup-final-build.log）。台账仍为 41 个唯一编号，四项修复分别本地提交，未 push、未启动游戏或修改 hosts；LA-040 联合 LA-010/038 与 LA-041 开播空挡观测继续待实机验收。日志只留本地忽略目录。

## 23. 无需求任务有限保留与自动回收

核实原实现仅暂停执行，任务留在 jobs 中直至调用方 Close；租约兜底没有释放 MaxTasks。现在没有有效需求的任务保留 30 秒，包括提交后从未 Add 的任务；保留期内新有效需求取消回收计时，再次归零才开始新的保留期。重复提交、无效需求更新及暂停收尾均不能续期。到期进入退休状态并拒绝新需求，沿用既有收尾协议，执行器返回后关闭 Done/通知 Watch 并释放任务容量；显式 Close 和引擎关闭立即退休。收尾的真实错误优先于取消，未完成的执行器不提前释放容量。

固定期限用标准库链表按进入无需求状态的顺序管理，重接/完成常数时间移除；期限并入原事件循环的单一定时器，不扫描全部任务、不增加任务级 goroutine。处理命令前也检查到期队列，防止命令与定时器同时就绪时迟到重接越过退休边界。该保留只涉及核心元数据，不延长来源租约、交接或上游执行许可，也不增加原始来源字段。

确定性虚拟时间回归覆盖容量为 1 的租约到期/主动撤销/从未添加需求、保留期前后边界、重复提交不续期、Watch 完成通知、旧句柄不能影响同键新任务、有效重接取消旧计时、其他孤儿独立到期、Blocking 与长冷却中的排队/暂停任务回收，以及延迟执行收尾和真实错误保留。核心完整 race 连续 20 轮通过（orphan-retention-core-final-race-2.log）。核心与缓存完整 `go test -tags=no_easytier -race ./internal/blocksched ./internal/cacheproxy -count=1 -timeout=120s -json` 通过，481 个测试/子测试通过、8 项按条件跳过，无 race 报告（orphan-retention-integration-race-2.jsonl）。首次缓存回归的 Windows 硬链接、目录联接和相关子进程受沙箱权限拒绝，失败日志 orphan-retention-integration-race.jsonl 保留，使用正常文件权限重跑同一范围通过，未修改产品行为绕过检查。

全部受控 Go 源码目录的 `go vet -tags=no_easytier` 和 `CGO_ENABLED=0 go build -tags=no_easytier` 通过（orphan-retention-vet.log、orphan-retention-build.log）。台账仍为 41 个唯一编号；本次补充 LA-040 的管理层失联、任务容量回收和实际播放证据，LA-041 观测继续保留。只提交本修复及对应文档，其他研究文件/脚本不属于本次变更；未 push、未启动游戏或修改 hosts，实机继续积攒。

## 24. Blocking 绕过准入，已有下载自然完成

按用户更新的独立带宽假设，准入统一为 need.Blocking || active < open。删除“有 Blocking 就禁止普通准入”、优先级竞争选择暂停对象、200 ms 竞争计时、500 ms 恢复冷却及其等待堆；产品与实验夹具移除对应配置。Blocking 计入总 active，只有执行器完成正文关闭和全部收尾后才扣减，已经运行的同块继续共享。目标降低仍由供数/期限估算控制，但只影响后续补位；普通排队任务按 ReadyBy 排序。需求归零、租约到期、显式任务关闭及引擎关闭的取消/前缀保存/清理、30 秒孤儿回收继续保留。

新增虚拟时间回归覆盖 open=4、4 路普通加 2 路 Blocking、active=4 不补位/降到 3 才补位、Blocking 存续期间可复用空额度、关闭收尾期间仍计数、排队块晋升立即执行、身份变化共享已有执行、紧迫普通期限不能抢占在途，以及目标下降自然完成。原竞争暂停与小尾部保护断言删除；空闲计时与孤儿回收仍保留，暂停夹具改为需求撤销触发。HTTP 首读和 Seek 回归改为保持 Blocking 超过旧阈值，确认已有部分响应未关闭、没有重复 Range，随后全文件校验和发布正确。取消后的前缀、失败候选、重定向和响应有效性继续由现有会话恢复及首次请求恢复回归覆盖。新策略与关联生命周期定向 race 连续 3 轮通过（natural-admission-focused-race.log）。

LA-040 主项及服务/队列/并发说明同步到新规则；历史竞争暂停要求明确标记由本轮取代，保留历史记录和 LA-041。开发分别本地提交准入策略、完整回归发现的会话取消边界，以及只服务旧暂停选择的冗余统计状态清理。真实网络带宽假设、吞吐与音画效果仍待实机验收；未启动游戏、未修改 hosts 或 push。

## 25. 整会话取消不再返回块级可恢复暂停

移除恢复冷却后的首次全源码 race 回归发现：资源上下文已取消，执行器保存前缀后仍返回 ErrPaused；协调器尚未撤销调度需求时，核心会立即重启该任务。异步传播已取消的资源上下文前，额外 HTTP 尝试可能多读一段前缀。旧会话恢复测试捕获额外字节，另一个夹具随后因重复启动阻塞而超时；原失败日志 natural-admission-final-race.jsonl 保留，无数据竞争报告。

执行入口现在直接拒绝已取消会话；执行退出仅将“块可恢复暂停”转换为已取消会话的终态原因，保留存储错误和保存好的检查点。活跃会话中的块需求撤销仍可恢复。确定性回归刻意保留核心需求，分别在响应头前和首段正文后取消资源，确认执行一次、HTTP 一次、Task.Done 完成、后续迟到执行不发请求，正文检查点准确保存。新回归及原完整会话恢复、候选恢复、顺序本地写入错误、并行收尾错误回归连续 20 轮 race 通过（session-terminal-cancel-race.log）；新增的执行次数断言再连续 20 轮通过（session-terminal-cancel-final-race.log）。

第二次全源码回归中，旧 Seek 测试在所有完整 GET 已关闭后仍要求整文件发布，依赖原竞争暂停促使其他块先完成。尾块读取成功，但剩余交接正常到期后会话退出。测试现在显式再发完整 GET 来持有未完成块需求，校验全文件内容及发布；原共享读者不互相取消的断言保留，连续 10 轮 race 通过（natural-admission-canceled-seek-race.log）。失败输出 natural-admission-final-race-2.jsonl 留档；该夹具修正不改变产品行为。LA-040 联合 LA-038 增补取消边界与实际续播证据，仍待实机验收。

## 26. 清理旧暂停状态与本轮验证范围

移除已没有读者的 Progress.received、job.yielding、Blocking 汇总计数及 hasBlocking；Progress.Add 不再为旧小尾部暂停选择器重复累加原子计数。仍保留网络流量、有效性与剩余量估算、Need.Blocking 准入、Owner 公平排序和容量边界断言。该提交只清理未使用状态，不新增产品行为或实机编号；本轮行为仍按 LA-040 验收，LA-041 观测保留。核心完整 race 连续 20 轮通过（natural-admission-core-race.log）。

第三次共享工作区全源码 race 中，调度核心、缓存及另外 14 个包通过，无 race 报告；唯一失败是另一个并行变更提前写入资源元数据，使 TestLocalStorageFailureStopsConsoleFallback/index 的旧“先下载一次”预期变成零次下载（natural-admission-final-race-3.jsonl）。没有把该变更或其新增 LA-042 纳入本任务提交，也没有修改其文件来绕过测试。最终验证用 Go overlay 将 song_urls.go、storage.go、song_urls_test.go 映射到 7f3cff7 中的版本，并屏蔽其新增 song_url_metadata_test.go；本任务源码及所有其他原有测试原样执行。overlay 和原始日志只留本地忽略目录，工作区文件不被替换。

最终隔离范围检查通过：`go test -overlay=<本地隔离映射> -tags=no_easytier -race ./cmd/... ./internal/... ./data/... ./experiments/... -count=1 -timeout=180s -json`，17 个测试包、1,341 个测试/子测试通过、16 项按条件跳过，无 race 报告（natural-admission-isolated-final-race-2.jsonl）。同范围 overlay 的 `go vet` 与 `CGO_ENABLED=0 go build` 通过（natural-admission-isolated-final-vet.log、natural-admission-isolated-final-build.log）。首次 overlay 调用因 PowerShell 参数未加引号丢失文件扩展名而在加载前退出，改为带引号的绝对路径后通过，初次输出保留。共享工作区无 overlay 的 vet/build 也已通过（natural-admission-final-vet-2.log、natural-admission-final-build-2.log）；共享工作区的另一项改动仍由其自身验证与提交。

本轮三个提交分别承载准入策略、会话取消修复和无用状态清理，均仅在本地。LA-040 当前规则及 LA-041 开播时长观测保留待验收，联合 LA-038/010；未 push、未启动游戏、未修改 hosts。

## 27. GET 按请求到达及媒体比例计算期限

新增只读媒体估算，按 media.byte_size 和与资源 MD5 匹配的清单 end 计算字节/时间比例；直接资源尚无大小记录时可使用已验证 URL.s，不使用路径推断歌曲身份，不把 end 减去 start。只缺一项用 2 Mbps 补足，两项都缺用 45 MiB、210 秒；平均总码率由有效估算对派生，不保存冗余字段。SQL 故障继续进入存储错误分类，估计值不写回数据库。

HTTP 在地址解析前记录的到达时刻随 Range 起点一起传给 GET 需求和读取游标，重试保持原始时刻。块 ReadyBy 使用到达时间加相对媒体偏移，已越过的块立即到期；取消、交接、Blocking 和下载完整性规则保留。测试覆盖版本匹配、共享 MD5、缺失值、无事实回写、Range/后缀/条件 Range、HEAD/无效 Range，以及延迟处理和读取推进不滑动期限。新的计算/期限测试及已有 GET 生命周期、交接、首读/Seek、整会话取消定向 race 连续 3 轮通过（playback-clock-focused-race.log）。首轮媒体夹具的多语句绑定错误已修正并保留失败输出，产品查询没有据此放宽匹配。LA-040 增补对应实机步骤，LA-041 保留，未启动游戏或 push。

## 28. 队列使用 GET 预计结束及逐首时长

队列开始取所有有效 GET 预计结束时刻的最小值，按队列顺序累加本地清单时长，每块再加媒体比例偏移。最后一个 GET 传输退出后保留单一队列时间基准；不保留历史请求集合、不生成新的下载需求。无 GET 时从首次队列到达开始；实际重排、成员退出、元数据更新共用同一基准，重复快照不滑动。负 ID 占位保留典型时长，批量任务不设置播放基准；移除成员仍冻结其最后期限供有限交接使用。

一次只读查询加载队列窗口中的元数据，使用现有歌曲/MD5 映射。URL 解析完成后仅更新仍有效的队列来源，立即重算比例与后续时刻；重复快照可接收清单变化。不从 VRC 时长取值，不增加持久化模型。Debug 增加播放/队列估算输入及结束/开始时刻。

新的虚拟时间回归覆盖两个 GET 的最早结束、关闭后的重新选取、最后退出后保留基准、重复快照稳定及后到 GET 重定时；真实库测试覆盖不同歌曲时长、负 ID 占位、大小补齐、清单缺失、迟到来源不复活。已有重排/合并通知/交接测试改为新期限预期，保留生命周期断言。队列、媒体、GET 定向 race 连续 3 轮通过（queue-playback-clock-race.log）；旧固定参数断言的初次失败输出 queue-clock-initial.log 保留。LA-040 联动 LA-041 更新步骤与证据，实机继续积攒。

缓存与核心完整 race 通过，控制台整合回归暴露队列快照已更新、引擎还在加载元数据时提前派发的竞态，新调用取得空来源并被拒绝（playback-queue-integration-race.jsonl）。派发现在和队列应用共用已有 queueUpdateMu，在等待时仍释放控制台状态锁；显式延迟来源更新的移除、换房、移出窗口和重排回归，以及状态读取、停止取消、最新队列重试回归，连续 3 轮 race 通过（queue-clock-console-race-2.log）。第一次定向控制台运行因沙箱拒绝原子配置文件替换而失败，使用正常权限重跑，未改变文件操作语义。真实 HTTP Range 回归确认半程 GET 正文结束后队列仍以到达后剩余 60 秒为基准，连续 3 轮 race 通过（playback-http-anchor-race.log）。

## 29. VRC 日志只提供歌曲 ID 顺序

从 vrclog.Song 移除标题和时长字段，解码只保留 ID 顺序；其它 JSON 字段不进入业务状态。控制台名称从队列元数据同次查询的本地清单 name/composed_title 取得，通过独立的只读快照提供，未知名称回退 ID。状态接口和前端不再使用日志标题，失败列表也从本地清单补名称。

回归用错误类型的日志标题/时长及含玩家字段的快照，确认只输出原 ID 顺序；跨 UTF-8 写入、截断、换房/轮换、既有队列生命周期保留。真实库检查名称来自本地清单且返回值不能修改内部状态。日志、缓存、控制台相关 race 连续 3 轮通过（queue-log-order-only-race.log）；前端回归改用独立名称映射，并保留 ID 回退、稳定节点复用和 HTML 文本渲染断言。refresh/navigation/setup 共 221 项前端测试通过（playback-timing-frontend-2.log），旧标题夹具的首次失败输出保留。LA-040 增补边界验收，真实游戏继续积攒。

## 30. 条件 Range 与最终收尾

日期型 If-Range 复用已验证本地文件和 localChecks 配额核对修改时间，读取期间持有资源引用。匹配时沿用 Range 起点；不匹配或冷流无文件时间时，按实际整文件响应从 0 估算。真实 HTTP 回归核对匹配/不匹配分别返回 206/200，半程视频的队列起点分别为剩余 60/120 秒；与普通 Range、ETag 和请求到达时间场景连续 3 轮 race 通过（playback-condition-range-race.log）。移除旧固定估算专用的会话 started 和读取游标 at，不增加新的计时器或持久化字段。LA-040 增补条件请求的实际响应与期限对照。

最终全部受控 Go 源码目录 `go test -tags=no_easytier -race ./cmd/... ./internal/... ./data/... ./experiments/... -count=1 -timeout=180s -json` 通过：17 个测试包、1,378 个测试/子测试通过、16 项按条件跳过，无 race 报告（playback-timing-final-race.jsonl）。同范围 `go vet` 与 `CGO_ENABLED=0 go build` 通过（playback-timing-final-vet.log、playback-timing-final-build.log）；前端 221 项通过记录见上节。台账 42 个编号保持唯一，LA-040 的动态期限和来源边界、LA-041 的真实开播间隔继续待验收，关联 LA-038/010。

本轮分为 GET 媒体期限、队列预计播完时间、日志顺序与本地名称、条件 Range 和收尾四个本地提交；未 push、未启动游戏或修改 hosts。没有读取日常私有库或回填估计值，所有数据库和 HTTP 测试使用隔离夹具，原始自动验证日志只放本地忽略目录。

## 31. 完成事件的标准块耗时测量

加入标准 1 MiB 块从获准执行到通过检查并可读的测量，初始 T=1 秒，历史样本权重按 30 秒半衰期衰减，只有完成事件更新平均值。范围执行在完整块写入及完成登记后标记 Ready，正文/资源收尾仍须全部结束才能释放 active。尾块、部分续传、失败/暂停与顺序 200 不贡献标准块样本；完整块成功重试的全部连接与传输等待计入，正常慢样本不会被筛掉。此提交先建立测量和日志，窗口准入在后续提交替换旧供数控制。

虚拟时间回归覆盖排队/清理排除、重复 Ready 不延长采样、失败/暂停排除、慢样本与同批权重、半衰期和长期空闲。调度核心全部单元测试通过；调度及 Range/顺序/完成边界定向 race 结果见本地 block-duration-focused-race.log。LA-040 增补采样的实机步骤和证据，LA-041 保留；没有启动游戏或修改 hosts。

## 32. 事件驱动的启动窗口与按需缓增

删除累计剩余字节→吞吐→目标并发、500 ms 轮询、2 秒追赶窗口、无收益回退，以及只供旧估算使用的逐次 Read 原子计数、执行集合、剩余量采样状态。调度器用统一 T 和 M 判断有限期限堆首：ReadyBy ≤ now + T + M 才进入普通准入。默认 M=2 秒，Config.PrepareAhead 可调整；T 初始 1 秒，完成样本更新后直接移动边界，不重建堆、不保存块预测开始时间。已在途工作从启动堆移出，不按完整 T 再估一遍。

先立即复用已开放额度，只有窗口内仍有任务且 active≥open 才安排下一次 250 ms 增路，普通软上限 16；Blocking 仍直接启动并计入 active 至收尾。积压离开窗口即清除增路计时，无工作执行且无可启动候选时收缩到 1；连续替补不重新施加间隔。无期限补齐单独使用普通就绪堆，有限期限头尚未入窗时也能利用空额度，入窗的有限期限块优先；来源改动在两个索引堆间移动且不重复入队。下一次控制唤醒只取未来入窗、增路及生命周期时刻，过去的窗口时间不产生空转。日志以 block_time_ms/prepare_ahead_ms、work_bytes、active/open 替代 target/supply，只有 started 诊断 start_urgent 和 overdue，两者严格区分。原请求超时与失败处理保留。

虚拟时间回归覆盖四类期限和等号、只在窗口内积压时增长、无积压停止及恢复无积攒、连续替补、未来队列下空闲收缩、软上限等待不装过去的定时器、慢完成样本移动窗口但不改顺序、单个期限更新重设唤醒、在途过期不申请额外并发、双堆身份切换及 Blocking 绕过。原孤儿暂停夹具将启动期限改到窗口内，继续验证撤销和容量回收。实际 HTTP 夹具覆盖响应头延迟、成功重试耗时、完整块/尾块、同尺寸顺序 200 与整文件顺序回退，并核对最终内容发布。核心完整 race 连续 20 轮通过（activation-window-core-race.log）；窗口/采样/准入定向 race 连续 3 轮通过（activation-window-focused-race.log）。首次 HTTP 采样测试因沙箱拒绝原子重命名失败，正常 Windows 权限重跑通过，失败输出 block-duration-http.log 保留。

首轮全源码回归捕获两处依赖旧“未来任务立即预取”的测试：只读首字节后直接等全视频完成，以及要求未知占位和失败歌曲之后的队列项立即下载（activation-window-integration-race.jsonl）。播放夹具改为实际读取完整正文，继续核对并发软上限、Seek、共享及完整发布；队列夹具先确认后项等待且未发布，再将它提升为队首，核对沿用需求/解析去重、保留失败提示及最终文件。两项连续 3 轮 race 通过（activation-window-caller-race.log）。没有放宽运行时窗口或完整性规则来适配旧测试。

当前规则文档及 LA-040 同步到窗口模型，历史目标估算记录保留并明确已取代，LA-041 开播时长观测继续积攒。本轮未更改前端或响应模型，无需重复纯前端测试；未启动游戏、修改 hosts 或 push。

最终无 overlay 的全源码 `go test -tags=no_easytier -race ./cmd/... ./internal/... ./data/... ./experiments/... -count=1 -timeout=180s -json` 通过：17 个包、1,387 个测试/子测试通过，16 项按条件跳过，无 race 报告（activation-window-final-race.jsonl）。相同源码目录的 `go vet` 与 `CGO_ENABLED=0 go build` 均通过（activation-window-vet.log、activation-window-build.log）。台账仍为 42 个唯一编号，LA-040/041 保持待实机验收；修改分为完成耗时测量与窗口准入两个本地提交，不推送。

## 33. 调度等待与执行超时分离

移除资源会话创建时的总墙钟计时。DownloadTimeout 改为每次块准入后的执行预算，同次连接/候选/重试共用；顺序 200 回退按一次整文件执行计时。首次缓存校验、最终分块文件校验/发布及索引记录各有有限上下文；正常窗口和块间等待只受来源、租约、调用方及引擎生命周期控制。命令行说明和服务文档同步。

实际下载夹具用 150/300 ms 预算，在首次远期队列以及首块完成后等待两倍预算，确认没有提前请求/失败，队首提升或尾部实际读取后完成发布。校验额度满载仍按独立预算退出，存储熔断不把正常超时当本地故障。相关执行超时、顺序停滞、关闭、验证和错误分类回归连续 3 轮 race 通过（execution-timeout-focused-race-2.log）。第一轮暴露原 Err 方法注入依赖上下文未包装，读故障/取消注入移到真实校验函数入口，完整 prepare 的校验不匹配/Seek 故障覆盖保留；初次输出 execution-timeout-focused-race.log 保留。LA-040 补充上述实机步骤，未启动游戏、修改 hosts 或 push。

## 34. 同块有限期限与补齐准入独立保留

需求摘要新增 Opportunistic，用于表达仍有效的无期限普通需求可提前利用额度，不带原始来源。管理层及核心直接多需求合并均独立汇总该属性和最早有限 ReadyBy；过期/被覆盖交接不贡献权限，交接转换显式清除。核心为同一任务维护两个有界索引：有限期限和可提前准入；入窗的期限堆优先，否则使用提前准入堆，同块始终只有一个执行，启动/退休同步移出两堆。补齐撤销或到期仅移除自身提前许可，未启动块恢复等待原有限期限，在途仍按有效需求自然完成。两堆更新均 O(log N)，不扫描工作量、不抬高额度。

虚拟时间测试覆盖满额度时不能提前、入窗前后期限排序、两份贡献只执行一次、完成清除双索引、关闭与租约到期只撤销补齐。真实文件夹具复现 GET 首块 1 MiB 完成、尾块 123 字节期限在未来，再加入补齐即完整发布，GET 仍正确读取尾部且请求恰好两次。合并层还覆盖独立到期计时和交接权限清除。定向 race 连续 3 轮通过（opportunistic-focused-race.log）；对应 LA-040 增补实机步骤和证据，LA-041 保留，未启动游戏或 push。

## 35. 更保守的初始执行时间与准备量

默认 T=3 秒、M=30 秒，替代此前的 1/2 秒；时间衰减均值和样本口径保持。Config.PrepareAhead 仍支持较大的提前量，不额外钳制到几秒；新增 M=500 秒的虚拟时间回归，确认远期工作进入窗口后仍逐路缓增、达到普通软上限即等待，Blocking 可立即超限且计入 active，已入窗积压没有过去时间的空转唤醒。期限边界、慢样本移动窗口和单需求改期测试按新初值更新，测试的时序与排序目的保持。

本轮分别本地提交执行超时边界、同块补齐准入和保守参数，LA-040 增补各自步骤与证据，LA-041 保留；没有启动游戏、改 hosts 或 push。参数效果与提前缓存代价仍需真实播放验收。

最终全源码 `go test -tags=no_easytier -race ./cmd/... ./internal/... ./data/... ./experiments/... -count=1 -timeout=180s -json` 通过：17 个包、1,395 个测试/子测试通过，16 项按条件跳过，无 race 报告（window-boundaries-final-race.jsonl）。同范围 `go vet` 与 `CGO_ENABLED=0 go build` 通过（window-boundaries-vet.log、window-boundaries-build.log）。双索引引入后以及默认参数调整后，调度核心分别完整连续 20 轮 race 通过（opportunistic-core-race.log、conservative-window-core-race.log）。LA-040/041 继续待实机验收，自动结果不代替游戏内证据。

## 36. M 默认 500 秒及设置可调

2026-10-15：调度器默认 M 改为 500 秒，控制台新增「提前准备时间 M（秒）」及持久字段 `prepareAheadSeconds`，接受 1～86400 的整数；缺失/0 采用默认 500。同版本配置无需迁移，已有配置加载不强制改写。值经 `cacheproxy.Config.PrepareAhead` 传入共享调度器，沿用保存与运行配置分离的机制：保存只更新下次启动配置，重启整个应用后生效。设置草稿参与脏状态、轮询保护、放弃更改和保存；引导确认摘要包含此前已保存的 M 变更。

回归覆盖新配置落盘、旧 v3 配置缺字段回退、API 合法边界/非法类型与越界拒绝、失败不改文件、保存与运行快照分离、仅启停网关不应用、重启持久化、引擎向调度器传递短窗口，以及默认 ReadyBy 前 503 秒的启动等号边界。未来任务夹具改为 15 分钟或显式 M=30 秒，保留窗口外等待、补齐需求撤销、调度等待不消耗执行预算的原验证目的；慢样本窗口跟随新默认平移。T 初值、样本口径、250 ms 缓增、普通软上限和 Blocking 规则不变。

开发检查（不代表实机通过）：

- `go test -tags=no_easytier ./internal/blocksched ./internal/cacheproxy ./internal/console -count=1 -timeout=180s`：三个包完整回归通过，原始输出仅本地 `artifacts/prepare-ahead-checks/go-tests.log`。初次沙箱执行因 Windows 拒绝原子重命名造成存储/配置测试失败；正常权限下使用测试临时配置与缓存重跑通过。
- `node --test scripts/refresh.test.mjs scripts/setup.test.mjs scripts/navigation.test.mjs`：222 项通过，包含新设置默认值、草稿保护、放弃、保存数字及待重启提示；原始输出仅本地 `artifacts/prepare-ahead-ui-tests.log`。
- 正式源码目录 `./cmd/... ./internal/... ./data/... ./experiments/...` 的 `go vet -tags=no_easytier` 与 `CGO_ENABLED=0 go build -tags=no_easytier` 均通过，输出仅本地 `artifacts/prepare-ahead-checks/{vet-source,build-source}.log`。初次 `./...` 检查纳入忽略目录 artifacts 中的旧临时源码，因包名冲突、缺少嵌入文件等失败；保留原输出及历史资料，随后按上述正式源码范围检查通过。

新增待实机验收 LA-043，联动 LA-001/040/041；具体步骤、预期和证据见台账。未启动游戏、修改 hosts，未 commit/push；已有实机批次和历史失败证据保留。

## 37. 移除每视频固定候选数量

2026-10-16：删除 `scheduledWindow=8` 和每资源候选数量截断。完成 Range/实体初始化后，GET、队列及补齐对已接入资源的全部未完成块生成需求，保留各自 ReadyBy、来源和租约，由全局 T+M 窗口、250 ms 缓增及普通软上限 16 决定执行。没有改成另一固定候选数，也没有改动调度核心。首次响应头前仍仅提交首块，顺序 200 仍只占一次执行；每读取位置的当前块和后面两块继续是独立需求，实际等待才赋予 Blocking。同块去重、完成块过滤、交接、取消、内容校验和发布沿用原流程。

资源会话和全局任务/需求元数据容量保护保留；排队不分配正文缓冲。已接入资源的全部缺块现在计入全局注册容量，超过 4,096 个已注册块时沿用容量错误并清理，不能将“无每视频候选数限制”理解为无限任务内存。未接入的库资源不会展开。

新增 `scheduled_candidates_test.go` 覆盖三种来源的完整候选、初始化门禁与已完成块过滤、跨资源第 8 块以后的期限顺序、生产下载路径单视频升到 16 路且普通请求不超限、全部分块只请求一次及完整内容发布，以及全局容量拒绝后的任务回收。原读取窗口测试改为检查读取来源本身的撤销，避免把独立 GET 仍需的块误判为旧 Seek 残留；Blocking 超限夹具不再引用被删除的窗口常量。

开发检查（全部使用隔离夹具，不代表实机通过）：

- 旧产品文件通过 Go overlay 接入新增回归，GET/队列/补齐均只生成 8 个候选，跨资源排序在 `content/8` 处被 `later/0` 抢先；四处预期失败保留于本地 `artifacts/remove-resource-window/before-regression.log`，工作区产品文件未回退。
- 新增回归及相关读取/Blocking 定向检查通过（`focused.log`，5.088 s）。真实回环 HTTP 的 20 MiB 单视频在未触发额外 Read/Seek 时达到 16 路，仍阻止第 17 个普通请求；释放响应后完成全部内容及最终校验发布。
- `go test -tags=no_easytier -race ./internal/cacheproxy ./internal/blocksched -count=1 -timeout=180s` 完整通过，分别 38.499 s / 1.131 s，无 race 报告（`packages-race-normal.log`）。首轮沙箱日志保留为 `packages-race.log`：硬链接/junction 权限拒绝及 ThreadSanitizer 异常退出，未作为通过证据；正常 Windows 权限重跑成功。
- 两个相关包的 `go vet -tags=no_easytier` 通过（`vet.log`）；正式源码目录 `./cmd/... ./internal/... ./data/... ./experiments/...` 的 `CGO_ENABLED=0 go build -tags=no_easytier` 通过（`build.log`）。日志均位于上述本地忽略目录。

LA-040 更新本次步骤、预期和证据需求，新版本待实机验收；历史失败和报告保留，本次不宣称已解决历史卡顿/Seek 根因。未启动游戏、修改 hosts，未执行 Git commit/push；工作区其他并行变更保持原样。

## 38. GET 仅覆盖请求区间，移除固定读取窗口

2026-10-18：在 `f4882a0` 上通过隔离 overlay 复现：20 块媒体从 70% 开始读取，初始化块已完成，前面的 13 个缺块全部取得立即到期的 GET 期限；单路普通额度下它们全部先于向前预读块启动，实际 Blocking 块仍可立即准入。原测试把请求起点之前的第 0 块立即到期写成预期，相关五项现有测试仍通过。失败断言、实际启动顺序和 20 轮重复结果保留在本地忽略目录 `artifacts/seek-prefix-investigation/`；重复检查首次因沙箱拒绝 Go 缓存访问失败，正常权限重跑通过，原输出保留。

GET 时钟携带规范化请求结束位置，只为与实际响应区间相交的块建立播放需求；保留块内起点的零偏移截断，有限结束位置不改变整部媒体预计播完时间。无 Range、开放末尾、后缀和 If-Range 回退沿用 HTTP 语义。完整块仍是下载、共享和校验单位。取消固定三块读取窗口及其游标、回调、来源和交接处理；实际 Read 等待继续独立贡献当前块的 Blocking，普通准备统一由 GET 区间期限表达。队列与独立批量补齐保持原覆盖范围，不由局部 GET 隐式创建补齐来源。

冷 Range 的首次上游请求直接选择涉及的块，不先下载第 0 块；响应头前保持单个共享初始化请求，初始化结束后各 GET 恢复自身区间，不给临时初始化依赖产生交接。上游顺序 200 回退保留。区间完成但 GET 仍存活时，会话继续供读者消费已下载字节；没有缺块不等于没有读者。来源退出后的暂停检查点、复用、完整文件校验与发布保持原机制，局部 Range 不发布不完整文件。

开发验证使用隔离数据库和回环 HTTP：

- 新增 `get_range_demand_test.go` 覆盖 70% Seek、非对齐起点、有限末尾、跨块边界、后缀、独立补齐、多等待者、区间完成后的读者存活、初始化共享及无额外交接。实际 HTTP 核对初始请求块、完整上游块集合、正文内容、If-Range 匹配/不匹配和顺序 200；重复局部 GET 复用已下载块，不重复请求、不发布不完整缓存。最终这些回归与播放期限测试连续三轮 race 通过（`final-focused-race.log`）。
- 缓存和调度核心完整普通回归通过；完整 race 共 580 个测试及子测试通过，无 race 报告（`packages-race.jsonl`，43.232 s / 1.137 s）。复用本地已有 w64devkit 工具链。普通回归首次沙箱拒绝 Go 缓存读取的输出保留为 `packages-first.jsonl`，正常权限结果为 `packages-normal.jsonl`。
- `go test -tags=no_easytier ./cmd/... ./internal/console ./data/... ./experiments/... -count=1 -timeout=180s` 通过（`callers.jsonl`）；正式源码目录 `./cmd/... ./internal/... ./data/... ./experiments/...` 的 `go vet` 与 `go build` 使用同一 tag 通过（`vet.log`、`build.log`）。全部原始日志仅保留在上述本地忽略目录。
- 提交前复核将顺序 200 回退的 `w.demand` 参数统一为 `block * rangeBlockSize`，保持字节偏移契约；共享初始化和从非零块开始的顺序回退定向 race 各三轮通过（`review-units-race.log`、`review-sequential-race.log`）。

本问题及请求区间边界已由开发验证直接确认，不新增 VRC 验收条目。已有 LA-040 的实际慢网 Seek/卡顿失败仍保留，不能据此断言历史游戏问题全部解决。本轮未启动游戏、修改 hosts 或 push。

## 39. 顺序响应取消期间的新 GET 接入

2026-10-18：`TestNewGETWaitsForExitingFlightCleanup` 修复前重复 20 轮，8 轮失败，原日志保留为本地忽略文件 `artifacts/flight-exit-check-20261018.log`。其中 206 的失败是测试把“块响应开始清理”误当成“整个资源会话已经退出”：新 GET 可在尚未退出的会话内恢复分块，实际正文完整且无错误。200 的失败则是真实竞态，出现接入旧会话后正文为空、返回取消错误。

调度器可先于资源协调器处理交接租约到期，异步关闭响应；此时顺序执行器也可能尚未从 Read 返回，原有 `preempted` 标记还未设置。顺序 200 无法在同一会话安全重启，因此现在记录该次执行上下文，并在与 GET 接入共用的锁内检查取消状态：已取消就停止接入，等待旧会话完成正文和文件清理后再建立新会话。206 的块内暂停恢复、原交接时限、并发额度和本地存储错误优先级保持原机制。

回归测试分别覆盖整个会话明确退出，以及调度器先取消块执行、协调器和 Read 返回均被延迟的时序。后一种时序要求 206 复用原会话、200 等待新会话；两者都检查旧响应清理前没有第二次上游请求，以及恢复后的完整正文和校验发布。

开发验证（均使用 `no_easytier` tag，日志保留在本地忽略目录 `artifacts/`）：

- 受控时序测试在产品修复前复现 200 接入旧会话并返回空正文/取消错误（`flight-exit-fix-red-20261018.log`）；修复后连同取消和存储错误回归连续 3 轮通过（`flight-exit-fix-green-20261018.log`）。
- 两种清理时序与旧空闲快照回归连续 20 轮通过，81.917 s（`flight-exit-fix-repeat-20261018.log`）。
- 缓存、调度两个包完整 `-race -count=1` 通过，43.133 s / 1.120 s，无数据竞争报告（`flight-exit-fix-race-20261018.log`）。复用已有 Windows C 工具链。
- `./cmd/...` 回归通过（`flight-exit-fix-callers-20261018.log`）；`./cmd/... ./internal/...` 的 `go vet` 通过（`flight-exit-fix-vet-20261018.log`）。

本次问题可由受控并发测试判断，不新增 VRC 验收项，也不据此关闭 LA-040 的历史游戏失败。默认零容量策略保持不变。本轮未启动游戏、修改 hosts，未执行 Git commit/push。

## 40. 固定下载提前窗口

2026-10-19：按 canonical 设计将下载准入、堆首唤醒及容量比较统一改为 ReadyBy − M。删除动态 T 的衰减均值和 start_urgent；保留成功标准块实际准入至可读耗时及测速完成计数。默认 M=500 秒，设置和引擎输入范围 120～86400 秒，0 使用默认值；依本轮授权不兼容或迁移旧的小值。120 秒是留出传输基准余量的输入下限，不是及时完成保证。

开发验证（原始日志在本地忽略目录 artifacts/download-resource/）：

- blocksched、console、upstreamstate 整包普通回归通过；配置与测速定向重跑通过。缓存整包两次均在 TestCanceledSeekReleasesWorkerWithoutCancelingSharedReaders 失败，独立五轮通过；保留失败并继续核对共享读取生命周期，不把重跑通过写成竞态已解决。
- 窗口、注册边界、实际耗时及引擎透传的 Windows race 定向检查通过；四个相关包 go vet 通过。复用已有 w64devkit。
- pnpm build/check 通过；3 项受影响的 Edge 设置与向导回归通过，覆盖保存与活动配置、保存后同步及独立向导草稿。
- 首轮沙箱 Go 测试遇到 renameat Access is denied，浏览器启动也受沙箱限制；正常权限重跑得到上述结果，未将权限失败作为产品失败或通过。

这一步只落实固定窗口，不表示需求集合、本地保护或块文件已迁移。未启动 VRC、修改 hosts 或 push。

## 41. GET 消费与持久化完成分离

2026-10-19：HTTP 与 obtainDemand 共用一份 GET 来源，实际 200/206 响应的范围和已接受字节驱动块级撤销；消费状态与原始播放时钟分开，首尾部分块、其他 GET 贡献及来源身份独立保留。短写明确失败；正常响应和服务端失败退出不生成客户端交接，客户端取消仍交接未消费部分。

移除尾字节校验门槛、verifyFull 及完整响应等待发布后歌曲记账的逻辑。已可读数据可以完成 GET，后续校验/发布错误不追溯改变交付结果；缺失数据仍传播错误或取消。已收齐字节时协调器先排空执行收尾，避免正常消费退出取消独立发布；没有新增用于延长本地保护的来源。

开发验证（本地忽略日志 artifacts/download-resource/）：

- 缓存整包最终通过（get-cache-final.log，28.116 s）；定向 GET 来源、部分块、短写、冷热 HTTP、取消、If-Range 和校验失败测试通过。新受控测试在本地校验额度全部占用时确认完整 GET 先交付，释放额度后独立校验发布，重复三轮通过。
- 最终消费、HTTP 共享、独立发布、Seek 与清理定向 Windows race 连续三轮通过（get-final-race.log，10.697 s），未报告数据竞争。缓存包 go vet 通过。
- 初轮整包暴露 GET 返回早于发布的旧测试假设，以及消费结束取消顺序发布的问题；后者已修复，持久化断言改为明确等待发布/清理，未把这些等待放回 GET 路径。各轮失败日志仍保留。
- 上一步共享 Seek 失败在旧调度 overlay 整包中未复现，不能断言是旧代码问题。本步允许已经可读的内容先于会话收尾错误交付后，整包及三轮相关 race 通过；仅按这些检查覆盖范围收尾，不外推游戏慢网体验。

本地块保护、唯一块容量和独立块文件仍待下一阶段落实。真实游戏剩余风险沿用 LA-040/041/047，不为这些可控断言新增游戏操作。

## 42. 交接保留原 need-by 时间基准

2026-10-19：GET/队首交接分别使用原 need-by + 2 秒/+ 5 秒，移除与退出时刻取较晚值的公式。有效期仍为真实来源退出 + 2 秒/+ 20 秒，延迟协调、重复通知及原 need-by 已过去均不重新起算。来源/退出原因、GET 过滤与队列升降的现有机制不变；交接包含已完成但未消费块的目标由后续使用需求集合承接。

定向 Handoff、Withdrawal、Coalesced、SourceFiltering、SourceAndWithdrawal、QueueReorder 测试通过，覆盖过去/未来期限、两种交接时限和延迟观察。原始结果未替代真实游戏交接的 LA-041 证据。本次仅本地提交，不 push。

## 43. 唯一块使用需求容量

2026-10-19：新增独立于下载完成的唯一块引用索引。完整 GET 范围原子接入 4096 块容量；非 GET 新增唯一块后的总量限制为 2867，同块共享和非扩张更新允许继续。GET 按实际交付释放块引用，队列成功准备后仍持有引用；退出交接覆盖所有未消费块，按真实事件时刻 2/20 秒到期。容量不足明确返回 HTTP 503，不影响已有来源。

缓存及调度整包最终通过（use-capacity-final.log，28.082 s / 0.175 s）；容量、消费、交接、来源退出和队列重排定向 Windows race 连续三轮通过（use-capacity-final-race.log，3.477 s），两个包 go vet 通过。console、upstreamstate、cmd 调用方回归通过（use-capacity-full.log）。原始日志位于本地忽略目录 artifacts/download-resource/。

首轮保留两类失败：旧大视频测试期待登记超出 4096 的使用需求，与本轮契约冲突，已改为完整范围拒绝断言；过期通知使测试的单次协调快照失效，测试现按实际循环处理重算返回值。初轮 race 发现测试读取计数未加锁，已修复并重跑。未将历史失败删除或当作通过。

本步只完成完整使用范围的容量生命周期。按容量有序解析及异步结果回补、统一使用需求摘要、本地保护和块文件迁移继续后续阶段；未启动 VRC 或 push。

## 44. 按块需求容量有序接入来源

2026-10-19：队列与补齐拆开来源准入和完整视频结果收集。原有 Prefetch 调用仍返回校验/发布结果，WithPreparationAdmission 只在需求登记成功后通知上游处理下一项。按既有队列顺序与补齐优先级逐项解析；容量不足时保留一个已解析待接入项并等待来源通知，取消及时唤醒，不并发解析整个曲库。移除两个歌曲调用者和 32 个资源会话门槛；本地验证及全局网络额度保持独立。准备和 obtain 复用同一来源，队列完整本地命中也登记使用需求。

开发验证（本地忽略目录 artifacts/download-resource/）：

- 缓存、控制台、调度和 cmd 完整回归通过（preparation-full.log，29.404 s / 61.016 s / 0.148 s，命令行两包通过）。
- 定向并发测试连续三轮 Windows race 通过（preparation-race.log）：前两首未完成时第三首接入；40 个来源均到达独立资源会话，不在旧 32 个门槛等待；一个来源仅登记一次；容量等待、取消、队列失效替换、预算停止及本地共享命中覆盖。
- 新压力测试以 1 GiB URL 元数据占用需求槽位，不生成大文件或真实网络流量：两个资源准入、一个解析后等待、第四个不提前解析；普通队列成员移除后按通知回补。batch/queue 连续三轮 race 通过（preparation-pressure-final.log，2.796 s）。首轮 batch 断言误以为歌曲 1～4 没有内置优先级，已改用无内置先验的测试 ID，失败日志保留。

本步未改变完整文件格式、下载暂停恢复及本地保护规则；相关后续目标仍未完成。未启动 VRC、修改 hosts 或 push。

## 45. 交接归属完整使用需求

2026-10-19：planUseLocked 按来源统一投影完整使用范围，下载协调器随后过滤已完成块。GET 退出或队首移除时一次性转移剩余引用、冻结原时钟及固定到期，已下载但未消费的块也保留；不依赖任务曾经存在或协调器及时观察。取消回调与规划观察共用幂等的锁内退出路径，不产生中间无需求窗口或重复交接。移除下载协调器生成交接的分支和重复的队列资源映射，任务只保留调度/诊断快照；读取等待不生成使用交接。

缓存整包最终通过（usage-plan-cache-final.log，27.814 s）；console、blocksched 完整回归通过（usage-plan-full.log，64.088 s / 0.290 s）。来源退出、已下载块交接、延迟观察、固定时间基准、队列重排与准备准入定向 Windows race 连续三轮通过（usage-plan-race.log，4.149 s）。相关包 go vet 通过。

新夹具验证：协调器从未规划过原来源时仍能得到交接；取消回调等待锁时由规划原子承接；首块已消费不重新加入、已下载未消费块继续占槽位；过去/未来 need-by 都只平移 2 秒；完整来源交接到期不重新启动已完成块。旧测试中下载完成即排除交接的预期已按确认契约调整。最初定向检查还遇到沙箱重命名权限失败，正常权限重跑通过；失败日志保留于 artifacts/download-resource/。

本地保护摘要、60 秒延续保护和块文件仍待后续接入。本次未启动 VRC、修改 hosts 或 push。

## 46. 统一本地保护窗口与固定延续

2026-10-19：有限使用需求按独立的 LocalProtectAhead 进入本地保护，默认跟随下载提前量；最后一份块引用结束后，已有保护从真实结束时刻延续 60 秒。交接结束使用冻结的 E+2/E+20，不受回调延迟影响；普通消费按块结束。远期/无期限需求不额外保护，不刷新旧尾期；仅剩远期来源也不另加补偿宽限。完整文件清理和手动删除共用块保护，移除队列专属 30 秒文件保护、歌曲保护映射及旧 API。引擎退出结束运行期保护，必要引用仍保持原有独占边界。

定向集成通过（protection-integration-final.log，缓存 0.996 s / 控制台 1.841 s）；相关 Windows race 连续三轮通过（protection-race.log，3.030 s / 3.404 s）。虚拟时间覆盖独立窗口、need-by 后仍有效、块消费与共享、远期/手动重入、入窗来源退出只剩远期来源、延迟交接、旧尾期到期后新需求接管，以及队首 20 秒交接后 60 秒延续。真实文件夹具验证零预算定时回收、删除保护及删除保留期间的新引用等待。

首轮保留旧 API 编译遗漏及沙箱 rename 权限失败；修正后正常权限运行。延迟通知测试曾在 synctest 中持锁睡眠而超时，已改为冻结事件后延迟投递，避免虚拟时钟被非持久阻塞限制。完整回归中旧 HTTP 测试期待请求结束立即淘汰，现改为检查延续保护和关闭清理；均保留失败日志。控制台、blocksched、cmd 完整回归通过（protection-full.log）。最终缓存整包通过（protection-cache-final.log，37.147 s），cacheproxy、console、blocksched 的 go vet 及 diff 检查通过。原始日志保存在本地忽略目录 artifacts/download-resource/。

本步实现完整文件的统一保护判定，块文件逐块回收、持久化准入及下载截止仍待后续阶段；未启动 VRC、修改 hosts 或 push。

## 47. 来源集合输出通用使用摘要

2026-10-19：需求集合统一投影并整合完整范围，blockUse 不含来源身份。下载协调器只过滤本地可用块、叠加真实读取等待并维护调度句柄；移除任务里的来源 map、交接数组、逐来源期限补写和 128 份重复贡献限制。唯一块容量规则不变，150 个同资源来源仍共享 20 个块。保护与下载共用同一来源投影及整合函数；有限保护时刻包含仍有效的交接，GET 仅抑制交接的下载紧迫属性。

定向回归通过（aggregate-directed-final.log，2.332 s）；来源共享、GET 消费、交接、队列重排、保护和摘要过滤的 Windows race 连续三轮通过（aggregate-race.log，2.761 s）。console、blocksched 完整回归通过（aggregate-full.log，62.096 s / 0.164 s），相关三包 go vet 和 diff 检查通过。首轮整包中旧夹具依靠已移除的 handoff_sources 日志字段暂停协调器，改用聚合 lease_until 同步点；旧响应关闭期间新 GET 的接入/等待行为未修改，定向三轮通过（aggregate-cleanup.log，13.030 s），最终缓存整包通过（aggregate-cache-final.log，27.699 s）。其余编译调整和首轮失败日志保留。

后续仍需块文件存储、持久化准入及下载截止/停止任务语义；未启动 VRC、改 hosts 或 push。

## 48. 单次尝试超时与重试预算分离

2026-10-19：DownloadTimeout 默认仍为 10 分钟，改为每个实际 HTTP 尝试的响应头/正文时限；移除块执行含重试的总计时和候选平分剩余预算。顺序 200 保持完整本次尝试时限，不能在接受正文后取消超时；本地校验/发布独立计时。候选数、验证、重定向和父级取消规则不变，不新增重试总时限或并行冗余。尝试取消主动关闭正文，通过 sync.OnceValue 与调用方收尾共享关闭结果，收尾完成后释放请求活动引用。

定向回归通过（attempt-timeout-directed.log，8.033 s），三轮 Windows race 通过（attempt-timeout-race.log，25.413 s）；缓存及控制台完整回归通过（attempt-timeout-full.log，29.104 s / 63.379 s），go vet 和 diff 检查通过。新虚拟时间夹具验证：第一候选耗尽 1 秒后，第二候选仍能用新的 900 ms 成功；父级 1.5 秒截止仍能终止第二候选；不观察请求上下文的阻塞正文由超时关闭唤醒，关闭恰好一次、活动引用在回退前释放。已有顺序 200、Range 验证/正文回退、窗口等待及发布超时测试继续通过。

此步骤尚未加入 need-by+30 秒下载截止，也未迁移块文件；相关目标继续后续提交。未启动 VRC、修改 hosts 或 push。

## 49. 完整块文件与独立运行期资源

2026-10-19：Range 和顺序 200 共用 localResource/blockFiles。仅完整块普通写入并关闭后可读，半块保持在执行缓冲中，停止时丢弃；移除共享稀疏文件、半块前缀和 32 个/512 MiB 暂停检查点。引擎持有完整块，下载会话及旧读者持有独立引用；新会话使用当前地址、跳过完整块，不沿用上一次请求候选或重定向游标。没有完整块的失败资源立即释放，避免长期保留无数据元记录。顺序 200 仍需重开整段正文，已存块不重复写盘；后来支持 Range 时从缺失块开始。

收齐后取得完整块快照，顺序合并并计算 MD5，再同步关闭、原子发布。快照引用与读取引用保证清理不破坏旧文件，后来替换的块使用不同路径，旧删除不会误删新文件。GET 完成仍与发布结果独立。退出清理及下次启动残留清理识别 download-block-<数字>.part，不跨运行恢复；已有完整文件、导入和库存契约保持不变。顺序下载监控阶段改为 download，速率统计和内嵌界面同步更新。

缓存完整回归通过（block-integration-full-final.log，29.621 s），整包 Windows race 通过（block-integration-race-full.log，41.668 s）。定向并发连续三轮通过（block-integration-race.log，6.434 s）；最后的空资源释放、免重复写入、标准 EOF 检查及初始化前安全查询调整，连续三轮 race 通过（block-integration-final-adjustments.log，5.742 s）。console、blocksched 及 cmd 完整回归通过（block-integration-related.log，71.549 s / 0.160 s）；相关包 go vet、前端 build/check 通过，Edge 下载列表夹具 1 项通过（block-monitor-browser.log）。

新增或更新覆盖：跨块读取、40 个资源不按旧检查点上限淘汰、完整块跨会话复用、反复取消不保存半块、未验证 EOF 不形成可读块、顺序重开及转 Range、实体/范围校验、当前解析地址替换旧地址、合并错误分类、快照与重新写入并发清理、Windows 占用删除失败记录及重启清理。首轮保留旧检查点字段导致编译失败；完整回归发现旧流式夹具在不足一块时等待响应、阶段改名未同步速率统计，以及新会话误用原保存地址，均已修正，原失败日志保留。浏览器首次筛选命令中的竖线被 shell 解释，改为单一关键词后实际测试通过。

此步骤仅完成块文件、复用和合并发布。持久化准入/预留、零预算不合并与逐块积极回收、need-by+30 秒截止，以及调度核心暂停/首次响应头游标的移除仍待后续提交。未启动 VRC、修改 hosts 或 push；实机剩余风险继续合并到现有 LA-040/041/047。

## 50. 持久化预判、并发预留与结果分离

2026-10-19：完整块收齐后先申请 publicationReservation，再占用本地校验额度、取得快照并合并。准入和回收共用当前歌曲映射、资源分数及全部同分排序；库存已满仍允许高分替换低分，受保护的既有正式视频不计作可腾退空间，普通文件引用只推迟实际删除。并发候选按完整大小记账，共享资源由同一 flight 单次申请；更高分候选可撤销低分预留，取消其合并等待/执行。最终发布前重新核对保留集合与保护，重命名及库存结算与其他准入/回收串行；放弃释放预留，发布转正式库存。

零预算、单视频超过预算或优先级不足时，不合并、不计算整视频 MD5、不发布；仍保留完整临时块。GET 不依赖发布结果，连 flight 已结束才取得 reader 的分支也能从本地资源重新取得可读引用。队列 TEMP 表示临时可读，仍保留使用需求；手动准备以 ErrNotRetained 报告未取得保留资格，并遵循现有正常预算停止路径，不能记为持久化完成或通过换源重试。单轮补齐初始库存及已消耗额度记账保持不变。

定向检查覆盖满预算替换、低分拒绝且不进入 hash、保护阻止替换、低分先到预留可被撤销、旧释放不影响新预留、发布前保护改变、真正等待本地校验槽位的低分合并被取消、零预算多次 GET 复用临时块、不校验故意不匹配的 MD5、手动未完成和队列临时就绪。最终 cacheproxy/console/blocksched/cmd 完整回归通过（publication-admission-full-final.log，30.712 s / 60.040 s / 0.092 s）；相关三轮 Windows race 通过（publication-admission-race-final.log，3.782 s / 3.243 s），go vet 和 diff 检查通过。

首轮新保护夹具未初始化 map，在持锁断言时触发清理等待并超时，已修正；初次完整回归中旧测试仍期待未准入内容先发布再淘汰，现改为临时块复用，已有正式视频在预算降为零后的保护测试保留。原失败和重跑日志均保存在 artifacts/download-resource/。零预算逐块积极回收、任务截止与调度核心停止语义仍待后续实现；未启动 VRC、修改 hosts 或 push。


## 51. 零预算按块积极回收

2026-10-19：仅有限视频预算为零时，完整块提交、来源变化及保护尾期到期触发现有 retentionLoop。保护判断与从可用集合移除共用 retentionMu；实际块读取及合并快照延迟对应旧文件删除，整份会话和 reader 引用不额外保护其他块。新写入使用独立路径，旧清理不会删除替换数据；仅引擎持有的空资源同时释放。正预算（包括已满）和不限模式保持临时完整块，不因落盘未准入积极清理。

协调器保留当前零预算会话已经完成的块工作记录，回收不触发该会话反复补齐；实际缺块 Read 仍可重新请求，后续会话根据真实可用性重新准备。不为两个提前窗口不等长增加补偿保护，不因使用容量或资源引用而延长固定尾期。

定向检查通过（block-reclamation-targeted.log）；完整 cacheproxy/console/blocksched/cmd 回归通过（block-reclamation-full.log，30.230 s / 59.936 s / 0.169 s），相关 Windows race 连续三轮通过（block-reclamation-race.log，14.267 s），go vet 和 diff 检查通过。覆盖：零/正/不限预算区别；过期与远期块回收；正在读取的旧文件与重新写入并存；GET 消费后固定 60 秒回收且 reader 仍打开；真实后台到期唤醒；四块 Range 在途逐块删除而不反复下载；真实缺块读取重新申请。

原始日志位于本地忽略目录 artifacts/download-resource/。下载截止和调度核心停止语义仍待后续实现；未启动 VRC、修改 hosts 或 push。


## 52. 停止旧任务，不保留暂停重接

2026-10-19：删除 ErrPaused、剩余片段恢复结果、无需求 30 秒任务保留及 Submit 两步登记接口。任务和首份有效需求统一通过 Schedule 原子登记；最后需求关闭或到期即进入结束状态，拒绝新贡献和迟到更新，响应与写入收尾完成后关闭 Done 并释放额度。命令与到期同时可处理时先落实已到期租约，不能借新登记复活过期任务；重复结束保留首次终止原因。常规有界重试、容量延期和不按优先级抢占保持不变。

资源执行移除首次响应头前的候选、重定向恢复游标，每次新执行从完整块起点及原候选重新开始。新 GET 在旧任务清理期间到达时，Range 等待清理后在原资源会话建立新任务，顺序 200 则结束旧会话、等待新会话重新打开正文。完整块仍由本地资源独立持有。可读状态已形成但任务尚在收尾时，协调器继续等待成功结果，避免将完成误判为取消或在零预算回收后反复补齐。

完整 cacheproxy/blocksched/console/cmd 回归通过（stop-tasks-full-final.log，30.097 s / 0.176 s / 70.775 s），缓存和调度整包 Windows race 通过（stop-tasks-race.log，49.158 s / 1.232 s），go vet 和 diff 检查通过。新 GET/旧清理、完整块复用、原候选重开、零预算回收定向连续三轮通过（stop-tasks-reattachment-final.log，14.625 s）。停止测试覆盖排队/在途任务、撤销/到期、拒绝复活、独立旧句柄、清理前不释放额度，以及真实清理错误不被取消覆盖；已有共享、公平、Blocking、缓增及候选回退测试迁移到原子登记后继续通过。

保留的失败证据：机械迁移初轮有旧接口编译错误；首次完整回归发现新 GET 重接仍将旧任务拒绝更新解释为容量失败，随后定向检查又发现顺序 200 被错误当成可在原会话重建，触发重复关闭 streaming；均已修复。新增完成边界夹具最初误开放了其他未配置执行器的块并触发空指针，已收窄为单块。所有日志保存在本地忽略目录 artifacts/download-resource/，未删除历史失败。need-by+30 秒截止及到期来源处理继续后续阶段；未启动 VRC、修改 hosts 或 push。


## 53. 有限任务下载截止与独立顺序发布

2026-10-19：调度核心以同一索引堆维护排队和在途任务的 ReadyBy + 30 秒截止；不随开工、HTTP 重试、Blocking 或活跃租约续期顺延。真实来源变化可在到期前更新整合后的 need-by；命令、完成与截止同时可处理时先执行已到期截止，旧句柄不能复活任务。截止取消响应，清理完成前仍计入 active；无期限需求不能让同块过期执行继续，之后可独立重新登记。新登记的已过期有限需求明确返回 ErrExpired（可识别为 context.DeadlineExceeded）。

顺序 200 下载现在只负责读取和完整块提交，正文及任务收尾后与 Range 共用独立合并发布阶段；原下载截止不再截断已完成下载后的校验发布。GET 交付仍不等待发布。顺带修正竞态检查揭示的租约先结束、协调器尚未标记 retiring 时的结果识别：调度器的普通取消表示旧任务结束，新 GET 可在清理后重新申请，不能被误报为下载失败。

核心及缓存整包回归通过（deadline-core-full.log，0.270 s / 31.042 s）；截止、顺序发布、窗口等待和重接定向三轮 Windows race 通过（deadline-core-race-final.log，1.235 s / 20.608 s），租约先完成的确定时序测试三轮通过（deadline-lease-order.log），go vet 和 diff 检查通过。覆盖排队到期不执行、Blocking/手动共享不能续命、清理期间不释放额度、实际时刻更新、过期拒绝登记，以及顺序正文完成后跨原截止仍独立发布。

保留首轮测试失败：移除会话路径字段后夹具引用未同步；新发布测试过早占满本地校验槽位而阻塞下载前缓存检查；第一次 race 捕获上述租约完成时序问题，均已修正。日志保存在 artifacts/download-resource/。本提交只落地执行截止；到期后的来源失败、撤销、剩余需求重新整合仍待下一提交。未启动 VRC、修改 hosts 或 push。


## 54. 截止反馈、来源撤销与重新准入

2026-10-19：有限任务截止（含首次登记已过期）经 download_expiry.go 回到需求集合。沿用共享会话失败路径，结束仍附着该会话且尚有使用范围的 GET；来源先记录 need-ended，再以 ErrExpired 取消自己的上下文并撤销使用。HTTP 原请求上下文保持独立，未出响应头时可返回 504，已流式交付时沿用现有中断反馈；服务端失败不会生成断连交接，也不会被当作上游候选错误或持久化故障。

队列自身所需块也过期时结束该次整视频准备、释放使用范围，并拒绝同成员的重复快照/定时重试；新连续成员、重置或真实开始时刻/媒体时间估算变化可以重新准备。时刻仍有效的队列及无期限手动来源保留，等待旧会话收尾后用原来源重新进入统一窗口和容量判断。交接使用/保护仍按原固定期限结束，其已过期的下载贡献不再影响新整合。来源撤销以实际事件时刻启动已有本地保护的 60 秒尾期，重复失败不续期。

缓存最终整包回归通过（deadline-sources-cache-final.log，32.898 s）；blocksched/console/cmd 完整回归通过（deadline-sources-full.log，0.204 s / 59.743 s，缓存首轮失败见下），缓存及调度整包 Windows race 通过（deadline-sources-race-full.log，45.145 s / 1.127 s）。来源/重接三轮 race 通过（deadline-sources-race.log，20.508 s）；最后的响应头前拒绝、来源撤销、队列重算及完成夹具三轮 race 通过（deadline-sources-final-directed.log，8.447 s）；相关包 go vet 与 diff 检查通过。

新增覆盖：GET 明确失败且原上下文有效、无交接、固定尾期不被重复通知延长；手动与较晚队列仍参与整合；过期队列重复注册被拒绝、实际播放改变队列时钟后可再准备；过期交接保留保护而无执行资格；初始过期不发送 HTTP；Range/200 与手动共享时旧响应清理前无重叠执行，GET 收到截止错误，手动重新下载并完成校验发布。非截止错误仍使用原共享失败处理，没有引入一般按块错误隔离。

失败证据保留：初始筛选误选到依赖硬链接/重命名的用例，沙箱拒绝其 Windows 临时目录操作；改用已获授权的真实文件环境完成验证。完整回归发现两个夹具依赖旧实现：显式 Task.Close 被期待终止整会话、完成时序钩子仍包在原请求上下文；分别改为验证任务回收后重算、在拥有生命周期的来源上下文上保持原时序门闩。日志均在 artifacts/download-resource/。尚需最终按 canonical 文档逐项核对并清理已解决的待决说明；未启动 VRC、修改 hosts 或 push，游戏剩余风险继续合并至 LA-040/041/047。


## 55. 共用字节与块范围转换

2026-10-19：最终按 canonical 第 1 项核对时，将仍分散在来源、消费与准备中的向外取整统一到 ranges.go 的 blocksForRange/blockCount/blockBytes；本地块存储使用同一几何工具。采用非负起点、右开终点，与已有 HTTP 适配及存储接口一致；空范围不产生块，块数计算避免先加块大小造成整数溢出。没有增加独立翻译服务或重复状态。

复用既有 GET 首尾部分块、跨边界、冷热响应、Range 错误、消费进度、完整块 I/O、需求容量及准备测试；Windows race 定向检查通过（shared-geometry-race.log，13.006 s），相关 go vet 与 diff 检查通过。不为算式增加镜像测试。本提交只收拢共用转换，现行规则及最终验证映射另作文档收尾。


## 56. 同会话截止的批量来源重算

2026-10-19：最终检查发现，同一会话多个 GET 截止时若逐个标记再撤销，retimeQueueLocked 可能暂时采用另一个即将失败的 GET 时钟，并在全部移除后保留这个中间值，使结果依赖 map 遍历顺序。现在先冻结整批 GET 的结束原因并取消其来源上下文，再撤销范围、重新整合；真实剩余 GET 照常决定队列时钟，没有剩余 GET 时保留批量退出前的原锚点。

新增两个不同播放结束时刻共享一个失败会话的回归；来源截止、队列重算和 GET/手动清理后重接三轮 Windows race 通过（deadline-batch-withdrawal-race.log，8.195 s），go vet 和 diff 检查通过。本次仅修复同一事件的批量顺序，不改变交接期限、保护或正常独立请求退出语义。
