# 上游模块收敛验证

本日后续实施已将协议固定并改为按需备用清单，见[后续验证](fixed-upstream-protocol-20261031.md)；本文涉及三清单必测、协议回退和 Session 的描述仅适用于当时阶段。

日期：2026-10-31。现行规则见[上游请求、检测与直连通道](../upstream-channels.md)、[曲库设计](../song-catalog-design.md)及[资源域名规则](../resource-domains.md)。本批使用 Windows、Go 1.27.3、pnpm 11.25.0 和 Edge；原始日志、截图及构建产物位于忽略目录 `artifacts/upstream-refactor/`。

本文按实施阶段保留结果。前几节的“独立曲库周期请求／仅手动连通性检测”及构建身份属于当时实现，已由“自动与手动统一检测”一节替代；历史失败和证据不改写。

## 完成范围

- 外部 SOCKS5 与 Mihomo 使用当前生效的 HTTP 客户端及拨号器，不构造候选池、不发布 Channel、不创建 Monitor 或 SWD 目标 DNS 解析器。HTTP 和 HTTPS 透传均走当前上游，代理传递原域名；代理失败不回退直连。HTTPS 透传保持原 TLS 字节与证书校验。
- `catalogsync` 独立负责曲库初始化、周期同步、初始化退避及播放元数据补齐。有效完整清单水位仍是初始化完成依据；预置映射、视频目录状态和 Monitor 不决定曲库就绪。Monitor 与手动检测取得的合法响应可复用到相同的业务写入入口。
- `upstreamcheck` 承担当前请求测量和手动下载测速。诊断只借用当前生效客户端，不接受设置草稿、不发现候选、不使用 Monitor 历史、不更新选路；保留既有有界协议回退及下载测速条件。
- 资源域名、来源信息和业务下载活动由应用独立持有的 `upstreamrequest.Resources` 管理。直连 Channel 保留原候选有效性规则；代理业务不需要借用它。关闭后的资源记录拒绝迟到响应。
- WebUI 仅在直连显示“当前连接”。所有模式显示当前上游的连通性与下载测速，没有上游选择器。两项显示最后完成时间，包括失败；取消恢复前次结果及时间。测速失败时保留前次有效速度，并分别展示本次时间与有效结果时间。
- 直连原有 IP 观测、推荐和有限自动检测继续保留。本轮未增加直连选路算法；CF 实验只保留原有固定采样歌曲入口，不把曲库遍历重新放入 Monitor。

## 检查记录

Go 命令统一经 `scripts/with-test-temp.ps1` 使用隔离临时目录；Windows 沙箱原子重命名及浏览器管道限制见下文。没有使用真实用户数据库或代理配置作为测试夹具。

| 检查 | 结果与证据 |
| --- | --- |
| 完整 Go 回归 | `go test -tags=no_easytier ./cmd/... ./internal/... ./data/... ./experiments/... -timeout 180s` 通过，`go-final.log` |
| 静态检查 | 同范围 `go vet -tags=no_easytier` 通过，`vet.log`；Go 格式与 `git diff --check` 通过 |
| 并发与关闭 | `catalogsync`、`upstreamcheck`、`upstreamstate`、Console 的初始化、检测、取消、HTTP/S 与代理重点用例通过 race，`race.log`；独立 `upstreamrequest` 整包 race 通过，`resources-race.log`；资源关闭及代理曲库隔离补充断言通过 race，`isolation-final.log` |
| 可选 CF 实验兼容 | `go test '-tags=no_easytier,cf_block_experiment' ./internal/console -run '^TestCF(Network\|Bind\|Pinned\|Experiment\|Target)'` 通过，`experiment-fixtures.log`；未启用公网实验 |
| 前端生成 | `pnpm --dir webui build`、`pnpm --dir webui check` 通过，内嵌资源已更新 |
| 完整浏览器回归 | `pnpm --dir webui test --workers=4` 最终 363 项全部通过，`browser-final.log`；包含共享夹具修正后的完整复验 |
| 真实 Console 与浏览器 | `TestMihomoDashboardAndConsent` 和 `TestNetworkConnectivityReusesResponsesToInitializeStorage` 的可选浏览器驱动通过，`browser-live-final.log`、`live-dashboard/result.json`、`live-browser/result.json` |
| Windows 构建 | `go build -tags=no_easytier -mod=readonly -o artifacts/upstream-refactor/SWD.exe ./cmd/still-wanna-dance-console` 通过；SHA256 `bfe488a31391489f2b466e3d000d3dc4926322cae54cee3b4bbad9be8f761353`，未部署此 EXE |

