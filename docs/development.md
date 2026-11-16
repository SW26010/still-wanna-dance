# 开发环境

Still Wanna Dance 使用 Go，管理 UI 采用 React + TypeScript + Fluent UI React v9，资源编译进 EXE 后在本地浏览器运行。

下载架构以[下载需求与资源管理设计](download-resource-design.md)为 canonical 文档，维护现行规则、实现接口及验证边界。阅读当前实现时从[全局块调度设计](block-scheduler-design.md)开始：需求管理层解释 GET/队列与交接，`internal/blocksched` 统一处理块期限、租约、启动窗口和并发；资源层负责执行与文件一致性。[运行规则](download-concurrency.md)是当前参数依据，[开发记录](validation/block-scheduler-validation.md)按历史顺序保留实现和自动检查，不应将目标设计或早期策略当作现状。

## 功能完成与实机验收

开发者（含 agent）先完成能由自动测试、隔离服务和浏览器判断的验证；只有必须依赖 VRC 播放器、真实游戏事件或游戏内体验的剩余风险才进入[待实机验收台账](live-acceptance-backlog.md)，说明不可替代的原因，并优先合并到已有操作。无需为每项行为改动登记，也不把已通过的代码断言重复交给游戏用户。托盘、跨用户等 Windows 条件单独记录环境缺口，见[非 VRC 分流](validation-followups.md)。按[实机验收机制](live-acceptance-process.md)积攒必要游戏场景；等待期间 agent 继续验证和准备，实际开测后承担采集、脱敏、报告及清理。

前端源码在 `webui/src`；Go 的 `internal/console/webui.html` 只负责外壳和经过 JSON 转义的 bootstrap。`webui/build.mjs` 用 esbuild 生成并提交 `internal/console/assets/webui`、第三方声明和构建清单。普通 Go 构建直接嵌入这些产物；桌面构建脚本用 Node 校验源码和产物哈希，界面改动后必须先重建。前端开发：

```powershell
pnpm --dir webui install --frozen-lockfile
pnpm --dir webui build
pnpm --dir webui check
./scripts/with-test-temp.ps1 pnpm --dir webui test
```

使用 `webui/package.json` 固定的 pnpm 版本。本机和 CI 的夹具回归均使用锁定 Playwright 所附的 Chromium，首次运行前执行 `pnpm --dir webui exec playwright install --no-shell chromium`；Windows 真实 Console 联调使用 Edge。当前 `pnpm --dir webui test` 只调用内存夹具，不修改 hosts 或访问真实上游。截图保存在忽略目录 `artifacts/webui-tests`。Windows 工具测试用 `./scripts/with-test-temp.ps1 node --test scripts/*.test.mjs`。Windows CI 同样使用临时目录包装器；Linux 可直接运行包装器后的原命令。架构、浏览器覆盖和真实系统验收边界见[Fluent 迁移记录](fluent-ui-migration.md)。

曲库的真实 Console／下载引擎／Edge 联调可设置 `SWD_LIBRARY_BROWSER_EVIDENCE` 为隔离输出目录，再执行 `./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestLibraryBrowser$' -count=1 -v`。分别检查外部 GET 的完整下载发生在两次观察之间，以及曲库已读取但首次下载快照尚未采集时，曲库无需手动检查便显示已保存；同时覆盖共享资源、搜索、偏好与删除。两组证据分别写入 `between-reads` 和 `before-first-observation` 子目录。使用受控上游及临时文件，不启动游戏、网关或修改 hosts。

多窗口实时订阅的真实 Console／浏览器回归可设置 `SWD_REALTIME_BROWSER_EVIDENCE` 为隔离输出目录，再执行 `./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestRealtime(Network|HistoryAndBlocks)Browsers$' -count=1 -v`。网络、历史和块视图分别从三个增加至六个同时可见页面，检查普通请求不因实时订阅排队、WebSocket／SSE 连接数、检测／取消跨页同步及关闭后连接释放；结果分别写入 `network`、`history`、`blocks` 子目录。只使用隔离配置和受控检测传输，不启动网关或修改 hosts。

前端构建与检查统一启用 TypeScript `noUnusedLocals`、`noUnusedParameters`，移除失去用途的声明和参数。设置保存的真实双页面、持久化与重启回归可设置 `SWD_SETTINGS_BROWSER_EVIDENCE` 为隔离输出目录，再通过上述临时目录入口执行 `node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestSettingsSeparateBrowsers$' -count=1 -v`；测试固定第二个页面的旧状态快照以检查部分更新，并覆盖目录确认期间的策略变化、拒绝后重新确认及无关字段合并。原生选择器用隔离目录替代，检查与保存均经过真实 Console，不启动网关或修改 hosts。

设置字段校验的真实 Console 联调可设置 `SWD_SETTINGS_VALIDATION_BROWSER_EVIDENCE` 为隔离输出目录，再执行 `./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestSettingsValidationBrowser$' -count=1 -v`，验证缺少节点的拒绝不落盘、不产生桌面打开通知，以及合法 SOCKS5 设置保存后保持待重启边界。只使用隔离配置，不启动代理或网关。

