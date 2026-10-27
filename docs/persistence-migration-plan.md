# 持久化职责迁移计划

> 2026-10-27 首发前清理：本文旧阶段记录中的父目录锁兼容已被替代。现行数据库统一使用 `<文件名>.lock`，视频目录统一使用 `.swd.lock`，不与开发期旧写入者共用锁协议；历史布局探测、接入例外和 hosts 标记迁移亦已移除；版本校验、恢复文件保护及未知文件保留继续适用。下方阶段记录只证明当时构建，当前规则见[存储契约](storage.md)。

### 首发前入口收口（2026-10-27）

本轮按查询与数据字段、权限助手、外部控制、页面地址、历史存储探测的顺序继续清理，替代下方阶段记录中保留旧入口和旧布局保护的部分：

- 删除 `GET /api/cache` 及独立列表实现，统一使用曲库资源查询；保留删除和定位操作的存储身份、条目快照及保护检查。请求历史不再查询无人使用的当前歌曲关联；资源关联删除恒真 `current` 字段，活动下载仍显示关联歌曲。
- 删除桌面的 `-hosts-action` 和仅供它使用的 `-accept-terms`，权限操作只走会话助手。移除网关、队列和 hosts 的六个独立 HTTP 控制入口，统一由加速、退出和重启编排；测试专用网关启动包装移入测试文件。
- 删除五个旧页面别名，曲库定位改为 `#/library?view=songs|resources`，保留当前区域锚点和公开文档地址。移除旧 hosts 标记、资源映射迁移和 `needsMigration` 状态；当前标记的精确恢复与其他条目保留继续验证。条款同步为 `2026-10-27.1`，旧确认需要重新确认。
- 删除旧安装标记、历史布局和视频文件名探测，以及旧位置的选择例外；保留当前默认资料保护、数据库版本／结构校验、WAL／SHM／journal 保护、资源锁和未知文件保留，不迁移或删除用户资料。

原始日志在忽略目录 `artifacts/legacy-cleanup/`。完整产品包测试 `go test -p 1 -tags=no_easytier ./cmd/... ./internal/... -count=1` 首轮仅条款版本与正文不一致失败；同步正文后，legal 整包及受影响的历史、曲库、入口、首次启动、hosts、网关、队列、条款定向回归全部通过（`go-full-1.txt`、`final-regressions.txt`）。同范围 vet、实际 EXE 构建、前端类型与嵌入产物检查通过；脚本回归 13 项通过，工作区产物策略及 diff 检查通过。

Edge 内存夹具回归 100 项通过，覆盖新曲库链接、旧地址回退、焦点与导入草稿保留、历史和设置。真实 Console 与 Edge 的 `TestFirstRunBrowser` 通过，验证条款、默认资料库及统一加速，截图已检查。实际 EXE 隔离检查确认旧参数以退出码 2 拒绝、旧安装／布局文件不阻塞且字节不变、默认数据库生成、条款门禁有效并正常退出（`exe-smoke.json`）。首轮沙箱测试的原子重命名、junction 失败及浏览器在页面操作前的失败保留在原日志；普通用户环境复跑通过，未放宽产品检查。最初查询迁移的编译遗漏已修正，失败输出同样保留。

实网脚本已移除对旧启动接口的依赖，继续承担 CLI 实网和桌面条款检查；桌面网关冷／热播放及离线复用由现有 Go 受控上游集成回归承担，不将其称为桌面 EXE 实网验收。本轮未执行公网下载、真实 UAC／hosts、发行 ZIP 或 VRC；没有新增必须依赖游戏的行为或待验项。

后续审查修正：历史资源行仍带 `# SWD managed` 时，整文件标记检查会把它误认作当前托管条目，既可能触发无用授权，也会阻断本次 API 映射的退出恢复。新增临时文件／会话回归先复现失败，再将托管判断统一限定为当前 API 域名及标记；状态、恢复授权和恢复后的残留检查共用此判断，其他条目逐字保留，改写过的 API 地址、格式和别名仍明确拒绝自动恢复。hosts 会话、Windows 助手、加速及回滚定向回归和 console vet 通过，原始失败与成功分别保留在 `review-hosts-red.txt`、`review-hosts-green.txt`。同时修正 `song-preferences-live.mjs` 的旧链接；真实 Console／SQLite／Edge 的 `TestSongPreferencesBrowser` 通过，覆盖偏好保存、筛选焦点、引擎重开、屏蔽 GET 403、零网络解析和零预算补齐入口，桌面与窄屏截图已检查（`review-song-browser/`）。存储说明明确：不符合受管命名的文件原样保留；符合当前 MD5 文件名的普通文件即使无数据库记录，仍参与本地视频管理。上述检查没有修改系统 hosts 或用户资料。

### 首发前兼容清理验证（2026-10-27）

按配置／CLI、资源锁、观测通道的顺序清理：设置不再接受 `socks5` 别名，CLI 使用 `-max-local-validations`；数据库和视频目录按资源独立持锁，删除无产品调用的混合目录检查入口；Monitor、测速与测试夹具统一通过候选接口，移除普通 Transport 分支。吞吐快照改为格式 2，只保存带通道身份的样本，旧格式 1 黄金文件保留用于拒绝且不改写回归。现行边界分别见[服务参数](service.md)、[存储契约](storage.md)与[上游通道](upstream-channels.md)。旧数据保护、hosts 恢复及业务回源容错保持原用途。

完整 `go test -p 1 -tags=no_easytier ./cmd/... ./internal/... -count=1`、同范围 vet、前端构建与产物检查通过；Edge 设置草稿／代理保存／恢复回归 16 项通过。定向 Windows race 覆盖独立锁、候选替换与取消、吞吐持久化和诊断生命周期；`cf_block_experiment` 标签编译通过。实际 CLI 构建的帮助包含新参数，旧参数在解析阶段以退出码 2 拒绝。所有检查使用隔离资源，未迁移用户数据库或视频、未启动游戏或改 hosts，无新增 VRC 专属待验项。

原始日志保存在忽略目录 `artifacts/compat-cleanup-20261027/`。保留首轮 `monitor-compile-1.txt` 的夹具接口遗漏和 `monitor-tests-1.txt` 的两项失败：旧测试假定相同直连身份必须丢弃观测，另一个来源测试未发布候选。前者改为明确替换候选身份，并保留有效歌曲抽样的现行语义；后者提供真实候选接口。修正后的定向、整包及 race 结果分别见 `monitor-tests-2.txt`、`go-full-1.txt`、`race.txt`；没有放宽产品检查或覆盖旧失败记录。

复审修正所有权夹具：候选提供者保留 `CloseIdleConnections`，候选 Transport 指向原对象，避免函数包装掩盖误关闭。定向测试通过（`ownership.txt`）；通过 Go overlay 分别注入关闭候选提供者和候选 Transport 的错误，两次均触发 `closed borrowed connection pool` 断言（`ownership-provider-mutation.txt`、`ownership-candidate-mutation.txt`）。注入源码仅保存在上述忽略目录，不改动产品文件。

当前数据库已随响应来源统计升为 v7，新增请求与累计的三个固化字节字段，沿用开发期旧格式拒绝且不改写的约定。当前依据见[存储契约](storage.md)；下方 v6 首发收口记录保留当时验证范围，不能作为现行格式。后继格式演练测试已基于新 v7 样本继续验证。

## 2026-10-27 首发数据库收口