关键行为由以下实际检查支持：

- 代理业务与后台同步注入禁止候选访问的 Transport，并检查 Monitor 未创建、DNS 查询为零。外部 SOCKS5 通过本地真实协议服务验证原域名请求和取消；Mihomo 通过 SS、VLESS、VMess、AnyTLS、Hysteria2 真实回环协议夹具验证请求和关闭。
- 曲库退避采用虚拟时间验证 5、10、20、40、60 秒及一分钟上限，存储拒绝不算成功；已有清单沿用普通周期。歌曲选择检查共享业务遍历、缺失元数据优先和失败后的推进，额外初始化重试只取清单，不下载视频正文。
- 手动检测验证实际五项请求、仅本轮结果、合法响应入真实临时 SQLite、失败与取消时间、SSE 重连、互斥、条款和退出。失败后的结果不会混入上一轮成功；取消保留前次完整结果。
- 真实检测页使用实际 Console/API/SSE、内嵌 WebUI 及受控 HTTP 上游，检查无代理“当前连接”、无上游选择器、空对象启动、五项结果和完成时间、曲库更新，以及 390px 无横向溢出。截图 `live-browser/active-proxy.png` 已检查；这不代表访问了公网业务源。
- 真实 Mihomo/原版 zashboard 联调检查控制接口、代理面板与诊断导航、手机布局和主题。测试主动阻断外网，保留面板七次 GitHub 更新检查的 `Failed to fetch`；这些是已识别的上游联网行为，没有作为产品接口错误隐藏。

可选真实浏览器入口：设置 `SWD_DASHBOARD_BROWSER_EVIDENCE`、`SWD_UPSTREAM_BROWSER_EVIDENCE` 为各自证据目录后，运行 `go test -tags=no_easytier ./internal/console -run '^Test(MihomoDashboardAndConsent|NetworkConnectivityReusesResponsesToInitializeStorage)$' -count=1`。驱动使用测试创建的隔离服务，不启动桌面程序或修改 hosts。

## 失败与修正

1. Windows 沙箱内的原子重命名出现 `The handle is invalid`，Edge 启动遇到命名管道限制。原始输出保留在 `go-first.log`、`monitor-timeout.log`、`browser-first.log` 等；改在允许这些系统操作的环境执行相同验证后通过，没有改变产品原子写入规则。原 Monitor 测试持锁 Fatal 会使清理一直等待，修正了该测试的失败退出路径。
2. 真实 SOCKS5 取消测试发现 HTTP 请求取消后，`net/http` 的无等待者拨号仍可能占用连接。检测结束或取消后调用当前客户端的 `CloseIdleConnections`，在发布结束状态前清理这些拨号；不退休 Mihomo 核心、不关闭进行中的业务连接。定向取消、后续整包及 race 通过，见 `console-fourth.log`、`console-targeted.log`、`go-final.log`、`race.log`。
3. 首轮浏览器全套 363 项中 360 项通过，三个失败分别来自两处仍要求代理显示“当前连接”的旧导航断言，以及共享夹具使用空对象表示不存在的有效测速结果。更新代理导航/旧书签断言和夹具的空结果语义后，相关 12 项通过，见 `browser-all.log`、`browser-empty-result-error.md`、`browser-fixed.log`。完整复验结果另列于检查记录。
4. 中间回归还暴露了旧 Channel 资源归属、旧诊断入参和实验构造签名的测试假设；迁移到应用资源记录、当前客户端和直连构造接口后通过。一次原有 Portable 设置搬迁测试返回 `Access denied`，定向及最终整包复验通过，未为此修改搬迁实现。早期完整命令误含不存在的实验子目录导致入口失败，最终使用上表有效包范围重跑。

