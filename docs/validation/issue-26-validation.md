# #26 Portable 交付收口与覆盖升级

本文汇总 Portable 交付与覆盖升级证据。现行流程以[打包说明](../packaging.md)为准，可靠性与交付结果分别由 [#5](https://github.com/SW26010/still-wanna-dance/issues/5) 和 [#26](https://github.com/SW26010/still-wanna-dance/issues/26) 汇总；本地开发包及合成数据检查不等于正式公开 Release 已完成。

## 2026-10-31 beta.3 发布前修复

从指定提交 `9cc27069bce5ada5db7431a4823d671dfcb614a5` 创建 `codex/release-v1.0.0-beta.3`。基线已包含 beta.1/beta.2 的自动发布、冷缓存元数据收集、升级夹具和发布说明修复。最初预检只修改产物检查；后续完整 CI 揭示测试同步问题与 AnyTLS 依赖竞态，处理范围见下文。

原产物检查对全部历史 blob 一次执行 `git cat-file --batch`，在当前历史和独立 256 MiB 历史夹具中均复现 `spawnSync git ENOBUFS`。现在先查询对象大小，再按 16 MiB 目标分批读取；大于该目标的单个 blob 独立读取，仍完整检查。新增回归保留八版各 32 MiB 的历史，其中首版含随后被替换的二进制内容；修复前溢出，修复后仍准确拒绝首版，不缩短历史范围、不放宽内容规则。

扫描恢复后确认仅有八版 `app.js` 缺少精确登记，分别来自 `498d2544`、`9056d4a3`、`558c6107`、`fedf4650`、`46d5345a`、`46bd4960`、`f35435e7` 和 `9cc27069`。逐版核对 Git blob 字节与对应提交的 `webui/build-manifest.json` SHA256 后补齐，未改动产物本身。

本机预检：产物策略回归 **6/6**、全部 Node 工具回归 **52/52**、前端类型与产物一致性检查通过；基线完整历史扫描 **6,428 blobs、0 errors**，35 条已有体积提醒保留。首轮工具回归的两项失败来自沙箱符号链接限制和 PATH 中的 Go 1.27.1；切换已安装的 Go 1.27.3、正常用户权限后通过。当前工作区前端依赖需按固定锁文件重新安装，完成后检查通过。原失败与复核日志保存在忽略目录 `artifacts/release-v1.0.0-beta.3/`。

这些结果仅证明发布前修复与本机预检；最终交付仍以该分支的完整 `check`、候选到正式包覆盖升级，以及独立 release job 的公开回下载与冷缓存重建结果为准。

### 首轮 CI 暴露的真实时钟断言

发布前修复提交 `fb109c6786409a616d642bc6883e891dae1ae1d8` 的[产物检查](https://github.com/SW26010/still-wanna-dance/actions/runs/41190551073)通过，完整扫描 6,433 blobs、零错误。随后 [check #27](https://github.com/SW26010/still-wanna-dance/actions/runs/41190632666) 的 Linux Go 测试在 `TestCatalogInitializationFailureSchedulesRetryAfterConsent` 和 `TestNetworkConnectivityReusesResponsesToInitializeStorage` 失败；第一项得到 `5.00000039s`，却要求与 5 秒完全相等。该轮不能记为发布成功。

两项测试使用真实时钟，完成时刻和调度截止由两次 `time.Now()` 取得；同锁下状态已经完成，但取时之间仍经过时间。Windows 原测试重复 100 次通过；在两次取时之间临时加入 1 毫秒间隔后，两项同样失败，排除了仅靠重跑恢复绿灯的处理。精确周期已有 `network_connectivity_schedule_test.go` 的虚拟时间依据；现在这两项也复用 `synctest.Test`、`synctest.Wait` 和 `automaticTestConsole` 的通道隔离，保留原 5 秒/5 分钟等值断言及全部数据检查，不修改生产调度或增大容差。

修复后的两项及相关自动调度回归重复 100 次通过，完整 console 包通过。验证断言保留时，等待型探针在虚拟时钟的锁内阻塞，已停止该隔离测试；改用不阻塞的 1 毫秒完成时刻偏移，两项仍按预期失败，移除探针后再次通过。所有生产文件已恢复原字节，诊断探针不入库。首轮远端日志、探针失败/中断和修复结果保存在同一忽略目录的 `linux-attempt1.log`、`console-clock-*.log` 与 `console-full-fixed.log`，不覆盖历史失败。

### 第二轮 CI：通知定位、发现夹具与 AnyTLS 竞态

提交 `2760601041adc9d58cab8ac4dad84117e86ab99a` 的 [check #28](https://github.com/SW26010/still-wanna-dance/actions/runs/41191952799)未发布：Windows 浏览器 368 通过、1 失败；Linux 普通 Go 检查通过，race 在 desktop 与 AnyTLS 集成路径失败。收齐两平台日志后成组处理；没有放宽断言、跳过协议、关闭 race 或把重跑成功当作修复。

- `monitor-details` 的全页文本断言同时命中 Toast 标题与 Fluent 的 `aria-live` 朗读副本。本地原用例重复 20 次通过，固定时钟推进到排队的第二条朗读消息后复现同一 strict-mode 错误；只改为精确通知 `listitem` 定位，保持该故障窗口时通过。一起修正 hosts 通知中的同类定位，移除临时探针后相关 14 项浏览器回归通过。
- VRChat 退出发现夹具在虚拟时间推进后读写共享发现结果；`synctest.Wait` 不能替代相反方向的同步。本机 race 复现原三项失败；发现结果、错误与调用计数统一由夹具互斥保护。首版遗漏计数读取的失败也保留，补齐后四条退出路径重复 100 次 race 通过，生产进程等待逻辑未修改。
- Mihomo `v1.19.32` 的 AnyTLS Stream 由接收 goroutine 写 `dieErr`，调用者同时读写；当前上游文件仍有同一问题。原集成用例在本机重复三次没有触发，新增原生 Stream 并发读、写、关闭回归后稳定报 race。修改文件只原子发布、读取该错误，最小回归重复 10 次 race 通过；全部 console、desktop、mihomo 包 race 通过（分别 109.765、5.112、37.021 秒）。

用户选择在本仓库维护最小依赖补丁并验证完整交付链。最初 Go overlay 探针被工具链明确拒绝，未改写模块缓存；最终使用隔离副本和临时 `-modfile`，由构建、测试、vet、覆盖升级和源码重建共用入口。原版本、模块校验和、原文件及补丁哈希均核对；产物显式保留补丁身份，不将修改版冒充原模块。维护和移除规则见[依赖补丁](../../patches/README.md)。

本地发布工具 **55/55**、actionlint、实际桌面构建、实际 EXE 的补丁身份及 **114 个**依赖许可校验通过；原模块文件 SHA256 未变化。第二轮远端日志、Windows trace 附件、受控失败、overlay 拒绝和成功复核分别保留在 `artifacts/release-v1.0.0-beta.3/` 的 `*-attempt2.log`、`beta3-attempt2-webui-failure.zip`、`monitor-announcement-*.log`、`desktop-race-*.log`、`anytls-*.log`、`affected-race-patched.log` 和 `tools-patched.log`。这些是本地修复证据，最终公开交付结果另记。

### 第三轮 CI 的 Windows 深路径修复

[check #29](https://github.com/SW26010/still-wanna-dance/actions/runs/41194848967) 在 `2fc5061edf0a910030157cd3fd20333975769ec0` 上完成两平台各 369 项浏览器回归、Go 测试、vet 和编译；Linux 完整 race 与格式检查通过。Windows 候选包生成成功，但正式流程的嵌套干净检出中，五个较深的 Mihomo 包无法启动 `vet.exe`，报告 `The directory name is invalid`。发布 job 跳过，该轮没有创建公开 tag 或 Release。

本机新增真实依赖 vet 回归，将检出根目录延长到 136 字符，原实现稳定复现相同失败。补丁副本原先位于检出目录内，叠加 SHA256 目录名和依赖包路径后超出 Windows 子进程工作目录限制；改为进程临时目录下的独占 `swd-go-*`，与正式检出/源码解包目录并列，并保留完整补丁哈希。替换路径按 Go 字符串格式引用，二进制元数据按制表符解析，支持含空格的目录。补丁内容、原模块校验和、隔离与清理边界保持原规则。

同一深目录回归修复后通过；源码交付相关 12 项及全部 Node 工具 **56/56** 通过。日志保留于 `artifacts/release-v1.0.0-beta.3/` 的 `windows-attempt3.log`、`linux-attempt3.log`、`patch-path-red.log`、`patch-path-green.log`、`tools-path-fixed.log`。这些结果不替代完整本地正式打包及最终远端公开交付。

### 本地完整流程的冷缓存查询修复

`9bdddb0f842df5a990de75c0cba34eccc5ce06fc` 的本地候选 ZIP、114 个依赖许可校验及正式干净检出检查通过，约 812 MB 的完整源码归档生成并校验通过；正式 EXE 的冷缓存编译准备阶段随后失败。补丁入口的 `go list -m all` 加载整个模块图，索取未选中的 `github.com/stretchr/testify@v1.8.1.mod`。现行源码包只交付选定版本元数据和实际编译模块的 ZIP；这次入口新增的全图读取超出了生产构建需求，该次完整流程不记为成功。

新增回归使用真实 Mihomo ZIP、全新模块缓存和仅含该模块的文件代理，原入口会读取无关模块而失败。修复先只读解析 `go.mod`，仅对实际需要补丁的模块查询所选版本/替换，再维持原始模块、源文件和补丁哈希检查。相同回归通过；使用与正式源码包相同的 **225 个选定版本、114 个完整 ZIP** 构造隔离代理，真实桌面入口在全新模块/编译缓存、仅本地 `GOPROXY` 下编译成功，EXE 的 114 个依赖及补丁身份通过。全部 Node 工具 **57/57** 通过。

证据位于 `artifacts/release-v1.0.0-beta.3/`：`local-candidate.log`、失败的 `local-formal.log`、`patch-offline-red.log`、`patch-offline-green.log`、`offline-product-green.log` 和 `tools-offline-fixed.log`。诊断脚本首轮遗漏重建身份的 revision，在编译前被既有校验拒绝；补齐当前提交后才取得上述实际编译结果，该首轮日志也保留。实际完整正式包与公开交付仍须按最终提交复核。

### 本地源码重建与嵌套升级驱动补验

`db37ce9642577e6a20688226c8727af5e936dc89` 的第二次完整本地流程已通过干净检出检查、完整源码生成/解包校验、正式 EXE 构建及身份/许可核对、全新缓存的前端与 zashboard 离线重建、Go 独立重建、模块身份比较，以及实际 ZIP 单包和覆盖冒烟。最后的升级驱动中，三布局数据断言均通过，但 Go 同时对 Mihomo 包执行的 vet 报路径过长，整体命令仍失败，不把该次流程写成一次完整成功。

共同原因是干净检出中的 `with-test-temp.ps1` 再次按自身路径创建 `artifacts/tmp`，使已缩短的补丁目录重新落入更深的临时根。现在最外层包装器临时传递共同根，子包装器核对其与调用者临时目录的关系后创建并列的独占目录；环境恢复、退出码和只清理自身目录的规则不变。复制包装器到临时检出的回归修复前失败、修复后通过；全部发布工具 **58/58** 通过。

保留同一对实际 EXE，在重新导出的嵌套源码检出中仅应用包装器修复，重放升级驱动；vet 和默认存储、外置库、自定义配置 **3/3** 均通过，退出码为 0。起点为 `ci-beta3-local-1`（`9bdddb0f`，EXE SHA256 `1a46447cd5b3c7fe192948fcef54757ff8fdfbc05e80d48b761c0a10b7da4205`），终点为上述正式候选（EXE SHA256 `35318a8b2487f57f8b8b1b462c0463fb3f814b47e298c6c33b22136fe273a67e`）；不外推为公开 Beta 2 升级验证。复用已通过的源码重建和 ZIP 冒烟证据，最终提交仍由远端完整流程重新生成、校验并公开附件。

原始证据：`artifacts/release-v1.0.0-beta.3/local-formal-fixed.log`、`nested-temp-red.log`、`nested-temp-green.log`、`nested-upgrade-fixed.log`、`tools-nested-fixed.log`；失败升级的完整日志和实际 EXE 保留于 `artifacts/portable-upgrade/dab3d9c3a78d427f923e7e37947f4aec/`。本节不声称本地产物已经公开。

启动 [check #30](https://github.com/SW26010/still-wanna-dance/actions/runs/41213518252) 后，追加实际嵌套检出的完整工具套件检查，发现三项旧测试仍按脚本自身目录计算临时根。本机复现 **55 通过、3 失败**，主动取消该轮 CI，未等待其后续打包，未创建 Release。测试现在按同一继承契约确定预期根，保留目录隔离、环境恢复及清理断言；相同嵌套检出中的全部 **58/58** 通过。失败与成功日志分别为 `nested-tools-red.log`、`nested-tools-green.log`；这次只修正测试预期，没有再次改动产品或包装器行为。

## 2026-10-30 Beta 候选与自动发布

`dc8e922920ce46a6b02df04f32a88992e9e5d5fa` 的完整 [check #22](https://github.com/SW26010/still-wanna-dance/actions/runs/40913936608)在 Windows/Linux 全部通过。本地 `v1.0.0-beta.1` 候选完成正式 EXE、完整对应源码、空缓存离线重建及单包冒烟；此阶段仍为未公开候选，不将本地通过记为公开下载成功。

首次使用旧 `v0.0.0-ci-validation.20261029` 作覆盖起点时，其首次配置为 v7，被当前 v8 检查拒绝；没有增加旧配置迁移或放宽断言。改用同提交的 `ci-22` 后，ZIP 覆盖冒烟通过，真实 EXE 的三布局持久化检查均报 `upgrade changed storage.sqlite`。

定向复现用同一对 EXE 保留合成 SQLite 前后快照：仅 `catalog_state` 从 0 行变为 1 行、`media` 从 0 变为 10,446 行、`songs` 与 `song_media` 各从 0 变为 10,471 行，其余表未变化。通用 `newConsoleWithWorkForTest` 有意省略发行预置曲库，而真实 `New` 在启动时写入预置数据；升级夹具冻结的是未完成产品初始化的数据库。现在在冻结哈希前调用同一 `ApplyBundledMediaSeed`，仍逐文件检查数据库、配置、凭据回执、快照、媒体与未知文件，不忽略数据库或改成较弱断言。

同一对 `ci-22` / Beta 实际 EXE 重跑完整 ZIP 覆盖与持久化检查，默认存储、外置库、自定义配置 **3/3 通过**。诊断代码已移除，失败、合成快照及修复后结果保留在忽略目录 `artifacts/release-v1.0.0-beta.1/` 的 `upgrade.log`、`upgrade-diagnostic/` 和 `upgrade-fixed.log`；没有产品行为修改。

本轮将完整检查、候选覆盖验证、正式打包、Release 草稿附件上传及公开回下载校验接入 `check`，现行用法见[打包说明](../packaging.md)。最终公开交付以发布分支的新提交及该工作流的 `release` job 成功为准；上述本地候选和旧提交 CI 不代替新工作流的运行结果。

首次自动发布运行 [check #23](https://github.com/SW26010/still-wanna-dance/actions/runs/40921482572)在两平台浏览器/Node 检查通过后主动取消：核对真实 GitHub API 时发现，commit 查询对不存在的 tag 返回 422，原发布预检只接受 404。现改为先用 Git ref 接口判断 tag 是否存在，再核对已有 tag 的提交；没有放宽提交身份检查，也没有将该取消运行记为完整通过。

[check #24](https://github.com/SW26010/still-wanna-dance/actions/runs/40924202627)在 `152be7451c63a75047ec825c9760f0da8645082f` 完成两平台检查、正式打包、源码重建和三布局覆盖升级，但独立发布 job 的公开下载重建失败，工作流已将 Release 恢复为草稿。此运行不能计为发布成功；该运行结束时 tag 指向该提交，五个原始附件和 Actions 日志已保存，后续清理与成功发布见下文。

用该运行的实际源码附件复现：包内容存储可复用全部 149 个安装包，但清空 pnpm 元数据缓存后，169 条锁文件供应链校验报 `ERR_PNPM_NO_OFFLINE_META`。构建 job 的热缓存掩盖了缺失材料。仅复制默认 fetch 的缓存仍失败，因为在线校验可能使用未保留的发布时间证明。修复通过独立缓存和 pnpm `no-downgrade` 检查收集完整元数据，归档元数据输入而非校验结果，并在重建时明确使用包内缓存；同时保留 pnpm 原始错误输出。

修复验证：缺失元数据回归先失败后通过，全部 Node 工具测试 **47/47** 通过，actionlint 通过；实际附件源码调用修复后的收集与重建入口，通过全新缓存离线安装、类型检查及前端产物字节比较。另一次仅复制元数据、不复制 `lockfile-verified.jsonl` 的独立安装，重新校验 **169 条**供应链记录、复用 **149 包**且下载 **0 包**。证据位于忽略目录 `artifacts/release-v1.0.0-beta.1/ci-24-evidence/` 的 `offline-isolation-01.log`、`metadata-regression-before.log`、`metadata-regression-after.log`、`trust-metadata-offline-native.log`、`frontend-fixed-repro.log` 和 `tool-tests-after.log`。这些证明修复后的前端离线路径，不代表修复版已完成远程正式发布。

### v1.0.0-beta.1 公开交付完成

经用户确认，清理本次失败草稿、五个附件及旧 tag，并从非 main 分支 `codex/release-v1.0.0-beta.1` 的修复提交 `ad0559c4b79e36c73069be02dc166dccf677b50c` 重新运行 [check #25](https://github.com/SW26010/still-wanna-dance/actions/runs/40935226917)。2026-10-30 03:28（北京时间），Windows、Linux 与独立 release job 全部成功；[v1.0.0-beta.1](https://github.com/SW26010/still-wanna-dance/releases/tag/v1.0.0-beta.1) 为公开 Pre-release，tag 指向该修复提交，未修改 main。失败附件继续保留于本地 `ci-24-evidence/delivery/` 及原 Actions 产物，不将失败运行改写为成功。

两平台各 **345 项浏览器测试通过**；Windows 工具回归 **47/47**，Linux **39 通过、8 项 Windows 专属测试跳过**，无失败。Go 测试、vet、编译与 Linux race/gofmt 通过。最终正式包完成源码独立重建、单包冒烟，以及 `ci-25` → Beta 的默认存储、外置库、自定义配置 **3/3** 覆盖升级，持久化数据检查通过。

Actions 自动上传五个正式附件，核对 GitHub 的大小与 SHA256 后发布。全新 release runner 使用无凭据公共 URL 下载 Portable 与源码包，验证哈希、源码/EXE 身份并独立重建；最终输出 `verified=true`，提交为 `ad0559c4b79e36c73069be02dc166dccf677b50c`，发行 EXE SHA256 为 `dc23a009a4de7c2b7eb51183e892041e1c7e0a67c88321b48d411325862a6cd4`。公开发行清单与两份 SHA256 文件也已下载核对。未手动上传本机附件；仍不承诺 EXE 逐字节可复现。

| 正式附件 | 字节数 | SHA256 |
| --- | ---: | --- |
| Windows amd64 Portable ZIP | 20,895,382 | `b3b1d52c11d9a18c8d55682e8d226366f964ecc825a4bf4967fe9e8b6871b20c` |
| 完整对应源码 tar.gz | 399,162,469 | `c14aa69cb1fbd07b5a575f8005fd368920e9500f8dcf03faca564e859e92374d` |

本次回执与原始日志位于忽略目录 `artifacts/release-v1.0.0-beta.1/ci-25-evidence/`：`windows.log`、`linux.log`、`release.log`、`published-release.json`、`public-rebuild-result.json` 及 `logs/portable-upgrade/e8c6c4cd4d2b4e81b5d13fa1da618756/persisted-data.log`。本节证明该版本的公开交付，不替代实机台账中不同范围的历史结果。

### v1.0.0-beta.2 公开交付完成

从发布前最新 `origin/main` 提交 `5fccb01a641194122f871643b88ac88d1ec38b0b` 创建非 main 分支 `codex/release-v1.0.0-beta.2`，合入 Beta 1 已验证的自动发布、离线元数据收集与升级夹具修复，保留 main 后续 14 个提交。正式发行提交为 `a7dbeca4ae93454737d9195b9b8a278bd73bb8fa`；main 未修改，发布前 48 项工具回归、前端资源一致性及 actionlint 通过。

[check #26](https://github.com/SW26010/still-wanna-dance/actions/runs/40942588702) 首次运行于 2026-10-30 04:16（北京时间）完成，Windows、Linux 和独立 release job 全部成功，无失败重跑。[v1.0.0-beta.2](https://github.com/SW26010/still-wanna-dance/releases/tag/v1.0.0-beta.2) 已公开且标记为 Pre-release，tag 指向上述发行提交；附件全部由 Actions 自动上传。

两平台各 **355 项浏览器测试通过**；Windows 工具回归 **48/48**，Linux **40 通过、8 项 Windows 专属测试跳过**。Go 测试、vet、编译及 Linux race/gofmt 通过。正式包的源码独立重建、Portable 冒烟及 `ci-26` → Beta 2 的默认存储、外置库、自定义配置 **3/3** 覆盖升级通过；此处升级起点是同提交候选包，不将结果扩大为已验证公开 Beta 1 → Beta 2。

独立 release runner 从无凭据公共 URL 下载 Portable 和完整源码，核对哈希及源码/EXE 身份，使用全新缓存重建成功，输出 `verified=true`。发行 EXE SHA256 为 `7256ef32cd6a59295649e33ff051ed794325a5dec5862c56dcc760cc7576cb98`。五个附件的大小与 GitHub SHA256 一致；公开发行清单及两份校验文件也已下载核对。不承诺 EXE 逐字节可复现。

| 正式附件 | 字节数 | SHA256 |
| --- | ---: | --- |
| Windows amd64 Portable ZIP | 20,905,848 | `71d986dcb912cc3798b903536d2bdb7eefc6009a80e59f8356173c4da58a085e` |
| 完整对应源码 tar.gz | 399,466,350 | `dcd730e2d7f89fe69c035ca4333763d99ff7f83512d6ecb56ecff5fa4eefbedd` |

原始证据保留在忽略目录 `artifacts/release-v1.0.0-beta.2/`：两平台及 release 日志、`build-manifest.json`、`published-release.json`、`public-rebuild-result.json`、公开校验文件和 `logs/portable-upgrade/87d211edd26f49b8ac59222829202134/persisted-data.log`。本节记录该版本的公开交付结果，不替代既有实机台账。

## 2026-10-26 发布前核对

核对基线为 `41e49e09feddf159c15b5680c6458b4cf3f3cb11`。本节引用已有验证，没有重新执行产品测试；下方 2026-10-19 的包身份、失败和通过记录保留当时适用范围。

- 四步引导已移除，首次使用保留条款确认，默认自动准备配置、数据库与视频目录。当前配置 v7、数据库 v5、快照 v2，路径和升级边界见[存储契约](../storage.md)；不承诺旧配置或旧目录自动迁移，已有受支持数据库可明确接入。
- [2026-10-26 目录与存储验证](../persistence-migration-plan.md#2026-10-26-目录归位与状态命名)已覆盖真实 Console/浏览器首次使用、真实开发 ZIP 的生成/覆盖/搬迁、多配置隔离，以及两份同布局 EXE 在默认、外置和自定义配置下的数据保留；后续复审补齐 SQLite 伴随文件保护、旧资料明确接入和数据库文件别名拒绝。原开发包不包含其后全部复审修复，最终候选仍须重新打包验证。
- [同日持久化证据](../persistence-migration-plan.md#本轮已取得的验证证据)已补真实跨卷发布/导入、v5 全数据备份还原和受限 NTFS 卷真实耗尽/恢复。它们不代表真实断电、跨用户解密或全部文件系统已验证。
- 该提交的 [repository-artifacts 检查通过](https://github.com/SW26010/still-wanna-dance/actions/runs/40291909228)，仅证明仓库产物策略；核对时该提交尚无完整 `check` 结果，也未生成并公开正式产品 Release。旧临时验证 tag 的完整对应源码重建继续作为工具链证据，不能代替当前候选。

剩余交付工作按[正式流程](../packaging.md)执行：选定提交的完整检查、正式 tag/打包、最终 ZIP 与同布局前一候选包的覆盖升级、对应源码独立重建，以及同 Release 附件公开下载验证。Windows 原生交互、条款变更确认与跨用户凭据边界统一在[非 VRC 验证跟进](../validation-followups.md)维护；只有游戏能回答的剩余场景见 [VRC 台账](../live-acceptance-backlog.md)。退出恢复按当前已保存的设置执行、默认开启，不再沿用旧的独立恢复按钮或“退出不会恢复 hosts”验收预期。

后续原生环境补证见[2026-10-26 Windows 原生验收](windows-native-validation.md)：真实开发 ZIP 首次启动、人工条款确认、单实例、文件选择器、托盘/UAC/退出恢复、同用户重启及搬迁已按实际范围通过，并修复过时内嵌条款与 UAC 取消乱码。最终修复构建只覆盖隔离 EXE，未把它记为正式 ZIP 交付或跨版本最终包验收；下方历史包结果仍按原范围保留。

## 2026-10-19 实现

- ZIP 固定 `SWD/`，仅包含 `SWD.exe` 和六个许可证/对应源码文件。`scripts/portable-files.json` 是精确白名单；校验和、额外文件、空数据目录及未知路径在运行 EXE 前检查。未实现按白名单删除安装目录里的未知文件。
- 构建身份通过 `internal/buildinfo` 共用于关于页、启动日志和 `-build-info`。版本/UTC 时间经共用 linker 参数注入，正式 EXE 的 revision/dirty 取真实 Go VCS；对应源码重建明确标为 rebuild。正式校验保留 EXE 模块/源码绑定，在 Windows 额外读取运行时身份和条款导出。
- 删除外置 README、构建信息 JSON 和派生条款副本，保留全部软件许可和对应源码说明。帮助修正零容量、手动网络检测及升级说明；首次引导提供新窗口帮助，未确认条款也可只读访问，业务门禁继续生效。
- 增加两个 ZIP 的覆盖启动入口，以及实际 EXE 的持久化集成检查。开发和正式打包均接受可选 `-PreviousZip`；未提供时不声称已验证跨版本。

## 2026-10-19 已执行

- 两次完整打包检查中的 `go test -tags=no_easytier ./cmd/... ./internal/...` 和 `go vet` 通过。构建身份、帮助门禁、条款、同用户凭据和内部/外部路径搬迁定向回归通过。
- 全部 Node 工具测试 **35/35** 通过，包括真实小型 EXE 的正式身份、无 Git 的对应源码重建、错误身份拒绝、ZIP 白名单/校验和拒绝及失败证据保留。源码夹具测试不替代完整产品的正式源码交付。
- 前端构建与一致性检查通过；Edge 文档/引导 **10/10** 通过，包含帮助新窗口不触发同意或业务读取、390/1280px 浅深主题及焦点。另让 Edge 连接真实 `dev-issue26-b` EXE，无 API 模拟，核对首次引导、帮助、关于及始终返回 428 的业务门禁；390px 截图已检查。探测进程已退出。
- 最终扩展至全部 Edge 回归 **44/44** 通过（`artifacts/issue26/browser-all-final.log`），并再次通过前端一致性检查。历史文案快照保持原样，测试明确映射有意修改的说明，继续检查段落中的其它内容；另断言帮助页仅要求覆盖 EXE 和 licenses，且不再引用已移除的说明文件。
- `dev-issue26-b` 真实 ZIP 冒烟通过：独立工作目录、无附加运行库、条款导出、确认前帮助、身份/锁、同包覆盖、整目录搬迁、自定义配置隔离及旧布局安全拒绝。
- `dev-issue26-b` → `dev-issue26-c` 真实 ZIP 同目录覆盖通过；随后实际两个 EXE 在默认存储、外置库和自定义配置 **3/3** 场景通过。合成样本包含当前配置、Windows DPAPI 测试凭据与条款回执、通道密钥、批次快照、SQLite、经正常导入发布的 MD5 媒体及未知文件。两个版本均通过真实 HTTP 读取凭据状态、快照与缓存；覆盖前后持久文件哈希一致，日志保留并追加，数据库可重新打开。

| 开发包 | 构建记录的提交 | dirty | ZIP SHA256 |
| --- | --- | --- | --- |
| dev-issue26-b | `855b1257e6c2dcdb5154d48f31efa88f081688ad` | true | `92b6f78fad886ca4036f6c4e606f0211b41a18cf0439c1357e01f0843add6252` |
| dev-issue26-c | `6d6361f3e1a2a6043b4c7f1395155f6d22580895` | true | `51552f6ef946f6caa0e9f8d344a3ca5cd2624f49590ae9e24ae7d778ee0e9dc0` |

开发包身份如实包含工作区改动，不能当作干净正式构建。原始日志、ZIP、真实浏览器记录保留在忽略目录 `artifacts/issue26/`；持久化集成结果位于 `artifacts/portable-upgrade/20f2f11a35734a53bec3c0f4f8344a68/`。实际 EXE SHA256 在 `persisted-data.log`，不提交合成 DPAPI 密文或本机路径材料。

## 完整本地正式流程复验

在独立本地克隆中，从干净提交 `a52af7a6037be7f4a0f6c6a04413b9867ebeaf49` 创建临时验证 tag `v0.0.0-issue26-validation.2`，完整运行正式打包并传入 `dev-issue26-c` 的 ZIP。该 tag 仅存在于忽略目录中的验证克隆；未在工作仓库创建发布 tag，也未推送或发布。后续 `6fbbc08` 及本记录只修改测试/文档，不改变该产物的应用和打包实现。

- 干净检出的 Node 工具测试、Go 测试及 vet 通过。完整源码清单含 **61,805 个文件、548,659,746 字节、225 条模块记录**；实际 EXE 含 110 个第三方 Go 模块，许可声明和对应源码绑定校验通过。EXE 类型为 formal、dirty=false，目标 windows/amd64，工具链 go1.27.1。
- 从最终源码附件独立重建通过：前端离线重建及嵌入资源一致性检查、全新 Go 缓存和仅包内 file GOPROXY 的 EXE 编译、模块身份比较均通过。不承诺二进制逐字节可复现；发行清单明确记录 `independentRebuild.verified=true`、`bitReproducible=false`。
- 最终 ZIP 的单包冒烟通过；`dev-issue26-c` → 临时验证正式包的真实同目录覆盖及默认存储、外置库、自定义配置持久化 **3/3** 通过。嵌套 PowerShell 的条款中文比较也在完整入口中通过。
- 输出目录 `artifacts/issue26/formal-output-2/` 包含 ZIP、对应源码、两个 SHA256、发行清单及说明；再次核对复制后的两个附件大小/哈希与清单一致。正式 EXE SHA256 为 `3d9551c941dd1a88d32bbe7752a10c9f32ba6d06a8f2e74289dd3d7892a8d71f`；独立重建 EXE 为 `e7c4b2bb748f8c78e24818835e7db5db70b47b8056f83f00dc624bdf6d005926`。

| 本地产物 | 字节数 | SHA256 |
| --- | ---: | --- |
| Portable ZIP | 19,816,205 | `e72f8d3891d08ce4406fe38ee6830a8598c8642189b14002ad296508094b148c` |
| 完整对应源码 tar.gz | 297,369,740 | `c4b6b9f9fed9717cc7f1774751b9ccf583fe0e7af452b80917b35af3bdd1476e` |

完整日志为 `artifacts/issue26/formal-pipeline-2.log`；持久化结果保留于 `artifacts/issue26/formal-repo/artifacts/portable-upgrade/b7a7aa1959eb481392fb50c51b950a4d/persisted-data.log`。包装器临时目录已清理，本轮启动的测试 EXE 已退出，持久证据保留。产物中的 Release URL 是按验证 tag 生成的待发布地址，**未验证且未声称这些公共地址可下载**。

## 历史失败与边界

- 首轮定向 Go 检查遇到 Windows 沙箱原子重命名 `The handle is invalid`；同入口在正常用户权限下通过，失败日志保留。首次编译遗漏仍被 panic 日志使用的 `runtime/debug` import，恢复后通过。
- 第一版 `dev-issue26-a` 冒烟暴露 GUI EXE 输出捕获方式不正确，改用显式管道；同时补齐确认前帮助路由及首次引导入口。该失败包不作为最终通过证据。
- 首次完整本地正式流程在隔离克隆的临时 tag 上通过了源码收集、正式 EXE 校验及独立源码重建，但嵌套 Node → PowerShell 的 ZIP 冒烟将中文条款按系统代码页读取，比较失败。冒烟脚本现显式使用 UTF-8 并恢复原编码；同样的嵌套调用回归通过。失败日志 `artifacts/issue26/formal-pipeline.log` 和回归日志 `artifacts/issue26/nested-smoke.log` 保留；这次失败不计为完整正式流程通过。
- 全套浏览器首轮 **42 通过 / 2 失败**：历史文案快照仍要求本轮已替换的帮助说明，另有既有提前准备时间下限从 1 改为 120 秒后未同步的预期。核对实现后仅更新测试预期，未为通过测试改变产品行为；最终 44 项通过。首轮日志及截图保留于 `artifacts/issue26/browser-all-before.log`、`artifacts/issue26/browser-baseline-failure/`。
- 真实浏览器探测首次被既有临时目录包装器的单参数拆分问题阻止；使用两个实际 Node 参数后通过，失败输出保留，未修改产品逻辑绕过。一次 Edge 测试退出后有 BITS 临时文件仍占用，包装器明确保留其目录；不将该清理警告记成产品失败或强删其它进程文件。
- 本轮没有操作真实托盘菜单、UAC/hosts、第二 Windows 用户/独立机器或游戏。已有单用户 DPAPI/损坏/格式保护回归按覆盖复用；真实跨用户解密边界、条款变化后的用户确认体验和系统交互仍需对应 Windows 环境，不能用合成回执代替。
- 尚未创建本仓库的正式发布 tag、运行远程 CI、上传 Release 或核验公开下载。正式版本必须重新运行完整对应源码及独立重建流程，并按实际版本保留证据；本地检查不证明公开资产存在。游戏专属剩余场景继续使用既有 VRC 台账，不为本次打包改动新增游戏项目。
