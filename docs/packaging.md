# Windows Portable 打包

构建需要 Go 1.27.3 或更新版本、Node.js（CI 使用 22）、Git 和 PowerShell 7.3+；用户只需要 Windows x64。网页和 SQLite 编译进 EXE，无需外部运行库。脚本禁用自动工具链下载，PATH 中的 Go 必须满足 `go.mod` 的最低版本。

```powershell
./scripts/package-portable.ps1 -Version dev-local
./scripts/test-portable.ps1 -Zip artifacts/still-wanna-dance-dev-local-windows-amd64-portable.zip

# 正式交付阶段：业务/工具验证由预检及 Actions 门禁负责；不自动创建 tag 或发布
./scripts/package-portable.ps1 -ReleaseTag v1.2.3
```

开发包先运行依赖无关的前端源码/资源清单校验（`scripts/check-webui.mjs`），再通过 `scripts/go-patches.mjs` 对 `./cmd/... ./internal/...` 执行带 `no_easytier` 标签的 Go test 和 vet，失败即停止；独立干净检出可用 `-AllPackages` 扩展至 `./...`。正式包只负责交付构建与检查，业务和工具测试在 `scripts/release-preflight.ps1` 及 Actions 门禁中各执行一次，不在打包内部重复运行；它仍在独立干净检出中构建，排除原目录中被 Git 忽略的额外代码/嵌入资源。构建使用 `GOOS=windows GOARCH=amd64 GOAMD64=v1 CGO_ENABLED=0`、`-tags=no_easytier -trimpath -mod=readonly -ldflags '-s -w -H=windowsgui'`；不继承外部 GOFLAGS、工作区覆盖或 Go 实验配置，不自动下载工具链。

ZIP 名允许带版本；ZIP 内固定为：

```text
SWD/
  SWD.exe
  licenses/
    LICENSE
    LICENSE-MIT
    LICENSING.md
    THIRD-PARTY-NOTICES.txt
    SOURCE.txt
    SOURCE.json
```

不交付 config.json、data、videos、logs、work、本机 hosts 或空用户目录。精确交付白名单统一维护于 `scripts/portable-files.json`。使用说明在内置「使用帮助」中；完整条款由 `internal/legal/TERMS.txt` 嵌入，在应用内查看、保存纯文本，也可用 `SWD.exe -show-terms` 导出。ZIP 不再交付 README、条款副本或构建信息 JSON。默认布局、可选位置、生命周期及备份范围以[统一存储](storage.md)为准。

产物暂存于 `artifacts/still-wanna-dance-<版本>-windows-amd64-portable/SWD/`，ZIP 和 SHA-256 文件位于 artifacts 根。不指定版本时采用 `dev-日期-时间`；`-OutputDir` 可指定输出目录。遇到同名产物报错，不覆盖已有包。

「关于」、启动日志和 `SWD.exe -build-info` 的 JSON 输出共用 EXE 内的包类型、版本、提交 SHA、dirty 状态、UTC 构建时间、Go 版本及目标平台。机器出口在读取配置、选举实例、创建数据目录或启动服务之前返回；GUI 子系统 EXE 在 PowerShell 中可用 `& ./SWD.exe -build-info | Out-String` 读取。开发/正式构建共用 `scripts/build-info.mjs` 注入版本与时间，revision/dirty 来自实际 Go VCS 标记；缺失或 dirty 不显示为正式发行。对应源码重建独立标为 rebuild 并记录源码清单的提交，不冒充官方发行。`-Version dev-* / ci-*` 允许本地未提交代码，类型明确为 development，正式源码入口只在发行构建中提供。形如 `1.2.3`/`v1.2.3` 的版本必须改用 `-ReleaseTag`，禁止把 dirty 开发包冒充正式版本。

正式包同时生成独立的 `*-corresponding-source.tar.gz` 及 SHA256、`*-release.json` 和可用作 Release 正文的 `*-release-notes.md`。完整源码不放进 Portable。`licenses/SOURCE.txt` 给出该 tag 的准确源码附件 URL、提交、校验及构建入口；`SOURCE.json` 将 EXE 哈希与源码哈希绑定，发行清单再绑定 ZIP/源码资产哈希和实际 EXE 模块。详见[完整对应源码](corresponding-source.md)。