Windows 路径选择器的普通检查包含实际 Shell 选项、初始目录回退、助手错误协议及 DPI 失败恢复，不显示窗口。设置 `SWD_PATH_PICKER_EVIDENCE=1` 后，通过同一临时目录入口运行 `node scripts/go-patches.mjs test -tags=no_easytier ./internal/desktop -run '^TestPathDialogNative$' -count=1 -v`，自动选择或取消临时资料，检查三种模式、SWD 图标、PMv2 和请求取消后的窗口／进程清理；日志记录进程启动到可见窗口的采样耗时，不设 CI 时间阈值。默认运行测试进程内的真实助手实现；可另设 `SWD_PATH_PICKER_EXE` 为当前构建的桌面 EXE 绝对路径，验证实际内部入口及相同原生行为。需要普通 Windows 桌面权限；沙箱拒绝与外部前台窗口关联时保留失败并在普通用户权限下复验，不修改用户窗口或系统设置。

启动配置修复的真实进程／浏览器检查可设置 `SWD_RECOVERY_BROWSER_EVIDENCE` 为隔离输出目录，再执行 `./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./cmd/still-wanna-dance-console -run '^TestApplicationRecoveryAfterInvalidConfiguration$' -count=1 -v`。覆盖损坏 JSON 只读、预算矛盾与多个无效字段保留原值、保存、重启到新会话、320px 布局与退出。替代进程不接受条款，不启动游戏或修改 hosts；普通 Go 测试不设置此变量时仅验证恢复入口与退出且保持配置不变。

恢复页与正常设置共用表单的操作联调可设置 `SWD_RECOVERY_SETTINGS_BROWSER_EVIDENCE`，执行 `./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestRecoverySharedSettingsBrowser$' -count=1 -v`。隔离目录替代路径选择结果，真实创建数据库及校验／加密保存 Mihomo 配置；权限助手使用私有 hosts 夹具，验证取消后保留草稿、重试提交及退出清理，不触及系统 hosts。

恢复页关闭与真实托盘清理可设置 `SWD_RECOVERY_EXIT_BROWSER_EVIDENCE` 为隔离输出目录，再执行 `./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./cmd/still-wanna-dance-console -run '^TestRecoveryTrayExitsAfterBrowserCloses$' -count=1 -v`。使用私有损坏配置、真实 Windows 托盘与 Edge 两个页面，关闭页面后等待实际 60 秒，检查托盘运行循环返回、HTTP 服务结束及配置租约释放。替代实例登记，不竞争正在使用的桌面实例，不启动业务服务或更改 hosts。

Mihomo 完整配置的真实浏览器联调可设置 `SWD_MIHOMO_CONFIG_BROWSER_EVIDENCE` 为隔离输出目录，再运行 `./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestMihomoConfigurationBrowser$' -count=1 -v`，覆盖浏览器文件选择、文件／URL 的保存时自动检查、Base64 显式检查复用、检查失败不保存、原文加密／展示元数据普通保存、摘要刷新重载及待重启。只使用回环配置服务器、隔离数据和测试节点，不修改系统 hosts。

加速启停与退出策略的真实浏览器联调可设置 `SWD_HOSTS_BROWSER_EVIDENCE` 为隔离输出目录，并以同一入口运行 `node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestHostsPolicyBrowser$' -count=1 -v`。测试使用回环网关和私有 hosts 文件，不修改系统 hosts。Windows CI 同时运行此检查和上述设置联调。

更新检查的真实 Console 与浏览器联调可设置 `SWD_UPDATE_BROWSER_EVIDENCE` 为隔离输出目录，并以同一入口运行 `node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestUpdateBrowser$' -count=1 -v`；GitHub 响应受控，检查启动通知、多个页面、刷新和手动检查。另可设置 `SWD_UPDATE_GITHUB_LIVE=1` 并运行 `TestUpdatePublicGitHub`，通过产品直连网络访问公开 Release 接口，不启动游戏或修改 hosts。

存储确认与字段故障修复的真实浏览器联调可设置 `SWD_STORAGE_REPAIR_BROWSER_EVIDENCE`，再运行 `./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestStorageRepairBrowser$' -count=1 -v`。覆盖黄色一键确认、红色字段定位、视频策略取消／确认、保存后的待重启及重新打开，检查已有资料原字节保留；路径选择结果与 hosts 使用私有夹具。

批量失败结果的真实 Console／浏览器检查可设置 `SWD_BATCH_BROWSER_EVIDENCE`，执行 `./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestBatchResultsBrowser$' -count=1`。从完整的 10,575 条失败快照重启隔离 Console，检查状态摘要、每页 50 行、翻页、切回首页后卸载及 390px 布局；不启动视频服务、网关或游戏。性能对照与验证边界见[批量结果验证](validation/batch-results-20261115.md)。

