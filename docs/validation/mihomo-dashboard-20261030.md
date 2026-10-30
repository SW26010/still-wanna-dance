# Mihomo 单一出口与 zashboard 实施验证

日期：2026-10-30。现行规则见 [Mihomo 与 zashboard](../mihomo-dashboard.md)。本批在 Windows、Go 1.27.3、Node 24.22.1、pnpm 11.25.0 和 Edge 上验证，证据保存在忽略目录 `artifacts/mihomo-dashboard/`。没有启动 VRC、修改系统 hosts、提交或推送。

## 实现与验证范围

- 删除多代理候选构造、五节点活跃集合、保留节点 membership 和代理节点推荐/吞吐恢复；Mihomo 与外部 SOCKS5 均只有一个逻辑出口。直连的多 IP 推荐、迟滞和测速继续保留。业务连通性、播放 URL 观察与资源完整性检查继续使用现有机制。
- 八节点 Console 集成确认全部节点进入原生 SWD 组，只生成一个候选；无节点时不回退直连。确认条款前不启动核心，首次确认后才发布可用出口。
- 五种本地真实协议夹具覆盖 SS、VLESS、AnyTLS、Hysteria2、VMess/WS 的原生组选择、并发、取消、错误凭据和退休。两个可区分出口验证手动选择后旧 HTTP 连接仍走 A，新连接走 B；原生健康检查 API 判定 A 失效后，fallback 使用 B。统计/连接接口有真实数据，另起进程确认原生选择恢复。
- 控制 API 检查 token、Origin、条款门禁，禁止配置重载、GLOBAL 选择、核心/UI 升级、重启及原始日志。真实 WebSocket 能读取原生流量，退休核心及时关闭流；追加同时订阅两个内存流和连接流，修正共享缓存竞态后通过 race 检查。
- 真实 Console + 原生 API + 内嵌原版 zashboard 的浏览器集成通过。检查 SWD 设置入口、桌面/390px 选择、刷新、过期 token 更新且后端 UUID 不变、原生状态与 WebSocket，静态文件本地提供。测试主动阻断外网，节点控制仍工作；最终场景记录上游三次 GitHub 更新检查的 `Failed to fetch`，没有将这些已知上游行为隐藏为“无错误”。最新截图和结果在 `browser-final-verified/`，另保留 race 场景 `browser-reviewed/`。

## 检查记录

| 检查 | 证据与结果 |
| --- | --- |
| Go 产品测试 | `go test -p 1 -tags=no_easytier -mod=readonly ./cmd/... ./internal/...`，通过，记录 `go-test-final.log`；最后的原生并发保护修改后，Mihomo 与 Console 整包复验记录 `affected-final.log` |
| 静态检查 | 同一入口范围 `go vet` 通过，`vet.log`；最后受影响包复验 `vet-affected-final.log`；本轮 Go 文件 gofmt、diff 空白检查通过 |
| Windows race | 使用已有 w64devkit/GCC；策略组、选择恢复、单出口持久化边界和条款/控制 API 通过，`race.log`；追加流式关闭检查通过，`race-stream.log`；真实浏览器暴露的上游竞态修正后，并行选点/序列化/原生组健康检查/拨号和双内存订阅通过，`race-browser-reviewed.log` |
| 前端 | TypeScript 与内嵌资源生成通过；代理草稿、设置、监测、帮助与许可页面 20 项浏览器测试通过，`webui-tests.log` |
| Node 工具 | 发布链、源码/声明校验与精确产品归档例外共 50 项通过，`node-test-final.log` |
| 原生 API 与浏览器 | `native-final.log`、`race-browser-reviewed.log` 及受影响包执行中的真实浏览器场景；最新截图 `browser-final-verified/` |
| 桌面构建 | `SWD.exe` 59,413,504 字节；SHA256 `784bd189add09c7c59551e55b25b2e3f936a661f3af922767650dd41e7640bf6`；`build-final-verified.log`；未运行或部署该 EXE |
| 许可/产物 | 最终 EXE 的 114 个实际 Go 模块全部有审核记录；原生控制器新涉及 ajg/form、google/pprof，版本未升级。zashboard 63 项应用/worker/PWA/CSS 包及资源原文独立合并；变更文件按 Git LF 规范化后通过精确产物规则 |
| zashboard 源码 | 固定源码哈希、完整包存储/注册表元数据和清单边界通过；独立副本 `install --offline --frozen-lockfile --ignore-scripts` 后完成 Vite no-fonts 构建，`source-validation-reviewed.log`。上游 Git 标识缺失警告被保留，构建没有借用 SWD 的提交标签 |

