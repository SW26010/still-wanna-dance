# 非 VRC 验证分流（2026-10-16）

本文件承接[旧台账](history/live-acceptance-backlog-20261016-archive.md)中不需要启动游戏的验证，避免迁出后失去跟进。它不是新的逐改动登记表，也不要求把原 46 项全部换个地方重测。执行者为负责相关变更的开发者/agent：先核对已有证据与当前实现，只补不足部分；正常结果记回对应开发验证文档，真实阻塞写清环境和下一步。

**2026-10-16 的整理只完成分类，没有重跑历史产品测试，也没有宣布迁出项通过。** 后续修复结果按实际覆盖补记。下表的“已有依据”是复用入口，不代表任意新构建都已验证。原子文件写入、真实重启、系统控件或账号隔离不能由不覆盖这些行为的内存夹具冒充。

## 已知失败优先处理

- **LA-010：存储原因和重启提示缺失已于 2026-10-19 修复并完成开发验证**，见[修复结果](#la-010修复与验证2026-10-19)。[LIVE-20261015-01](acceptance/live-acceptance-20261015-01.md) 的 UI 失败仍作为历史记录保留；本次以故障注入、真实服务与浏览器补齐，不要求再进游戏复现。
- **LA-020/040：限速 Seek 未恢复/长等待和聚合慢网卡顿。** 游戏复测留在[LA-040](live-acceptance-backlog.md#la-040慢网拖动多播放器与后台竞争)；开发者继续基于历史日志定位、构造可控回归和核对后续修复，不能预设 A3 容量修复或其他自动回归已解决其根因。

### LA-010：修复与验证（2026-10-19）

`internal/console/console.go` 从共享引擎直接读取 `StorageError()`，通过 `applog.SafeError` 处理后返回状态；`webui/src/app.tsx` 复用公共 Fluent MessageBar 展示首个原因、停止新下载的影响、已有缓存可读及修复后的恢复步骤。没有另存故障状态或增加重试机制。生命周期规则统一见[配置与运行约定](settings-lifecycle.md)。

- 控制台定向回归通过：`TestLocalStorageFailureStopsConsoleFallback` 覆盖地址观察/资源索引写库失败及临时文件创建失败的状态输出；`TestStorageFailureStatusSurvivesRecoveryUntilRestart` 在真实独立目录注入创建、发布重命名故障，验证修复目录、读取完整缓存及两次网关启停均保留首个错误，不重下；关闭并重新创建 Console/引擎后恢复。既有操作错误隔离回归也通过。
- 缓存层定向回归通过：`TestStorageFailureStopsDownloadsButKeepsHits`、`TestPublicationDistinguishesLocalAndUpstreamFailures`、`TestParallelRangeCleanupPreservesBlockWriteFailure`、`TestDownloadedValidationDistinguishesStorageAndIntegrity`。覆盖块存储目标失效、发布失败、写库之外的本地 I/O 分类，以及上游失败、校验失败和取消的对照；没有用真实磁盘耗尽制造故障。
- Edge 的 `storage-failure.spec.ts`、`recovery.spec.ts`、`console.spec.ts` 共 **17 项通过**。新增场景覆盖轮询、切页、网关操作、连接中断保留错误、恢复状态后移除提示、错误文本转义，以及 320px 下长路径换行。
- 另用隔离测试进程承载生产 Console、缓存引擎、SQLite 与内嵌页面，Edge 连接真实 HTTP API，未模拟页面状态。临时目录替换为文件后触发冷下载失败；修复目录后旧进程仍返回同一原因，已有缓存 GET 为 HIT/200 且正文一致，后续冷请求不增加受控上游计数。真实启停网关后提示仍在；退出旧进程，在同一存储启动新进程，确认新会话、提示消失及冷下载成功。此检查验证真实服务和进程边界，不替代发行入口自动拉起、托盘或游戏体验。1280px 首页和 390px 运行状态截图已检查，探测进程及监听端口已退出。
- 前端构建与 `pnpm --dir webui check`、`go vet -tags=no_easytier ./internal/console`、控制台 `no_easytier` 构建通过。原始日志、探测源文件、截图和 `real-browser-results.json` 保存在本地忽略目录 `artifacts/la010-fix/`，私密路径及条款凭据副本不入库。

控制台测试的沙箱首轮因 `renameat: The handle is invalid` 无法创建配置，Edge 首轮因沙箱内进程管道中断无法启动；沙箱外使用同一临时目录入口重跑通过。首次独立探测另遇包装器单参数传递、同 URL 导航保留旧页面两项脚本问题，修正探测调用后通过；失败输出保留，未修改产品代码绕过检查。历史游戏失败报告不改写为通过，本次错误展示缺口已关闭，无需新增 VRC 待验项。

## 在开发环节完成的检查

2026-10-25：四步引导已移除，LA-029 中的步骤切换、独立草稿、最终应用、回执续接与目录选择等专属场景不再适用；不记为验收通过。条款门禁、普通设置、曲库导入和网络诊断继续按现行入口验证，见[移除后的验证](validation/webui-validation.md#2026-10-25-移除使用引导保留条款确认)。Windows 文件选择器用于 VRCX 与当前业务数据库，原生交互仍按下表跟进。

2026-10-26：存储职责与目录调整已有[验证记录](persistence-migration-plan.md#2026-10-26-目录归位与状态命名)，包括真实 Console/浏览器首次使用、真实 ZIP 首次生成与搬迁、两份同布局 EXE 的默认/外置/自定义配置覆盖升级，以及伴随文件和数据库别名保护。此前同日完成的[真实跨卷、v5 全数据备份还原及 128 MiB NTFS 受限卷耗尽](persistence-migration-plan.md#本轮已取得的验证证据)继续按原范围复用；块写入、视频发布及数据库写入/恢复三组耗尽场景已通过，不再笼统列为“磁盘满未验证”。这些结果不证明真实断电、所有文件系统或最终正式包已验收；本次文档核对没有新增测试结果。

| 合并范围 / 原编号 | 已有依据与适合的检查 | 剩余工作的处理方式 |
| --- | --- | --- |
| 网页、条款、配置和展示：LA-001/002/013/021/023/026/029/031/032/033/036/043/046/047，A7 | [UI 验证](validation/webui-validation.md)、[配置生命周期](settings-lifecycle.md)、[视频存储预算验证](validation/cache-capacity-validation.md)；`webui/e2e` 与控制台集成/重启测试；[旧引导验证](validation/issue-16-validation.md)仅作历史依据 | 浏览器完成草稿、切页、错误、时间/图标、零值/非法输入、saved/active 等；涉及真实库、保存与重启时连接隔离服务。原 LA-033 的网页门禁与条款展示已归此类；条款变化后的实际确认、托盘/辅助技术见下表，不追加游戏项 |
| 存储、导入、升级/搬迁、快照及隔离：LA-003/004/007/013/014/015/022/023/024/047，A5/A8 | [存储约定](storage.md)、[文件生命周期](runtime-lifecycle.md)、[持久化验证](persistence-migration-plan.md)、[Portable 检查](validation/issue-26-validation.md)、[视频存储预算验证](validation/cache-capacity-validation.md)；`internal/cacheproxy`、`internal/console` 测试 | 复用上述 2026-10-26 结果，按后续变更补必要检查；最终候选 ZIP 的覆盖升级仍需按实际版本记录。隔离副本、文件哈希和数据库检查验证发布/取消/清理、冲突/损坏拒绝、未知文件保留、导入回执及重启；真实断电另外区分 |
| 网络、代理、Monitor、元数据：LA-005/016/017/019/030/034/035/042/045，A1/A2/A4/A5/A6/A7 | [回源通道](upstream-channels.md)、[B2 验证](validation/issue-14-b2-integration.md)、[现行 UI 验证](validation/webui-validation.md)、[URL 元数据验证](validation/song-url-metadata-validation.md)；受控代理/API、回环服务、SSE 与浏览器 | 先复用候选失败/重定向/取消/并发/草稿隔离/事务检查；真实出口可由请求工具验证。凭据跨账号/机器的部分见下表，只有实际播放器接入兼容性留在 LA-025 |
| 调度、共享读取、续传、容量和生命周期：LA-008/019/020/037/038/039/040/043，A1/A3 | [调度验证](validation/block-scheduler-validation.md)、[需求调度验证](validation/demand-scheduling-validation.md)及[旧台账中的 A1–A8 结果](history/live-acceptance-backlog-20261016-archive.md)；受控 HTTP、确定性时序、race、哈希 | 已有边界检查按覆盖复用，缺口在开发环节补。需要虚拟时间、精确取消/写入竞态或数千任务的条件不交给游戏用户；旧 6/4 路和 1 秒宽限等不再适用 |
| 持续负载、退出与资源：LA-012/018，关联 LA-035/040 | [Windows race 收尾](validation/issue-14-b2-integration.md#windows-发行与-race-收尾)、[定向检查](validation/issue-14-directed-validation.md)、[日常会话报告](acceptance/live-acceptance-20261015-02.md)；进程/资源采样、自动持续负载及日志轮转 | 既有约 2.5 小时会话及数据库结果保留；资源趋势、取消泄漏、句柄独占、端口退出和轮转由 agent 采样。只有具体游戏内卡顿问题保留 LA-040，不再泛泛要求用户陪跑长时间 |

2026-10-16 A1–A8 与后续等价重构的已执行自动检查保存在[分流前快照](history/live-acceptance-backlog-20261016-archive.md)中，包括全产品范围 race/vet/build 与明确的沙箱限制/重跑结果。它们按实际范围作为开发证据，不再把每个通过的故障注入用例复制成游戏验收步骤。

## 确实依赖 Windows 环境的部分

这些检查不需要 VRC。下表区分已取得的局部证据和剩余环境边界。2026-10-26 后续已实际开展[本机原生验收](validation/windows-native-validation.md)，与同日较早的文档核对分别记录。能使用当前工具和隔离环境完成时由 agent 执行；确实缺专用条件时，只在对应功能记录阻塞，不要求用户为普通开发准备全套环境。

| 范围 / 原编号 | 具体条件与后续处理 |
| --- | --- |
| 托盘、原生选择器、单实例、UAC/hosts：LA-006/022/029/031/046 | [2026-10-26 本机原生验收](validation/windows-native-validation.md)已覆盖真实托盘启动/运行中灰显/退出、同安装重复启动、不同目录/配置原生冲突提示、当前数据库选择器取消/选定、UAC 同意/取消，以及托盘和网页退出恢复。取消错误乱码已修复并真实复测，结束后 hosts 哈希恢复、进程与端口清理。原生图标及[隔离权限助手](validation/hosts-session-validation.md)证据继续复用。剩余持久化 hosts 模式、其他接入故障、另一管理员身份、任务栏重建及 VRCX 专用原生入口按实际需要补测；不把通过范围扩大为所有 Windows 场景。退出恢复仍按现行 [hosts 契约](console.md#hosts) |
| 条款重新确认、CurrentUser 与跨用户/机器：LA-009/017/033/034 | [本机原生验收](validation/windows-native-validation.md)已由操作者确认当时条款，并验证同用户覆盖 EXE、重启和完整目录搬迁后保留原凭据；内嵌过时告知已纠正为 `2026-10-26.1`。该批次使用历史单条格式，不作为当前格式兼容承诺。[8 槽验证](validation/issue-15-validation.md#2026-10-278-槽确认记录)已覆盖新格式保存、淘汰、模拟环境切换、真实同用户 DPAPI 新进程和浏览器重开；旧单条格式明确不兼容。[原有条款门禁](validation/webui-validation.md#2026-10-25-移除使用引导保留条款确认)、[条款凭据步骤](validation/issue-15-validation.md#待环境验收la-009)及[节点验证](validation/issue-14-validation.md)按覆盖复用。旧条款已确认后升级的实际重新确认、写入失败的人工重试，以及第二 Windows 用户/独立机器解密边界仍未测；操作者原批次只安排当前机器。不可解密时应按保留上限保存其他槽的原密文和其他数据，不隐式切换直连，不能用合成回执代替 |
| 真实注销/关机与取消注销：LA-018 | 需可丢弃专用 Windows 会话；普通进程退出/取消先由集成检查完成。WM_ENDSESSION 语义缺口只在相关平台验证中跟进，强杀不替代 |
| 断电恢复：LA-027 | 需独立可丢弃设备及备份，作为专项可靠性边界保留；VM 硬关机、进程强杀与真实断电区分。没有条件不执行，不为此影响日常机器，不占用游戏验收时段 |
| 系统 DPI、高对比度与屏幕阅读器：LA-028，关联 LA-029/046 | 浏览器键盘、布局和缩放先由 agent 检查；Narrator/NVDA、系统 DPI 和原生托盘大小仅在有对应工具时验证并记录实际条件。普通视口截图不证明系统辅助技术已通过 |

## 如何收尾

非游戏验证不足时，完成说明明确“开发验证尚缺某具体检查/环境”，不能笼统说“开发完成，等用户实机”。已有充分证据的范围直接引用并收尾，环境阻塞不扩散到无关功能。分流范围无需永久留作一套强制门禁；补齐后在相应开发文档写结果，本文件仅保留迁移依据和未解决的实际问题。

新发现确实只有游戏能回答的事实，才按[准入标准](live-acceptance-process.md#vrc-台账准入与合并)并入当前场景或新建最小场景；不因旧文档仍写“实机待验”自动回填。