## 结论边界

本轮证明受控网络下的模式隔离、当前请求检测、业务同步、取消/并发、页面交互及构建；没有测公网代理吞吐、真实 VRC 播放体验、最终发行包或新的系统辅助技术场景。未启动游戏、修改系统 hosts、提交或推送。已有游戏专属验收范围不因本次模块拆分自动扩大。

早期验证中的“代理也发布单一候选”“独立诊断 Monitor”“检测设置草稿”“测速固定候选”及“HTTPS 透传始终直连”等描述，只代表当时实现，已由本轮现行规则替代；历史失败和证据不改写。

## 提交前审查后的时间修正

审查指出取消测速会覆盖 `Finished`，前端又改读 `LastValid.Finished`：首次失败后重试再取消会丢失最后测试时间；已有成功结果时则回退到旧成功时间。Go 定向回归复现取消错误地产生完成时间，浏览器分别复现失败时间消失和回退，原始失败见 `review-times-red.log`、`review-browser-red.log`。

修正后只在非取消结束时更新完成时间，包含成功和失败；取消保留此前完成时间。界面始终使用该时间，恢复较早的有效速度时另标其时间，不增加状态或历史记录。`console.md` 同时删除“不同资源域名各自固定连接候选”的旧约束，引用现行手动检测规则。

测速模块与 Console 测速/SSE 集成的定向 race 通过（`review-times-race.log` 中的 `upstreamcheck` 结果、`review-console-race.log`）；诊断与组件浏览器回归 9 项通过（`review-browser-fixed.log`），包括无旧成功结果和有旧成功结果两种失败后取消路径。前端构建和生成一致性检查通过（`review-ui-build.log`、`review-ui-check.log`）。首轮 race 的 Console 编译与前端生成同时读取/更新内嵌资产，发生 `unexpected length`；前端生成完成后顺序复验通过，保留原日志，未改产品代码规避。上文 363 项完整浏览器结果对应本次审查修正前的构建，本次按影响范围复验。

受影响两包的 `go vet` 和 Windows 桌面构建通过（`review-vet.log`、`review-build.log`）。修正后的 EXE 为 `review-SWD.exe`，SHA256 `647315146df49fd542ea33f56e8af190f24a7d74ab08cff11eb329a00333431b`；未部署。上表 `SWD.exe` 保留为审查修正前的历史构建。未提交或推送。

## 曲库直连容错与清单定义收尾

需求审查发现后台同步直接使用普通直连客户端，TCP 成功后的 TLS/HTTP 失败不会继续另一候选；Monitor 的抽样不能保证补齐指定缺项。修复前复跑受控 TCP/TLS 实验，目标歌曲 `900001` 连续三轮均未补齐，当前 Monitor 三轮只采样 `42`，原候选流程则三轮均补齐目标；结果保留在 `artifacts/review-spec-fallback/recheck-confirmed-20261031.log`。同目录 overlay 使用修复前接口，仅作历史复现；第一次复跑的 PowerShell 参数解析错误另保留在 `recheck-20261031.log`。

现在 Console 通过原有 `operationClients` 为后台同步提供请求路径，直连沿用既有推荐及未测候选容错，Monitor 未启动也能完成指定缺项。`catalogsync` 只接收客户端提供函数，`upstreamcheck` 对调用方提供的路径共用一个既有 Session；候选发现和请求共用总期限，协议回退不重置尝试预算。代理及手动诊断仍只提供当前生效客户端，没有增加后台选路策略、历史或重试层。

清单入口 ID、URL 和顺序集中到 `internal/catalogmeta/sources.go`；候选发现、实际探测、同步及存储来源常量引用这份定义。播放来源和存储数据语义保持原状。