正式流程用最终源码附件进行全新缓存、仅包内 file GOPROXY 的独立重建，比较实际二进制模块；再运行真实 ZIP 的 Portable 隔离验证，全部成功才复制到输出目录。缺少 notices、源码、构建脚本、错误 tag/commit、dirty 或版本/哈希不符均失败。`go-lzo` 版本选项已由上游澄清，依据见[许可审计](issue-30-license-audit.md)；新发行的 `acceptedRisks` 为空，旧发行的准确历史记录仍可验证。Mihomo 已接入桌面，实际覆盖版本见[开发验证记录](validation/issue-14-validation.md)；原 LA-034/035/036 已按[验证机制](live-acceptance-process.md)分流为开发、浏览器和 Windows 环境检查，只有实际播放器接入兼容性留在 LA-025。发行链验证不能替代这些不同范围的证据。

已有产物可再次独立验证：

```powershell
node scripts/release.mjs verify --manifest artifacts/still-wanna-dance-v1.2.3-release.json
```

官方默认在同一 GitHub Release 同时上传 Portable、对应源码、两个校验文件与发行清单，并将生成的 Release notes 中源码入口置于二进制下载旁。发布正文面向使用者，提供下载、解压运行说明和简短许可入口；源码重建、再分发及审计细节通过文档链接提供。版本更新摘要和实际影响使用的已知问题可补充在正文，内部验证过程和已解决疑点不作为常驻风险提示。发布后、对外宣布前，从无凭据公共 URL 检查实际附件：

```powershell
node scripts/release.mjs verify-published --manifest artifacts/still-wanna-dance-v1.2.3-release.json
```

此命令确实下载两个公开附件、核对哈希和对应关系并独立重建；任一附件 404/403/内容不符都会失败。构建前附件尚未发布，不能把本地校验当作公开可用性通过。若公开检查失败，先停止提供受影响的二进制下载，修正源码交付再重新核验。镜像需同等、免费、持续地提供匹配源码并保留声明；离线再分发默认同时附完整对应源码，不能只依赖在线链接。GitHub 自动源码快照不替代本附件。

`licenses/LICENSE` 为 GPLv3 全文，`licenses/LICENSE-MIT` 保留历史 MIT 授权与版权；自有新贡献采用 GPL-3.0-only，第三方许可不改写。`licenses/THIRD-PARTY-NOTICES.txt` 包含直接/间接依赖及内嵌组件许可全文；打包追加实际 Go 工具链声明。脚本从 EXE 模块版本校验声明覆盖，未登记版本或 replacement 会阻止打包。更新依赖需核对实际许可，不能只改声明中的版本号。单独分发 EXE 也应附带必要许可。

用户双击 SWD.exe 显示托盘，默认打开 WebUI；已确认条款时可按设置关闭启动打开，未确认当前条款时仍打开确认入口。自动加速默认关闭，首次确认后由用户点击「开始加速」；启动行为以[配置生命周期](settings-lifecycle.md)为准。同一实际 EXE 和配置重复启动才打开已有实例，不同安装或配置显示冲突及归属。无需 CMD / PS1 启动脚本。仅编译可执行 `./scripts/build-desktop.ps1`，默认输出 `bin/SWD.exe`。

默认配置为 EXE 旁的 `config.json`，首次成功打开即保存 schemaVersion=8。程序日志位于 `logs/`，格式、分片与容量清理统一见[日志说明](logging.md)。首次启动自动准备 `data/database/storage.sqlite` 与 `videos`；已有资料保护和自定义位置的明确新建遵循[配置生命周期](settings-lifecycle.md)。自定义 `-config`、多实例隔离、相对路径及外置库约定见[统一存储](storage.md)。无版本和版本 1～7 测试配置拒绝打开并保留原文件；已有 v8/v9 数据库可明确接入，v8 在实际打开时迁移，不迁移其他数据库格式或自动搬动旧布局。