请求历史的隔离 SQLite／Console／Edge 联调可设置 `SWD_HISTORY_BROWSER_EVIDENCE` 为证据输出目录，执行 `./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestRequestHistoryBrowser$' -count=1 -v`。六百万条持续写入验证另设 `SWD_HISTORY_LARGE_EVIDENCE=1`，运行 cacheproxy 的 `TestHistoryLargeReadsPersistConcurrentObservations`；两者均不启动游戏、播放网关或修改系统 hosts。范围、结果和保留失败见[有限读取验证](validation/history-bounded-reads-20261115.md)。

## 测试维护与 CI 修复

目标是平衡发现真实问题的效果与长期维护成本，让失败容易复现和定位。测试数量、模拟数量和单次全绿都不能单独证明验证质量；选择能判断具体风险的最小可靠检查，验证分工以[实机验收机制](live-acceptance-process.md)为准。

1. **按行为选择测试层次。** 业务规则优先通过真实 Go 实现的单元或集成测试验证；浏览器夹具主要验证展示、交互和故障反馈。需要证明保存、持久化或重启等前后端协作时，选择少量代表流程连接隔离真实服务，优先复用现有联调脚本。夹具通过不能外推为真实服务通过。
2. **控制模拟与辅助机制的维护成本。** 模拟保留必要的数据和故障注入，避免继续复制后端业务规则。共享浏览器夹具按方法和路径明确支持请求，未支持的 API 返回 404；仅确认受理的操作不模拟任务进度，需要进度或失败状态的用例自行提供对应响应。事件流由夹具服务器处理。共用助手应消除多处重复或竞态；若调用者必须记住越来越多的例外，应收窄用途或调整测试层次。迁移覆盖时删除已被替代的重复测试，保留仍验证不同风险的用例；不为整理测试另建通用框架。
3. **先归因，再成组修复。** 收齐相关平台的失败与证据，区分产品缺陷、夹具偏差、时序错误和环境限制；先建立能复现同一症状的定向检查，再检查同类调用点并成组修正。本地完成受控对照和受影响回归后再推送，用远端检查确认平台差异。同类失败再次出现时重新检查共同原因与验证层次，避免逐条追加补丁、反复依赖远端试错。
4. **等待可观察的完成条件。** 按请求结果、最终界面状态或实际事件同步测试，精确时序断言放在可控的层次。不得用增加固定等待、超时、重试或删弱断言代替根因修复；诊断用延迟和探针在验证后移除。保留历史失败和必要证据，明确本机与远端、模拟与真实环境的覆盖范围。
5. **按证据收敛工作。** 已知失败得到解释、受控复现通过、相关回归及所需远端检查通过后结束本轮。仅在出现新失败、相关改动或明确证据缺口时扩大验证；不为追求绝对无偶发而无限重跑。已稳定的测试不因潜在风险启动整体重写，维护改进优先处理已确认的重复逻辑和时序耦合。

CI 浏览器失败保留 trace 与截图；产品检查、race 和按需打包各自运行。上述原则的失败对照与适用边界见 [CI 稳定性验证](validation/ci-stability-20261028.md)；近期完整本机检查与六组并行的结果见 [2026-11-17 验证](validation/local-full-checks-20261117.md)。验证记录描述当时构建，不替代本节现行规则。

PR/main 的 `check` 工作流照常启动，工具和仓库检查保留。普通 push 比较整个 `before..HEAD`，PR 比较合并基点到分支 head；删除和重命名同时检查原、新路径。仅当全部改动属于明确的非业务范围，且比较起点在同一工作流中已有成功检查，才跳过产品作业与 race 测试。日常 race 作业只报告无需检查，跳过源码检出、工具链安装和测试，以保持 `race (0)` 状态名稳定。没有成功记录（包括检查仍在执行或失败）、历史不可用或 API 查询失败时保留业务检查。手动运行和版本 tag 不使用这条日常跳过规则，未执行的检查不能成为正式发行的完整验证基线。

非业务范围与发行验证复用共用 `scripts/release-validation.mjs` 的分类：明确列出的交付文件、说明文档，以及验收／证据等约定目录的静态资料。不能将整个 `docs/` 视为非业务：前端构建清单声明的输入和产物始终需要产品验证，未知资料或代码也默认需要检查。只在作业条件中分流，避免整条工作流被路径过滤后缺少必需状态。

本机和 Actions 使用同一 `release-preflight.ps1` 入口。Node、Python 版本分别以根目录 `.node-version`、`.python-version` 为准，Go 以 `go.mod` 为准，pnpm 以 `webui/package.json` 的 `packageManager` 为准；预检发现版本不符即停止，并在本次进程内设置 `CI=true`，结束后恢复调用者环境。Go 业务测试显式使用 `-count=1`，避免本机测试缓存掩盖实际执行。

Python 通过现有 uv 管理。本机和 Actions 均使用 `uv run --no-project --managed-python --python <.python-version 中的版本> python ...`，复用已安装的匹配解释器，缺少时由同一管理器取得；不依赖系统 `python` 别名或其他应用附带的解释器。Actions 固定 uv 版本，预检记录实际 uv 和 Python 版本。

