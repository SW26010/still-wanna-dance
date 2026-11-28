# 块观察紧凑快照与按需详情（2026-11-15）

## 目标与实现

首页需求视图可见时，即使时间轴折叠，也需要逐块状态。因此保留折叠订阅、共享后台观察者、事件触发的 50 ms 合并窗口和现有需求／保护／调度规则。本轮只优化观察 module，不建立另一份调度状态，也没有引入跨帧增量缓存。

原 `capture` 通过 `projectUseLocked` 为每个块建立来源 map，构造全部 `BlockDetail`／来源切片并排序，再反向生成需求列表。用户提供的 4096 块剖析显示，逐块来源 map 约占分配量的 64%；本轮当前基线 `f576f01c` 上复跑原基准为约 5.82 ms、8.33 MiB、44,967 次分配。原报告约 9 ms 的耗时保留为当时条件，不把不同批次的时间差当成优化收益。

实施两项相连的内部修改：

1. 通过已有 `visitUseLocked` 复制紧凑的块／来源关联，来源身份、名称、位置和固定需求属性每来源只保存一份；引用计数只用于预留关联切片容量，不影响实际遍历结果。业务锁内复制必要值，现有 `plannedNeed.use`／`mergeUse` 需求整合、分组、排序、显示字段及需求列表组装在锁外完成，移除产品中的逐块来源 map 投影。
2. 已发布快照仅保留摘要、需求列表及私有的来源记录／块关联。`BlockSnapshot.Detail(id)` 按需生成一个块的完整详情并排序，返回调用方独有的来源切片；读取既不访问当前运行对象，也不修改快照。Console 只将原 `Details[id]` 读取改为该入口，`ObserveBlocks`、订阅 `Snapshot`／`Close` 及传输事件内容保持不变。

