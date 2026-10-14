# 全局块调度开发记录

依据会话「全局调度器设计」（`01a13b63-0ffa-7ad2-918a-fe93a9f25c70`）最后确认的模型实施，分批本地提交，不推送。

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