## 失败与修正

1. 初次关闭核心在 Windows 留下 `cache.db` 句柄：上游 executor.Shutdown 不关闭该缓存。将原生 cachefile 纳入 Runtime 的关闭责任后，跨进程恢复与临时目录清理通过。
2. 原导入测试要求名称丢弃，与面板识别节点的目标冲突。名称改为受保护快照字段；连接身份继续排除名称，公开报告仍不泄露名称/地址。相关测试更新后通过。
3. ZIP entry 不实现 Seek，直接使用 FileServer 时声明文件返回 `seeker can't seek`。固定包初始化时解压到只读内存，ServeContent 使用可寻址 reader；真实浏览器和静态资源检查通过。
4. Windows 沙箱使临时文件原子重命名返回 `The handle is invalid`，影响大量原有条款/状态测试；Monitor 某测试在持锁 Fatal 后清理等待至超时。保持产品原子写入规则，改在可正常重命名的环境运行原测试后通过。原始报错保留在工具执行记录，独立 `upstreamstate.log` 和后续整包日志记录复验。
5. 整包回归发现独立条款收据夹具没有网络客户端，新增启动路径发生 nil dereference；为该既有边界补保护后，条款、Console 与 race 通过。新测试中的方法名编译错误也在复验前修正。
6. 浏览器初次选择超时来自原版组默认折叠，改用实际展开交互后通过。最初严格 CSP 阻止了上游自动更新查询；最终保留原版 HTTPS 数据请求能力，并在离线测试中明确记录更新查询失败，核心/UI 写入升级接口仍禁止。
7. Node 发布链测试第一次使用 PATH 中 Go 1.27.1，因 `GOTOOLCHAIN=local` 不满足项目要求而失败；显式使用已安装的 1.27.3 后 50 项通过。两次日志分别保留。
8. 源码收集首次被 pnpm 的 `no-downgrade` 拒绝两个固定版本。逐项比对 registry/锁摘要及上游源码后，仅为这两个精确版本记录 provenance 例外，保持其它信任、冻结锁、禁脚本与完整性检查。原告警不是误报，边界与证据见[核对记录](../licenses/zashboard/dependency-trust.md)。修正后离线重建通过。
9. 模拟重启 token 变化时发现，zashboard 将 secret 不同的连接视作另一个后端。入口现在更新同一地址/路径的现有记录并保留 UUID，浏览器断言只保留一个后端且 token 已刷新。移动布局切换时，原测试定位到了正被替换的节点元素；改为在实际组内容内定位，并等待选中样式再截图。
10. 真实浏览器与 race 联跑暴露 Mihomo 原生 Selector.Set/MarshalJSON 及统计 Manager.Memory/Snapshot 两处数据竞争，原失败保留 `browser-token-final.log`。用同步适配器保护原生选择器，自动组不再暴露无用的手动固定能力；内存流直接读取同一个进程内存查询函数，不写原生共享缓存。没有修改依赖或重写选点/故障切换算法。并行控制/真实拨号及双内存流回归、真实浏览器 race 均通过；兼容边界写入 canonical 文档。

## 提交前复审修正（2026-10-30）

本节记录复审时的中间实现与证据。随后用户明确选择保留原生名称缓存并移除身份映射及测试版迁移；节点选择和缓存的现行行为以本节后的「按用户决策精简」及[现行设计](../mihomo-dashboard.md)为准。