更新固定工具版本时，先核对相应 Actions 安装器的可用版本，再使用相同版本完成本机预检；本机某个发行渠道已安装的版本不自动代表远端安装器能够提供。

浏览器测试固定两个 worker、零重试，双方均保留失败 trace。夹具回归使用锁文件固定的 Playwright 所附 Chromium，预检负责安装同一浏览器构建，避免系统 Edge 与 runner 镜像更新不同步。真实 Console 联调共用浏览器启动入口，Windows 使用 Edge，其他平台使用 Playwright Chromium，不按 `CI` 标记切换浏览器。每次浏览器套件启动时打印实际 Node、Playwright、浏览器版本及并发参数，排查环境差异时对照这些记录。

慢封面响应、浏览器图片缓存回收、迟到的历史消息与错误提示的无障碍播报重叠属于常驻回归输入，PR/main 的代表性检查包含对应测试文件。它们主动构造已知风险顺序，不依赖碰巧繁忙的机器；响应延迟用于模拟输入，测试仍按实际完成事件同步，按现行行为契约判断结果，不放宽期限。临时诊断开关、探针和额外日志在归因后移除。

成功保存设置的用例通过 `webui/e2e/settings-actions.ts` 等待页面明确显示“待重启”或“已生效”，再读取状态或刷新页面；按钮禁用也可能表示请求尚未完成。验证处理中、失败和校验拒绝的用例仍直接控制请求并检查对应状态。

验证异步轮询最终状态时，使用 `webui/e2e/clock.ts` 在断言等待期间持续推进模拟时钟；请求到达或局部 UI 更新不代表下一次轮询定时器已经注册。该助手保留默认断言期限，只用于验证持续轮询与最终状态；精确周期、超时和停止后的无请求检查仍显式推进指定时长。短暂通知的操作先完成，再对目标页面截图，避免截图采集与通知自动消失争抢时间。

## 发布前本机 Windows 完整预检

正式发行前，由开发者先在本机对待发行源码完成以下完整预检，再启动发行 Action。统一入口为 `scripts/release-preflight.ps1`，Actions 的工具、产品和 race 作业也使用它。日常开发仍按变更影响选择检查，PR/main 的代表性浏览器和 race 检查不代替完整预检，也不要求每次编辑或推送都重跑整套发布流程。

预检使用干净的独立检出，避免 `artifacts/` 中的历史探针被 `./...` 收集。Windows 交付预演应选择较短的检出位置（如仓库下的 `artifacts/r`），为临时目录和 pnpm 路径预留空间。离线前端重建的 esbuild 可执行路径达到 260 字符时，Node 启动会报 `ENOENT`，即使文件存在；本机同一二进制的 259／260 字符对照已复现该边界。缩短检出位置后重跑交付阶段，不通过放宽校验或跳过离线重建处理。

Go、Node、pnpm、Edge 和 race GCC 与 [`check` 工作流](../.github/workflows/check.yml)及锁文件对齐；工具检查还需要 Python 3。先在当前终端选择 `.node-version` 指定的 Node，再设置 `CC` 指向工作流固定版本的 GCC，在独立检出中准备仅本地的测试 tag（不推送），然后运行：

```powershell
./scripts/release-preflight.ps1 -ReleaseTag v1.2.3-preflight -PreviousTag v1.2.2
```

顺序为工具与仓库检查 → 产品检查 → 完整 race → 最终交付预演；遇到失败即停止，避免在基础错误后继续耗时构建。`-PreviousTag` 下载并校验实际公开的前一版本 ZIP，已有有效下载只复核哈希；也可用 `-PreviousZip` 指定本地真实旧包及其 SHA256。首发没有旧包时省略，不为覆盖升级额外构建同源码候选包。产物使用测试 tag，不上传、不发布。日志与 `result.json` 保留在忽略目录 `artifacts/release-preflight/`，失败记录也保留；状态只说明该次运行的实际范围，不作为 Actions 跳过测试的凭据。

| 范围 | 完成条件与现有入口 |
| --- | --- |
| 产品与工具检查 | 冻结锁文件安装前端依赖，运行前端一致性检查和完整浏览器套件、全部 `scripts/*.test.mjs`、Go `./...` 的 test/vet/build、gofmt、仓库产物策略及 Python 证据工具测试；Go 检查统一使用 `scripts/go-patches.mjs` 和 `no_easytier`，产品构建使用 `CGO_ENABLED=0` |
| 前后端联调 | 运行本页的真实 Console 双浏览器设置检查；保存、持久化、重启等其他受影响流程复用对应隔离联调，不能用内存浏览器夹具代替 |
| 完整 Windows race | 设置匹配 CI 的 `CC`，使用预检的 `-Scope race` 完成全部六组和其余 Go 包；分组入口与分配规则见下文 `test-race-shard.ps1`，保留完整执行结果和跳过项 |
| 正式包预演 | 按[打包入口](packaging.md)完成必要对应源码收集、全新缓存离线重建和实际 ZIP 启动；打包不再嵌套工具或业务全量测试 |
| 历史版本升级 | 使用实际前一发行包和隔离持久数据验证覆盖升级；涉及数据库、配置或持久化格式变化时，另完成对应历史格式迁移验证。同源码的候选包不能证明历史格式兼容 |

