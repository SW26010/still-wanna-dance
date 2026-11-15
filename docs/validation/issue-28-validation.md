# #28 VRCX 历史偏好导入验证

2026-10-20。实现范围与现行契约见[歌曲统计设计](../song-catalog-design.md#vrcx-历史初始化)；本次增加可跳过的引导/设置入口、只读预览、空统计导入及显式整表覆盖，复用原需求计分与存储事务，不增加数据库版本或持久历史副本。

## 2026-11-15：导入与完整缓存播放隔离

旧实现跨越屏障和整个导入事务持有 GET 登记锁，并占用唯一数据库连接。新增 TestHistoricalUsageImportAllowsCachedPlayback 在真实 SQLite 插入触发器中暂停导入，经回环 HTTP 请求完整缓存歌曲：修复前连续两次在 1 秒客户端期限内收不到响应头，释放事务才结束等待，见 artifacts/vrcx-import-playback-before.log。这证明事务耦合，不代表日常导入必然造成同样等待。

最终实现将导入作为统计 module 的有序队列操作，前序观察先写入、替换原子提交或回滚、后序 GET 再累计；等待控制操作的队列空间时也释放 GET 登记锁。闲置库复用同一 worker 执行替换，但不启用采集或清理。Store 保留唯一写连接，另持有播放短读的只读连接；直接视频 URL 的已验证 HIT 先识别相同媒体大小，避免无变化登记等待写连接。导入仅在固定写连接上临时关闭 cache_spill，防止长写入期间提前取得排他锁，结束后恢复原值；保留原日志模式与存储故障机制。脏页内存和正常提交阶段的锁边界见[存储契约](../storage.md)，未改 schema 或配置版本。

最终定向回归通过（3.686 秒，artifacts/vrcx-import-no-spill.log）：真实 HTTP 覆盖歌曲 GET、Range、直接 URL 与 HEAD，分别在成功提交、取消回滚、4,000 首且 cache_size=1 的导入暂停期间返回 HIT；并验证旧已提交统计仍可见，提交／回滚后的需求累计、30 秒去重及 cache_spill 恢复。满队列控制操作等待可取消，GET 登记与响应完成保持原非阻塞溢出策略。闲置导入不清理已有请求明细、不接收新请求，之后启用采集仍正确累计；停止 worker 不关闭播放读连接，Store 关闭后两条连接均不可用。覆盖替换、空表权限、占位歌曲、失败回滚、重启、资源排序和淘汰的既有检查继续保留。

最终定向 Windows race 通过（161.368 秒，artifacts/vrcx-import-final-race.log），包括导入／GET、满队列、闲置导入与采集启用、并发计分、锁冲突恢复、读写连接关闭及事务缓存参数恢复。缓存、Console、VRCX 三包 vet、gofmt 和差异检查通过，见 artifacts/vrcx-import-final-vet.log。

最终三包完整回归通过：cacheproxy 244.138 秒、console 208.906 秒、vrcx 1.492 秒，见 artifacts/vrcx-import-final-packages.log。使用普通 Windows 权限和原有存储故障夹具，命令为 `test -tags=no_easytier ./internal/cacheproxy ./internal/console ./internal/vrcx -count=1`；包含恢复后的冻结数据库、回滚日志、历史可读及存储故障锁存检查。受控 HTTP 只证明服务端交付与统计时序，不声称验证真实播放器体验或任意磁盘条件下的起播耗时。

评审补充：修复已提交结果与请求取消的返回竞态。`TestHistoricalUsageCommittedResultSurvivesCancellation` 包装真实 SQLite 的 Commit，在提交成功后取消请求并暂停回传，用 synctest 确认调用仍在等待 worker；修复前稳定返回 `context canceled`，见 `artifacts/vrcx-import-result-before.log`，修复后按 worker 的成功结果返回。已入队操作继续通过原 context 取消 SQL，只有入队前的等待可直接返回取消。Console 原有导入／重试用例增加已完成覆盖令牌的重试，确认后续新增需求仍保留。

本次补充后的导入与队列回归通过（2.695 秒），VRCX Console 回归通过（11.367 秒）；提交后取消、暂停导入期间播放、覆盖令牌重试的定向 race 通过（cacheproxy 41.288 秒、console 7.104 秒），两包 vet、gofmt 和差异检查通过。输出分别保留于 `artifacts/vrcx-import-result-after.log`、`artifacts/vrcx-import-result-console.log`、`artifacts/vrcx-import-result-race.log`、`artifacts/vrcx-import-result-vet.log`。上段完整三包结果对应这次返回边界补充前的实现；本次按新增风险定向复验，没有重复执行整包。

保留的探索与失败：首次扩展回归暴露直接 URL 的同步媒体登记，见 artifacts/vrcx-import-focused.log。中间方案曾启用业务库 WAL，定向 race 通过，但三包回归暴露日志模式／文件头夹具及 Console 共用回滚日志故障注入助手不再适用；见 artifacts/vrcx-import-packages.log、artifacts/vrcx-import-packages-normal.log。最终收敛为仅导入期间抑制溢出，撤回全局 WAL 及对应夹具修改，不能把中间 WAL 的通过记录当作最终实现证据。首次沙箱 race 的 ThreadSanitizer: failed to restore address 保留于 artifacts/vrcx-import-race.log，不计为通过。

命令均经 scripts/with-test-temp.ps1 node scripts/go-patches.mjs，race 使用已有 w64devkit 2.10.0。只使用合成数据与隔离服务，未读取私人 VRCX 数据、启动游戏或修改 hosts，不新增 VRC 待验项。下文保留 10 月实现和交互调整时的历史验证范围，不作为当前导入锁实现的依据。

## 2026-11-15：合并后的观测同步与导入结果

本节记录 `codex/workspace-2` 与 main 的状态读取隔离、`codex/workspace-3` 的曲库下载观察修复合并时的兼容处理。上节“已入队后等待最终结果”收窄为**开始执行后**：尚未执行的导入可立即响应取消，之后 worker 使用已取消的上下文，不会提交；worker 在开始事务操作前标记执行，调用方此后等待真实结果，保留“提交成功后取消仍返回成功”的边界。

同步屏障与导入共用 `enqueueControl`，等待队列空间不持有生命周期锁或 GET 时间戳／入队锁；保留 main 的 `flush(ctx)`、状态只读已提交统计和屏障结束批次。播放短读与媒体大小无变化检查继续使用只读连接，同时保留 main 的数据库错误分类。新增批量偏好回归适配 main 已有的 `DownloadFillProgress` 回调，没有改变进度语义。

原有满队列／已入队取消回归改用 `synctest.Wait` 确认真实等待状态，不再通过持有某把实现锁判断；取消后释放数据库并排空队列，断言取消的导入没有落库。状态读取回归在真实明细提交回调中暂停 worker，保留不强制排空的检查，移除已经被有序导入替代的通用暂停回调。

定向同步、导入期间真实 HTTP HIT 和提交后取消检查通过（`artifacts/merge-workspaces-20261115/synchronization.log`）；曲库受控 Edge 回归 42 项通过，真实 Console／引擎／Edge 的 `TestLibraryBrowser` 通过（9.010 秒，`library-browser.log` 与 `library-browser/`）。三包 vet 和桌面开发构建通过；本轮未启动构建后的 EXE、VRC 或修改系统 hosts。

首轮合并检查的 cacheproxy 编译失败保留在 `focused-go.log`：新回归仍使用旧进度回调类型。Console 同轮回归通过，cacheproxy 的失败不计为通过；适配后另行执行下述合并回归。此前各工作区的失败与验证范围保持原记录，不外推为本轮全仓库、完整 race 或游戏体验结论。

三包完整执行记录为 `packages.log`：VRCX 通过（1.897 秒），cacheproxy 276.346 秒内仅两项决策互斥回归失败，Console 222.858 秒内仅便携目录改名出现一次 `Access is denied`。两项互斥回归定向连续复现（`failures-repro.log`）；原因是既有夹具没有真实 worker，只设置 `started`，没有同步新增的 `running`，使 `flush` 在到达原屏障前返回。仅补齐合成运行状态及清理，不改变生产实现、等待期限或互斥断言，原用例连续两轮通过（0.820 秒，`retention-fixture-after.log`）。其余 cacheproxy 检查复用本次完整执行结果，未为测试夹具状态调整重复整包。

适配后的两项决策互斥回归另通过 Windows race（20.848 秒，`race-retention-fixture.log`），没有竞态报告。

合并交界处的 Windows race 分组通过：同步／满队列／取消／提交结果 73.411 秒（`race-synchronization.log`），导入期间歌曲 GET／Range／直接 URL／HEAD 播放 35.838 秒及 Console 已完成令牌重试 8.804 秒（`race-playback.log`）。较宽组合曾触及 180 秒整包预算，当时堆栈位于 SQLite 种子夹具构造，无 DATA RACE 报告；保留 `race.log`，该次不计通过。分组没有增加期限、修改行为断言或外推为整包 race。

便携目录改名在相同权限、相同隔离入口下定向两轮及另外十轮均通过（`failures-repro.log`、`portable-move-recheck.log`）；十轮共 4.820 秒。Console 整包独立复核通过（179.586 秒，`console-recheck.log`）。没有修改 Close、文件替换或目录操作，也没有根据一次失败推定具体外部进程占用，首轮失败继续保留。全部 Windows 检查沿用隔离临时目录入口；最终 cacheproxy vet、gofmt 与差异检查通过，产物及源码一致性与实际浏览器检查范围以上述结果为限。

## 引导交互调整后的验证

本节对应后续确认的现行交互，替代初版“引导中手动预览并立即导入”的验证前提。引导仅显示选项和指定的一行小字，不提供展开或手动选择 VRCX 路径；新库/空统计默认勾选，非空默认不勾选。进入启用页才读取默认历史，检查失败或无有效记录时跳过；最终应用才写入，非空覆盖也只在此时警告。

- 目标默认状态检测、引导只读验证、最终写入、另选目标、未勾选/缺预览/过期/目标变更/未授权覆盖/新需求出现时跳过、重启及重复提交不重放等 Go 回归通过。`artifacts/issue28-deferred-go.txt`；同范围 race 通过，`artifacts/issue28-deferred-race.txt`。
- 24 项 Edge 检查通过，覆盖上述默认值、无路径选择、检查结果、取消与最终覆盖、失败继续应用、重置、接入检查刷新及原有引导/预算/文案。`artifacts/issue28-deferred-browser-final.txt`。
- 合成数据库与真实 Console 联调通过：设置入口导入后，在引导确认非空默认关闭、勾选不弹窗、进入启用页读取、最终覆盖可取消，确认后返回成功摘要；没有启动资源引擎。`artifacts/issue28-deferred-live.txt`，截图 `artifacts/issue28-deferred-live/wizard-storage.png`、`wizard-check.png` 已检查。
- console `go vet`、前端类型检查/重建/资源哈希、差异检查及完整桌面构建通过。引导复用既有应用回执，只持久化结果摘要，不保存源路径、预览令牌或聚合历史；配置保存后、重启前执行历史导入。
- 首轮浏览器检查发现常驻但隐藏的设置页也挂载历史导入组件，产生重复说明节点，并会提前缓存旧默认值。改为只在设置页可见时挂载该临时组件，离开即释放预览；原失败保留于 `artifacts/issue28-deferred-browser.txt`，修复后 24 项通过。
- 2026-10-21 审查补充：此前漏测“非空目标 + 空历史”，最终应用会多弹一次覆盖确认，后端仍会跳过。确认函数现同时要求非空预览令牌、正数有效事件和已有目标统计；无有效预览不发送覆盖授权。补充空/非空目标与缺库/空历史的组合，13 项 VRCX Edge 检查通过（保留有效覆盖确认及取消场景），前端检查和桌面构建通过；`artifacts/issue28-empty-overwrite-browser.txt`。

## 自动与集成验证

以下保留初版实现及审查跟进的证据；引导交互以本页上一节及现行契约为准。

| 范围 | 结果与证据 |
| --- | --- |
| 受影响 Go 包完整回归 | `go test -tags=no_easytier ./internal/vrcx ./internal/cacheproxy ./internal/console ./internal/desktop -count=1` 通过；`artifacts/issue28-go-suite.txt` |
| 并发检查 | 上述前三包中 `Test(Histor\|VRCX\|SongDemand\|SongURLStrict)` 的 `-race` 检查通过，使用已有独立 w64devkit/GCC；`artifacts/issue28-race.txt` |
| URL、时间与评分 | 严格主机/路径/ID、参数顺序、HTTP(S)/节点变体、无效/未来时间、乱序、同歌 29.999/30 秒边界、跨歌曲、60 天衰减及当前查询通过；历史累计值与原生 GET 写入逐字段一致 |
| 只读源与失败边界 | 读取已提交 WAL，同时存在未提交写事务；主库/WAL 哈希不变；独占锁、缺库/表/列、视图冒充表、损坏、取消均安全失败；1 万条记录及注入较低行数上限验证完整读取/拒绝截断 |
| 替换及后续使用 | 空/非空权限、整表删除未重合旧统计、占位歌曲、保留媒体映射和视频、触发器失败回滚、空结果拒绝、预览后新增需求、并发重复提交、令牌失效、重启、真实 HTTP GET 后续累计、共享资源汇总及按分数淘汰通过 |
| SQLite 写入失败 | 限制 `max_page_count` 触发真实 `SQLITE_FULL`，以及阻止占位插入触发 `SQLITE_CONSTRAINT_FOREIGNKEY`；两种失败都保留旧统计/歌曲，且无半份新数据。`artifacts/issue28-rollback.txt`；不声称模拟过物理磁盘断电 |
| 审查跟进：失联库保护 | 引导、VRCX 预览与提交共用存储检查。12 个组合覆盖重启恢复的库存覆盖、批量结果及已结束尝试证据，分别在预览前或提交前移走库、保留空根或缺失整根；均返回 409，不建库、不改证据或原库。另选新目标及原有首次初始化回归通过。VRCX、引导和已有扫描失联保护回归通过：`artifacts/issue28-storage-guard-final.txt`；定向 race 通过：`artifacts/issue28-storage-guard-race.txt`；console `go vet`、gofmt 与差异检查通过 |
| 浏览器内存接口 | 25 项 Edge 场景通过，含预览前不读取、直接导入、二次确认/取消、409 后重新预览、缺库/空结果/失败、自定义目标、原引导与设置草稿、帮助文案和 390/1280px 深浅主题；`artifacts/issue28-browser-final.txt`，布局截图已检查 |
| 真实 Console + 浏览器 | 合成 VRCX SQLite、真实 HTTP handler 和独立持久库，完成预览→导入→刷新→再次预览→取消覆盖→确认替换；核对数据库保持两首，未启动资源引擎；`artifacts/issue28-live-browser-final.txt`、`artifacts/issue28-live-browser/`。最终截图关闭过渡动画后已检查 |
| 本机 VRCX 格式及数据 | 实际表为 `gamelog_video_play`，`created_at` 为带 UTC 时区的 ISO 文本。只读扫描 16,714 条，识别 14,778 条，去重后 14,731 次需求、3,461 首，跳过 1,983 条；写入独立临时库并重新打开引擎后数量一致，读取前后源主库哈希不变。`artifacts/issue28-local-vrcx.txt`；不保存私人事件、ID 列表、URL 或房间信息 |
| 静态与构建 | 受影响包 `go vet`、前端类型检查与重建、内嵌资源哈希核对、`git diff --check` 及 gofmt 检查通过；普通桌面入口构建为 `bin/SWD-issue28.exe`（未启动），内嵌资源与源码一同更新 |

Windows 测试统一使用 `scripts/with-test-temp.ps1`；需要原生文件替换或 Edge 的检查在沙箱外运行，目录仍为仓库内独立测试目录。实际源库只读，真实 SWD 数据和配置未修改；未启动游戏、改 hosts、关闭 VRCX 或请求播放上游。

复验可选集成：设置 `SWD_TEST_VRCX` 为明确授权的完整源路径，运行 `TestVRCXLocalReadOnlyIntegration`；或将 `SWD_VRCX_BROWSER_EVIDENCE` 设为忽略证据目录，运行 `TestVRCXBrowser`。前者只读本机历史并在 `t.TempDir` 导入，后者只使用合成数据。普通 Go 套件默认跳过这两项，不要求个人数据、Node 或 Edge。

## 历史失败与验证边界

- 首轮既有目录选择测试在沙箱内报 `renameat ... The handle is invalid`，符合[开发说明](../development.md)记录的 Windows 限制；沙箱外通过，产品文件操作没有绕过权限校验。
- 最初回滚夹具添加触发器后再次从正式入口开库，被冻结 schema 校验正确拒绝；改为在已打开的测试连接上注入事务失败，保留正式开库校验。输出保存在 `artifacts/issue28-core-first.txt`。
- 初版闲置库导入只初始化数据库，随后普通存储检查因缺少 `videos` 目录拒绝该库。已在新库初始化时建立标准空目录，并补入引导存储检查回归；不创建或修改视频文件。首轮结果保留在 `artifacts/issue28-backend.txt`，最终包回归通过。
- 浏览器两轮暴露取消覆盖后按钮焦点丢失。调整请求锁定时机后仍不足，最终使用 Fluent `useRestoreFocusTarget` 标准机制修复；`artifacts/issue28-browser-first.txt`、`artifacts/issue28-browser-verified.txt` 保留失败，最终 25 项与真实后端联调通过。
- 源 WAL 的并发语义由合成运行库验证，本机快照只证明该时刻的数据兼容。行数上限用较低限额注入验证，不声称执行过 200 万条压力测试。原生文件选择器的真实 PowerShell/COM 定义加载、DPI 恢复及请求取消/串行化回归通过；本轮未人工操作原生文件选择窗口，也未重新覆盖屏幕阅读器和系统 DPI 全矩阵。

这是本地历史读取、统计事务与界面功能；剩余平台边界不依赖 VRC，不新增游戏待验项。