- **节点隐私与选择恢复**：复审发现早期实现将面板名称直接存入原生选择缓存，名称和无名节点的默认文案都可能包含地址。隔离回归在修复前重现两处明文（`review-privacy-red.log`）。现在选择请求先转换为完整 HMAC 身份，Mihomo 仍执行选择和保存；恢复时转换回当前名称。测试逐个扫描实际缓存，验证名称和地址不出现、摘要确实写入；重启、节点改名和临时身份下不覆盖原选择均通过。只在下次启动 Mihomo 时安全删除旧测试版的确定缓存文件；不安全入口不会被删除，也不会启动核心。旧选择重置；未启用核心前旧缓存可能仍存在。
- **身份密钥异常**：恢复界面提示；损坏或不可读的密钥保留原文件，本次使用临时身份并禁用原生选择缓存的读取和写入。`review-selection-and-identity.log` 与 `review-race-browser-final.log` 包含相关回归。
- **跨平台面板源码输入**：上游 workspace 未声明多平台架构，原 Windows store 只收集了 Windows x64 原生包；原结论仅证明 Windows 重建，不能外推。收集时在临时 workspace 指定 Windows/Linux/macOS x64/arm64 和 Linux glibc/musl，保留未修改的上游源码与锁文件。全新 store 实际包含 22 个 esbuild、Rolldown、Tailwind 原生包；八个目标的 pnpm 离线安装与链接检查通过（`review-cross-platform-installs.log`），完整新收集和 Windows 实际离线 Vite 构建通过（`review-dashboard-source-final.log`）。本机没有 Linux/macOS 运行环境，这不是目标系统上的实际构建验证。第一次手工收集使用了相对路径，因 pnpm 在暂存目录解析该路径而失败（`review-cross-platform-collect.log`）；正式收集和复验使用绝对路径。
- **审查副本影响**：本轮曾以 SQLite 只读方式查看既有审查副本的 store 索引结构，`v11/index.db-shm` 的修改时间随后发生变化；主索引的大小与写入时间未变，WAL 仍为空，这些时间与大小不是逐字节未变证明。没有删除原副本文件；后续检查使用新建副本。审查会话中删除校验缓存的操作曾被自动审批拒绝，本轮未重新尝试。
- **面板偏好**：默认桌面端口随机，zashboard 的浏览器偏好按完整来源隔离，重启后可能重置；同来源内刷新才保留。节点选择与浏览器来源无关。上游代码的 IPv6 检测可能把节点显示名保存在浏览器本地存储，此数据不受 SWD 的 DPAPI 保护。该限制和站点数据边界见[现行设计](../mihomo-dashboard.md)。
- **测试与构建**：修正后 `go test -p 1 -tags=no_easytier -mod=readonly ./cmd/... ./internal/... -count=1` 全部通过（`review-go-final.log`）；真实浏览器和选择/缓存并发 race 通过（`review-race-browser-final.log`），Node 发布工具 50 项及最终产物规则检查通过。完整 WebUI 测试首轮 355 项通过、1 项失败（`review-webui-tests-final.log`）：测试只推进 2.5 秒，晚于动作刷新到达的新样本需等 5 秒轮询。冻结时钟可重现（`review-monitor-deterministic-red.log`）；改为等待实际状态请求后，同文件双用例并发重复 5 次均通过（`review-monitor-repeat-fixed.log`），完整套件复跑 356 项通过（`review-webui-suite-rerun.log`）。第一次重复检查还暴露测试新增的 `pauseAt` 与墙钟竞争（`review-monitor-repeat.log`），改为固定虚拟起点与未来时间后通过。
- **当时的桌面构建**：`review-SWD.exe` 59,418,624 字节，SHA256 `486f8c7a072d29833bd038b8a7d6b346444ca9863938b9cd1b44600f7c8a5cff`；实际链接的 114 个 Go 模块声明核对通过。没有运行或部署该 EXE。此 EXE 及上表的 `SWD.exe` 都是后续精简前的历史构建，予以保留。

## 按用户决策精简（2026-10-30）