失败先归因并成组修复，按受影响阶段补验：`-Scope tools`、`-Scope repository`、`-Scope product`、`-Scope race` 或 `-Scope delivery -ReleaseTag ...`；`repository` 维护独立的产物策略及 Python 检查，避免与 Actions 工具作业重复。本机完整 race 默认同时启动全部六组，每组独立保存 `race-shard-N/checks.log` 和 `result.json`；等待六组全部结束后汇总，任一组失败则本阶段失败，保留其他组的实际结果。六组成功后，其余 Go 包只检查一次。定向复验可用 `-Scope race -RaceShard 0`（0～5），显式选择第 0 组时也检查其余包一次。`-Representative` 只供日常产品/race 检查，不能用于完整预检或正式交付预演。新增了业务行为或依赖变化时补全相应产品、浏览器和 race；仅修改交付脚本时补工具与交付预演，纯文档修改不触发业务全量重跑。需要额外真实 Console 联调时仍按本页受影响行为选择，统一入口不宣称覆盖全部按需场景。

完成记录写明源码、实际工具版本、通过/失败/跳过范围和证据路径；skip 不算通过，缺少专用环境只限制对应结论。本机通过后，最终发行仍须由 GitHub Actions 构建、上传和回下载公开附件。本地成功不代替公开交付验证。Actions 内的完整业务结果复用与失败重试规则统一见[打包说明](packaging.md)，无需为每个版本复制工作流。

## Console 路由与共享引擎

诊断包真实 Console／Edge 下载检查使用 `SWD_DIAGNOSTIC_BROWSER_EVIDENCE` 和 `TestDiagnosticExportBrowser`；完整复跑命令与隔离范围见[诊断包验证](validation/diagnostic-export-20261114.md)。

普通管理请求在 [`management.go`](../internal/console/management.go) 选择处理函数后统一校验会话 Token 和 Origin；新增普通管理读取入口应登记在该路由组，POST 操作继续由 `serveAction` 分发。业务处理函数不再重复鉴权。将需要通知的运行故障写入管理接口响应时，通过 `preserveResponseFault` 显式保留原错误；HTTP 状态和响应正文只表达接口结果，不决定是否通知。输入、忙碌、过期状态及缺少确认等预期拒绝通过 `rejectRequest` 标记，JSON 请求解码共用 `decodeRequest`，字段校验复用该标记；预期拒绝与取消既不上报，也不清除旧故障。保留错误链用于同因去重和混合错误判断，对外文案和脱敏方式仍由处理函数决定。共用操作分发显式指定业务来源；仅对应操作成功或业务明确解除该故障时清除，不按 URL 第一段合并不同操作。条款操作复用同一校验函数，保留同意前可访问的规则；公开读取、媒体签名、SSE 和 WebSocket 仍由各自入口按现有规则处理。回归从 `ServeHTTP` 验证准入及方法错误，不能只测试校验函数。

共享引擎由 Console 持有，入口集中在 [`engine.go`](../internal/console/engine.go)。视频任务和网关在已有 `lifecycleMu` 保护的启动流程中调用 `ensureEngineLocked`，调用时不持有状态锁 `mu`，直接使用返回的引擎；导入先完成目录校验，曲库更新则直接使用已有 Store。任务登记与退出等待仍由各业务生命周期负责。预览通过 `retainEngineRead` 一次取得引擎及释放函数，涵盖首次淘汰前的保护。创建、发布通知与关闭不借用调用方的状态锁；状态读取继续只取得短期快照，不等待引擎 I/O。运行语义以[配置生命周期](settings-lifecycle.md)为准。

网关启停沿用与设置、共享引擎共用的 `lifecycleMu`；启停方法自己取得状态锁，不借用调用方的 `mu`。独立设置编辑器使用同一事务锁；其快照锁不覆盖文件或授权等待，保存发布与状态读取的锁序为 Console `mu` → 编辑器 `mu`。关闭前先取消编辑器的路径选择和授权等待，再等待事务锁。停止先取消队列并等待来源撤销，再释放网关监听。队列任务自行持有状态、取消和完成信号，以及状态锁和更新锁；展示侧读取独立的状态快照，Console 保留网关随动意图和应用级启停协调。队列状态变更与引擎保护同步由任务的更新锁串行化，等待保护更新时不持有状态锁，日志的同批快照保持整体顺序。退出的 `BeginShutdown` 先取消任务，后续 `Close` 再等待清理。