- `TestCatalogSyncDirectFallbackPreservesMissingSong` 使用真实本机 TCP/TLS 服务及临时 SQLite，覆盖 TLS 失败、HTTP 503、无效清单/播放地址三种首路径故障；三份清单均更新，指定缺项成功写入，未创建 Monitor 或视频引擎，未下载视频正文。记录 `catalog-fallback-fixed.log`。
- `catalogmeta`、`catalogsync`、`upstreamcheck`、`upstreamstate` 模块回归通过，`catalog-modules.log`；新增虚拟时间检查验证发现耗时与 HTTPS/HTTP 尝试共用每入口 30 秒期限，曲库与 Console 的相关取消、模式隔离、手动诊断和容错 race 通过，`catalog-fallback-race.log`。
- 存储消费者整包通过；Console 整包首轮只有既有目录搬迁测试出现 Windows `Access denied`，单独连续三次通过，未修改搬迁逻辑。第二轮整包的目录搬迁通过，但 `TestHostsSessionRestoresAPIAlongsideUnmanagedEntries` 在替换临时 hosts 夹具时出现同类错误，单独连续三次通过；该测试不创建曲库同步，也未改动其实现。两次整包不能记作全绿，失败与定向复验分别保留在 `catalog-consumers.log`、`catalog-layout-recheck.log`、`catalog-console-final.log`、`catalog-hosts-fixture-recheck.log`；未触及系统 hosts。
- 受影响六包 `go vet` 通过，`catalog-vet.log`。本轮没有前端行为变更，沿用上一节对应范围的浏览器证据。
- 最终 Windows 构建通过（`catalog-build.log`），产物 `catalog-SWD.exe`，SHA256 `eab29e36de2df1e41d8f4d7f8bb36928bbf1d5420207a573a9740109bab79e8f`。这是本轮最新构建，前文两个 EXE 为历史产物；未部署、提交或推送。

## 自动与手动统一检测

后续需求调整为自动周期和手动入口共用一次请求流程、缺项遍历与检测结果。现在 `upstreamcheck.CheckConnectivity` 依次取得清单、请求曲库选择一首歌曲、解析播放地址并用同一 URL 读取一个资源正文 byte。有效响应直接交给既有清单同步和播放地址事务，不再另发同步请求。`catalogsync` 删除网络客户端、周期循环及初始化状态，只保留业务选歌进度、短期普通样本与更新时间；Console 组装访问能力、业务回调和唯一调度。直连沿用既有请求容错，代理只提供生效客户端，清单来源继续共用 `catalogmeta` 定义。