- 普通显示名称允许由 Mihomo 原生缓存；移除复审中加入的名称与身份双向映射、面板请求改写，以及针对未发行测试版缓存的运行时迁移。无名节点改为「节点 1」等名称，附短摘要区分同名节点，不自动拼入服务器地址或端口。订阅自带的显示名称可能本身包含地址，原生选择缓存和浏览器部分功能可能明文保存该名称；连接配置与凭据仍由 SWD 的受保护快照管理。
- 本轮真实核心缓存测试确认含地址的订阅显示名按名称保存，无名节点的选择缓存不包含自动生成的地址或连接凭据；跨进程验证原生选择恢复、名称变化后的默认回退，以及临时身份不覆盖原选择。真实面板与并发 race 通过（`native-names-race-browser.log`），受影响包整包复验见 `native-names-affected.log`，静态检查通过。
- 测试使用隔离临时目录并自动清理；本轮检查的工作区状态目录和面板验证目录未发现遗留的 Mihomo `cache.db`。未对审查副本 store 或其他用户数据做删除。之前的跨平台依赖修复、浏览器偏好边界及 WebUI 全套 356 项复验继续适用。
- 精简后的桌面构建为 `native-names-SWD.exe`，59,414,016 字节，SHA256 `346c91ac427530e47a0599b93c49f1917c333b29dc4ef54ced38bfd58cc9d5f9`；114 个实际链接的 Go 模块声明核对通过。没有运行或部署该 EXE。

## 后续提交前审查修正（2026-10-30）

- **pnpm 版本切换**：上游面板声明 11.20.0，本发行重建版本是 11.25.0。使用未设置 `pm_on_fail` 的原版 pnpm 11.25.0 和空缓存，离线安装复现了尝试解析 11.20.0 后失败（`pnpm-clean-red.log`）；此前本机封装器强制 `pm_on_fail=ignore`，掩盖了冲突。共用 pnpm 启动器现显式禁止按项目声明切换版本，并先检查实际版本与发行声明一致；面板源码收集和重建在上游目录再次核对。新增测试验证所有调用都覆盖宿主冲突设置，并拒绝错误的实际版本。使用只转发参数、不设置策略的原版 pnpm，在独立环境完成完整收集与 Windows Vite 重建（`pnpm-clean-verified.log`）；再以新的空 pnpm home/配置/缓存执行离线重建（`pnpm-clean-offline.log`）。另用官方 Corepack 0.36.0 在隔离目录预置 11.25.0，验证禁用项目版本切换及工具下载后可离线重建（`corepack-offline.log`）。上游 `package.json` 和锁文件未修改，信任策略与精确例外不变。之前的离线重建记录只证明当时封装器环境，不能单独证明普通 pnpm 环境。
- **同意保存后的启动失败**：隔离核心故障重现已保存同意却返回“无法保存”的错误（`review-terms-red.log`）。保存失败继续保持条款门禁；保存成功后若代理或监测启动失败，接口返回已保存及警告，界面显示修复和重启提示，并在设置中保留代理启动错误；启动失败期间不消费待执行的自动加速。真实 Console 和浏览器验证保存、提示及设置入口（`review-terms-real-browser.log`、`review-autostart-order-final-race.log`）。首次浏览器回归因可见通知与无障碍播报同时匹配导致定位歧义（`review-terms-ui-fixed.log`），限定可见通知后通过。后续核对发现 HTTP 监听可能先于自动启动任务执行：隔离故障在两种时序均复现待执行意图被消费，其中开启自动加速的场景实际启动了视频服务（`review-autostart-order-red.log`）；现以同一锁下的启动失败状态阻止消费，并用 race 检查代理故障、监测故障和正常启动（`review-autostart-order-final-race.log`）；条款浏览器回归 7 项通过（`review-autostart-order-webui.log`）。第一次运行定向回归时误用 PATH 中 Go 1.27.1，未满足模块要求，日志为 `review-autostart-order-go-version.log`；改用项目要求的 1.27.3。
- **整组测速连接峰值**：Mihomo 的组测速实现会并发探测组内节点，绕过 SWD 的 160 个业务连接准入槽；原生自动健康检查使用自己的调度。遵循将节点测试交给 Mihomo 的既定边界，没有增加另一套测速或调度逻辑；设置中的面板入口和现行设计明确说明大节点组可能产生连接高峰。实际面板联调检查提示可见。一次 race 浏览器回归在切换至手机宽度时仍按桌面组件的展开状态查找节点而超时（`review-followup-console-race.log`）；改为等待实际上屏的手机组卡片后，真实浏览器 race 通过（`review-followup-console-race-fixed.log`），另连续三轮通过（`review-dashboard-repeat.log`）。
- **文档与工具**：清理 `internal/upstreamstate/README.md` 中代理吞吐样本恢复及自动测速的过时说明。Node 发布工具 51 项通过（`review-followup-node-final.log`）；桌面入口、Console 和 Mihomo 整包回归通过（`review-autostart-order-affected.log`），条款、代理草稿和设置浏览器回归 18 项通过（`review-followup-webui.log`）。该阶段 EXE 为 `review-final-SWD.exe`，59,417,088 字节，SHA256 `acd89853a8bfcc0a17a0912e3cedcf7e68c02cb2413f3a056138a488df75315d`；实际链接的 114 个 Go 模块声明核对通过，没有运行或部署该 EXE。前述其他 EXE 均为历史构建。