网络诊断的职责和退出顺序以[上游检测与测速](upstream-channels.md#上游检测与测速)为准。生命周期测试直接构造 `networkDiagnostics`，注入既有请求和业务能力的夹具，以 `testing/synctest` 验证退避、截止时间、独立取消与退出等待，无需准备 Console、数据库或条款。Console 集成测试继续验证条款准入、当前生效配置、真实代理请求、响应复用及状态流；读取诊断状态通过快照方法，不以 Console 的锁保护诊断内部字段。

## Windows 环境

桌面开发需要 Windows amd64、Go 1.27.3+、Node.js 22+、Git 和 PowerShell 7.3+。前端依赖使用 `webui/package.json` 固定的 pnpm。Go 最低版本和依赖选择分别以 `go.mod` 与锁文件为准。

2026-10-28 将最低 Go 版本提高到包含当前安全补丁的 1.27.3，并更新 `golang.org/x/net` 至 v0.60.0 及其必要依赖；依据为 [Go 发布记录](https://go.dev/doc/devel/release)、[HTTP/2 公告](https://pkg.go.dev/vuln/GO-2026-6610)和 [X.509 公告](https://pkg.go.dev/vuln/GO-2026-6809)。桌面构建与打包固定使用 `GOTOOLCHAIN=local`，因此 PATH 中的 Go 安装也必须满足最低版本；普通 `go` 命令的自动版本切换不代表构建脚本已使用补丁版。GitHub Actions 的 `check` 工作流通过 `actions/setup-go` 的 `go-version-file: go.mod` 安装项目指定版本，并保留 `GOTOOLCHAIN=local` 校验实际工具链；不再单独维护版本号。此前依赖升级的验证范围见[发布前修复记录](validation/release-readiness-20261028.md)，其中 CI 尚未对齐的说明仅描述当轮状态。

在 PowerShell 中检查：

```powershell
go version
go env GOOS GOARCH GOPATH GOROOT GOPROXY GOSUMDB
```

保留默认模块代理 `https://proxy.golang.org,direct` 和校验数据库 `sum.golang.org`。无需手动设置 GOROOT，也不必把项目放进 GOPATH。

Go 自带构建、测试、格式化和静态检查工具。当前纯 Go 方案无需 C/C++ 编译器；运行网页 UI 无需 Node.js；执行前端回归和实网验收脚本需要 Node.js，CI 使用 22。

## 开发命令

构建、测试和 vet 统一经过 `scripts/go-patches.mjs`，应用已确认的 AnyTLS 关闭竞态修复；版本、原文件校验与移除条件见[依赖补丁](../patches/README.md)。直接调用 `go build/test` 会使用未修补的上游代码。桌面打包和对应源码重建已自动接入，格式化不需要该入口。

SQLite 使用纯 Go 驱动 `modernc.org/sqlite`。开发命令：

```powershell
go fmt ./cmd/... ./internal/...
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./cmd/... ./internal/...
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs vet -tags=no_easytier ./cmd/... ./internal/...
node scripts/go-patches.mjs build -tags=no_easytier -o bin/SWD.exe ./cmd/still-wanna-dance-console
```

Windows 测试统一通过 `scripts/with-test-temp.ps1` 运行，定向检查也只需在原命令前加这个入口。它保持调用目录和参数，每次在忽略目录 `artifacts/tmp/<随机标识>` 创建独立临时目录，仅为本次命令及子进程设置 `TEMP`、`TMP`、`GOTMPDIR`；正常返回或命令报错后恢复环境并清理本次目录，原样返回命令退出码。无法清理时明确警告并保留路径，不影响测试失败的退出码。测试日志和需要保留的报告放在该临时目录之外。打包脚本的开发检查与正式发布准备也使用同一入口。

此入口避开 Windows MXC 默认 `sandbox.{GUID}/AC/Temp` 的重命名权限缺陷（[Microsoft 已确认的问题](https://github.com/microsoft/mxc/issues/1204)），不修改产品文件操作、用户/系统环境变量、ACL 或沙箱设置。换到工作区后 `os.Root.Rename`、硬链接、目录联接、浏览器及子进程仍可能受其他沙箱限制；保留失败证据，按实际权限要求验证，不能把所有失败都归为临时目录问题。强制结束 PowerShell 进程时可能来不及清理，残留目录不复用，也不自动清扫其他调用的目录。

对已确认存在上述限制的本机沙箱，明确依赖受限操作的测试可依照当前审批机制直接在沙箱外以普通用户权限执行，仍使用同一临时目录入口；不要求每次先重复触发已知失败。其他测试照常在沙箱内执行。沙箱、浏览器或工具链变化后，使用相关定向检查重新判断适用范围。

2026-10-19 验证：同一 `TestPublicationAdmissionRanksFullCacheBeforeMerge` 在 MXC 默认临时目录报重命名拒绝，经入口运行通过。`TestAtomicWriteRetainsLastGoodFileAndBoundsWork` 在工作区内仍报 `renameat: The handle is invalid`，沙箱外经同一入口通过。工具回归在沙箱内为 28/29（已有 junction 夹具被拒绝），沙箱外 29/29 通过；新增入口回归覆盖原子替换、参数/工作目录/退出码传递、环境恢复、异常清理及嵌套隔离。原始成功与失败输出保留在忽略目录 `artifacts/windows-test-temp/`，未修改产品代码绕过失败。

2026-10-28 CI 复核：`check` [第 12 次运行](https://github.com/SW26010/still-wanna-dance/actions/runs/40706434250)的 Ubuntu 全部通过，两平台浏览器套件均通过；Windows Go 测试因 runner 的 `RUNNER~1` 临时目录别名触发存储路径校验，并使 `TestPathIdentity` 的完整路径断言失败。CI 的 Windows 浏览器、Node、Go 测试及 vet 统一接入现有包装器，存储的路径别名保护保持不变。本机在隔离目录构造 8.3 短路径，直接运行 `TestPathIdentity` 和 `TestShutdownClosesPausedReaderAndFlushesUsage` 均复现同样失败；保持该父环境、仅经包装器运行后两项通过。原始对照保留在忽略目录 `artifacts/ci-fix-20261028/windows-short-temp-{before,after}.log`。

同轮本地验证：使用 Go 1.27.3 后 46 个 Node 工具测试及全部跟踪 Go 包的 vet 通过。Go 检查限定 `cmd`、`internal`、`data`、`experiments`，避免本地忽略目录中的历史探针被 `./...` 收集；CI 干净检出保留 `./...`。首次包测试仅 Console 的临时 hosts 夹具发生一次替换拒绝访问，其他包通过；原用例连续 10 次复核及 Console 整包复核通过（85.440 秒），未修改产品文件操作或放宽测试。过程中的旧 Go 拒绝、历史探针加载失败和夹具失败均保留在同一忽略目录，最终结果见 `windows-node-toolchain-after.log`、`windows-tracked-go-after.log`、`windows-tracked-vet-after.log`、`consent-startup-recheck.log` 与 `windows-console-recheck.log`。

Windows CI 的 PR 和 main push 运行代表性 race；手动或版本 tag 检查额外运行全包 race。独立作业下载固定的 w64devkit 2.10.0 并核对 SHA256，再设置 `CGO_ENABLED=1` 和 `CC`。Windows 下大量 SQLite 测试开启 race 后整包超过 Go 默认的 10 分钟上限，因此缓存和 Console 包由 `scripts/test-race-shard.ps1` 依据 Go 列出的顶层测试分为六组；每个测试及其子测试恰好分配一组，其余包在六组外统一检查一次，CI 由第 0 组负责。每组设 45 分钟的整包期限，测试自身的等待期限不变。本机使用同版本 GCC，运行 `./scripts/release-preflight.ps1 -Scope race`；`test-race-all.ps1` 通过六个独立 PowerShell 后台任务同时执行各组，沿用当前工具链与临时目录隔离。定向使用上述 `-RaceShard` 入口，避免重复检查或遗漏其余包；工具及验证日志保存在忽略目录 `artifacts/`，不依赖全局 PATH。此前三组在本机通过，但 CI 第 2 组触及 45 分钟整包上限；改为六组以减小每组工作量，保留原失败记录和测试期限。

真实磁盘耗尽是独立的 Windows 环境检查，不在普通测试中自动执行。`scripts/test-storage-full.ps1` 使用 Hyper-V 的虚拟磁盘命令创建新的固定 128 MiB NTFS 镜像；只接受本仓库 `artifacts/` 下的测试二进制和证据目录，校验新磁盘身份及容量后才初始化，并为宿主卷保留至少 512 MiB 空间。测试再次校验卷标记与容量，分别填满块、发布和数据库所在的可丢弃卷，检查错误、关闭、清理及已提交数据；结束后卸载并删除本次镜像，保留日志与 `result.json`。不接受现有磁盘号或镜像路径，不填满日常使用的卷。构建不需管理员权限，挂载阶段需要 Windows 管理员令牌；UAC 未确认时此项仍为未验证。

```powershell
New-Item -ItemType Directory -Force artifacts/storage-full | Out-Null
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier -c -o artifacts/storage-full/cacheproxy.test.exe ./internal/cacheproxy
# 在本仓库的管理员 PowerShell 中执行已构建的定向测试：
./scripts/test-storage-full.ps1 -TestBinary artifacts/storage-full/cacheproxy.test.exe -EvidenceDirectory artifacts/storage-full
```

临时 Go 探测源码即使放在 Git 忽略的 `artifacts/` 中，仍会被 `go test ./...` 和 `go vet ./...` 发现。独立探测文件应在首行添加 `//go:build ignore` 并空一行；需要执行时用 `go run` 明确列出入口及其辅助源文件，避免多个 `main` 混入同一个包。也可放入独立的工具模块。

CF 专用实验的新构建要求显式指定本地网卡配置，统一绑定 API、媒体、Monitor 与 DoH 出站；配置错误或绑定失效不回退其他网卡。实现仅在实验测试文件中，正式 EXE 不读取此配置。配置、原始地址和个人运行偏好放入已忽略的 `local/`、`artifacts/`，仓库只保留通用实现和规则，详见[实验网卡绑定](../experiments/cf-block-paths/network.md)。

桌面入口位于 `cmd/still-wanna-dance-console`，协议、本地资源和下载管理位于 `internal/cacheproxy`。运行方法见 [WebUI 与桌面程序](console.md)。默认本地存储、构建产物及测试覆盖率文件已忽略；测试使用 Go 的独立临时目录，不写入原版本地视频或证据目录。安装环境不会启动播放网关或修改 hosts。

桌面构建使用 `scripts/build-desktop.ps1 -Version dev-名称`，版本与 UTC 构建时间写入 EXE，源码提交和 dirty 状态取 Go VCS 标记。`& ./bin/SWD.exe -build-info | Out-String` 输出机器可读 JSON 并立即退出，不读取配置、创建数据目录或争用桌面实例；启动日志复用同一身份。正式标签只经发布流程注入，缺失/dirty 的 VCS 不显示为正式发行；无 Git 的对应源码重建独立标为 rebuild。通过补丁入口直接执行 `build` 仍是开发构建，未注入的时间明确留空。

Windows amd64 的桌面 EXE 图标使用 `internal/branding/logo.ico`。同目录已登记的 `logo_windows_amd64.syso` 由 Go 自动链接，开发构建与桌面／便携打包均生效。更新 ICO 或 Common Controls v6／默认 DPI 清单 `app.manifest` 后运行 `go generate ./internal/branding`，使用固定版本的 [rsrc](https://github.com/akavel/rsrc/tree/v0.10.2) 重新生成资源；正常构建无需安装或下载此工具。图标源、生成步骤与哈希登记见[资源约定](evidence-storage.md#git-历史和防回归)。

2026-10-21 图标验证：普通 `go build -tags=no_easytier` 与桌面构建均通过；用 Win32 资源 API 从两个实际 EXE 读取的九档图标逐字节匹配 `logo.ico`，`PrivateExtractIconsW` 在 16–256 像素九档尺寸均可提取，256 像素提取图已检查。原生图标解码、资源策略的三项回归及生成资源重复构建字节一致性通过。原始核对结果与提取图在忽略目录 `artifacts/exe-icon/`；未检查已有资源管理器窗口的图标缓存刷新行为。

可选桌面条款凭据检查 `scripts/acceptance.mjs` 使用 Node.js 标准库：

```powershell
node scripts/go-patches.mjs build -tags=no_easytier -o bin/SWD.exe ./cmd/still-wanna-dance-console
node scripts/acceptance.mjs bin/data/state/terms.json
# 也可显式指定桌面程序
node scripts/acceptance.mjs bin/SWD.exe bin/data/state/terms.json
node --test scripts/acceptance-protocol.test.mjs
```

检查前须由操作者在当前 Windows 用户下阅读并确认当前条款。只传一个参数时，它是 `terms.json`，桌面程序默认使用 `bin/SWD.exe`；传入两个参数时，依次为桌面程序和 `terms.json`。脚本在 `test-runs/acceptance-*` 中建立独立配置，原样复制密文并启动关闭自动加速的桌面程序，通过门禁验证凭据，并核对内嵌条款与当前源码一致；不会解密、伪造或自动点击同意。失效凭据或缺少桌面程序返回 environment/blocked；报告记录程序 SHA256 和结果，进程日志留在测试目录。

条款保护回归在 Windows 可运行 `./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test ./internal/console -run Terms -count=1`。Windows 使用真实 DPAPI，包含独立子进程读取和同用户搬迁；Linux 测试进程使用临时认证加密替身，生产代码仍明确拒绝非 Windows 凭据保存。这不替代真实跨用户/跨机器测试，验证范围见 [#15 验收记录](validation/issue-15-validation.md)。

桌面条款检查以 `-no-tray -no-open` 启动，关闭自动加速并使用动态回环控制端口，不占用 80/443。脚本不改 hosts、不读取旧库，结束时关闭本次进程。桌面网关的冷／热播放及离线复用由 Go 受控上游集成测试覆盖；[可选实网检查](console.md#http-播放-api-接管与回源)验证产品上游协议，不等同于桌面 EXE 的完整下载验收。

`results.json` 的总体 `outcome` 为 passed/failed/blocked，退出码分别为 0/1/2；程序检查失败时返回 1。blocked 不表示检查通过，失败原因可结合报告和进程日志判断。

`scripts/game-hosts.ps1` 仅用于人工联合验收：先要求 1343 的 API 热缓存 Range 探测通过，再添加 `api.udon.dance` 映射；Disable 只清理当前验收会话标记的 API 映射。它不会添加两个历史视频域名；旧版操作结果见[验收记录](acceptance/early-service-20260925.md)。日常接入使用 WebUI 或托盘，开发检查不自动执行此脚本。

WebUI 的设计约定、浏览器验收与平台验证边界见 [可访问性验收](webui-accessibility.md)。
