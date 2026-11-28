# 发布前功能诊断（2026-11-15）

基线 `af7b0ae16360e984695cf2ee2d9f4db0290a4658`，本轮只新增曲库检查与验证记录，没有修改产品行为。未启动游戏、网关、后台 Monitor 或修改 hosts；测试使用隔离数据库、配置和工作目录。不是发行全量预检，也不是新的游戏验收。

## 空库更新与失败恢复

针对 [2026-10-28 的空库 EOF 观察](../validation-followups.md)，新增 `internal/console/catalog_recovery_test.go`：

- `TestEmptyCatalogRefreshRecoversAfterEOF` 使用真实回环 HTTP、生产 Console 更新入口及 SQLite，分别返回空正文和声明长度不足的截断正文。验证无完整清单时不伪报覆盖率，库存保留缺清单错误，曲库同步状态保留具体原因；同一实例再次更新成功后清除两处错误，关闭并重新构造 Console 后仍保留曲库和库存快照。
- `TestLiveEmptyCatalogRefreshAndReopen` 复用 `SWD_LIVE_CATALOG=1` 显式入口，使用生产 DNS 解析器、默认直连客户端和真实清单地址，在无已接收清单的独立数据库上执行更新。取得 **10,575 首**、版本 **20261114135116**，更新耗时 **3.381 秒**，关闭并重新打开数据库后数量、版本及库存状态一致。未启动视频下载；测试辅助函数仅为隔离配置生成测试条款回执，不修改日常凭据。

本轮已完成原条目的正常直连核对及可控故障恢复检查，没有复现当前版本持续失败或必须重建数据库的问题。**不把 10 月 28 日的 EOF 追溯归因为临时代理，也不称作产品修复。** 本次公网结果只证明该时段和网络；重开为同一测试进程中的真实关闭／重新构造与磁盘持久化，不冒充发行 EXE 跨进程升级。

## SWD 恢复检查与世界播放器行为边界

**本轮未发现 SWD 请求处理或供数恢复缺陷。进度回零发生在 WD owner 重载后重设起播基准的路径；刷新按钮未触发重载也是世界内部的行为。两项按世界播放器行为记录，不列为 SWD 待修功能、待验阻塞或正式发布条件。** 保留观察是为了区分视频传输与世界播放控制，不表示 SWD 承诺修复世界播放器。

继续复用 [owner 回零](../acceptance/live-acceptance-20261113-05.md)和[加载中刷新](../acceptance/live-acceptance-20261113-09.md)的原始结论：

- owner 样本中，正文中途截止后 WMF 按非零位置续读；恢复供数后 HTTP 仍非零续读。WD 重载时进入 owner 分支，重设起播基准并记录 `Syncing video to 0.00`。进度重置发生的位置有直接日志依据；SWD 的截止可触发播放器错误恢复，播放进度是否回零由播放器的恢复行为决定。
- 加载窗口出现按钮点击但没有重载回调，正常播放／刚卡顿对照能触发回调；没有证据说明内部是哪条条件拒绝刷新。

本轮运行以下现有测试，**5 个顶层用例全部通过，4.771 秒**：`TestRequestTerminalFailureHistory`、`TestExpiredGETKeepsFutureBlockExecution`、`TestExpiredGETBeforeHeadersFailsWithoutUpstream`、`TestStreamResponsePreservesCancellationDuringSeek`、`TestRangeFailureStatusPreservesRootCause`。

检查覆盖响应开始前的到期／超时／上游失败映射、响应开始后的中断及历史结果、后续需求重新接入与在途块共享。普通上游失败和到期在已提交正文后均走中断响应路径；未提交时错误状态按原因区分。部分用例直接调用生产 HTTP 处理函数和响应记录器，**不是 WMF、WD 或真实 TCP 客户端的等价对照**；到期历史用例注入错误原因，真实调度交接另由 continuation 用例覆盖。普通上游故障下的 owner 进度需结合对应实机事件判断。

### 世界内部实现的调查边界

本地源码、`local/`、`experiments/`、`artifacts/` 和备份目录中未找到对应的 `USharpVideo`／播放器／刷新按钮源码。通过 GitHub connector 搜索 `owner not relaoding` 和 `ReloadVideo Button Clicked`，分别只得到本仓库的 3 和 4 条报告／证据记录，没有得到世界实现；WannaDance 仓库名与网页搜索也未取得可确认属于目标世界版本的工程。非公开或未被索引的源码超出此次搜索范围。用户确认没有对应源码或工程地址。