## 缓存故障初次核对（2026-10-30，修复前记录）

本节记录仅核查审查意见、尚未修改产品代码时的结果，后续处理见下一节。忽略目录 `artifacts/mihomo-dashboard/cache-review/` 保存可重复运行的探针、Go overlay 对照与原始日志；每个场景使用独立进程和全新临时目录，没有读取或修改用户缓存。

- 当前实现的 6 个组合确认：正常缓存路径能启动；非 Bolt 文件被删除重建；`cache.db` 被目录占用时核心返回 `mihomo: selection cache unavailable`。后两项在启用、关闭选择持久化时均复现，见 `current-behavior.log`。固定上游的 `initCache` 对 `ErrInvalid`、`ErrChecksum` 和 `ErrVersionMismatch` 执行删除重建；本轮动态复现了非 Bolt 文件，另外两种错误分支由源码确认。
- 对照构建仅用 Go overlay 移除 SWD 的缓存不可用致命检查，不改工作区产品文件。同样不可打开缓存的场景中，真实本地代理请求及原生面板选点均成功，见 `optional-cache-control.log`；异常文件被重建的行为仍存在。证明“缓存不可用阻止代理启动”来自 SWD 的额外要求，原生选点接口可以在缓存句柄为空时继续工作。
- `store-selected=false` 只跳过选择映射读写；SWD 的显式缓存检查和原生面板选点仍调用缓存单例，不能将其视为“不初始化或不碰缓存文件”的保证。正常 Console 构造先加载条款、再创建代理，核心错误会沿这条路径使 Console 创建返回错误。本轮未启动完整桌面程序。

## 缓存降级与边界确认（2026-10-30）

用户确认继续按上一轮建议处理：允许 Mihomo 对已知 `cache.db` 使用原生初始化与异常重建机制，不新增缓存备份、格式迁移或恢复层；其他未知文件的保留约定不变。`store-selected=false` 只约束选择记录读写，不能解释为禁止缓存初始化。现行存储及生命周期文档已同步，并明确后续写入失败仍由上游机制处理。

缓存打开失败现在仅记录 `mihomo_selection_cache_unavailable`，核心继续运行；设置复用现有反馈区提示“代理仍可使用，本次的节点选择不会保存”。正常缓存继续由 Mihomo 保存及恢复；关闭只处理实际打开的缓存句柄，没有引入新的选点、持久化或重试逻辑。