本节替代下方 2026-10-26 迁移范围内“v5 不改表、列、索引”的阶段约束；此前验证仍只证明其记录的 v5 构建。职责、目录、配置 v7 和辅助状态格式保持不变，当前数据库契约以[存储约定](storage.md#持久化格式与语义)及冻结 [v6 DDL](../internal/cacheproxy/schema/storage-v6.sql)为准。

- 14 张表采用 STRICT，补齐计数／字节非负、清单键非空和 MD5 格式约束；`song_urls` 的歌曲及资源使用事务提交时检查的外键。请求失败身份和独立访问证据仍不受外键限制。
- 保留全部歌曲资料列、偏好和统计语义、清单摘要 v1。删除 `media.source_path` 及无调用方的扫描目标入口；研究探测复用已保存 URL，不再拼造来源地址。
- HTTP 请求历史移除 `counts_as_demand`、恒定 `source`；`resource_md5` 和正整数 `song_id` 允许 NULL，对外 JSON 仍以字符串和空串表示身份。预下载保留原有日志，不写请求明细；保留 `host`、`file_bytes` 作为诊断证据。时间分页和清理共用复合索引，显式索引由 4 个收敛为 3 个。
- 新增覆盖全部 14 张表的 v6 二进制样本；旧 v2～v5 DDL 和样本逐字保留，打开时拒绝且不改写。不提供开发期旧格式迁移，不操作用户现有资料。
- 测试中从冻结 v6 演练一个假设的后继格式：重建请求表、保留剩余列、更新版本；在 8 个步骤分别注入失败，核对事务及版本回滚、文件字节保持、重开重试、完整性、外键、新建／升级结构一致和降级拒绝。该演练不构成产品 v7 格式或已发布升级支持。

验证基线为 `db84e2fcf669c68ebe0598504964e0dfb9356eed` 加本轮工作区修改。原始输出位于忽略目录 `artifacts/db-v6-20261027/`；测试通过 `scripts/with-test-temp.ps1` 使用隔离临时目录，没有操作用户运行库。

| 检查 | 结果与证据 |
| --- | --- |
| 产品包回归 | `go test -tags=no_easytier ./cmd/... ./internal/... -count=1` 首轮除 cacheproxy、console 外均通过；修正两包旧夹具后，完整重跑这两包通过（`all-tests-1.txt`、`affected-tests-2.txt`） |
| 约束、身份与预下载 | 新增 STRICT／负值／NULL／MD5／关联约束；验证最大 int64 歌曲 ID 对外无精度损失、未知身份仍为空串；真实回环预下载成功及失败均不落请求明细、需求、播放或访问统计 |
| 冻结样本与未来升级演练 | 14 表离线备份还原、旧 v2～v5 拒绝且字节不变、v6 重开不改字节；后继格式成功与 8 个失败点回滚／重试均通过（`upgrade-1.txt`）。独立只读核对 v5／v6 的 songs 全部 21 列定义相同，完整性和外键检查通过（`schema-comparison.txt`） |
| 并发、静态与构建 | 数据库、URL、需求／历史统计及预下载相关 `go test -race` 通过；产品包 `go vet` 和 `go build` 通过，均带 `-tags=no_easytier`（`race-1.txt`、`vet-1.txt`、`build-1.txt`） |
| 真实后端与 Edge | `TestRequestHistoryBrowser` 通过：601 条真实回环请求、数据库重开、13 页历史、映射变更后仍保留 ID 42 和原资源、601 个首正文样本、刷新和零脚本错误；桌面与 390px 深色截图已检查（`history-browser.txt`、`history-browser/results.json`） |
| 辅助脚本与产物 | 研究探测脚本语法检查通过；v6 样本按精确路径、102400 字节和 SHA256 登记。仓库历史产物检查无错误，既有体积提醒保留；本轮工作区文件检查无错误和提醒（`repository-artifacts.txt`、`working-tree-policy.json`）。`git diff --check` 通过 |

保留首轮失败：定向测试曾缺少 `strings` 导入；旧夹具仍写 media 三列、读取 source_path，或制造不存在的 song_urls 资源外键，均已按新契约修正。沙箱内硬链接用例被 Windows 拒绝，沙箱外隔离回归通过。整套回归曾有一次配置目录移动报拒绝访问，单独复查和后续整包回归均通过，未改生产代码掩盖该失败。

范围限制：升级演练使用合成后继格式，不等于正式升级链、断电或磁盘满验证；2026-10-26 的受限卷结果仍仅证明当时 v5 构建。本轮没有重做最终打包、用户数据迁移或 VRC 验收，不新增游戏台账项。截图另见桌面长 MD5 链接挤入相邻结果列的布局问题；前端本轮未变，该问题留作独立 UI 修复，不把此次浏览器集成通过表述为完整视觉验收。

## 2026-10-26 目录归位与状态命名

状态：实现与本轮非游戏验证已完成。经独立复审及用户确认，按[默认目录与状态命名](persistence-design.md#默认目录与状态命名)收拢路径：主配置位于 EXE 旁，默认数据库位于 data/database，视频、日志和 work 为独立一级目录，辅助状态统一位于 data/state；batch.json、attempt.json 分别改为 batch-last-success.json、batch-last-attempt.json。不新增额外分类层级或空资料目录，不改变数据库 v5、配置 v7、快照 v2 及其业务语义。

- 统一调整 apppaths、桌面/CLI 工作区、状态读写、原子工作槽清理、路径冲突校验与 Mihomo Home。配置实例与实际资源分别持锁，保留相对路径和自定义配置隔离。
- 自动初始化前检查新目标及有限的旧布局证据，发现原有资料或检查失败时阻止自动建库；已配置资源严格打开，不因旧文件残留阻塞。无旧配置解析、迁移、扫描或删文件。
- 同步帮助、存储与升级说明、发行脚本和测试。默认备份 config.json + data，state 可排除，外置资源按实际位置另行保管；升级保留全部用户目录。
- 已验证首次启动、已有资料保护、两份任务结果命名与清理、多配置隔离、便携相对路径和移动、真实 EXE/浏览器的实际位置与升级保留；具体范围见下表。

本轮验证使用隔离夹具和开发包，没有操作用户正在使用的资料、真实 hosts 或 VRC。两份 EXE 均使用本次目录布局，验证的是该布局下覆盖升级的保留行为，不代表旧布局自动迁移。下载发布、数据库格式和跨卷算法未改动，未重复上一轮真实磁盘耗尽和跨卷验证；VRC 实机验收仍未进行。

| 验证 | 结果与本地证据 |
| --- | --- |
| Go 测试、静态检查及开发包构建 | `package-portable.ps1 -Version dev-directory-layout-20261026` 执行 `go test`、`go vet`（`-tags=no_easytier ./cmd/... ./internal/...`）、嵌入 UI 一致性及许可检查，全部通过；[日志](../artifacts/directory-layout-package.txt) |
| WebUI 文档与导航 | `documents.spec.ts`、`document-navigation.spec.ts` 共 17 项通过，覆盖两种宽度、明暗主题及路径／备份说明；[日志](../artifacts/directory-layout-ui.txt) |
| 真实服务与浏览器 | `TestFirstRunBrowser` 通过，检查首次使用、同意条款、默认资料库、帮助中的实际路径和首次加速；[日志](../artifacts/directory-layout-browser-final.txt)、[路径与备份截图](../artifacts/directory-layout-browser-final/paths-and-backup.png) |
| 脚本回归 | 验收协议／请求测试 10 项、便携包预检拒绝测试 3 项通过；[协议日志](../artifacts/directory-layout-scripts.txt)、[便携预检日志](../artifacts/directory-layout-portable-tools.txt) |
| 真实便携 ZIP | 首次生成目录、相对路径、覆盖、整体移动、多配置并行、旧布局证据阻止自动建库等烟雾检查通过；[日志](../artifacts/directory-layout-portable.txt) |
| 两份真实 EXE 覆盖升级 | `TestPortableExecutableUpgrade` 的默认、外置资料库、自定义配置三种布局全部通过；核对数据库、视频、凭据、快照和未知文件保留；[日志](../artifacts/directory-layout-upgrade.txt) |

本地开发包为 `artifacts/still-wanna-dance-dev-directory-layout-20261026-windows-amd64-portable.zip`，SHA256 为 `5efffc45800dd6d65daaa51cf6dfb14198c013255ed53cf1e5abfd9d2d6c0a16`，不是正式发行。上述证据由忽略的 artifacts 目录保留，不随源码提交。

保留失败记录：最初沙箱运行触发 Windows 原子重命名 `The handle is invalid`，见 [targeted 日志](../artifacts/directory-layout-targeted.txt)；切换到普通用户执行环境后发现旧测试夹具仍假定数据库与视频同父目录、删除模拟资料库时连带删除新 state、注入状态文件时未创建新父目录，见 [targeted 普通环境日志](../artifacts/directory-layout-targeted-unsandboxed.txt)和 [Go 首轮日志](../artifacts/directory-layout-go.txt)。已按测试原意修正夹具，相关[定向回归](../artifacts/directory-layout-regressions.txt)及上述全包验证通过。

### 复审修正（2026-10-26）

复审发现四项实际缺口，已修复；目录划分、数据库 v5、配置 v7 和快照格式均不变：

- 共同建库入口在取得数据库锁后、创建主文件和连接 SQLite 前拒绝任何同名 WAL、SHM、journal 目录项，检查错误同样拒绝，保留原资料并释放锁。真实 SQLite journal 和模拟 WAL/SHM 的[原复现](../artifacts/outgoing-review/confirmation-20261026.txt)会删除或接受伴随文件；[修复后复现](../artifacts/outgoing-review/confirmation-fixed.txt)全部拒绝且字节不变。此证据不承诺孤立伴随文件一定能恢复数据库。
- 旧布局保护与明确接入共用已知位置集合。程序目录取已验证的启动身份；只对现存的已知旧资料提供接入例外，仍验证数据库和视频目录、要求保留策略确认，不放宽旧位置新建或任意程序内目录。回归覆盖默认、实例根、自定义配置三种布局的检查、保存、重开及收藏／视频保留，以及缺失、损坏、其它文件名／目录和新建拒绝。
- 数据库选择请求使用与 VRCX 一致的可取消等待，保留后端 5 分钟上限；普通请求和新建请求继续使用原超时。浏览器覆盖等待 16 秒后成功选中、取消后保留草稿并重试；未实际打开原生选择窗口。
- 两份 README 同步备份范围、配置 v7、已有 v5 接入及交付文件清单；相关现行契约已同步。

验证：新增数据保护回归先失败后通过，见[首次失败](../artifacts/outgoing-review/fix-regressions-red.txt)、[旧资料接入失败](../artifacts/outgoing-review/reconnect-red.txt)及[修复后定向回归](../artifacts/outgoing-review/fix-regressions-green.txt)。`go test -tags=no_easytier ./cmd/... ./internal/...` 与同范围 `go vet` 通过，见 [Go 日志](../artifacts/outgoing-review/fix-go.txt)、[vet 日志](../artifacts/outgoing-review/fix-vet.txt)；存储与 VRCX 浏览器回归 12 项通过，见[浏览器日志](../artifacts/outgoing-review/fix-ui.txt)。WebUI 类型检查、构建和嵌入产物一致性通过。首次沙箱测试遇到 Windows 原子重命名句柄错误，浏览器沙箱启动未完成并中止；普通用户环境重跑完成。没有操作用户资料、真实 hosts 或 VRC；该次复审时尚未提交或推送修复。

### 后续复审：文件别名与交付检查（2026-10-26）

- 数据库采用明确的支持边界：写入和只读入口共用文件检查，要求普通文件、单一硬链接名称和实际完整路径；硬链接与 Windows 短文件名别名拒绝，普通大小写处理及现有锁保留。短目录别名的新建请求在创建主文件前拒绝，不留下空库。保持所选路径的资源身份，不引入身份登记文件，也不影响视频硬链接导入。已打开后新增硬链接时，另一个入口仍不能取得写入权；不支持外部在运行期间移动、替换或改名数据库。
- 当前 bundle 的 1,165,317 字节及完整 SHA256 已追加到产物策略。完整历史检查另发现 7 个同类漏登版本，逐一核对所属提交与该提交构建清单中的输出哈希后追加，保留全部原登记，见[历史版本核对](../artifacts/spec-review-alias/historical-bundles.json)。没有提高大小阈值或放宽检查规则。
- 修正存储文档中普通打开、明确新建与视频备份的混用表述，引用已有 128 MiB NTFS 受限卷实测并保留未验证场景，不将有限范围结果扩展为断电或全部文件系统已验证。

验证：先复现[硬链接双写](../artifacts/spec-review-alias/confirmation.txt)，新增硬链接、运行后新增别名和真实 `PERSON~1.SQL` 短文件名回归均[先失败](../artifacts/spec-review-alias/regression-red.txt)后[通过](../artifacts/spec-review-alias/regression-green.txt)；同组验证普通数据库重开、数据保持和视频硬链接导入／清理，另有[短目录新建检查](../artifacts/spec-review-alias/short-path-final.txt)。Windows 全包 `go test` 与 `go vet`（`-tags=no_easytier ./cmd/... ./internal/...`）通过，见 [Go 日志](../artifacts/spec-review-alias/go-all.txt)、[vet 日志](../artifacts/spec-review-alias/vet.txt)。Linux amd64 核心测试程序交叉编译通过，未在 Linux 实际运行。产物检查覆盖 4,111 个历史 blob 和当前修改／新增文件，零错误，保留原有 24 条非阻塞体积提醒；检查器的 3 项测试通过，见[完整结果](../artifacts/spec-review-alias/artifact-policy-final.json)、[检查器测试](../artifacts/spec-review-alias/artifact-tests.txt)。嵌入 UI 一致性与 diff 检查通过。本轮未再次运行未改动的浏览器流程、磁盘耗尽或 VRC；该次复审时尚未提交或推送修复。

### 提交整理（2026-10-26）

英文 README 的首次使用说明同步为自动初始化默认配置、数据库和视频目录，自定义位置按需设置，并清除已移除的引导流程描述。按用户授权，将复审修复归入对应的实现、测试和文档提交，保留独立的网络身份修复；只整理 `aeb5f079` 之后的未推送历史，仓库外保留整理前的 Git bundle。最终文件树与提交前快照一致，历史验证中的源码 SHA 和文件哈希保持原样。本次仅本地提交，不推送。

下文阶段一至六保留上一轮已完成的资源职责迁移记录；其中路径和文件命名由本节及现行存储契约替代，历史验证仅证明当时布局。


2026-10-26 制定，基线为 `aeb5f0794b6d36924a422d867cbbc7f21fe1d89d`。原则与边界以[持久化数据职责与边界](persistence-design.md)为准。迁移包括代码、数据归属、界面和文档调整，以及现有核心数据的保留方案；不建立旧测试配置兼容层或覆盖全部历史格式的迁移链。

上一轮完成状态：2026-10-26 六阶段的实现和当轮非游戏验收已完成，逐项核对见阶段六。数据库、完整视频与固定工作区分离；桌面配置 v7 和 CLI 均要求明确打开／创建，旧混合根适配已移除。真实跨卷、v5 全数据备份还原、两个开发版 EXE 的覆盖升级及可丢弃受限卷真实耗尽均已验证。所有更改按独立目的本地提交，不推送；没有操作用户正在使用的数据。真实 VRC 场景按用户要求暂缓，本计划保留为当轮完成记录。

迁移按原则文档中的六类职责逐项归属，不按文件数量压缩分类。配置重要但可重建；核心数据库同时保留大量歌曲、资源信息、几乎不可重建的个人积累，以及重要性较低但因规模、查询、关联和更新方式适合数据库管理的业务数据；完整视频重建成本较高。先判断明确职责及数据库适配性，再将无需入库的资料与辅助状态按保留价值和生命周期归类。每项搬移或删除都要说明信息是否保留、损失影响和重建方式，并比较存储方式的适配性与总维护成本，不能仅以“可以重新获取”或“不够重要”为由移出数据库。

## 本轮复核结论

本轮必要改动集中在资源定位、创建与打开、数据库生命周期、工作文件所有权和相应界面；没有证据要求重构现有数据模型或统一所有小文件的格式。下表保留对基线代码的复核依据，现行实施状态以各阶段记录和存储契约为准。

| 原计划问题 | 代码核实与影响 | 修订结论 |
| --- | --- | --- |
| 将 `catalog_checks` 改为仅内存，并为此升级数据库 | [清单提交](../internal/cacheproxy/catalog.go)将成功检查与清单接纳放在同一事务；[读取](../internal/cacheproxy/catalog_snapshot.go)和[回归](../internal/cacheproxy/catalog_snapshot_test.go)覆盖重启、旧版本响应、冲突和失败回滚。当前写入者只维护 `songs` 一行，不累积检查历史 | 撤回移出、删表及其格式升级。保留全部 14 张表和 v5；减少一行状态没有证明足以抵消业务改动、升级与回退成本 |
| 把 `.json.d` 当成旧配置兼容推导一并删除 | [ForConfig](../internal/apppaths/paths.go)与[隔离测试](../internal/apppaths/paths_test.go)表明它是当前不同配置文件的辅助文件、锁和默认库隔离规则 | 保留现行能力；不兼容旧配置字段不等于取消自定义配置实例隔离 |
| 把条款等已有文件再次集中搬位置 | [路径入口](../internal/apppaths/paths.go)已经为 `terms.json`、`channel.key`、`state/`、`logs/` 规定位置；不同文件有不同保存及失效语义 | 保留文件、位置和格式；不增加搬迁步骤、合并容器或空的“辅助资料”目录 |
| 将所有个人数据入口笼统视为依赖引擎 | [收藏](../internal/cacheproxy/song_policy.go)、[VRCX](../internal/console/vrcx.go)已有离线入口；但 [openOfflineStorage](../internal/cacheproxy/storage_database.go)仍锁混合根、可隐式建库并创建 `videos`。清单和地址观察仍通过 `ensureEngine()` | 复用现有 SQL 与离线能力，解除实际耦合，不重写全部入口；搬连接时必须保留与淘汰、映射更新的串行关系 |
| 把拆分生命周期描述成关闭时绝不丢统计 | [usageStore](../internal/cacheproxy/usage.go)已有有限队列、满队列丢弃与批次写失败只记日志；[流量统计](../internal/cacheproxy/stats.go)已有单独错误字段 | 保留当前采集口径和有限队列，正常关闭排空已接受工作、公开写入失败；不借本轮新增持久队列或承诺故障下零丢失 |

以下清单分别写明保留和改动的证据。没有业务收益的物理搬移不进入实施；仍需实现验证的事项单列在阶段一与验收表，不先当作已解决。

## 当前内容与目标归属

复核基线的默认配置位于 `EXE/data/config.json`，数据库、视频和临时媒体混在 `data/storage`；辅助文件分布在配置根及 `state`。下表保留迁移前的定位及处理依据；已实现部分见各阶段状态，当前定位规则见 [apppaths](../internal/apppaths/paths.go)，实际文件契约见[运行生命周期](runtime-lifecycle.md)。

### 文件与运行状态

| 当前内容 | 本轮处理及必要性 | 已核实的依赖与边界 |
| --- | --- | --- |
| `config.json` 中的用户设置 | 必改：一个 `StorageDir` 改为独立数据库文件和视频目录字段；其他配置语义保留 | [resolveSettings / saveSettingsLocked](../internal/console/console.go)、[格式读写](../internal/console/config_file.go)：保留 saved/active、相对路径基准及重启；目前普通设置保存也探测旧存储可写性，需解除。`LogDir` 是外部 VRChat 日志输入，不是应用日志路径 |
| `config.proxySecret` | 保留密文、原位置和配套连接设置的一次提交 | [config_file.go](../internal/console/config_file.go)：省略保留、明确替换、解密失败保留原值；拆文件会增加地址与密码错配风险，无收益 |
| `config.proxyNodes` | 保留为配置，不拆成辅助资料 | [proxy_nodes.go](../internal/console/proxy_nodes.go)：预览在内存，一次设置保存原子提交，失败保留原值、重启生效；导入集合有大小和节点数限制，无需第二存储 |
| `channel.key` | 保留原位置和独立文件；仅收拢为实例准备一次并共享 | [proxy_identity.go](../internal/console/proxy_identity.go)、[network_mihomo.go](../internal/console/network_mihomo.go)、[诊断入口](../internal/console/network_diagnostics.go)都可读／建同一 key；写或 Sync 失败后其他调用可再次读取。统一本实例结果可避免身份与失败状态不一致；不宣称发现死锁，不新增密钥格式或用户备份要求 |
| `terms.json` | 保留现位置、独立确认语义与格式；无搬动必要 | [terms.go](../internal/console/terms.go)：DPAPI、条款版本、成功保存才确认、自动加速门禁。丢失／失效可重新确认 |
| `state/throughput.json` | 原样保留小型可重建状态，不进核心库 | [throughput.go](../internal/upstreamstate/throughput.go)：先保存尝试时间再自动测速，写失败抑制自动大流量；显式手动测试仍可运行。样本按通道身份复用及清退，跟随配置实例而非视频盘 |
| `state/inventory.json`、`batch.json`、`attempt.json` | 保留各自最近一份；必须改归属与失败反馈 | [snapshots.go](../internal/console/snapshots.go)已有共用机制；[storage_usage.go](../internal/console/storage_usage.go)把恢复结果用于建库判断，须移除该依赖。[inventory.go](../internal/console/inventory.go)和 [batch.go](../internal/console/batch.go)部分内存结果仅在保存成功后发布，须解耦。数量有界不等于字节固定，失败列表随一次批量规模增长；本轮不增加历史库或压缩框架 |
| `storage.sqlite` 及 SQLite 必要伴随文件 | 必改定位、打开和所有者；保留 v5 数据模型 | [storage_database.go](../internal/cacheproxy/storage_database.go)、[server.go](../internal/cacheproxy/server.go)：现由引擎打开并由统计 worker 关闭；普通写打开用 `rwc`，缺库判断还查看 `videos`。迁移后不依赖视频盘，不将伴随文件当垃圾清理 |
| `videos/{md5}.mp4` | 必改为独立目录，直接使用所选位置；保留现有文件和策略 | [storage.go](../internal/cacheproxy/storage.go)、[retention.go](../internal/cacheproxy/retention.go)、[manage.go](../internal/cacheproxy/manage.go)：路径、收藏保护、文件身份和删除校验均依赖旧根；引擎初始化即执行淘汰，接入旧视频前必须明确保留策略 |
| `tmp/download-block-*.part` | 必改到 EXE 旁 `data/work/` 的实例区；不增加设置 | [local_resource.go](../internal/cacheproxy/local_resource.go)、[local_blocks.go](../internal/cacheproxy/local_blocks.go)、[block_reclamation.go](../internal/cacheproxy/block_reclamation.go)：只有运行期引用与 pin，没有重启恢复；保留回收和活动读取保护 |
| `tmp/download-publish-*.part`、`tmp/import-*/video` | 保留必要暂存，但与块使用不同落点；仍在目标视频卷 | [block_publication.go](../internal/cacheproxy/block_publication.go)合并、校验、Sync 后 Rename；[import.go](../internal/cacheproxy/import.go)同卷硬链接、跨卷仅复制到目标暂存，再硬链接发布。全部移到 EXE 工作区会破坏这些约束 |
| `logs/app.log` 与轮转文件 | 保留原位置、大小上限和轮转 | [writer.go](../internal/applog/writer.go)：5 MiB × 当前文件加 3 份备份，有界排障材料；[桌面启动](../cmd/still-wanna-dance-console/main.go)先打开日志，块清理需调整启动次序以避免磁盘满时卡在日志之前 |
| `state/mihomo/` | 保留现有 Home，不新增清扫、搬迁或第二业务库 | [runtime.go](../internal/proxyengine/mihomo/runtime.go)传入内存配置，关闭配置选择／fake-IP 持久化与地理数据自动更新；锁定依赖及隔离探测见下文，不能把“可能有 cache.db”当成已发现必要文件 |
| 配置锁、原 `storage/.lock` | 保留 OS 锁机制；必须随独立资源调整锁归属 | [config_lock.go](../internal/desktop/config_lock.go)、[storage_database.go](../internal/cacheproxy/storage_database.go)、[manage.go](../internal/cacheproxy/manage.go)覆盖配置、离线写／删除和引擎；新设计须覆盖共用 DB、共用视频与工作区，不删除锁文件来解锁 |
| `.<文件名>.swd-tmp`、目录权限探测 | 保留必要临时机制及白名单清理；不集中搬进 work | [atomic.go](../internal/workfiles/atomic.go)同目录原子替换，[config_lock.go](../internal/desktop/config_lock.go)持锁清理已知目标；权限探测必须发生在被验证目录。调整根检查，不能机械禁止所有派生目录创建 |
| 本机会话发现记录 | 保留系统用户会话位置和现有关闭清理 | [discovery.go](../internal/desktop/discovery.go)以 PID、启动时间和 EXE 校验旧记录；它服务跨启动发现，移到每个安装目录反而改变发现范围，不需要备份 |
| hosts 与 `.swd-backup`、外部输入、旧测试残留 | 保留特殊职责与所有权，不纳入一般迁移 | [hosts.go](../internal/console/hosts.go)及 [hosts_session.go](../internal/console/hosts_session.go)处理系统接入；VRChat 日志、VRCX 库及导入源不是应用自产资料。源视频删除仍需本次导入回执和显式确认，不扫描收集或删除未知材料 |

三个快照复用现有读写和原子替换机制，不扩展成任务历史或恢复系统。旧格式或不匹配的结果忽略，不新增兼容迁移；坏文件不影响核心数据。快照只保留过去结果，不能替代当前检查或授权操作。旧测试材料不自动扫描搬运或删除。

### 当前数据库表

以当前 [v5 结构](../internal/cacheproxy/schema/storage-v5.sql)为基线，14 张表全部保留，不增删表、列、索引或另建低价值 SQLite 库。数据库不存本地库根路径；`media.source_path` 保存上游资源路径，改变本地 DB／视频位置不需要改写它。严格结构验证仍由 [storage_schema.go](../internal/cacheproxy/storage_schema.go)统一负责，不在业务读取中补表。

| 表 | 实际读写及依赖 | 本轮处理 |
| --- | --- | --- |
| `songs` | [catalog.go](../internal/cacheproxy/catalog.go)清单更新，[library.go](../internal/cacheproxy/library.go)曲库查询；个人数据写入也可补歌曲占位 | 保留，大量歌曲资料及关联锚点 |
| `media` | [storage.go](../internal/cacheproxy/storage.go)、[song_urls.go](../internal/cacheproxy/song_urls.go)写资源描述；扫描、排序和本地复用读取 | 保留，不把资源描述等同于本地文件存在 |
| `song_media` | 清单／URL／完成下载更新映射；[song_policy.go](../internal/cacheproxy/song_policy.go)按共享资源聚合收藏和屏蔽 | 保留事务与映射优先级，不能独立搬走 |
| `song_preferences` | [song_policy.go](../internal/cacheproxy/song_policy.go)事务写入，播放准入、队列、淘汰和界面读取 | 保留不可重建个人偏好及提交前后保护 |
| `song_urls` | [song_urls.go](../internal/cacheproxy/song_urls.go)、[song_load.go](../internal/cacheproxy/song_load.go)、[song_metadata_candidates.go](../internal/cacheproxy/song_metadata_candidates.go) | 保留，播放候选／失败回退与元数据缺项查询共用；每个歌曲、API、节点只更新最近一条，不追加每次响应 |
| `song_usage` | [usage.go](../internal/cacheproxy/usage.go)聚合需求，[usage_import.go](../internal/cacheproxy/usage_import.go)事务替换 VRCX 导入；[priority.go](../internal/cacheproxy/priority.go)参与排序 | 保留个人积累、衰减规则和导入覆盖语义 |
| `song_playback` | [playback_stats.go](../internal/cacheproxy/playback_stats.go)随事件事务记录传输、统计播放传输量 | 保留，重下视频无法重建过去记录 |
| `traffic_totals` | [stats.go](../internal/cacheproxy/stats.go)串行保存累计节省量、命中／未命中 | 保留；请求明细清理不重置累计量 |
| `media_access` | [usage.go](../internal/cacheproxy/usage.go)更新最近请求，[manage_list.go](../internal/cacheproxy/manage_list.go)与 [manage.go](../internal/cacheproxy/manage.go)参与文件最近使用排序 | 保留稀疏聚合状态，不新增每次访问历史 |
| `catalog_state` | [catalog.go](../internal/cacheproxy/catalog.go)按版本、摘要判断新旧和冲突，并随清单提交 | 保留业务一致性事实 |
| `catalog_members` | 同事务维护接受清单的当前成员；[catalog_snapshot.go](../internal/cacheproxy/catalog_snapshot.go)读取当前集合，历史歌曲仍可留库 | 保留，不能仅靠 `songs` 全表替代 |
| `catalog_checks` | 同一次清单成功事务更新；错误仅改错误字段、不推进成功时间；[inventory.go](../internal/console/inventory.go)可在无引擎时读取 | 保留单份清单配套状态，不搬、不删；不因同库提高其备份价值 |
| `request_events` | [usage.go](../internal/cacheproxy/usage.go)批量事务写入，[request_history.go](../internal/cacheproxy/request_history.go)按索引分页、时间范围及分位数统计 | 保留用户可见历史；沿用默认 30 天、0 为不清理及分批清理，不是普通文件日志 |
| `request_observation` | [usage.go](../internal/cacheproxy/usage.go)记录采集起点并随清理推进水位；[request_history.go](../internal/cacheproxy/request_history.go)解释统计覆盖是否完整 | 保留一行状态，与明细保留语义配套 |

`song_urls` 的播放复用时限与元数据覆盖判断保持原有区别：超过 24 小时的地址不再直接作为播放候选，但仍可证明某歌曲和 API 节点已有资源描述。迁移应用存储生命周期时保留这一行为、旧响应不覆盖新观察、同次更新失败回滚及关联冲突规则。此表不新增另一份缓存副本，不执行导出、拆分或删表；原先的移出方案撤回。

### 第三方依赖核实范围

- [go.mod](../go.mod)锁定 `modernc.org/sqlite v1.40.1`，当前连接使用 `foreign_keys(1)`、`busy_timeout(1000)` 及单连接限制。现有事务、严格 DDL 校验和驱动可以继续使用；本轮没有引入 ORM、第二数据库或升级驱动的依据。路径、连接生命周期和应用级排他规则仍须由本程序处理，SQLite 锁不能替代视频淘汰与业务所有权。
- Mihomo 为 `v1.19.32`。核对该版本 `hub/executor/executor.go`、`component/profile/cachefile/cache.go`、`component/fakeip/pool.go` 和 provider 调用：`cache.db` 的 bbolt 打开是按需发生；当前适配器禁用的功能不能被当作已经生成文件的证据。2026-10-26 用当前适配器、隔离 Home 和一个本地 SOCKS5 节点仅启动／关闭，无请求；两次枚举 Home 都为 **0 个子项**。因此保留约定目录，不臆造必须保留的 cache.db，也不新增删除规则。此探测未覆盖各协议实际传输、所有异常路径或 Home 外文件事件，不能外推为第三方永不落盘；若未来要清理具体依赖文件，先补对应场景证据。
- [workfiles](../internal/workfiles/atomic.go)、现有 OS 锁和日志轮转已有明确职责。复用它们即可；块的引用保护及发布、导入的同卷约束不能用一个通用“临时目录”抽象抹平。

## 现有数据的迁移与回退

本次只制定流程，不操作用户正在使用的数据。“配置重新设置”与“数据库重新创建”严格分开。

- **配置：** 路径字段变化需调整配置格式及样本，不保留旧字段双读；`.json.d` 实例归属与此无关，继续保留。保留原配置作为参考，在新的配置语义下恢复所需设置；不得因旧配置不可读而创建一份空库替代原库。导入节点和受保护密码仍是原配置的一部分，不能把“可重新配置”解释为可自动丢弃用户唯一保存的连接资料。
- **数据库：** 当前 v5 原样承接，不执行结构升级、表搬移、删表或版本递增；先用隔离副本验证新的打开入口、个人数据与关系。现有文件在原位置可直接被新路径字段指定，独立定位不要求物理搬家。来源不匹配则停止并保留，不自动猜测或重建；不顺带承诺 v2、v3、v4 等全部历史格式。只有发现新的、必要的结构变化，才重新提出有代码依据的升级方案。
- **完整视频：** 优先将新的视频位置直接指定为原来的实际 `videos` 目录，沿用 MD5 文件，避免无必要搬动大文件。启用视频管理前明确恢复或选择保留策略，并核对当前数据库中的收藏保护；不得直接以新配置的默认零预算清理旧视频。确需换盘时再停机迁移，不把块或发布残留作为完整视频搬运。
- **辅助资料与状态：** 不为分类重新搬目录。当前代理节点随配置保管，不另建资料目录；继续使用同一配置实例时，原有 key、条款和测速状态可按现行格式读取，不主动使它们失效。换配置实例不要求用户逐项搬迁，可按各自失效语义重建；结果快照因归属字段变化使用新格式并忽略旧快照。不搬工作残留，不扫描收集未知旧文件，也不长期双写。

正式执行顺序为：正常退出并确认资源关闭 → 保留完整可恢复备份 → 在隔离副本验证新入口及文件落点 → 设置新的独立位置，仅在确需换盘时移动文件 → 新配置指向已验证的数据并明确视频保留策略 → 检查收藏、历史、累计值和歌曲关联 → 启用视频管理并检查本地复用。数据库若仍依赖 SQLite 伴随文件，备份按完整恢复集合处理。

接入或移动失败不发布成功状态、不以空库继续，原文件与备份保留。本轮没有 DB 格式回退步骤；旧程序仍要求旧配置和旧路径布局，v5 格式相同不代表任意新布局或新旧进程并行可用。回退使用配套旧配置及已核对的数据位置／备份，并停止另一版本。实际用户数据的执行范围在实施任务中明确，本计划不直接执行搬移或原地改库。

## 执行前决策状态

用户已确认低成本保留上次结果，并选择临时工作目录固定在程序旁，不增加高级换盘设置。代理节点继续作为配置、通道身份按可重建状态管理、复用旧视频前明确保留策略；`song_urls` 保留现有业务职责与存储，不再列为待拆分项。

本轮未发现必须再次交给用户选择的重大产品取舍。现有文件及全部表的保留结论、必要改动的依赖已在上表明确；具体字段、默认值、工作区派生规则、锁顺序与事务协调接口由开发者按阶段一完成，不能用泛称“实现时处理”跳过这些安全契约。任何新发现的合并、拆分或删表安排仍须先核对读写者、事务关系和用户行为，按[收益核查原则](persistence-design.md#目标与取舍原则)解释收益与成本；若必须牺牲功能、改变保留承诺或新增用户管理任务，再提交具体取舍。实际数据来源和位置属于执行前事实核实，当前尚未获取，不是本轮要用户重做的原则决策。

## 阶段一 定清最小实现契约

状态：实现契约已确定并落地。以下六项保留原完成要求；实际落点、提交语义及证据见本节补充和后续各阶段。

1. 以固定 `EXE/data/config.json` 为默认入口，给数据库文件和完整视频目录定义各自字段、默认值、相对路径基准及选择／创建动作，优先复用现有位置。保留 `ForConfig` 的实例归属、辅助状态和日志位置；临时工作区固定在 `EXE/data/work/` 下，明确跨重启可定位的实例隔离与清理所有权，不随自定义配置位置换盘，也不增加工作路径设置。检查所选 DB／视频位置与配置、work、暂存的冲突，防止清理范围覆盖重要文件。第四类资料无实际内容时不创建空目录。
2. 复用已有快照机制定义数据库与视频位置的结果归属、不支持格式的忽略规则、内存结果与落盘失败的独立反馈，以及只读展示过去结果的边界。代理节点继续使用当前配置原子提交，不设计跨文件节点保存流程。
3. 将现有地址观察、播放候选、失败标记、元数据缺项查询及清单业务一起归入应用持有的业务存储；保留 `song_urls`、`catalog_checks` 等全部表和事务。先列清 `mappingMu`、`retentionRunMu`、资源 pin 与提交后淘汰／队列通知的先后关系：仅“提交后回调”不能替代收藏提交与淘汰之间原有的互斥。不先要求新包、通用仓储层或统一改写只读查询。
4. 按实际资源定锁：配置／工作区、独立数据库文件、视频目录各有明确所有者和固定获取顺序。特别验证不同配置共用数据库、不同数据库共用视频、路径别名的冲突；所有离线写和删除入口一起调整。当前旧程序锁 `storage/.lock`，新锁必须明确旧布局接入时的交接／冲突处理；不能假定 v5 格式可读就代表新旧版本可安全同时运行，也不为此虚构 DB 结构升级。
5. CLI 继续参数模式，共用数据库和视频访问规则。明确独立数据库、视频参数及默认位置；工作区同样固定在对应 EXE 旁，明确与桌面实例的隔离及锁归属，不增加工作路径参数或引入桌面网络功能。此前“看见 config.json 就拒绝整个 R”的规则基于已撤回的整根模型，不直接照搬；也不默默读取桌面设置或共享未受保护的工作文件。
6. 列明 v5 原样接入的核对项与备份／回退操作，并同步配置、快照新格式样本。真实迁移前核实数据来源；若不是当前 v5，先明确范围，不能因配置不兼容直接让用户放弃数据库。无需为本轮创建数据库升级器或完整备份管理产品。

完成条件：上述落点和提交语义写回本计划及目标设计；没有靠新增永久目录身份、强制接管开关或隐式重建解决矛盾。若需要改变既有业务行为，列清具体变化及理由，不把它藏在路径改名里。

配置与入口契约：桌面配置 v7 的 `databasePath`／`videoDir` 在首次打开时自动准备为实例 `storage/storage.sqlite` 与 `storage/videos`，相对路径基于配置父目录；正常使用无需手动建库或重启。初始化边界以[配置生命周期](settings-lifecycle.md)为准，已配置数据库打开失败不会改建空库。自定义位置的明确新建只创建 DB，普通打开／保存不创建 DB；新选择的视频目录可在保存时创建，未变化的失联位置不重建。CLI 的普通运行显式要求 `-database`、`-video-dir`，`-create-database` 仅建库后退出，不读取桌面配置。资源位置重启生效，按规范路径校验归属；不新增永久 ID 或接管字段。条款、代理凭据和节点继续各自已有的提交语义。

已实现的基础契约：`cacheproxy.OpenStore(path)` 只接受已有合法 v5，`CreateStore(path)` 才能排他创建新文件，`Close()` 释放连接和写入所有权。二者不创建视频／工作目录、不启动统计采集；空文件也不是允许初始化的依据。固定名 `storage.sqlite` 复用父目录 `.lock`，其他数据库使用文件旁 `<文件名>.lock`，从而与旧布局写入者互斥；锁不是用户配置或迁移材料。连接参数、结构校验与既有实现共用。旧混合根调用及其隐式创建分支已删除，测试也通过明确创建／打开资源进入引擎。

固定工作区契约：`apppaths.WorkForConfig`／`WorkForService` 将工作区放在 `EXE/data/work/console-<摘要>`／`service-<摘要>`，分别按规范配置路径、CLI 的 DB／视频路径组合定位；EXE 目录内的路径参与计算时采用相对位置，整包移动不改变实例区。无登记文件、用户配置项或按次启动随机根。`OpenWorkDirectory` 持独立 `.lock`，先清理本实例已知数字命名块文件，再允许日志／数据库启动，最后在全部使用者停止后释放。不清扫其他实例、完整视频、发布暂存或未知项；用户数据位置不得与整个 work 树相互包含。引擎借用工作区，不再重复清理其中的块。

## 阶段二 拆出核心业务存储生命周期

状态：应用与视频引擎的数据库生命周期已接通。桌面在读取配置后准备首次默认数据库或打开已配置数据库，失败保留在状态中；Monitor、收藏、VRCX 与离线删除共用应用持有的 Store。桌面路径已拆分；核心不再接受旧混合根，应用须明确提供数据库、视频和工作区。

已实现：清单、歌曲 URL、个人偏好、需求统计导入及优先级查询共用 `Store`，可在没有视频引擎时运行；映射与淘汰仍共用原有互斥锁，提交后按需通知已连接引擎。引擎借用应用传入的数据库，关闭引擎不关闭它；应用须先关闭引擎和其他使用者，再排空统计并关闭数据库。仅启动引擎时开启请求采集，不因打开数据库提前设置采集起点或清理明细。后台写入失败保留可查询错误并唤醒等待中的下载；关闭报告未保存的后台工作，但不重复报告调用者已经收到的同步业务失败。重复引擎启动在工作文件清理之前拒绝。旧入口的混合根创建分支已删除，引擎关闭不再有自行关闭数据库的分支。

- 复用当前 SQLite 查询、事务和串行化规则，让应用持有业务存储；视频引擎使用它，不能继续负责关闭共享数据库。必须解除元数据写入经过 `ensureEngine()` 的间接依赖，不能仅搬出 `sql.DB` 指针。
- 调整清单、播放 URL 对应的持久事实、收藏、历史、VRCX 导入等入口。现有收藏／VRCX 离线写、历史只读能力直接复用，消除其混合根检查与隐式创建；访问个人数据不启动引擎、建立视频目录或触发淘汰。引擎存在时仍保留与映射、发布和淘汰的协调关系。
- 后台统计写入与关闭顺序随应用生命周期归属；先停止使用者，再处理已接收写入、关闭数据库并释放所有权。原 `openUsage` 同时建库、设置 `request_observation.started_at` 并启动明细清理，现已移除；统计 worker 只借用应用连接，由引擎开始采集时启动，不把“应用打开 DB”解释成“请求采集已开始”。保留采集起点、覆盖水位与明细清理的既有业务条件。
- 将核心数据库故障暴露给应用状态，独立于下载引擎；保留配置、数据库与视频故障的具体影响，不把上游错误、取消或单条坏样本误报为数据库损坏。

桌面接线验证：在视频目录缺失时，Monitor 清单／地址观察和收藏仍能读写数据库，不创建视频目录、不启动引擎、不写请求采集起点；应用在引擎启动前持有数据库排他权，关闭后释放。缺失数据库在状态中明确报错，不通过空库存或旧结果快照授权重建。VRCX 覆盖确认显示目标数据库文件，7 项浏览器交互回归通过。完整回归、竞态与静态检查结果见本次验证记录；不将夹具结果视为真实用户资料迁移验收。

主要入口：[引擎](../internal/cacheproxy/server.go)、[统计写入](../internal/cacheproxy/usage.go)、[监测清单](../internal/console/monitor_catalog.go)、[地址观察](../internal/console/monitor_playback_url.go)。

完成条件：没有视频引擎或视频目录失联时，个人数据查询和适当写入仍可工作；数据库写失败真实返回或显示，不假报成功；正常关闭先排空已接受的统计工作，再关闭 DB 和释放锁，失败须报告。保留有限队列及现有采集口径，不承诺 I/O 故障、队列饱和或强杀时零丢失，不新增磁盘队列、重试日志或另一套统计。

## 阶段三 分开定位和创建 打开流程

状态：定位、创建／打开与入口改造已完成。固定块工作区已接入桌面和 CLI，发布／导入使用独立落点并仍在视频卷。核心与 CLI 已切换独立路径及相关资源锁；桌面配置 v7 已改为独立文件／目录，首次不自动选择或建库；明确新建、保存及重启已接通。核心的混合根 fallback、Config.StorageDir、旧隐式数据库打开函数及混合临时目录清理均已删除。各调用方明确准备资源，未知旧工作文件保留；C 盘工作区到 D 盘视频目录的真实发布及跨卷导入已验证。受限卷故障验收见阶段六。

- 将混合的 `StorageDir` 拆为数据库位置、完整视频位置和内部工作路径。路径计算集中到已有路径入口，发布暂存与块缓存使用各自位置。
- 普通数据库打开使用不创建文件的方式；仅明确的新建操作初始化结构。配置丢失或重设、视频目录为空、快照不存在都不能成为建库依据。
- 配置可以恢复或重建，而不强制与数据库同目录、同时存在。数据库路径不明时允许选择原文件；损坏、空文件和不支持格式保留原样，不自动补齐。
- 普通写入不能递归重建已失联的受管根。仅在明确创建动作中创建新根；已持有有效根下的派生工作目录按既有安全规则建立。
- 数据库／视频切换保存后重启，不自动搬运。数据库操作绑定当前数据库；视频及当前策略相关操作绑定数据库与视频组合。同步调整 `CacheStorageID`、文件 Stamp、历史查询、批次目标和导入回执，复用会话校验，不新增永久 ID。
- 依照新资源所有权调整 CLI、桌面、离线读写和删除入口的锁。普通写打开用 `mode=rw`、只读用 `ro`；允许创建的模式仅限明确创建分支，不能漏留隐式建库入口或只持配置锁。

主要入口：[数据库打开](../internal/cacheproxy/storage_database.go)、[路径](../internal/apppaths/paths.go)、[原子写](../internal/workfiles/atomic.go)、[应用业务存储](../internal/console/business_store.go)、[视频管理](../internal/cacheproxy/manage.go)。

完成条件：配置缺失不重建核心数据；视频盘失联不重建空视频库；不同入口冲突能够拒绝；不同盘的数据库、视频与块目录可正确工作；切位置后旧操作不能作用于新资源。

## 阶段四 整理辅助状态与短期数据

状态：通道身份的一次准备、固定块工作区、早期清理、快照组合归属与内存结果发布已完成；独立路径的媒体暂存已使用视频目录 `.swd-tmp/`；桌面和 CLI 均已切换到所选视频目录的 `.swd-tmp/`，不再使用旧媒体根 `tmp/`。

通道身份由每个 `Console` 的独立 `sync.Once` 准备，外部 SOCKS5、保留节点集合、Mihomo 和诊断读取同一结果；成功密钥及失败后的临时密钥均不在本实例中重读或提升为另一种身份。调用方拿独立副本，清除副本不影响其他入口。坏文件原样保留，下一实例才重新读取；文件格式、存放位置、外部 SOCKS5 的临时池身份规则和既有持久观测筛选均保留，不新增备份要求。定向测试与 `-race` 已验证并发副本、失败后文件恢复仍保持本实例结果、重启读取、损坏局部降级及诊断／测速相关回归；输出见 `artifacts/persistence-implementation-20261026/identity-*.txt`。

- 保留条款、通道 key、测速文件的原位置、格式和语义，不合入 config 或 DB；代理节点继续作为受保护连接配置保存，不新增独立资料库。通道身份只收拢一次初始化与实例内读取，失败时保留原文件和局部降级，不引入新存储协议。
- `catalog_checks`、`song_urls` 及其他 v5 表原样保留；本阶段不改数据库结构，不编写升级分支，不在业务查询时补结构。
- 保留三个快照的共用读写机制，调整到数据库与视频组合的归属校验，删除由快照推断初始化的依赖。扫描／任务结果更新不依赖落盘成功；重启仅展示匹配的上次结果及时间，缺失或失效则显示未知或无历史结果，不伪装成零库存、当前检查或已恢复任务。不新增格式迁移、长期历史、目录登记或用户管理入口。
- 将块残留清理放在对应工作区所有权下，早于需要成功写文件日志或恢复数据库的步骤。确认工作区归属不依赖视频盘在线或完整 DB 可写打开；不能用猜测补足归属，也不能为清理损坏库而触碰 SQLite 伴随文件。
- 保留已有引用保护、发布／导入清理、安全文件删除和未知项保留规则；停止生成旧状态不意味着扫描并删除原测试文件。

完成条件：新生成文件符合职责清单；不为减少文件数而新增 DB 表或重复配置状态，数据库归属有业务关系、规模和访问方式上的依据；坏辅助文件不损坏个人数据。可用的测速文件继续恢复冷却，写失败抑制自动大流量；缺失／损坏不承诺恢复已丢失的冷却时间，不为此增设另一份记录。快照缺失本身不新增整库网络任务；新实例只清理属于自己的已知残留。

## 阶段五 接通界面 备份说明与发行入口

状态：CLI 参数、帮助和实网验收入口已调整；曲库界面已区分视频目录不可用与空目录，偏好绑定数据库。桌面位置设置、明确建库、视频策略确认和备份帮助已接通，浏览器及隔离 Go 服务验证通过；发行脚本已同步 v7，真实 ZIP 的首次启动、同包覆盖、整包移动和多配置隔离验证通过；两个实际开发版 EXE 在默认、外置和自定义配置布局的覆盖升级已验证；v5 全表离线备份还原通过，不包含旧配置迁移。

- 将含混的“本地存储目录”改为明确的数据库位置与完整视频位置，显示实际生效位置及待重启状态；普通用户无需编辑块、锁或日志目录。
- 新选择的位置才做相应校验／明确创建；主题等无关设置保存不检查未变化视频目录的可写性。“不保留视频”仍允许修改视频位置。配置重建后接入已有视频，或换数据库而复用视频时，在可能触发删除前明确当前数据库与保留策略；不新增每次启动确认。
- 首次默认存储由应用自动准备，不给条款页或其他业务入口增加手动建库步骤。“打开已有数据库”和“明确新建”只用于自定义位置或恢复已有资料，不另行重做引导系统。已有数据不重新创建；数据已建而条款保存失败时只重试确认；损坏核心数据保留原文件，提供恢复或重新选择入口，不扩建恢复中心。
- 用现有错误展示区呈现数据库、视频和辅助状态的实际影响。更新帮助中的升级、数据库备份、视频搬迁及辅助资料说明，不让用户日常处理内部文件。
- 同步桌面重启、CLI 参数和帮助、Portable 验证及发布脚本。发行包继续只含发行文件，不加入运行配置、空数据库、用户资料或缓存。

已核实的关联入口（下表问题描述指迁移前基线，相应修改均已落地）：

| 入口 | 必要关联修改 |
| --- | --- |
| [settings.tsx](../webui/src/settings.tsx)、[storage-directory.tsx](../webui/src/storage-directory.tsx)、[types.ts](../webui/src/types.ts) | 现有单个 `storageDir` 同时说明视频、资料、统计，且在不保留视频时禁用；拆成两个位置，保留原设置保存与重启流程 |
| [request-timeline.tsx](../webui/src/request-timeline.tsx)、[vrcx.tsx](../webui/src/vrcx.tsx) | 现按 active `storageDir` 切换查询／预览；改为数据库作用域，VRCX 已有整表替换确认继续保留 |
| [cache.go](../internal/console/cache.go)、[import.go](../internal/console/import.go) 与设置页导入区域 | 删除、选中项、导入目标及回执依赖旧根／StorageID；改为当前数据库与视频组合，保留文件 Stamp、会话校验与源文件确认 |
| [app.tsx](../webui/src/app.tsx) 及帮助路径展示 | 显示两个生效位置、备份优先级与内部文件的管理责任；不增加缓存目录管理页 |
| [CLI main.go](../cmd/still-wanna-dance/main.go)、[acceptance.mjs](../scripts/acceptance.mjs)、[test-portable.ps1](../scripts/test-portable.ps1) | CLI 当前参数为混合 `-storage-dir`，验收脚本固定配置 v6、旧字段和 `videos` 派生路径；按实际新契约修改。Portable 对自定义配置 `.d` 隔离的断言应保留，而非删除以迎合新计划 |

完成条件：浏览器与隔离真实服务验证位置编辑、重启、缺失／失联、保存失败、条款重试和旧页面操作；默认启动不需要新的快捷方式流程。现行文档与实际实现同步，不把目标规则留在多个相互冲突的位置。

## 阶段六 验证与收尾

### 2026-10-26 首次使用回归修正

迁移时把“已有数据库打开失败不得改建空库”错误扩大成“新用户必须手动建库”，破坏了默认打开即可使用的体验。原有验证预先准备了数据库，或验证了手动创建流程，未覆盖空目录下的正常使用；以下修正替代早期记录中“初始两个路径留空、用户先建库”的要求。

核对 `aeb5f079`：`newConsole` 首次自动保存带默认路径的配置；数据库由 `openStorageDatabase` 在首次写入时创建，既可能由后台曲库同步经 `ensureEngine` 触发，也可能由收藏或 VRCX 的 `openOfflineStorage` 触发，视频目录随存储准备创建。创建不是加速按钮专属操作，也不是用户必须理解的步骤。

当前保留自动准备的用户体验，并按现有所有权放在应用初始化层：首次默认数据库和视频目录准备后，路径保存为配置相对路径，直接供各业务入口共用；数据库已有应用级生命周期，集中初始化比再增加一套延迟建库状态更简单。视频引擎、请求采集、下载和其他辅助文件仍按功能需要启动／生成。自定义位置继续是可选设置；已配置数据库丢失或损坏仍拒绝重建，未配置但默认位置已有数据库伴随文件或完整视频时保留原状。规范边界以[配置生命周期](settings-lifecycle.md)为单一依据。

验证：从完全空目录进入正常曲库、后台目录同步、收藏写入与重开、一键加速均通过；已有配置的缺库、空库、坏库与未配置位置残留资料保持原状。控制台整包、桌面入口、定向 `go vet`、前端构建，以及便携包异常布局工具测试通过。真实 Go Console + Edge 覆盖首次条款确认、曲库、历史、默认设置与加速，全程没有提交存储设置或调用建库 API，也无需重启；另验证自定义数据库／视频位置保存及重开。这些测试隔离 hosts、DNS 和监听端口，没有启动游戏或修改系统 hosts。

证据位于忽略目录 `artifacts/first-run-20261026/`：`console-fixed.txt`、`desktop-entry.txt`、`browser-fixed.txt`、`browser-fixed/` 和 `custom-storage/`。保留 `console.txt` 中旧夹具断言失败与 `browser.txt` 中浏览器脚本误监听旧曲库接口的失败；修正后的首次浏览器回归监听实际 `/api/library`。初次沙箱回归另遇到已知的原子重命名句柄限制，沙箱外同一测试复现产品错误“请选择数据库文件”，修复后通过。便携包脚本的默认路径断言已同步；本次未重新执行完整 ZIP 覆盖升级或真实 VRC 验收。

### 原迁移验收记录

状态：已完成代码、验收证据与逐项要求复核；关闭排空、可丢弃受限卷真实耗尽和清理复核均通过。受限卷用例在用户确认 UAC 后实际运行，三组场景都取得操作系统盘满错误及零可用空间；不以故障注入、跳过的可选测试或环境准备代替实测。真实游戏与跨用户解密不属于本轮已验证范围。

| 必须判断的行为 | 验证方式及通过标准 |
| --- | --- |
| 首次使用与核心数据保护 | 空目录首次打开自动准备默认配置和业务库，各业务入口直接可用；已配置库丢失、空／损坏／未来库以及未配置但默认位置有资料时保留原状，不覆盖或重置 |
| 当前数据库原样接入 | 使用 v5 隔离样本验证新定位及所有者、不改 DDL／版本；核对个人偏好、历史、累计值、清单状态及关系保持，备份可恢复；非 v5 仍拒绝且不改写 |
| 存储独立与锁 | 两入口、不同配置／相同数据库、相同视频／不同数据库组合；不能绕过所有权写入或清理，共用视频按当前实例策略的边界明确 |
| 提交与关闭 | 并发设置／条款／节点保存、后台统计、关闭和重启；成功点一致，失败反馈真实，旧提交不覆盖新值，无跨进程回执恢复 |
| 上次结果 | 同一数据库与视频位置可查看最近结果和失败列表；换位置、坏文件或不支持格式忽略；保存失败仍显示本次结果；无任务续跑、自动重试或删除授权恢复；移除快照不能使普通打开变成隐式建库 |
| 业务存储生命周期调整 | 受控 API 验证 URL 乱序、失败标记、关联冲突、超过 24 小时的元数据覆盖与重启复用；清单检查与内容同事务回滚、重启状态保持；收藏提交与淘汰互斥、历史、累计口径及离线命中保留 |
| 视频与块的位置 | 真实跨卷发布和导入，引用中的块、取消及强杀后的安全清理；工作区固定随 EXE，不随配置或视频换盘；发布暂存仍在目标卷；不误删未知文件 |
| 已有视频接入 | 重建配置、换数据库后复用非空视频目录；确认保留策略前不触发自动淘汰，确认后按当前数据库与原有算法执行，不误称原收藏保护仍有效 |
| 磁盘耗尽及 I/O 故障 | 在可丢弃受限卷中制造真实耗尽并配合故障注入；失败能显示、可停止退出、已提交核心数据保持，下次能执行安全清理；不填满用户日常磁盘 |
| 备份和还原 | 正常退出后备份并在隔离位置还原数据库及必要伴随文件；配置重设、视频另置时个人数据仍完整；不把单个主文件的在线复制当作已验证备份 |
| 辅助材料 | 验证坏 key、坏条款、失效凭据和坏测速状态的局部失败；沿用 Mihomo Home，不因路径整理升级依赖或新增删除规则；需清理具体第三方文件时补真实路径证据，跨用户保护另记环境边界 |
| 使用与发行 | 浏览器和 Portable 隔离检查覆盖升级只替换发行文件、实际路径显示、保存后重启及中文／空格路径；不用内存夹具代替真实文件结果 |

开发检查沿用[开发说明](development.md)中的临时目录包装器、Go 测试／vet、前端构建／检查和浏览器工具。按改动和未决事实选择验证，不为文档修订机械运行全部产品检查。下面按阶段保留取得的证据；较早记录中的“尚未完成”指当时范围，当前缺口以本节状态和逐项核对为准。

### 本轮已取得的验证证据

- 2026-10-26 11:36 受限卷实测完成：用户确认 UAC 后，在源码 `7f15417` 的干净工作区构建并执行 `TestBoundedVolumeExhaustion`。测试二进制 SHA256 为 `0ADC38F0922F476BB97A0E74154975B010A06182BDA887607C646AB136ADB852`。脚本新建固定 128 MiB VHD，实际 NTFS 容量 133099520 字节，宿主卷创建前剩余 58186813440 字节；三个场景均填至可用空间 **0**，取得真实盘满错误。块写入与视频发布返回存储故障、停止成功，独立 DB 的个人偏好保持；下次工作区打开回收已知块残留并保留未知文件，失败发布无正式视频和暂存残留。数据库场景同时验证同步写失败、后台写失败和关闭错误；释放空间、重新打开后十四张表全部列值保持，`integrity_check=ok`，新写入成功。三组全部通过，退出码 0。镜像已卸载并删除；独立复核确认原盘符消失、镜像不存在、运行器和测试进程均退出、二进制哈希匹配。证据位于 `bounded-volume-7ebe935085664b2bbc7e2823db5d2f3d/` 的 `test.txt`、`harness.txt`、`result.json`、`cleanup-check.json`；未填满日常磁盘，未改产品代码规避故障。

- 2026-10-26 收尾核对：核对本计划六阶段要求、文件分类、全部 14 张业务表及当前代码调用方；`go.mod`、`go.sum`、v5 DDL 相对基线无改动。配置、快照、身份、锁、工作文件、UI 与发行证据的对应关系见下表。新增 `TestStoreCloseDrainsAcceptedBusinessEvents`：占用唯一连接后排入 32 首歌曲的需求和请求记录，不显式 flush，直接关闭应用 Store 后重开，核对 32 条请求和累计需求完整。该用例及已有后台失败用例、定向 `-race` 通过，证据 `close-drain-1.txt`、`close-drain-race-1.txt`。

- 受限卷脚本准备阶段：新增 `TestBoundedVolumeExhaustion` 与 [test-storage-full.ps1](../scripts/test-storage-full.ps1)，覆盖块写入、视频发布、无引擎数据库同步／后台写入、关闭、释放空间后恢复与已提交数据核对。普通运行默认跳过；脚本仅创建新的固定 128 MiB VHD，校验磁盘身份和容量后格式化，完成后卸载并删除自己创建的镜像。编译、Go vet、PowerShell 语法及输出边界拒绝检查通过；首次 UAC 启动被 Windows 取消，未获得测试进程 PID，未产生运行目录、镜像或挂载。保留当时未实测的证据 `full-volume-preflight-1.json`、`full-volume-vet-1.txt`；后续取得用户授权后的实测结果见上项。

- 2026-10-26 数据与交付验收：在现有 v5 二进制样本副本补齐四张空表，十四张表均有样本；正常关闭后复制数据库及存在的 SQLite 伴随文件，再在中文含空格的新位置还原。核对所有列和值、原结构和版本、integrity_check、foreign_key_check；视频位置尚不存在时仍可读个人偏好，另置视频目录并启停引擎后数据不变。原库与备份逐字一致，证据 `backup-restore-1.txt`。C、D 为独立 NTFS 卷；以跨卷硬链接返回 ERROR_NOT_SAME_DEVICE 证明测试落点确实不同，块引用仍有效时发布完整视频成功，发布暂存与视频同卷，跨卷导入为独立副本且无暂存残留；已有跨卷导入／回执删除／淘汰用例一并通过。证据 `cross-volume-1.txt`、`cross-volume-environment.json`，D 盘本次随机目录已清理。

- 两个实际开发版 EXE `dev-persistence-paths-20261026` 与 `dev-persistence-explicit-20261026` 的覆盖升级通过默认、外置库和自定义配置三种布局；验证进程身份、实际设置、同用户凭据解密、快照、数据库与视频哈希、日志延续和退出后重开，所有测试进程已退出。二者均为带 dirty 标记的开发构建，完整身份和 SHA256 见 `delivery-upgrade-1.txt`；新 EXE 构建输出为 `delivery-final-build-1.txt`。两个版本均使用 v7 配置和 v5 数据库，此项不证明旧配置迁移、正式发布版本升级或跨用户解密。以上证据位于 `artifacts/persistence-implementation-20261026/`。

- 受限盘初始环境核对：本机有 Hyper-V 虚拟磁盘命令，但原令牌不是管理员，WSL 未安装；当时尚未创建、挂载或填充虚拟磁盘。已有 SQLITE_FULL 页数限制回滚及 I/O 故障注入证据未被当作真实卷耗尽；后续通过有界脚本及用户确认 UAC 补齐实测，没有安装 WSL 或填满日常磁盘。

- 2026-10-26 核心旧入口清理完成：删除 Config.StorageDir、New 的隐式建库／混合根分支、旧数据库打开函数、mixedWork 清理及引擎自行关闭数据库的分支。全部入口和夹具明确准备 Store、VideoDir、Work。持久化回归真实关闭并重开数据库；强杀恢复分别重新取得工作区和视频所有权，块、发布和导入残留按各自位置清理，未知旧文件及完整视频保留。核心整包通过（`legacy-core-tests-4.jsonl`），桌面整包通过（`legacy-console-tests-4.jsonl`），CLI／桌面命令包通过；最终定向回归、相关 race 和 vet 通过（`legacy-core-targeted-6.txt`、`legacy-core-race-1.txt`、`legacy-core-vet-1.txt`）。证据均在 `artifacts/persistence-implementation-20261026/`。保留首次编译及测试失败记录：旧夹具试图替换含持有锁的工作目录，失败后工作 goroutine 未返回；模拟时钟夹具不应构造全局网络对象；延迟统计测试曾在后台清理仍运行时更改保留期，现先停止采集再按固定时点验证。未据这些检查宣称真实跨卷、受限盘或用户数据迁移通过。

- 核心旧入口清理第一步：删除未使用的混合目录 `InspectStorage` 和“缺库即空状态”读取助手；统计 worker 不再自行打开／关闭数据库，去掉 ownDB 分支，测试改为明确创建夹具。低级只读根助手仅留在测试文件中。统计、历史、缓存管理、应用所有权和目录联接创建拒绝的定向回归通过，相关 race 与 vet 通过；证据为 `legacy-readers-targeted-2.txt`、`legacy-readers-race.txt`、`legacy-readers-vet.txt`。原测试把明确建库留下的锁文件误当成引擎启动，已改为核对视频引擎资源未生成，首次失败记录保留。Config.StorageDir 与核心 New 的混合根适配尚待移除，此步骤不等于旧入口全部清完。

- 2026-10-26 桌面独立位置（历史验证；其中手动首次建库流程已被后续首次使用修正替代）：配置 v7 使用 databasePath/videoDir，初始均为空，不自动接管已有默认库；相对路径仍基于配置父目录，状态、条款和自定义实例归属不变。创建 API 只建库，重复创建不覆盖；新视频位置在保存时可创建，失联但未变的视频位置不因其他保存而重建。接入已有视频或更换数据库复用视频，保存前须确认当前保留策略，确认不入配置。无效／旧配置保留原文件。后端整包及相关命令入口通过；60 项浏览器回归通过，窄屏截图已检查。真实 Go 服务与 Edge 验证首次新建、保存、重开后两个独立位置生效，以及拒绝覆盖已有库，未启动视频引擎。输出位于 `artifacts/persistence-implementation-20261026/desktop-paths-*`、`storage-live/`。早期旧夹具自动建库假设、JSON 样本转换丢失大整数及浏览器误用 alert 选择器的失败均已修正，原日志保留；当前 v5 数据结构和历史样本未改。真实跨卷、受限盘耗尽和完整数据恢复验证尚未完成。

- 桌面阶段补充：位置创建／保存／重开、配置缺失不接管已有默认库、空视频位置不回落旧目录及路径空白处理的定向 `-race` 通过。打包规定的全部 `cmd/...`、`internal/...` 测试及 `go vet` 通过，10 项验收请求协议测试通过；布局顺序调整后的 8 项浏览器回归通过。真实开发 ZIP 冒烟通过首次启动、条款门禁、同包覆盖、整包移动、默认工作区身份稳定、两个外置自定义配置各自持锁且 work 仍在 EXE 旁。证据为 `desktop-paths-race-4.txt`、`desktop-paths-package-2.txt`、`desktop-paths-script-tests-2.txt`、`desktop-paths-browser-2.txt`、`desktop-paths-portable-smoke-2.txt`；原始失败记录保留，包括新增测试未遵守内部锁前置条件、删除旧探测函数后的未使用导入、旧 Portable 清单遗漏 work。此处 Portable 为同包覆盖，不声称跨版本或真实游戏通过。

- 2026-10-26 快照格式 2：分别记录数据库与视频位置，任一位置变化时忽略旧结果；相对位置随整包移动恢复，旧格式拒绝读取。实际占用原子写槽模拟保存失败：扫描、成功批次及最近任务仍发布本次内存结果，磁盘旧快照逐字保持，重启只读取已保存结果且不续跑任务。定向检查和相关 `-race` 通过，输出为 `artifacts/persistence-implementation-20261026/snapshot-v2-*.txt`。首次新增夹具因同版本清单更改内容被现有保护拒绝，修正其版本后通过，失败记录保留。

- 2026-10-26 应用数据库接线：`cacheproxy` 与 `console` 整包通过，CLI／桌面命令入口测试通过，元数据、偏好、VRCX、共享存储与关闭的定向 `-race` 通过，相关 `go vet` 通过。桌面无引擎删除用应用已持有的 Store，删除后仍可查询，未启动引擎；视频缺失时元数据及偏好仍可写。前端构建及 VRCX 7 项浏览器回归通过，已检查窄屏预览；当前主位置设置仍为旧根，VRCX 覆盖确认中的目标已经是数据库文件。原始输出为 `artifacts/persistence-implementation-20261026/application-store-*.txt`；早期完整回归的旧夹具重复开库、把缺失库当空库以及引擎空指针失败均保留，修正到真实应用生命周期后复核通过。没有运行用户数据迁移或真实游戏验收。

- 2026-10-26 独立路径：核心引擎接收已打开 `Store`、既有 `VideoDir` 和固定 `Work`，只可创建视频根内 `.swd-tmp` 子目录；库列表与删除作用域包含 DB 和视频，偏好只绑定 DB。视频盘缺失不再显示零库存，目录不可用时仍显示歌曲资料；本地状态筛选拒绝未知结果。`.swd.lock` 与旧 `videos` 父目录 `.lock` 联合排他，数据库落入受管视频／发布路径、块目录与重要资源重叠均拒绝。v5 数据结构无改动。CLI 改为必填 `-database`、`-video-dir`；`-create-database` 只建库后退出，已有视频必须显式给保留策略。桌面仍保留临时旧布局适配，不能将此阶段当作整个迁移完成。
- 验证：下载核心、Console 与 CLI 整包通过；独立目录所有权、旧锁冲突、旧删除凭据、离线读取、发布／导入及扫描保护的定向检查和 `-race` 通过，相关 `go vet` 与前端构建通过。曲库浏览器回归原有 27 项通过，新增视频不可用用例修正文本定位后通过；截图已检查。真实 CLI EXE 在中文含空格隔离路径明确建库成功，重复创建拒绝且数据库逐字保持，未生成视频／块文件；未接受条款或启动服务。输出见 `artifacts/persistence-implementation-20261026/paths-*.txt` 与 `cli-probe/creation-report.json`。首次沙箱回归的文件操作限制、两处仍使用旧扫描锁键的测试夹具，以及浏览器定位失败均保留为历史失败；最终针对修正点复核通过。尚未验证真实跨卷、磁盘耗尽或桌面新设置流程。

- 2026-10-26 工作区实现：`cacheproxy`、`console`、`apppaths` 和两应用入口整包通过；新增分离写入／发布目录故障／导入、队列临时块保护、工作区所有权、移动与路径冲突的定向 `-race` 通过，相关 `go vet` 通过。隔离桌面 EXE 先以坏配置退出，再人为阻止日志文件创建；第二次启动仍先删除本实例块残留，未知文件、独立放置的数据库和完整视频夹具逐字保留。进程均已退出，未接受条款、启动游戏或改 hosts。报告在 `artifacts/persistence-implementation-20261026/startup-probe/report.json`，测试输出在同级 `work-*.txt`。此探测证明启动次序与保护范围，尚未覆盖真实跨卷、受限盘耗尽或完整新版 Portable 流程。
- 2026-10-26 阶段二核心实现：新增无引擎业务读写、引擎借用／归还数据库、关闭后拒绝工作、重复引擎不清理活动块，以及后台只读／事务失败可见的回归。`internal/cacheproxy` 与 `internal/console` 整包在沙箱外通过；共享存储、清单、歌曲 URL、元数据候选、收藏、需求统计及导入的定向 `-race` 通过。沙箱内整包曾在硬链接、目录联接与进程终止夹具处失败，故使用同一临时目录包装器在沙箱外复核；早期回归还发现了事务失败标记遗漏及无存储诊断夹具的空指针，均修复后重跑。原始成功输出在忽略目录 `artifacts/persistence-implementation-20261026/`。这些结果只覆盖当前核心拆分，尚不证明新路径、桌面接线或界面已完成。
- 基线源码无产品改动。通过临时目录包装器执行 `go test -tags=no_easytier`、`-count=1`：`internal/cacheproxy` 的 `LocalCatalogChecksAndSnapshot`、`CatalogCheckStatusSurvivesRestart`、`CatalogCheckMigrationAndRollback`、`LoadCatalogStatusRejectsMissingTrackingSchema`、`SongURL.*`、`SongMetadataCandidates.*` 用例通过；`internal/apppaths` 的 `LayoutIsLazyAndSeparatesSiblingConfigurations`、`ReservedCompanionsCannotBeConfigurations` 通过（名称均带 `Test` 前缀）。
- `internal/console` 的 `StorageEntryPointsRejectLostLibrary`、`InventoryPersistsAndKeepsLastSuccessOnFailure`、`ScanOnlyPersistsSuccessAcrossFailureAndCancellation` 在沙箱内于夹具配置保存阶段报 `renameat ... The handle is invalid`，未达到业务断言；通过相同包装器在沙箱外复核后全部通过。保留这次失败及其范围，不将其当作业务失败或省略不报。

### 收尾逐项核对（2026-10-26）

本表核对当前实现，而非只检查是否存在同名测试；证据文件位于 `artifacts/persistence-implementation-20261026/`。早期通过记录只覆盖其实际构建，核心旧入口删除后的整包回归及后续定向验收补齐变化范围。没有执行用户资料搬迁、真实游戏或跨用户解密。

| 要求与阶段 | 当前代码与可复核证据 | 结论 |
| --- | --- | --- |
| 六类职责、全部文件与 14 张表；阶段一、四 | 上述逐项归属清单；`apppaths`、`config_file.go`、`store.go`、`work_directory.go`、`snapshots.go` 与运行生命周期互相核对。v5 DDL 及依赖锁相对基线无差异；未新增辅助资料空目录、第二业务库或状态历史库 | 已核实；保留已有小文件各自语义 |
| 独立位置、默认值、自定义配置与 CLI；阶段一第 1、5 项，阶段三、五 | `apppaths/paths.go`、`work.go`，`storage_selection.go`、`storage_api.go`、两个 main；`storage_api_test.go` 覆盖相对路径、配置丢失、明确建库与重启，`work_test.go` 和 Portable 冒烟覆盖外置配置、整包移动及 work 固定随 EXE | 已验证；不引入工作路径选项或整根切换 |
| 普通打开不建库，损坏／空／未来格式保留；阶段三 | `OpenStore` 只用 `rw`，只读入口 `ro`，`CreateStore` 使用排他创建；`store_test.go` 核对拒绝后原文件逐字不变。无 `rwc` 或核心 `StorageDir` 兜底；旧字段只用于明确拒绝 | 已验证；未知数据不自动修复或替换 |
| 应用持有 DB，无视频时元数据、偏好和历史仍可用；阶段二 | `business_store.go`、Monitor、VRCX、只读历史与离线删除入口；`store_business_test.go`、Console `business_store_test.go` 及 Monitor 测试核对未创建视频目录／引擎／采集起点，关闭释放数据库 | 已验证；不是仅将连接指针搬位置 |
| 事务、映射、收藏与淘汰协调；阶段一第 3 项、二、六 | 对照基线核查 `catalog.go`、`song_urls.go`、`song_policy.go` 的事务及共享 `mappingMu`／`retentionRunMu`。URL 顺序、失败标记、关联冲突、过期 URL 仍供元数据覆盖，清单失败回滚与重启均有业务断言；核心整包与相关 race 通过 | 已验证；`song_urls`、`catalog_checks` 保留，无拆表或行为简化 |
| 各资源排他与旧布局冲突；阶段一第 4 项、三 | `store.go`、视频 lease、`WorkDirectory` 与配置锁；不同 DB 共用视频、同 DB 多入口、旧父目录锁和重解析点拒绝均由相应 Store／独立路径／Windows 测试覆盖。重复引擎在清理之前拒绝 | 已验证；不删除锁解锁，不增加永久绑定文件 |
| 已接受写入排空、同步及后台错误；阶段二、六 | `Console.Close` 先停消费者／引擎，`Store.Close` 等已进入操作和 usage worker 后关闭连接。新增关闭后重开计数断言与失败报告用例通过，有限队列和 `usage_dropped` 语义对照基线不变 | 已验证正常排空、故障注入与真实盘满后的错误报告及恢复 |
| 三份最近结果与身份／条款／测速／凭据；阶段一第 2 项、四 | 快照 v2 按 DB+视频归属，旧格式忽略；占用工作槽时本次内存结果仍发布、旧磁盘结果逐字保持。`proxy_identity_test.go` 验证实例一致性与坏 key 局部降级；条款写失败不授予同意、代理密文原子保存与自动测速冷却回归保留 | 已验证；不恢复任务、选择或删除授权，不新增用户维护步骤 |
| 块、发布和导入落点及安全清理；阶段三、四、六 | `work_directory_test.go`、`temp_crash_test.go` 覆盖所有权、未知项与强杀恢复；启动故障探测证明清块早于日志及 DB。`cross-volume-1.txt` 以 `ERROR_NOT_SAME_DEVICE` 验证 C/D 真跨卷，发布暂存仍与视频同卷、导入为独立副本，引用块保持 | 已验证；未将临时块并入完整视频 |
| 已有视频接入与保存生效；阶段五 | `inspectLibrarySelection` 与保存时复查、`storage_api_test.go`、`storage-locations.spec.ts` 覆盖换 DB 复用视频、保留策略确认、取消不落盘、不保留模式可改位置及失联视频不阻止无关保存。确认不写入配置 | 已验证；不自动搬移或静默接管旧视频 |
| UI、真实服务与发行使用；阶段五、六 | 60 项浏览器回归及最终布局 8 项回归；真实 Go 服务与 Edge 的 configure/reopened 结果无页面错误；实际 ZIP 首启、覆盖、移动和多配置，以及两个开发版 EXE 三种布局升级通过。发行检查和 notices 校验通过 | 已验证对应构建与同用户范围；不声称正式版本／旧配置升级或游戏通过 |
| v5 接入、停机备份还原与回退说明；阶段一第 6 项、六 | `storage_backup_test.go` 对实际 v5 二进制样本的 14 表全部列值、结构、完整性及关系逐项比较；关闭后复制主文件和存在的伴随文件，新位置／独立视频启动后数据不变。备份说明区分核心优先、视频按需及完整媒体库；旧程序回退要求配套旧配置和布局 | 已验证隔离副本；未操作用户原资料 |
| 可丢弃受限卷真实耗尽；阶段六 | `storage_full_windows_test.go`、`scripts/test-storage-full.ps1` 在新建固定 128 MiB VHD 上实际执行，三场景都写至零可用空间；错误、关闭、十四表数据保持、重开写入与安全清理全部通过，镜像及挂载独立复核已清除 | 已验证；真实操作系统耗尽，未以 SQLITE_FULL 注入替代 |

本轮实现与非游戏验收已完成。真实 VRC 播放、日志交接和多人体验继续按既有实机台账安排，不为此次文件归属整理另增机械测试项；这不等于本轮已测游戏。

- Mihomo 隔离启动探测见上文；探测源码位于忽略目录 `artifacts/persistence-review-20261026/mihomo-files.go`，临时 Home 随包装器清理。没有读取用户配置、使用真实节点、启动游戏或改 hosts。
- 初始复核时尚未实现新目录布局，未验证该布局、跨卷、磁盘耗尽和升级后界面；这些缺口已由前述分阶段实测补齐。资源锁交接、提交与淘汰协调、固定 work 实例归属及早期清理的要求保留，验证依据见逐项表。实际用户资料搬迁没有执行，隔离备份还原不被称作用户资料已经迁移。

上述场景由开发者准备和验证，默认不需要 VRC，不启动游戏、不改 hosts。强杀、VM 关机和真实断电分别描述，不能互相冒充。真实环境缺口按[验证机制](live-acceptance-process.md)记录，不机械新增游戏台账项。

## 边界与文档完成条件

- 本轮使用隔离目录和副本完成实现与验收，没有对用户正在使用的数据执行迁移、改库、移动或删除。以后实际迁移须按已明确的来源、范围和备份执行，不能把手改版本号当作结构升级。
- 不随本轮修改视频保留算法、块调度策略、硬链接导入模式、网络能力或请求统计口径；保留上次结果查看，解除其与核心数据创建及当前结果展示的耦合，不扩展任务恢复能力。
- 不直接继承旧整根方案的快捷方式、两锁上限、配置与数据库绑定或 CLI 目录标记判断；它们不再是实施依据。
- 随各阶段更新 `storage.md`、`runtime-lifecycle.md`、`settings-lifecycle.md`、`song-catalog-design.md`、`upstream-channels.md`、`service.md`、界面说明及打包／验收脚本中实际受影响内容；原验证记录保留适用构建和历史失败。
- 规则在职责设计维护，路径、格式及恢复操作已同步现行契约；本计划记录各阶段实现、验证范围与历史失败，作为完成记录保留。上游通道说明中的 key 备份措辞也已改为按需保留，曲库说明的旧格式保护按独立 DB／视频资源表述。
- 初始记录阶段只交付原则和计划；当前已按后续授权分阶段实现并本地提交，不推送。完成状态以各阶段的实际代码和验证结果为准。