因此未能建立可无人运行、直接判断“owner 保持原进度”和“加载中刷新触发重载”的世界内部反馈回路。该限制不构成 SWD 的验证欠账。本轮结束 SWD 侧专项跟进，保留实际观察，不把世界进度回零改写成保持进度通过；不修改 N+30 秒或交接策略。

若以后另行开展世界播放器兼容性研究，可使用以下条件；不是发布前待办：

1. 取得对应世界版本的按钮处理、重载入口、加载状态和 owner 起播时间处理实现，建立实际路径对照。
2. 用户以后安排游戏验收时，先准备可控的普通上游故障基线，与 SWD 截止在相同歌曲、角色和已开播条件下比较。必须能确认故障注入没有经过待比较的 SWD 截止路径，否则不是有效对照；只围绕能区分责任的条件，不追加换歌／重复点击矩阵。

无需为发布取得 WD 源码或补普通直连故障矩阵。只有以后出现指向 SWD 请求处理异常、数据错误或可避免回归的具体证据，才按实际问题重新开启 SWD 缺陷调查；缺少世界源码本身不构成这样的证据。

## 命令、结果与保留的失败

### 文档与对外说明复核

同日复核中英文 README、使用指南、功能范围、资源域名／上游／队列／下载说明、WebUI 帮助与请求结果文案，以及关联验收和跟进记录。修正了使用指南与资源域名文档中已被后续实测替代的“API 接入仍是假设／中途接入等尚待验证”表述；README 改用请求记录指导用户确认接入，分别说明 SWD 的预加载与复用价值、上游供数和播放器控制职责。关联报告中的“owner 失败／未修复”明确为 WD 重载后重置进度的观察，旧慢网记录不再形成无具体缺陷依据的当前待办。

WebUI 已按证据区分客户端取消、上游错误、下载截止和本地故障，本轮无需修改界面或重新构建。真实功能边界（HTTP 加速／HTTPS 透传、SHA 未支持、视频预算、跨重启续传等）、已确认的历史 SWD 缺陷及原始证据保留；不把原因未知的错误改称外部故障。未修改公开 Release 或发送外部消息。

### 执行记录

Windows 使用仓库临时目录包装器，首次沙箱运行在测试配置保存处出现 `renameat .terms.json.swd-tmp terms.json: The handle is invalid`。普通用户权限复跑后解除该限制。

```powershell
$env:SWD_LIVE_CATALOG='1'
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run 'Test(EmptyCatalogRefreshRecoversAfterEOF|LiveEmptyCatalogRefreshAndReopen|CatalogInitialization|CatalogTruncatedBodyIsNotFormatError|BackgroundCatalogRecoveryClearsManualRefreshError|CatalogFallbackCompletesSilently)' -count=1 -v
```

该轮公网用例通过；新增故障用例最初要求库存错误包含 EOF，实际具体原因归属曲库同步状态，空 JSON 的解析错误也不叫 EOF。修正测试以同时检查两个状态，没有改变生产代码。首轮失败日志保留，不作为产品缺陷复现。

移除公网环境变量后运行：

```powershell
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run 'Test(EmptyCatalogRefreshRecoversAfterEOF|CatalogInitialization|CatalogTruncatedBodyIsNotFormatError|BackgroundCatalogRecoveryClearsManualRefreshError|CatalogFallbackCompletesSilently)' -count=3 -v
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run 'Test(RequestTerminalFailureHistory|ExpiredGETKeepsFutureBlockExecution|ExpiredGETBeforeHeadersFailsWithoutUpstream|StreamResponsePreservesCancellationDuringSeek|RangeFailureStatusPreservesRootCause)$' -count=1 -v
```

结果分别为 `ok internal/console 6.241s`（8 个顶层用例各 3 轮）和 `ok internal/cacheproxy 4.771s`。Console 的 `vet -tags=no_easytier`、gofmt 与 `git diff --check` 也通过。日志保存在忽略目录 `artifacts/release-functional-diagnosis-20261115/`：`catalog-baseline.log`、`catalog-recovery.log`、`catalog-recovery-recheck.log`、`playback-http-boundaries.log`、`vet.log`。未执行全量测试、race、浏览器或新的游戏验收；没有产品代码修改需要以这些结果冒充修复完成。