常规 5 分钟周期、首次完整清单退避、测速互斥与取消语义以[上游规则](../upstream-channels.md#上游检测与测速)为单一依据。自动和手动均发布实际结果、延迟、完成时间及逐行观测时间；初始化重试只更新检查过的清单行。数据库失败独立保留，不把有效网络响应改成连接失败。已有 URL 单纯过期不进入补齐队列，下载速度测试仍仅手动触发。未扩大直连 Monitor 的选路职责。

| 检查 | 结果与证据 |
| --- | --- |
| 统一流程定向回归 | 自动／手动共享选歌与响应、周期重算、初始化退避、取消、请求总期限、真实 TCP/TLS 候选回退及存储复用通过，`automatic-targeted-verified.log` |
| 定时与失败隔离 | 虚拟时间验证 5 分钟、5/10/20/40/60 秒及一分钟上限、手动加入运行轮次、自动等待测速结束、退出无新任务；SQLite 触发器制造写入失败后网络五项仍成功，未检查行保留原时间，`automatic-schedule-verified.log` |
| 整包与业务规则 | `catalogsync`、`upstreamcheck`、`upstreamrequest`、`upstreamstate`、`upstreamnet` 整包通过，`automatic-packages.log`；Console 修正旧夹具后整包通过，`automatic-console-verified.log`（44.138 秒）。存储缺项规则回归包含过期 URL 不续期，`automatic-metadata-policy.log` |
| 并发 | `catalogsync` 选歌、Console 自动周期／初始化／检测／取消／直连容错及 Monitor 共存定向 race 通过，`automatic-race-verified.log`；该命令的 `upstreamcheck` 过滤后没有独立用例，探测实现由 Console 集成场景覆盖，不称为其整包 race |
| 浏览器 | 诊断、曲库初始化与刷新相关 27 项通过，`automatic-browser.log`；逐行时间布局调整后诊断与组件 10 项通过，`automatic-browser-final.log` |
| 实际 Console 联调 | 自动完成的五项结果先显示，手动重测复用同一流程，SQLite 资料更新、真实 API/SSE、390px 无横向溢出通过，`automatic-live-browser-final.log`、`automatic-live-browser-final/result.json`；已检查最终截图 |
| 静态与生成 | 受影响包 `go vet`、前端类型及生成一致性、Go 格式和 `git diff --check` 通过；`automatic-vet.log`、`automatic-web-build-final.log`、`automatic-web-check-final.log` |
| Windows 构建 | `automatic-build-final.log`；最新 `automatic-SWD.exe` SHA256 为 `d663fbb1b1d724a5a0f0c50de8b0fc37540cc52387fbb198a4a937ef9fa1528c`，未部署 |

本阶段保留的失败与修正：首次虚拟时间用例跨 bubble 共用全局 Channel 通知，导致测试运行时拒绝关闭；改为各 bubble 独立测试 Channel 并在 Console 清理后恢复。故障恢复用例最初试图在同一实例清除已锁存的存储错误，与既有存储契约不符；现分别验证持续存储故障与普通网络恢复，不改变产品故障规则。见 `automatic-schedule-tests.log`、`automatic-schedule-tests-after.log`。首次 race 指出新测试计数器未建立与后续计时器写入的同步，改用原子计数及锁保护记录；见 `automatic-race.log`。整包首次仅两处旧测试仍断言自动轮次没有资源读取，现明确允许并验证一个 byte 的 Range 请求；见 `automatic-packages.log`。这些修正未放宽请求预算或业务断言。首次窄屏截图的时间文字与结果挤在同一行，改为每项结果下方的次级文字；前后截图分别留在 `automatic-live-browser/` 与 `automatic-live-browser-final/`。

本阶段使用隔离数据库、受控网络和本地浏览器，不代表公网代理性能或 VRC 播放体验；未启动游戏、修改系统 hosts、部署、提交或推送。

## 提交审查收口：HTTPS 验证入口与说明

提交审查指出两项非阻塞遗漏，经核对均成立。`TestLiveHTTPSUpstreams` 与 `TestLiveDirectDualStack` 的 relay 分支没有传入应用资源集合，解析得到合法视频地址后仍会被 SNI 准入拒绝；前者的重定向判断还在读取旧全局 Channel。两处现统一使用 `c.resources`，测试 helper 的资源参数改为必填，使漏迁移调用在编译时暴露。Mihomo 文档删除“代理不自动探测视频”的旧定义，区分统一自动轻量检查与手动下载测速，并引用现行周期规则；Monitor README 同步说明业务活动通过 `Options.Resources` 接入。这次只修改测试入口与说明，没有扩大产品行为或直连选路职责。

- 本地证书、TLS/Range 保持、未知 SNI 拒绝、拨号与已建立连接关闭、视频/API 活动区分回归通过，`review-closeout-https.log`。
- 开启 `SWD_LIVE_UPSTREAM=1` 和 `SWD_LIVE_DUALSTACK=1`，两个可选公网入口实际通过：强制 IPv6、强制 IPv4、普通双栈、本地 TLS relay 及 CF HTTPS 资源转发均验证证书并取得 206／1024 bytes；`review-closeout-live-https.log`，合计 8.173 秒。仅访问公开业务源并读取小段 Range，不改变系统 hosts，不启动网关或游戏，不下载完整视频。
- Console `go vet`、Go 格式与 `git diff --check` 通过，`review-closeout-vet.log`。相关 helper 的所有调用均已显式提供资源集合，未残留全局 `Default.IsResourceDomain` 校验。

以上公网结果仅证明该时段的连通、证书与转发行为，不是代理性能或 VRC 体验验证。未新增产品构建、部署、提交或推送；前一节 Windows 产物仍为现有验证构建。
