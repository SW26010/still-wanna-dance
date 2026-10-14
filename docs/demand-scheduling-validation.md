# 需求调度验证（issue #22）

2026-10-11 起的 Windows 本地自动化验证记录。各节保留对应版本的行为；当前实现范围和运行语义见[队列预缓存](queue-prefetch.md#资源需求与优先级)，2026-10-14 的 GET 宽限和等级顺延见本文末节。这些结果不代表当前构建已完成 VRChat 实机验收。

## 自动化结果

| 检查 | 结果 |
| --- | --- |
| `go test ./cmd/... ./internal/... ./data/... -count=1 -timeout=240s` | 全部通过 |
| `go vet ./cmd/... ./internal/... ./data/...` | 通过 |
| `go build ./cmd/...` | 通过 |
| 新增缓存调度与 GET 生命周期回归，`-count=20` | 通过 |
| 控制台删歌、换房、移出窗口、新队首重排回归，`-count=10` | 通过 |

新增缓存回归覆盖：

- 多个队列 ID 共享 MD5；删除一份需求不取消另一份仍需要的内容。
- GET 已收到前段、等待新字节期间保持 P0；队列变化优先抢占其他低等级下载，最后一个 GET 结束后重新评估。
- 两个 GET 引用分别释放；请求取消在回调执行前也立即影响有效等级。
- 无竞争时，取消预取调用不浪费已启动的下载，最终仍完整校验落盘。
- 人为延迟网络正文的 `Close`，确认旧 worker 清理前后台计数及真实额度不释放，也不发布部分内容。
- 同 MD5 的旧 worker 正在取消时收到 GET，等待清理后再启动；P0 先于等待的 P3 获取额度，不重复下载。
- HEAD 与已完成的短 Range GET 不留下 P0；后续 GET 可以抢占已无需求的旧下载。
- 结束后没有遗留 `.part` 文件。既有共享下载、Range、选路、缓存交接保护、淘汰与关闭测试随正式包测试通过。

可重复执行核心缓存回归：

```powershell
go test ./internal/cacheproxy -run 'TestDemandRanks|TestQueueDemandPreemption|TestNoCompetition|TestPreemptionWaits|TestGETReattaches|TestHEADAndCompleted' -count=20 -timeout=60s
go test ./internal/console -run TestQueueInvalidation -count=10
```

## 环境边界

本机直接运行 `go test ./...` 会纳入 Git 忽略的历史 `artifacts/` 和 `test-runs/` 探测源码，其中存在重复 `main` 和旧 `stepstash/internal/cacheproxy` 引用，导致总体失败；正式代码包通过。保留这些本地文件，使用上表明确的正式包路径验证。

受限 Windows 沙箱阻止部分原子重命名、文件身份和链接测试；完整正式包测试在正常权限下使用隔离临时目录执行，没有修改真实 hosts 或启动游戏。仅新增缓存回归在仓库内专用临时目录也通过。本机未配置 Windows race 所需的 C 编译器，因此未运行 `-race`，应由已有 Linux CI 补充；本次按要求没有 push 或触发远程 CI。

## 延后的真实游戏检查

1. 两首慢速预缓存时删除旧队首并加入新队首，确认新队首及时开始，日志记录抢占原因，旧资源没有半成品落盘。
2. 游戏 GET 正等待后续 Range 块时删歌或换队首，确认当前响应不断流；播放器停止或断开后，旧下载按剩余需求降级。
3. 快速离队、重新入队、换房和停止监听，检查没有旧队列错误复活；普通离队的 30 秒文件保护仍有效。

本阶段先完成代码与可控网络回归；以上实机检查尚未执行。

## 2026-10-14：失去整视频席位后的进度核查（修复前）

**以下保留修复前的核查结果；当前行为见下一节。** 当时跨视频抢占会取消整个 flight；已完成分块所在的临时文件和未完成分块的内存前缀均不保留。仍有需求时会自动等候席位并重试，但重试从首块开始。它与 #20 的同一 flight 内首块/Seek 让位不同：后者保留 worker 的部分缓冲，从 `start + received` 请求剩余区间。

代码依据：`demand.go` 的 `preemptLocked` 调用 `cancel(errPreempted)`；`cache.go` 的 worker 收尾调用 `spool.finish/release` 并移除 flight；`stream.go` 在最后引用释放时关闭、删除 `.part`；`prefetch.go` 捕获抢占错误后重新 `obtainMode`；`routes.go` 的 `openUpstream` 重新请求从 0 开始的首块。`ranges.go` 的 `received`、缓冲、完成块集合和候选进度只属于当前 flight，不跨任务恢复。

新增 `internal/cacheproxy/preemption_resume_test.go` 的 `TestWholeFlightPreemptionRestartsRangeDownload` 是**现状特征测试，不是续传修复**：

1. 使用受控内存上游和隔离临时库，将资源席位设为 1；4 MiB 视频完成前两块（2 MiB 落盘），另两块各接收 64 KiB 后阻塞，共已收到 2,228,224 字节（2.125 MiB）。
2. 发起另一视频的前台 GET，触发真实调度路径中的抢占。确认旧任务返回 `errPreempted`、临时文件已删除，且半成品没有发布。
3. 释放前台请求，保留原预取调用使其自动重试。确认 0、1、2、3 MiB 四个起点各请求两次，最终 4 MiB 内容完整发布且临时文件清理；此前收到的全部 2.125 MiB 均被重新获取。

对照已有 `TestSeekYieldsAndResumesPartialBlocksOnTheirExistingChannels`：同一 flight 内让位后从分块内 64 KiB 偏移恢复，保留已选通道/重定向终点；实体变化和错误 Range 仍拒绝。不能将该对照的通过外推为跨视频席位续传。

自动检查（Windows，产品基线 `796bfa18a347fb9299c9ffccd0b9efb4f7cc70b6`，另含本次新增测试）：以下五组测试连续 10 轮通过（3.998 秒）。通过表示当前行为已稳定复现，不表示已经支持整视频续传。

```powershell
go test ./internal/cacheproxy -run 'TestWholeFlightPreemptionRestartsRangeDownload|TestSeekYieldsAndResumesPartialBlocksOnTheirExistingChannels|TestQueueDemandPreemption|TestPreemptionWaits|TestGETReattaches' -count=10 -timeout=120s
```

初次使用沙箱默认临时目录时，发布阶段 `Rename` 被系统拒绝；改用仓库内忽略目录 `artifacts/seat-preemption-check/tmp` 作为进程级 `TEMP/TMP` 后执行。此处只记录开发测试，未启动游戏或修改 hosts。此次只新增测试和本文，不改变产品运行行为，不新增实机编号；**LA-008 / LA-037 保持未结项**。后续若实现席位暂停续传，应更新该现状测试，并登记暂停文件/部分块保留、重新获席位后的请求区间、内容身份变化拒绝与最终校验等验收条件。

## 2026-10-14：本次运行内的席位续传

开发实现位于 `range_resume.go`、`ranges.go`、`cache.go`、`stream.go`、`routes.go`、`server.go`。被抢占的 Range 下载保留原临时文件，未完成块的有效前缀在 worker 退出前写入；进度元数据仅由当前引擎持有。暂停不占席位、HTTP 额度或块缓冲。重新取得席位后重建读取状态、继续使用原文件并恢复候选进度；旧读者延迟关闭不会误删仍被使用的文件。保持原优先级、同 MD5 去重、Range/强 ETag 校验与最终大小/MD5 校验。无 Range 的 200 顺序回退仍重新下载；完整块尚未验证 EOF 时仍须重取。

正常关闭引擎时先取消并等待任务与读者，再关闭、删除所有暂停文件。失败删除有警告；下次启动沿现有受管清理规则重试，进度不跨引擎恢复。未被重新请求的暂停资源保留到引擎关闭，不自动复活下载；临时磁盘空间不计入原本只约束正式缓存的 `MaxCacheBytes`。

现状测试已替换为 `TestWholeFlightPreemptionResumesRangeDownload`：同样的 4 MiB/已收 2.125 MiB 场景，在首块和第二块各只请求一次的情况下完成；两个部分块分别从块内 64 KiB 续取，暂停时确认前缀已在文件中，最终文件一致。覆盖自动预取恢复、前台 GET 接手、弱 ETag、强 ETag 变化、错误 Range、正文损坏及关闭清理；恢复不重访已失败通道或已跟随的重定向。暂停写入失败保留 `ErrLocalStorage`，不保留错误检查点，也不继续尝试其他下载。

新增反复抢占回归覆盖首块连续三次暂停、偏移累计至 192 KiB、每次暂停后席位和 HTTP 额度归零、旧读取器延迟关闭、完整字节未确认 EOF 的重取；Windows 清理失败测试验证占用导致删除失败时有日志、释放占用后新引擎清理残留且不恢复进度。响应头前被抢占的正常通道恢复时仍可使用。

自动检查（Windows amd64，Go 1.27.1，race 使用已有便携 GCC 16.2.0，进程级 `CGO_ENABLED=1`；测试数据在忽略目录 `artifacts/seat-preemption-check/tmp`）：

| 检查 | 结果 |
| --- | --- |
| `go test -race -tags=no_easytier ./cmd/... ./internal/... ./data/... -count=1 -timeout=240s` | 14 个有测试包通过；包含正常关闭、启动清理和隔离子进程强杀后清理 |
| 最终定向 race 回归，下面的命令，连续 10 轮 | 通过，18.208 秒；覆盖本次新增场景、响应头前让位、Seek 续传、并行 Range 失败清理及原抢占回归 |
| `go vet -tags=no_easytier ./cmd/... ./internal/... ./data/...`；最终 `go vet ./internal/cacheproxy` | 通过 |
| `git diff --check` | 通过 |

```powershell
go test -race ./internal/cacheproxy -run 'TestWholeFlight|TestRepeatedPreemption|TestPreemptionDoesNotTrust|TestPausedCleanup|TestResourceResume|TestSeekYields|TestParallelRange|TestPreemptionWaits|TestGETReattaches|TestQueueDemandPreemption' -count=10 -timeout=120s
```

全仓检查后收尾去除重复的候选恢复调用、优先保留并发发生的本地写盘错误，并补充同 MD5 不同大小拒绝测试；上述最终定向检查与缓存包 vet 覆盖这些收尾变更。首次沙箱内缓存包全量运行的硬链接、目录联接及含硬链接的子进程夹具被权限拒绝，在普通测试权限下全仓 race 通过。最终定向回归有一次沙箱内 ThreadSanitizer 报 `failed to restore address` 而中止（没有测试断言失败或 DATA RACE 报告），同一命令在普通测试权限下十轮通过；保留失败记录，不将检测器中止计为通过。原始开发日志仅留在忽略目录：`cache-tests.log`、`full-race.log`、`resume-race.log`、`final-resume-race.log`、`final-resume-race-outside.log`。

仅为开发验证，**开发完成，新增 LA-038 待实机验收**，联动 LA-008/018/037。没有启动游戏、修改 hosts 或新增实机通过结论；未执行 Git commit/push。

### 恢复首块调度的审查修复

**本节保留首轮修复及其验证记录；后续发现的尾部跳转循环等待与当前修正规则见下一节。**

审查反例已纳入 `range_resume_startup_test.go`：六块视频中块 0 未完成、块 1/3 已完成，两个读者只读取完整块，因而仅留下预读位置，没有缺块需求。修复前 `TestResumedForegroundClaimsStartupBeforeReadAhead` 的两个子用例均取得 `claimed=[2 4 5]`，所有后台 worker 将等待无人领取的首块；读者关闭也不能释放这个依赖。

首轮修复让 `range_work.go` 先选择真实 FIFO 读取需求，再让后台 worker 领取未完成且无人负责的启动首块，最后才选择近端预读与其他缺块。首块若先被专用读取 worker 领取，也不能因唯一读者关闭而放弃，否则其他后台 worker 仍可能全部等待它；补充用例在这个保护加入前复现 `context canceled (queued=[2 4 5])`。当时首块领取者保留到完成或整个 flight 取消，单次尾部读取对照通过，但没有覆盖专用读取 worker 保留首块后再次 Seek 的连续操作。

自动检查（与上节相同的 Windows、Go/race 工具链及隔离临时库；仅开发测试）：

| 检查 | 结果 |
| --- | --- |
| 三个 `TestResumedForeground*` 测试，含读者保持打开/关闭、两类 worker 的尾部需求及首块读者关闭 | 修复前分别确认无人领取和领取者被取消；最终随下述定向 race 连续 20 轮通过 |
| 下述定向 race，连续 20 轮 | 通过，33.721 秒；同时覆盖整视频/反复抢占、未确认 EOF、Seek 续传、并行 Range、领取顺序及原需求抢占回归 |
| 最终 `go test -race ./internal/cacheproxy -count=1 -timeout=180s` | 通过，26.406 秒；包含下述已有测试夹具的修正 |
| `go vet ./internal/cacheproxy`、`git diff --check` | 通过 |

```powershell
go test -race ./internal/cacheproxy -run 'TestResumedForeground|TestWholeFlight|TestRepeatedPreemption|TestPreemptionDoesNotTrust|TestSeekYields|TestParallelRange|TestRangeScheduling|TestPreemptionWaits|TestGETReattaches|TestQueueDemandPreemption' -count=20 -timeout=180s
```

验证中保留两类失败记录。首次沙箱定向 race 遇到 Windows ThreadSanitizer `failed to restore address` 中止，未计为通过；普通测试权限下重跑成功。随后缓存包全量暴露已有 `TestQueueCancellationDoesNotCancelJoinedRangePlayback` 夹具的偶发问题：Seek 合法让位后重试首块，重复 `close(prefix)` 导致上游 panic，且将 HTTP 请求数不等于 2 误判为创建了另一个下载任务。用修复前的 `range_work.go` 作 Go overlay，对原夹具无延迟复查 120 次未复现；仅在该用例的尾块正文前加入 25 ms 延迟后 5 次均复现。`song_load_test.go` 改为一次性通知首块开始，并直接检查前后共享的 flight 对象；保留相同延迟的对照在修正后 20 次 race 通过（2.985 秒）。延迟仅在忽略目录的定位探针内，未加入正式测试或产品。此夹具修正不改变产品行为，不另增实机项。

原始开发日志保留在忽略目录 `artifacts/seat-preemption-check/`：`startup-before-fix.log`、`startup-owner-before-fix.log`、`startup-after-fix-race.log`、`startup-final-race.log`、`startup-final-cache-race.log`、`startup-existing-fixture-baseline.log`、`startup-existing-fixture-baseline-100.log`、`startup-existing-fixture-latency-baseline.log`、`startup-fixture-fixed-race.log`、`startup-final-cache-race-fixed-fixture.log`。首块责任与尾读验收步骤已补入 **LA-038，仍待实机验收**；没有启动游戏、修改 hosts 或执行 commit/push。

### 后续尾部跳转的循环等待修复

后续审查指出，首块读者关闭后，专用读取 worker 仍可能持有首块；新的尾部 Seek 使它等待尾块，而三个后台 worker 都在等待首块，尾块无人领取。此前“保留首块负责人”的保护还必须限定领取者角色。

当前 `range_work.go` 在选择真实需求时，禁止专用读取 worker 领取启动首块；由后台 worker 按原有需求及启动优先级取得首块并负责到完成或整个任务取消。专用读取 worker 可以跳过较早的首块需求处理尾读，首块暂停时仍有 worker 可处理后续 Seek。其他需求的 FIFO、首块优先于推测性预读、部分续传及全局请求预算不变。

`range_resume_startup_test.go` 将首块读者关闭测试改为后台持有首块，并补充 `TestResumedForegroundReservesReaderForLaterTailSeek`：块 1/3 已完成，首块和尾块 5 均有需求，先让专用读取 worker 选择；修复前它错误领取块 0，回归失败。修复后它完成尾块 5，后台领取首块及其余预读块；关闭首块读者并请求未领取的尾块 6，使首块让位。专用读取 worker 仍能完成尾块 6，随后首块及其他等待块全部完成。这一连续场景与原有已缓存块读取、读者关闭、尾读绕过首块一起验证。

自动检查（沿用上述 Windows/race 工具链与隔离临时库）：

| 检查 | 结果 |
| --- | --- |
| 上节定向 race 命令，连续 20 轮，含新的连续 Seek 回归 | 通过，33.796 秒 |
| 最终 `go test -race ./internal/cacheproxy -count=1 -timeout=180s` | 通过，26.352 秒 |
| `go vet ./internal/cacheproxy`、`git diff --check` | 通过 |

首次完整测试在已有 `TestParallelRangeGlobalBudgetLeavesRoomForPlaybackAndSeek` 的上游 handler 峰值断言失败（8），没有抹去该记录。该夹具将客户端已经关闭、但服务器尚未退出的 handler 也计为在途请求。用修复前暂存的 `range_work.go` 作 overlay，仅让 handler 延后 25 ms 返回，5 次均复现（峰值 7–10）。`range_parallel_test.go` 改为在客户端 transport 发起请求时计入，失败或正文 `Close` 完成后归还，并检查全部下载完成时计数为零；原有前台/Seek 可推进及全局最多 6 个请求的断言保留。同样的 handler 延迟对照修正后 20 轮 race 通过（6.595 秒），正式测试不加入延迟。这是测试观测边界修正，不改变产品请求额度，不另增实机项。

原始日志在忽略目录 `artifacts/seat-preemption-check/`：`startup-tail-before-fix.log`、`startup-tail-final-race.log`、`startup-tail-cache-race.log`、`startup-tail-budget-baseline.log`、`startup-tail-budget-fixed.log`、`startup-tail-cache-final-race.log`。**开发完成，LA-038 仍待实机验收**；台账已补充首块关闭后连续尾读的时序及 worker 证据要求，没有启动游戏、修改 hosts 或执行 commit/push。

## 2026-10-14：GET 重连宽限（方案三）

在 `392245f18eb379507a60c6ffba8e380d6b9f544b` 的引擎内抢占续传基础上实现用户选定的方案三：P0 为仍有有效视频 GET 的资源；最后一个 GET 结束或取消后，未完成下载或暂停检查点获得固定 1 秒的 P1；原队首、后续队列、补齐需求顺延为 P2、P3、P4。按 MD5 合并需求，同一资源的重叠 GET 不提前启动宽限；重连立即恢复 P0，下一次全部结束再计算完整 1 秒。

实现位于 `demand.go`、`cache.go`、`server.go`、`request_budget.go`。到期主动唤醒容量等待者，按当时的队列/补齐需求重新排名；队列清空或变化不抹掉尚未到期的 GET 宽限，引擎关闭则立即清除计时器。旧计时器使用状态身份校验，不能降级已经重连的新 P0。完成发布在唤醒读取者之前清除宽限，避免已完成下载在清理窗口内重新取得 P1。HEAD、完整缓存读取和没有下载任务的结束请求不保留宽限。

P1 可以被活跃 P0 抢占；存在可抢占的更低级资源时，P0 优先选择后者。P1 不获得 P0 的 HTTP 优先额度，也不独立创建或复活下载任务。已有有效等待者按资源合并后的排名参与调度；已取消或已移出队列的等待者不能借 P1 复活。被抢占的 Range 任务沿用现有检查点续传，原有大小/MD5 校验、非 Range 顺序回退及引擎内恢复范围不变。

新增 `playback_grace_test.go` 覆盖重叠 GET、取消回调尚未运行时的排名、重连与旧计时器竞态、精确到期与主动唤醒、队列清空/重排、完成及热缓存排除、抢占选择、有效等待者与无效等待者、HTTP 优先额度和关闭清理。`TestWholeFlightPreemptionResumesRangeDownload/grace` 验证 P1 被新 P0 抢占后，两个完整块不重取、两个部分块分别从块内 64 KiB 续取，最终内容与 MD5 一致。已有优先级与短 GET 抢占测试同步更新。

自动检查（Windows amd64，沿用已有 Go/race 工具链；测试临时目录与日志均在忽略目录 `artifacts/playback-grace-check/`）：

| 检查 | 结果 |
| --- | --- |
| 定向普通回归，覆盖宽限、排名、HEAD、队列让位及抢占续传 | 通过，3.299 秒 |
| 下述定向 race 连续 10 轮 | 通过，33.726 秒 |
| 最终 `go test -race -tags=no_easytier ./cmd/... ./internal/... ./data/... -count=1 -timeout=240s` | 14 个有测试包全部通过；缓存包 29.615 秒，包含完成发布和无效等待者的最终收尾修正 |
| `go vet -tags=no_easytier ./cmd/... ./internal/... ./data/...` | 通过 |
| `gofmt`、`git diff --check` | 通过 |

```powershell
go test -race ./internal/cacheproxy -run 'TestPlaybackGrace|TestDemandRanks|TestHEADHasNoGrace|TestQueueDemandPreemption|TestWholeFlightPreemptionResumesRangeDownload|TestRepeatedPreemption|TestGETReattaches|TestVideoRequestBudget' -count=10 -timeout=120s
```

首次定向 race 发现新增完整下载测试误传空上游 handler，导致夹具 panic/502；修正为返回测试正文后重跑通过，没有 DATA RACE 报告。失败日志 `targeted-race.log` 保留，不计为通过。最终全仓 race 在普通测试权限下运行，以覆盖需要 Windows 文件链接及子进程的已有夹具。其余原始日志为 `targeted.log`、`targeted-race-final.log`、`full-race.log`、`vet.log`；定向十轮之后的产品收尾修改由最终全仓 race 覆盖。

**开发完成，LA-039 待实机验收，联动 LA-038 的抢占续传验证。** 历史 LIVE-20261014-02 只记录旧版本 GET 行为，不作为本次宽限策略的通过依据。本次没有启动游戏或修改 hosts。
