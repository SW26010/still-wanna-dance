# 视频存储预算零值与独立不限选项：开发验证

现行三选一行为及配置格式见[本地视频保留与存储预算](../cache-retention.md)，2026-10-20 起替代下方旧二选一规则。

2026-10-19 起按[术语与命名](../download-resource-design.md#术语与命名)使用“视频存储预算”“本地视频”“房间队列预加载”。下方既有验证结果保留当时的名称和环境；本轮术语检查见末节。

初次验证：2026-10-16；默认值于 2026-10-18 调整为零预算。实机状态：**待验收 LA-047**。

## 初版行为与实现（历史）

- 设置页与首次引导共用 `webui/src/cache-capacity.tsx`，只提供「不限制／限制为」。限制值允许 0，最多两位小数；切换选项保留数值。全新配置与引导「恢复本页默认」均限制为 0 GiB，已保存的明确容量选择保留。
- `cacheUnlimited` 独立表示不限；`maxCacheBytes` 表示字节预算。限制为 0 时复用现有引用、队列及交接保护，保护解除后回收正式视频；已有受管视频也按该预算处理。允许临时写盘，保留歌曲资料、统计及未知文件。不提供旧配置兼容或迁移逻辑。
- 保存和引导应用均携带两个字段，保存后仍需重启生效。引导确认页和本地视频页正确区分零预算与不限。
- 零预算不接纳需要新增空间的补齐下载；已有受保护文件可复用。网页禁用补齐并解释原因，仍允许检查缺失视频和队列预加载。
- CLI 显式 `-max-cache-bytes=0` 使用零预算，正数使用有限预算，`-cache-unlimited=true` 独立选择不限。两种显式选择互斥；不传参数使用零预算。

主要代码：`internal/cacheproxy/{config,retention,cache,batch_budget,server}.go`、`internal/console/{console,setup_apply}.go`、`cmd/still-wanna-dance/main.go`、`webui/src/{cache-capacity,settings,setup,cache,home-library}.tsx`、`webui/src/types.ts`。嵌入前端与 `webui/build-manifest.json` 已重新生成。

## 自动检查

以下为 2026-10-16 的开发检查，当时默认不限、容量输入预填 40 GiB；不代表真实游戏、托盘或跨进程重启场景通过。

| 命令 / 范围 | 结果与覆盖 |
| --- | --- |
| `go test -tags=no_easytier ./internal/cacheproxy ./internal/console ./cmd/still-wanna-dance -count=1 -timeout=180s` | 三包通过。新增覆盖零预算冷播完整响应、现有文件启动回收、未知文件保留、多引用保护、队列预取后两次播放仅下载一次、交接计时到期后自动回收、零预算补齐拒绝新资源且允许现有资源复用；设置与引导保存 0/不限并保持 active 配置；已有正数淘汰、补齐与保护回归通过。 |
| 在 `webui/` 执行 `pnpm build` | TypeScript 检查和嵌入资源构建通过。 |
| 在 `webui/` 执行 `node node_modules/@playwright/test/cli.js test e2e/cache-capacity.spec.ts e2e/setup.spec.ts e2e/console.spec.ts e2e/recovery.spec.ts e2e/copy.spec.ts --workers=4` | 27 项通过，含新增 4 项：保存 0/不限、刷新与放弃草稿、正数草稿、active 保持、引导加载零值/确认/提交、两入口非法输入、零预算展示与补齐门禁。包含窄屏、导航、草稿生命周期及条款回归。 |
| `node scripts/check-webui.mjs` | 源码与嵌入产物一致。 |
| `node --test scripts/check-webui.test.mjs` | 2 项产物校验工具测试通过。 |
| `go vet -tags=no_easytier ./internal/cacheproxy ./internal/console ./cmd/still-wanna-dance` | 通过。 |

首轮沙箱内 Go 测试被临时视频发布和配置文件原子重命名的权限限制阻断，工具测试被临时目录链接权限限制阻断；均在正常权限下重跑通过。首次沙箱浏览器运行未成功完成并已中止，之后使用无界面 Edge 与本地模拟 API 完成上述 27 项。自动测试不接入游戏，不修改 hosts。

已查看自动测试生成的 390 像素宽首次引导存储页与深色设置页截图：两个入口使用相同选项、容量标签及说明，没有横向溢出。截图及测试日志仅在忽略目录 `artifacts/` 中用于开发核对，不作为实机验收证据。

## 2026-10-18 默认零预算

桌面新配置、命令行默认配置及引导「恢复本页默认」统一为 `cacheUnlimited=false`、`maxCacheBytes=0`。已有配置的明确值继续保留；缓存复用测试和实网冷/热缓存验收脚本显式选择不限，保持原场景的保留条件。

首轮 `go test -tags=no_easytier ./internal/cacheproxy ./internal/console ./cmd/still-wanna-dance -count=1 -timeout=180s` 中，缓存引擎与命令行包通过；控制台的覆盖率重算、导入清理、共享资源扫描和扫描快照 4 项因隐含依赖默认保留视频而失败。已在这些夹具中显式启用不限，继续验证其原有业务目的；控制台整包按相同参数重跑通过（58.553 秒）。默认配置与零预算回收仍由首次启动持久化和零容量测试覆盖，已有 0/正数/不限保存与重新加载回归通过。

`pnpm --dir webui build`、`node scripts/check-webui.mjs` 与相关三包的 `go vet -tags=no_easytier` 通过；`node --check scripts/acceptance.mjs` 通过，未执行公网验收脚本。Edge 的 `e2e/cache-capacity.spec.ts` 和 `e2e/setup.spec.ts` 共 9 项通过，覆盖恢复零值、保存与刷新、已有不限设置、非法输入和引导提交。首次沙箱运行因 Edge 临时配置替换失败而未进入页面测试，正常权限重跑通过。浏览器使用内存 API，不代表真实进程重启或游戏体验。

## 下一轮实机验收

游戏专属步骤见[LA-047](../live-acceptance-backlog.md#零容量缓存与独立不限选项)：只补真实播放器重连/拖动与队列交接之间的保护风险，并复用 LA-040/041 的操作。设置/CLI、保存重启、精确回收和异常进程清理由 agent 在开发环节验证；已有上表结果按范围复用，不重复塞进游戏轮次。分流不表示未覆盖部分通过；本次未启动游戏验收。

## 2026-10-19 存储术语统一

设置和引导共用“视频存储预算（GiB）”，说明以“本地视频持久化存储预算”开头；完整视频列表、目录选择、删除/预算提示、队列预加载、命中统计、帮助、README、命令行说明和现行设计同步采用新术语。HIT 仅称完整本地视频命中，运行期块复用仍按原请求分类记录。配置键、接口路径、日志事件名、文件布局和预算行为保持不变，历史基准与验证证据保留原文。条款仅统一相关用词，版本更新为 `2026-10-19.1`，原版本/摘要门禁会要求重新确认。

验证结果：

- `pnpm --dir webui build`、`pnpm --dir webui check` 通过，嵌入资源和构建清单已更新。
- 经 `scripts/with-test-temp.ps1` 运行 Edge 内存夹具全套 45 项通过；两个预算入口均检查标签和全称说明，含零值/不限、草稿/保存、导航、删除、帮助、320/390px 与深浅主题。已查看引导存储、设置和本地视频页截图，文案换行正常，无横向溢出。英文错误夹具同步后，存储故障定向 1 项再次通过。
- 经同一入口运行 Go 存储、预算、配置、引导和条款定向回归通过；最终英文错误提示同步后，`internal/cacheproxy`、`internal/legal`、`cmd/still-wanna-dance` 整包通过（42.031 / 0.097 / 1.756 秒）。错误链及原存储行为未变。
- 首轮沙箱内 Edge 在启动阶段因进程/命名管道限制失败，Go 配置原子替换报 `renameat ... The handle is invalid` 等错误；随后在普通权限环境用同一隔离入口通过，不修改产品逻辑绕过环境限制。浏览器首轮错误上下文保留于忽略目录 `artifacts/storage-terminology-browser-sandbox/`，成功日志为 `artifacts/storage-terminology-{browser,go,go-final,browser-final}.log`。

本轮只验证术语与既有行为回归，未启动游戏、修改 hosts 或操作用户库；未新增 VRC 待验项。


## 2026-10-20 三选一与显式保存

设置与引导统一按「不保留 / 限制为 / 不限制」排列，默认和恢复默认均选择「不保留」。只有「限制为」启用正数容量输入，模式切换和保存重载保留有效容量；「不保留」界面显示运行时预算为 0。说明沿用“本地视频持久化存储预算”“本地视频”“视频块缓存”的术语边界。

配置格式升级为版本 4，以 `videoStorageMode=none/limited/unlimited` 明确保存选择，`maxCacheBytes` 保留容量输入，移除原 `cacheUnlimited` 配置字段。保存、引导应用、状态快照和待重启比较均使用明确模式；创建引擎时才把 none 映射为有限零预算，其余模式沿用既有预算机制。版本 1～3 不兼容、不迁移；拒绝旧格式时不改写原文件。

验证与边界：

- `pnpm --dir webui build`、`pnpm --dir webui check` 通过，嵌入资源及构建清单已更新；相关 Go 包的 `go vet -tags=no_easytier` 通过，验收脚本的 Node/PowerShell 语法检查通过。
- 经 `scripts/with-test-temp.ps1` 运行 Edge 内存夹具的预算、设置、引导、恢复和文案回归，27 项通过。覆盖三个选择保存/刷新、记住容量、不保留覆盖正数草稿、默认恢复、非法值、输入小数时不切换模式、active 设置待重启和补齐门禁。已查看设置控件及 390px 引导截图，三项顺序、标签和说明正常，无横向溢出。
- 经相同隔离入口运行 `go test -tags=no_easytier ./internal/console ./internal/apppaths -count=1 -timeout=180s`；`apppaths` 通过，修正下述 golden 样例后 `console` 整包复跑通过（59.080 秒）。覆盖真实配置文件中的三项选择、容量保留、重新构造应用后的引擎回收、不保留覆盖正数预算、引导保存、非法模式/有限零预算拒绝，以及旧版本拒绝且不改写。最终成功日志为 `artifacts/video-storage-mode-go-final.log`。
- 复核时为 E2E 夹具的 `settings` 显式标注 `Settings`，独立对 `e2e/api.ts` 与 `e2e/cache-capacity.spec.ts` 执行 TypeScript 严格检查通过；默认 `webui check` 仍仅检查 `src` 和嵌入产物。升级步骤改为引用统一存储契约，避免遗漏旧配置版本。
- 沙箱内 Go 配置原子替换仍报 `The handle is invalid`，Edge 27 项在页面操作前失败且未正常结束，已中止并保留原输出；普通权限环境使用相同隔离入口完成浏览器验证。Go 首轮普通权限整包仅有旧版本 golden 配置样例失败，已将样例及读取入口更新为版本 4，保留失败日志。

日志与截图留在忽略目录：`artifacts/video-storage-mode-{browser,go}.log` 为沙箱尝试；`artifacts/video-storage-mode-browser-normal.log`、`artifacts/video-storage-mode-browser-normal/` 为通过的浏览器日志和截图；`artifacts/video-storage-mode-go-normal.log` 保留旧 golden 样例失败。

本轮未运行公网验收、真实进程 UI 重启或 VRC 游戏；浏览器结果限于内存 API，Go 文件/引擎检查使用隔离目录。没有新增游戏专属风险，不追加 VRC 待验项。

## 2026-10-20 本地存储目录的收起与禁用

设置和引导复用同一目录区域组件，以 `videoStorageMode` 控制默认展开和可编辑状态，交互规则见[Web UI 内容约定](../webui-content.md)。视频存储预算先于目录展示；选择「不保留」时目录默认收起，可展开查看灰显的路径和修改控件；切回「限制为」或「不限制」时恢复编辑，保留原路径及自定义位置选择。目录检查错误保持可见，选择文件夹期间暂时禁用预算选项。沿用现有配置格式及保存流程，不增加旧配置迁移或兼容逻辑。

- `pnpm --dir webui build`、`pnpm --dir webui check` 和 `git diff --check` 通过，嵌入资源及构建清单已更新；修改的预算浏览器测试及 API 夹具单独通过 TypeScript 严格检查。
- 经 `scripts/with-test-temp.ps1` 在普通权限环境运行 Edge 的预算、设置、引导、恢复及文案回归，29 项通过。新增检查覆盖键盘展开、灰显禁用、默认目录直接继续、三组选项切换保留自定义路径、保存重载和引导提交；原有预算校验、恢复默认、导航、320px 强制颜色及 390px 布局继续通过。
- 已查看深色设置目录、390px 引导目录展开禁用及默认收起截图，路径与操作状态清晰，无横向溢出。日志为忽略目录 `artifacts/storage-directory-browser.log`，截图在 `artifacts/storage-directory-browser/`。

本次只调整前端交互，浏览器测试使用内存 API；真实配置持久化与运行预算映射复用上一节验证，不据此宣称新增实际文件选择器或进程重启验收。未启动游戏或修改 hosts，无新增 VRC 待验项。


## 2026-10-20 激进的缓存回收策略

本节取代此前固定“仅零预算积极回收”的现行约束；前文的历史测试和失败记录保留。初版设置与使用引导共用“激进的缓存回收策略”：自动（默认）／开启／关闭。显式保存 aggressiveCacheReclamation=auto/on/off；auto 根据有效视频存储预算计算，只有“不保留”开启，“限制为”（包括预算已满）和“不限制”关闭，记住的容量输入不改变结果。手动选择不随预算变化，初版恢复引导本页默认回到 auto；引导入口和恢复默认行为已由下方“使用引导移除回收策略选项”替代。沿用正式术语与用户指定说明。

配置格式升级为 5，版本 1～4 明确拒绝，不增加迁移兼容；前文版本 4 是当次历史格式。CLI 通过 -aggressive-cache-reclamation 使用同一策略。回收、防止无期限反复下载、有效需求缺块重取及完成判断共用策略，保护窗口与必要引用保持原规则。合并快照前正常回收导致块不完整时返回未保留并释放预留，不误报存储故障；关闭回收且不保留视频时，块仅在本次运行内复用，退出仍清理。

验证结果：

- 通过 scripts/with-test-temp.ps1 运行 internal/cacheproxy、internal/console、internal/apppaths 和 cmd/... 整包测试；覆盖三种策略与三种预算组合、GET/队列缺块重取、正预算和不限时激进回收不反复补齐、不误记存储故障，以及零预算关闭回收后的跨会话复用和退出清理。配置、设置 API、引导应用、保存/运行快照及重新构造应用的重启边界通过；旧格式拒绝且原文件不被改写。
- 使用仓库已有独立 GCC 工具链执行 cacheproxy 的回收、重取与发布定向 go test -race 通过；相关 Go 包的 go vet、前端 build/check、浏览器测试源码严格类型检查、验收脚本语法检查及 git diff --check 通过。
- Edge 内存夹具的 cache-reclamation、cache-capacity、setup 共 13 项通过。覆盖自动联动、手动选择保留、放弃草稿、保存重载、引导恢复默认与最终核对载荷。已查看 390px 深色设置和浅色引导截图，无横向溢出。
- 首轮沙箱 Go 配置写入遇到已有 Windows 原子替换限制（The handle is invalid），Edge 在页面操作前失败且未正常结束，已中止；同一隔离入口在沙箱外通过。原始失败保留在 artifacts/cache-reclamation-{go-initial,webui-initial}.log，成功证据为同目录的 go、webui、race、vet 日志。
- 独立新构建实际启动了隔离控制台，禁用网关和队列自动启动，仅复制既有条款凭据。凭据早于当前条款版本，/api/status 正确返回 428，未执行同意操作，因此真实浏览器保存与实际进程重启未验证。测试实例通过退出接口关闭，result.json 记录 cleanup=true；原配置未修改。失败证据在忽略目录 artifacts/cache-reclamation-real-browser.log 及 artifacts/cache-reclamation/runtime-*/。Go 的实际文件与服务集成结果不外推为此项浏览器通过。

未启动 VRC、修改 hosts 或操作用户视频库；保护规则未改变，本次不新增 VRC 专属待验场景。当前缺口仅为使用有效的现行条款凭据补做上述隔离进程浏览器检查。

### 使用引导移除回收策略选项（2026-10-20）

使用引导的存储页只展示视频存储预算、目录及导入，移除回收策略控件与说明；设置页保留原有选项和行为。引导沿用已保存策略，切换预算、恢复本页默认及最终应用均不覆盖它；新配置继续使用默认 auto。核对页不再常驻展示回收策略，仅在此前保存的策略尚未生效时展示差异及“此前已保存”提示。此规则替代上方初版的引导入口与恢复 auto 行为，历史验证结果保留。

前端 build/check、浏览器测试源码严格类型检查及差异格式检查通过。通过隔离临时目录运行 Edge 的 cache-reclamation、cache-capacity、setup 共 16 项内存 API 场景，全部通过；覆盖 auto/on/off 的隐藏字段保留、恢复默认、预算切换、前后导航、校验与应用载荷、待生效策略核对，以及原设置页交互。已查看 390px 浅色引导和深色设置截图，无横向溢出。首轮沙箱内 Edge 启动时报管道关闭，未进入页面断言且测试进程未正常结束，已中止；沙箱外同组检查通过。原始日志为 `artifacts/setup-reclamation-removal-browser-initial.log`、`artifacts/setup-reclamation-removal-browser.log`，错误上下文与截图在 `artifacts/setup-reclamation-removal/`。

本轮仅调整前端入口、草稿恢复与核对展示，不改后端配置或回收机制；内存 API 结果不外推为真实进程保存／重启验证，上方既有环境缺口保持原状态。本次无需新增 VRC 待验场景。