现行实现与边界见[按需块观测](../block-scheduler-design.md#按需块观测2026-10-20)。

## 同机矩阵对照

Windows amd64、AMD Ryzen 7 9850X3D、Go 1.27.3。将原基准夹具扩展为 256／1024／4096 个块，每块分别由 1／4 个来源共享；同一命令使用 `-benchtime=300ms -count=3`，表中每项为三次中位数。持续调用实际 `capture`，保持调度器和空 Store 夹具，来源身份缓存沿用实际观察者；没有业务服务、网络传输或游戏负载。

| 块数／共享来源数 | 完整 capture 耗时 ms（前 → 后） | 分配 MiB/op（前 → 后） | allocs/op（前 → 后） | Server.mu 持有 ms/op（前 → 后） |
| --- | --- | --- | --- | --- |
| 256／1 | 0.310 → 0.107 | 0.512 → 0.164 | 2,660 → 707 | 0.107 → 0.027 |
| 256／4 | 0.632 → 0.300 | 0.648 → 0.245 | 3,973 → 748 | 0.177 → 0.064 |
| 1024／1 | 1.349 → 0.515 | 2.074 → 0.682 | 11,122 → 3,020 | 0.661 → 0.161 |
| 1024／4 | 2.934 → 1.359 | 2.683 → 1.066 | 16,284 → 3,070 | 1.382 → 0.242 |
| 4096／1 | 5.668 → 2.043 | 8.329 → 2.733 | 44,967 → 12,275 | 2.515 → 0.667 |
| 4096／4 | 12.329 → 5.073 | 10.766 → 4.266 | 65,498 → 12,334 | 3.662 → 1.065 |

4096 块单来源的分配量降低约 67%、分配次数降低约 73%、完整 capture 耗时降低约 64%；四来源共享的分配量降低约 60%。表中结果用于比较同一合成负载下的观测成本，CI 沿用现有断言。

持锁数据来自独立的临时插桩轮次：取得 `Server.mu` 后开始计时，在释放前结束，包含内层 `retentionMu` 的锁域；不包含等待取得 `Server.mu` 的时间，也不单独声称测到了 retention 锁的持有或竞争时间。没有竞争方，测量覆盖无竞争时的持锁开销。插桩副本、原始源码和计时脚本留在忽略的证据目录，每次在 `finally` 中按原字节恢复；正式实现没有计时字段、回调或性能开关。

单块 `BenchmarkBlockDetail` 在三种总块数下均只分配一次：1 来源为 96 B，4 来源为 352 B；读取成本只展开所选块来源，未预生成其他块详情。该轮时间约 60–666 ns，受并行验证影响，不据此声称任意总量下延时固定。`BenchmarkBlockObservationDisabled` 仍为 0 B、0 次分配，约 0.37–0.38 ns/op。

可重复命令：

```powershell
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run '^$' -bench '^BenchmarkBlockObservation' -benchmem -benchtime=300ms -count=3
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run '^$' -bench '^BenchmarkBlockDetail$' -benchmem -benchtime=200ms -count=3
```

本轮证据均在忽略目录 `artifacts/block-observation-optimization/`：原基准为 `baseline-original.txt`，矩阵为 `baseline-matrix.txt`／`final-matrix.txt`，锁计时为 `before-lock.txt`／`after-lock.txt`，按需详情为 `detail-matrix.txt`，汇总为 `comparison.json`。`after-matrix.txt` 保留首轮未预留共享关联容量的结果；后续基于已有引用计数预留容量，4096 块四来源从约 6.77 MiB 降至表中 4.27 MiB。未覆盖前序结果。

## 行为与生命周期验证

- 既有块观察、队列增长、来源退出、交接、保护尾部、标题优先级、稳定身份、按视频块号排序及可配置块大小检查通过。原测试经 `Detail(id)` 读取同样的详情结果，没有删弱内容断言。
- 新增检查先发布快照，再更改来源名称、位置、时钟和撤销共享来源，首次读取旧详情仍得到旧值；验证来源 ID 排序、缺失块、返回切片由调用方独立持有，以及八个并发读取方不会互相修改结果。
- 定向 `-race` 通过，覆盖 `TestBlockObservation*`、`TestConfiguredBlockSize*`、`TestUnifiedHandoff*`、`TestUsageHandoff*`、`TestSourceExit*`，使用本机已有 GCC 16.2.0。观察生命周期检查继续验证两个订阅共享同一观察者，最后关闭后快照、来源和视频资料释放，观察工作协程、速度／缓存采样及调度通知退出。
- Console 的 `TestBlockEvents*`、`TestBlockDelta*`、`TestBlockEngineWaiter*`、`TestRealtimeHistoryAndBlockSockets` 通过；原 SSE 兼容检查、重连发送锚点与现行 WebSocket adapter 均沿用。
- 扩展现有 `TestBlockTimelineBrowser`，使用真实 Console、内嵌 UI、调度器、临时块文件及受控 Range 上游在 Edge `157.0.4341.60` 检查：首页折叠仍有需求订阅；悬浮详情离开后关闭；双页固定同一块时共四条块订阅；关闭一页仍保留另一页的概览和详情；离屏全部释放并恢复固定详情；3,145,765 字节缓存显示与文件一致；需求结束后转保护尾部；无订阅期间物理回收；返回后缓存为零。`browser/result.json` 与同目录截图保留结果，无 pageerror。
- 改动包 `go vet`、`gofmt`、`git diff --check` 和 `pnpm --dir webui check` 通过。未改前端源码，既有内嵌资源及构建清单保持一致。

真实首页复跑：

```powershell
$env:SWD_TIMELINE_BROWSER_EVIDENCE = Join-Path (Get-Location) 'artifacts/block-observation-check'
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestBlockTimelineBrowser$' -count=1 -v
```

## 边界与保留的限制

首次锁计时尝试使用 Go overlay，被仓库补丁入口按既有规则拒绝，未修改该限制；改用临时源码插桩并通过同一受审查入口运行。失败的 Python 启动尝试未进入测量，最终使用现有 Node；不将工具启动失败当作性能数据。

本轮不做跨帧增量缓存、调度策略变更或刷新降频。未执行全部 Go／UI 套件、完整 race 或发布前完整预检。未启动游戏或修改系统 hosts，没有新增必须 VRC 判断的剩余项。以上为未提交工作区结果，本轮未 commit／push。