- 回归先在修复前重现核心启动、已有同意记录的 Console 创建、首次同意后的启动失败（`cache-review/regression-red.log`）。修正后，缓存不可打开和格式异常、启用和关闭选择持久化的组合均通过实际本地代理请求、面板选择和正常关闭检查；无关文件保留，正常缓存及临时身份的跨进程选择恢复回归通过。
- 定向 race 与真实 Console／Edge 联调通过（`cache-review/regression-fixed-race-browser.log`）；真实界面确认缓存失败提示可见，原版面板在桌面、手机宽度下能选点、刷新与切回自动组。上游更新查询仍被测试主动断网，记录中的相关 `Failed to fetch` 为已知外部查询限制，节点控制通过。
- 桌面入口、Console、Mihomo 整包回归通过（`cache-review/affected-packages.log`），相关 `go vet` 通过（`cache-review/vet.log`）。前端源码未变，嵌入产物校验通过。新构建为 `cache-review/SWD.exe`，59,417,600 字节，SHA256 `827703bd22b005f4882a2bda4314cc3f349cc26bbaa986a2fea39a3eabe76e66`，114 个实际链接 Go 模块的许可核对通过；未运行或部署此 EXE，未提交或推送。

## 网络页原版面板嵌入（2026-10-30）

网络页现在按当前生效配置选择默认主体：Mihomo 显示原版 zashboard，直连／外部 SOCKS5 保留既有界面。SWD 检测与接入诊断仍由页内链接访问；设置中的面板入口进入网络页，并保留独立打开。外层使用 Fluent v9，标准 iframe 保留上游原版组件与主题，未修改固定 ZIP、增加面板分支或新增持久化配置。只为面板引导页和静态页面允许同源嵌入，其他 Console 响应继续禁止嵌入。

只在网络页的面板视图可见时挂载；切页、进入诊断或浏览器页面隐藏时卸载，返回后重新接入当前实例。核心和节点选择不随界面卸载而关闭。小视口允许页面自然撑高，Flex 分配正常窗口的剩余空间，不使用猜测的页头高度补偿。

证据位于忽略目录 `artifacts/mihomo-dashboard/embedded-network/`：

- **界面和配置生命周期**：相关浏览器回归覆盖草稿／已保存配置不提前切换、原有诊断、导航焦点和布局。首轮 36 项中 35 项通过，一项错误提示同时匹配隐藏设置与当前面板；限定所属区域后通过。新增极矮视口用例与最终 3 项面板用例通过（`browser-fixtures.log`、`browser-fixtures-fixed.log`、`browser-short-fixed.log`）；全部覆盖共 37 个不同用例，没有将重复复核计作新用例。
- **真实服务与安全边界**：Go 隔离真实 Console、原生 Mihomo 和固定 zashboard 的 race 联调通过，包含 8 个本地夹具节点、缓存不可用时的告警和面板选点、原生选择恢复、自动组切换、过期 token 更新、独立窗口、键盘进入与退出、同源加载及跨源嵌入拒绝（`real-race-verified.log`、`browser-verified/result.json`、`browser-warning-verified/result.json`）。受控触发页面可见性事件后，浏览器实际数据订阅全部关闭；SPA 进入诊断同样验证关闭事件，选择仍由核心保留。后台隐藏使用受控浏览器事件验证，没有以它替代真实系统最小化或休眠验收。
- **测试观察修正**：早期联调断言同时匹配 Tooltip 和设置正文，随后连接计数包含被拒绝的日志握手；整页刷新还会使 Playwright 1.64.1 清空旧 WebSocket 映射而不补发所有 close 事件。计数现只跟踪当前文档中实际收到数据的连接，hash 导航及 iframe 移除仍必须收到真实关闭事件。键盘进入 iframe 可能恢复此前选中节点的焦点，改为沿原生反向 Tab 顺序验证能返回 SWD。相关失败保留在 `real-race.log`、`real-race-fixed.log`、`real-race-streams.log`，没有为这些观察差异修改产品核心。
- **布局**：真实浏览器检查 320／390／800／1280 像素宽度、深色与 forced-colors，并检查 640×360、320×180 的小 CSS 视口。初版固定页面高度导致极矮视口下背景不能覆盖溢出内容，`browser-final/dashboard-short.png` 和 `browser-short-layout.log` 保留失败证据；最终使用自然撑高的最小视口高度，定向回归通过。小 CSS 视口覆盖缩放时的布局约束，不等同于真实浏览器缩放、Windows DPI 或屏幕阅读器验收。面板原生的可选外部更新查询仍在测试中主动阻断，相关 `Failed to fetch` 不外推为节点控制失败。
- **构建**：TypeScript、嵌入资源一致性、Console `go vet` 与 `git diff --check` 通过。最终验证构建为 `embedded-network/SWD.exe`，59,422,208 字节，SHA256 `4a10691f7396d1883e5d4772162e04a32630244d1a7bca3ff1921406f710f072`；114 个实际链接 Go 模块的声明核对通过。没有运行或部署该 EXE，没有提交或推送；之前各阶段构建及失败日志保留原适用范围。