数据库 v9 在打开受支持的 v8 库时[静默一次性迁移](storage.md#v8-到-v9-的静默迁移)；完成后旧 v8 程序不能打开该库，更早数据库格式仍拒绝，配置版本不变。下文既有覆盖升级检查只证明当时格式，本轮迁移见[v9 开发验证](validation/storage-v9-20261101.md)。

覆盖升级先退出并备份，然后只将 ZIP 的 SWD/ 内交付文件覆盖到原安装目录；不覆盖 config.json、data、videos、logs、work、外置资源或未知文件。ZIP 内层路径不随版本变化，不能将新解压的另一目录当作原实例启动。详见下方[覆盖升级与备份](#覆盖升级与备份)。

`scripts/test-portable.ps1` 解压真实 ZIP，从不同工作目录启动无托盘 EXE，检查固定路径、许可及模块覆盖、默认配置/数据库/视频目录自动准备、未用功能不生成状态或凭据、条款门禁、配置锁、程序日志、重复实例拒绝及强制停止后的重启。首次启动、重启、移动和自定义配置均断言身份 API 中的 SWD 协议、实际 EXE、配置、PID 及 OS 进程创建时间。随后覆盖解压并在原位置启动替换后的 EXE，逐文件比较覆盖前后的用户数据哈希；整目录移动后再启动，并验证同父目录的两个自定义配置并发启动且锁/日志/默认媒体隔离。Go 回归补充确认记录、通道密钥、快照的移动和外置绝对路径；已有资料保护统一见[配置生命周期](settings-lifecycle.md)，持久化版本边界见[存储契约](storage.md#持久化格式与语义)。

验证不主动接受条款、不启动播放网关、不下载歌曲、不改 hosts；真实托盘及 UAC 属于 Windows 环境检查，游戏专属场景按 [VRC 台账](live-acceptance-backlog.md)另行验证。测试退出只终止自己创建的进程，成功或失败均保留隔离目录及其中的日志供检查。默认位置为仓库忽略目录 `artifacts/portable-smoke/Still Wanna Dance smoke <随机标识>`，退出时输出实际路径；可用 `test-portable.ps1 -EvidenceDir <目录>` 指定其他持久位置。正式发布显式使用原仓库的该目录，避免材料随 `TEMP` 包装器或临时检出一起被清理；自定义目录同样应放在这些临时目录之外。

2026-10-19：发布与临时目录定向回归 7/7 通过。使用缺少 EXE 的 ZIP 验证默认位置，以及临时检出配合显式证据目录两种入口：冒烟失败退出后，包装器临时目录已删除，解压材料仍可读取。日志保存在本地忽略目录 `artifacts/windows-test-temp/release-smoke-retention-final.log`；本次未执行完整正式发布构建。

`-PreviousZip <前一版本ZIP>` 可在同一安装目录验证两个不同版本；两包都需匹配的 SHA256 和当前精确白名单，版本和 EXE 字节必须不同。未指定时仍为同包覆盖冒烟，跨版本验收需指定相应的旧包。此脚本保持条款门禁，不生成有效同意；首次启动会自动准备默认数据库和视频目录，但不导入或下载媒体。有内容的持久化数据另由下面的集成检查覆盖，不能用首次空库的覆盖结果代替。

启动发行工作流前，先完成[发布前本机 Windows 完整预检](development.md#发布前本机-windows-完整预检)；本节维护产物与公开交付规则。

[`check` 工作流](../.github/workflows/check.yml)按工具与仓库检查 → 产品/race 并行 → 一次正式交付 → 自动发布执行，各验证范围共用本机预检脚本。PR/main 按[日常分流规则](development.md#测试维护与-ci-修复)决定是否运行产品与 race 测试；需要运行时使用代表性浏览器与 race，手动运行和 tag 使用完整浏览器及全包 race。产品作业完成真实 Console 联调、Go test/vet/build 和 gofmt。`repository-artifacts` 在分支 push／PR 时独立执行，在手动或 tag 检查时由 `check` 调用并作为打包门禁；它通过 `-Scope repository` 执行产物策略及其 Node 测试和 Python 测试，工具作业不重复这些测试。Windows 真实 Console 联调使用 Edge，夹具浏览器使用锁定的 Chromium，race 使用固定版本、校验哈希的 w64devkit GCC。同一分支或 PR 的新提交取消过期检查，同一发布 tag 的运行串行且不自动取消。没有发行 tag 时只检查，不再生成重复候选包。

新正式版本优先从选定分支手动运行：在 `check` → `Run workflow` 选择分支，填写 `release_tag`（例如 `v1.2.3`）。此入口在完整检查与交付验证通过后，由 Actions 创建远端版本 tag 并发布；触发前不必推送正式 tag。当前工具无法触发手动入口时，应先解决触发方式，避免提前绑定版本后又因验证修正需要移动 tag。`release_tag` 留空只检查；工作流也支持直接检查已推送的版本 tag。已有 tag 必须指向本次提交，不移动已有 tag，也不覆盖已经发布的 Release。

如需先确认 Actions 的完整检查结果，第一次手动运行留空 `release_tag`，让产品和六组 race 全部结束并收齐失败。检查成功后，核对将要发行的提交与成功运行的 SHA 一致，再填写 `release_tag` 和该次 `validation_run_id`。这条路径把完整检查做一次，正式交付单独运行。途中修改代码、测试或验证规则时，重新完成受影响的本机检查和新的完整 Actions 检查。

版本更新与升级注意事项保存在 `docs/releases/<tag>.md`；存在对应文件时，打包作业将其附加到生成的 Release 正文。内容面向使用者，保留实际功能变化和升级影响。

发布只构建一套正式 ZIP、对应源码、校验文件和清单，保留必要源码输入与许可、离线独立重建、实际 ZIP 冒烟和覆盖升级检查。源码交付范围以[对应源码说明](corresponding-source.md)为准，不增加与 SWD 交付无关的上游重建门槛。升级默认使用最近公开版本（含候选版）的实际 ZIP；`previous_tag` 可指定其他历史版本，通过公开清单与 SHA256 校验后下载，省去同一源码的 ci 候选构建。无历史发行时只做单包验证。持久化格式变化仍须补相应旧格式数据迁移验证。

产品和全量 race 全部通过，或完成下面的严格复用校验后，才进入交付构建。独立发布 job 只取得该次 Actions 已验证的五个正式附件并获得 `contents: write`，上传草稿后核对 GitHub 返回的大小与 SHA256。最终 Release 的附件始终来自 Actions。

业务验证复用通过可选 `validation_run_id` 指定本仓库一次成功的完整 `check` 运行。脚本核对工作流、来源、完成状态、产品完整检查以及六个 race 作业的实际成功步骤；普通 main/PR 代表性检查、跳过或失败作业、其他仓库、复用后的运行均不能作为基线。用完整 Git diff（含删除和重命名）按与日常分流相同的分类确认只有非业务交付文件或文档资料变化；前端构建清单中的输入和产物（包括位于 `docs/` 的数据及嵌入许可）、业务代码、业务测试、前端/Go 依赖、构建规则、验证入口和工作流变化一律拒绝复用。`release.mjs` 与 `release-source.mjs` 同时负责生产构建，整体排除在复用清单外，避免构建入口、编译参数或身份规则变更沿用旧业务结果。清单集中在 `scripts/release-validation.mjs`，工具回归与全部实际交付检查仍执行。留空即运行完整业务检查；不通过校验会立即停止，不静默扩大或缩小范围。初次采用本流程需要建立新的完整成功基线。

| 后续情况 | 最少需要完成的工作 |
| --- | --- |
| 仅文档变化 | 复用仍适用的本机证据；发行时可指定有效完整运行，工具与最终交付仍验证 |
| 仅允许清单内的交付脚本变化 | 本机补工具/交付阶段；Actions 指定有效完整运行复用业务结果 |
| 业务、业务测试、依赖或构建／验证规则变化 | 本机补受影响验证，正式 Actions 完成产品与全量 race |
| 同一次运行的打包或发布失败 | 归因后重跑失败作业；成功测试不重跑，发布复用本次已上传产物。后续复用检查按各产品／race 作业的最新尝试核验，保留先前已通过且未重跑的作业 |
| 产物字节发生变化 | 重新完成受影响交付检查；不能覆盖已公开版本或不同内容的同名附件 |

附件完整后自动发布，并从无凭据公共 URL 回下载 ZIP、源码、两个校验文件和发行清单，核对与已验证附件的身份、字节数及 SHA256。正式打包已对同一源码附件完成全新缓存独立重建，公开下载的哈希相同，因此自动流程不再重复重建；单独运行上面的 `verify-published` 仍会重新独立重建。失败时尝试恢复为草稿，工作流明确失败；只有公开附件检查成功才表示公开交付完成。带 `-beta` 等后缀的版本自动标为 Pre-release；此流程不设置 Latest。已有相同标签的草稿可复用，已有同名附件不覆盖，内容不符即停止。重跑失败的发布 job 会复用本次 Actions 附件；若重新构建导致附件不同，应检查草稿或使用新版本，不能覆盖已公开的交付。本地打包脚本负责准备与验证，附件由 Actions 上传。

首次正式发布由 [#5](https://github.com/SW26010/still-wanna-dance/issues/5) 汇总可靠性与公开交付、[#26](https://github.com/SW26010/still-wanna-dance/issues/26) 汇总最终 Portable 与覆盖升级结果。#23/#24/#25/#27/#30 的基础实现和证据按适用范围复用，历史记录中的同源码候选包路径由上面的实际旧版本基线流程替代。开发和 Windows 剩余范围以[非 VRC 验证跟进](validation-followups.md)为准，游戏剩余场景以 [VRC 台账](live-acceptance-backlog.md)为准。历史证据见 [#26 验证记录](validation/issue-26-validation.md)，不以历史包的结果替代最终交付。

## 覆盖升级与备份

1. 正常退出整个应用。优先备份所选数据库及恢复所需的 SQLite 伴随文件，建议保留主配置；完整视频按需备份。完整媒体库备份才包含全部视频。可重建状态、日志及临时文件无需日常备份；外置资源按各自位置保管。
2. 校验新版 ZIP 的 SHA256，解压到临时位置，将 `SWD/` 内的 `SWD.exe` 和整个 `licenses/` 覆盖到原安装目录，保留 config.json、data、videos、logs、work、外置资源与未知文件。从原目录启动，避免误用新目录的空配置。
3. 核对「关于」版本及「使用帮助」中的当前路径。条款不变时，同用户有效确认和凭据应保留；条款变化要求重确认。跨用户/普通跨机器无法解密时保留原密文和其它数据，重新确认或配置，不隐式切换直连。
4. 遇到损坏或未来格式停止并按诊断处理，不手改版本号或删除数据库。无版本及不受支持的旧配置、旧布局不迁移，支持范围以[统一存储契约](storage.md#portable-文件契约与生命周期)为准；首发前的未公开构建须按各自的目录布局核对升级路径，也不自动扫描删除旧文件。

新增或移除交付文件须同步白名单、两条打包入口、验证脚本和说明；未来正式版之间若废弃文件需要清理，单独明确其所有权及安全处理，不按新白名单删除未知文件。停用或卸载时，确认「停止加速或退出时恢复游戏直连」已开启并成功保存，再正常退出，核对本程序管理的 hosts 映射已恢复并备份所需数据。退出复用已准备的权限助手，不在退出时申请 UAC；缺少权限、助手故障或强制终止可能中断恢复，按 [hosts 契约](console.md#hosts)处理。删除 Portable 不会自动恢复系统 hosts 或删除外置媒体。

跨版本入口：

```powershell
./scripts/test-portable-upgrade.ps1 -PreviousZip artifacts/previous-portable.zip -Zip artifacts/current-portable.zip
```

`test-portable-upgrade.ps1` 先验证两个真实 ZIP 的门禁、精确白名单、SHA256 和覆盖启动，再在默认目录、外置媒体、自定义配置三种布局上运行两个实际 EXE。集成测试隔离生成当前格式的配置、同用户 DPAPI 测试凭据/条款回执、通道密钥、任务快照、真实 SQLite 和通过导入入口发布的合成媒体；检查两个进程读取成功、持久文件哈希保持以及日志追加。它不读取个人数据、不接入游戏或修改 hosts。合成回执仅用于集成测试，真实用户的条款确认按实际操作记录，也不替代跨用户环境或真实视频播放验收。

开发包与正式包均可加 `-PreviousZip` 在输出前执行此检查；正式流程在独立干净检出中执行，证据输出至原仓库的 `artifacts/portable-upgrade/`。有历史发行时使用其实际公开 ZIP；无前一包时只完成单包验证，不能宣称连续版本已验收。格式迁移范围仍按存储契约验证。历史交付结果见 [#26 验证记录](validation/issue-26-validation.md)。

## 2026-10-11 #23 / #27 本地验收

后续 #24 运行身份收口及便携包验证见 [issue #24 本地验证](validation/issue-24-validation.md)。以下保留当次 #23 / #27 记录。

在 Windows amd64、Go 1.27.1 上验证最终代码 `798df22`（打包时待提交项仅为文档）：

- `go test ./cmd/... ./internal/...` 全部通过，`go vet ./cmd/... ./internal/...` 通过。使用正式包路径排除本地 artifacts 中历史实验 Go 源码；没有将整个含实验产物的工作目录宣称为 `go test ./...` 通过。
- `node --test scripts/*.test.mjs`：128 项通过。打包脚本自身再次执行其中 120 项 UI 测试及完整 Go 包测试/静态检查。
- `package-portable.ps1 -Version issue23-27-final` 成功；本地 ZIP 的 SHA-256 为 `64a51a80fb9847d433250378e43f225f7d4274c75cc65d79e2b83fdf2e0a2c50`。仅本地产物，没有推送、发布或上传。
- `test-portable.ps1` 对该 ZIP 实测通过：固定 SWD/ 交付、按需生成目录、独立工作目录、条款门禁、配置锁、重复实例拒绝、进程终止后重启、同格式覆盖时用户数据哈希保持、整体移动、同父目录自定义配置并发隔离、旧布局拒绝。实际 app.log 的时间为 UTC Z。
- 自动化回归另覆盖固定 SQLite/配置/快照样本、缺表/索引/新增列拒绝、未来/旧版本拒绝、缺失数据库不重建、事务失败回滚、纳秒查询竞争及时间回拨、CurrentUser DPAPI 同用户移动、篡改/未知封套保留、强制代理失败而非直连、显式凭据重配及保存失败不丢原密文。

Windows 受限沙箱中的临时文件重命名曾返回 Access denied；最终结果来自正常用户权限的本地测试，并非忽略那些失败。跨用户/普通跨机器的真实 DPAPI 环境、真实断电/磁盘耗尽、VRChat 音画、托盘交互及 UAC 联合测试本轮未执行。旧试验配置/数据库无迁移，便携覆盖验证针对本次测试的格式；#15 法律凭据防伪及 #24–#26 各自剩余范围仍独立推进。

### Fluent UI 构建检查

界面源码修改后先按开发指南重建并运行浏览器测试。CI 校验完整前端构建一致性并执行浏览器场景，测试范围及最新结果统一见 [UI 验证记录](fluent-ui-migration.md#验证与待验收)；开发包可直接使用已提交资源，只需 Node 做哈希校验。正式对应源码包额外要求固定 pnpm，并收集 npm 源码存储、验证前端完全离线重建。Portable 第三方声明包括 `docs/licenses/WEBUI-NOTICES.txt`。`test-portable.ps1` 在未确认条款的隔离进程检查新 bootstrap、JS/CSS 和 API 门禁，仍不自动接受条款或启用游戏链路。