### 审查修正（2026-10-31）

保留旧 `#network` 书签到「当前连接」的落点与焦点，`#/network` 在 Mihomo 模式下继续打开面板；当前连接链接沿用旧锚点，移除本轮曾新增的重复锚点及可变面板 ID。可访问性文档同步限定为已完成的键盘、窄屏和小 CSS 视口检查，不再将其表述为真实浏览器缩放验收。

本轮证据位于 `artifacts/mihomo-dashboard/embedded-review/`：

- 17 项浏览器回归通过（`browser.log`）。新增用例使用简化 iframe 夹具，覆盖 Mihomo 模式下旧书签直达、刷新、两种落点间的前进／后退。既有当前连接、诊断与面板生命周期回归通过。
- 隔离真实 Console、Mihomo 与原版 zashboard 的定向 race 联调通过（`real-race.log`、`browser-real/result.json`），确认当前连接导航关闭面板订阅，回到面板后可继续操作。此前缓存异常与系统显示设置的验证边界不变。
- 前端类型与嵌入资源一致性检查通过。首次打包受沙箱写入限制，原日志保留在 `webui-build.log`；相同构建在获准写入后通过（`webui-build-unrestricted.log`）。更新的桌面验证构建为 `embedded-review/SWD.exe`，59,421,696 字节，SHA256 `da0a9e0ad8a23c874ca4b6baa4aa3e2b2e2fe31d401132366040d4c766a6232d`；114 个实际链接 Go 模块的许可声明核对通过。未运行或部署此 EXE，未提交或推送。

### 嵌入面板的浏览器历史（2026-10-31）

在隔离 Console、Mihomo 与 Edge 中，从设置的网络标签打开原版 zashboard。修正前两次复现后退仍停在 `#/network`。路由记录显示 iframe 回到 `#/setup`，该页又自动进入 `#/proxies`。固定版的 setup 登录使用 `router.push`，每次返回都会再创建历史项。证据位于 `artifacts/mihomo-dashboard/embedded-history/` 的 `before.log` 和 `before-trace.log`，路由记录仅保留路径和不含查询参数的片段。

引导页选好当前受管后端后直接替换到代理页。真实 Edge 通过 `goBack()` 和 `goForward()` 在正常启动（8 个本地节点）与选择缓存不可用（3 个本地节点）时各往返两轮。每次后退一次即回到原设置标签 `#network-proxy` 并卸载 iframe，前进一次即重新显示 `SWD` 代理组。过期 token 刷新时沿用原 UUID，其他后端保留，已有列表为无效 JSON 时显示提示并保留原值。定向 race 联调通过，见 `real-race.log`、`browser-final/result.json` 和 `browser-warning/result.json`。

Console `go vet`、嵌入资源校验和桌面构建通过。验证程序 `embedded-history/SWD.exe` 为 59,419,648 字节，SHA256 `d725fd03f5d173303b3700ba6b4cf8ff8cc827e5280191f2c0d6ed9a721381da`，114 个实际链接 Go 模块的许可声明核对通过。

## 未覆盖范围

没有使用私人订阅、公网代理或游戏，不能据本地夹具推断线路速度、可用性或 VRC 播放体验。本次不新增机械式 VRC 台账项；已有实机规则继续有效。

没有生成带 tag 的正式 Portable/对应源码成对发行，也没有验证公开下载或发布。当前验证覆盖新增面板的完整源码收集/离线重建、发布工具回归及桌面编译/许可；正式发行仍需对选定干净提交走现有发布链。固定上游 ZIP 未修改，不声称重建输出与官方 ZIP 逐字节一致，也不声称从原始图形重建旗帜字体。
