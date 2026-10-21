# 证据目录

存储与复算遵循[证据与大文件约定](../evidence-storage.md)。完整实验附件见[归档索引](archives.json)；各批次清单区分 Git 中的小文件与附件中的完整材料。只有公开下载和哈希回验成功的附件才标为 `published-verified`。

实际开展 VRC 验收时，由 agent 按[实机验收机制](../live-acceptance-process.md)准备简明报告及 `live-YYYYMMDD-NN/` 必要脱敏证据（含 `manifest.json`），在本索引增加入口并更新[台账](../live-acceptance-backlog.md)。同一证据可支持多个场景，不要求逐旧编号重复采集；普通开发检查不套用整轮 LIVE 交付。历史证据保持原样，不补造缺失数据或将历史通过外推到当前构建。下方旧记录中的待验编号按[2026-10-16 分流](../live-acceptance-backlog.md#旧编号去向2026-10-16)解释，不自动恢复为当前游戏要求。

这里保存精简、脱敏后的测试证据，使仓库不依赖旧对话即可理解已验证的结论。它们不是完整网络抓包，包含原版调查、StepStash 历史构建和 SWD 各阶段的验证；每项只证明索引所列的构建、范围与条件。证据文件名和观测内容保留采集时的名称与记录。

Fluent UI 迁移已替代旧引导布局；浏览器验证记录见[UI 验证](../fluent-ui-migration.md#验证与待验收)。游戏报告归入 `docs/acceptance/`，开发报告归入 `docs/validation/`；历史证据清单中的源码路径仍指采样时快照。直接收录报告正文的清单随报告移动更新路径与 Git blob 哈希，测量数据、构建身份与结论不变。

## 文件索引

#14 B2 后续定向验证：[热流取消/排空/恢复清单](issue14-directed-20261013/hot/manifest.json)，24 项真实回环检查及有界但不保证恢复的通用模型，见[报告](../validation/issue-14-directed-validation.md)。仅开发测试，不代表实机通过。

同轮[重试/失败反馈清单](issue14-directed-20261013/retry/manifest.json)：19 项受控入口检查、3 包定向回归、初轮反例与沙箱失败；明确本地容量/退役分类仍有接入缺口，不提前决定 B2/C。

收尾复核：[热流会话身份与辅助层修正](issue14-directed-20261013/hot-session/manifest.json)最终 25 项通过，保留实际 WaitGroup panic 和修正后测试；[最终驱动重试复测](issue14-directed-20261013/retry-final/manifest.json)19 项通过。历史轮次未覆盖重写。

| 文件 | 来源与用途 |
| --- | --- |
| [vrchaturllogger-20261021/manifest.json](vrchaturllogger-20261021/manifest.json) | #21 v1427 原程序、原 DLL 定向调用与测试机抓包：104 候选通用测速、真实 HTTP 视频六组三轮、80/443 实际转发及 HTTPS 超时对照；保留未提权直连误启动与全部失败，hosts/设置按哈希恢复。仅机制调查，未接入产品，见[报告](../validation/issue-21-vrchaturllogger-20261021.md) |
| [cf-family-paths-20261021/summary.json](cf-family-paths-20261021/summary.json) | 自动推荐、DNS IPv4/IPv6 分块训练赢家及额外候选四组对照；下午完成 27 次训练、29 次预热/正式加载后因候选消失停止，31 次缺测保留；IPv6 已通过完整资源与 16 路并发，速度和 DNS 边界见[报告](../validation/cf-family-paths-20261021.md) |
| [cf-family-paths-20261021/discovery-summary.json](cf-family-paths-20261021/discovery-summary.json) | 双域名候选失效定位：157 次主动发现及 78 次 HTTP 响应成功；同一视频 DNS 租约过期空档的两次只读空列表与邻近 IPv4/IPv6 TLS 成功；另由夹具复现新短 TTL 提前覆盖旧有效租约。未锁定先前窗口根因，未修改生产逻辑 |
| [cf-block-paths-20261021/summary.json](cf-block-paths-20261021/summary.json) | 当前产品双栈 Monitor 与额外候选的初步两组分块对照；3 资源、12 对正式下载，吞吐中位增益 +26.9%；保留迟滞未选中当轮最快 IPv6 的限制，后续改为独立四组协议，见[报告](../validation/cf-block-paths-20261021.md) |
| [cf-ip-library-20261021/summary.json](cf-ip-library-20261021/summary.json) | cf-speedtester v1.1.2 的回环缺陷复现、18 IP 初筛及独立三轮复测：优选地址相对预检基线约 +26%，保留首次外层限时中断；仅当前网络单资源单连接，见[报告](../validation/cf-ip-library-20261021.md) |
| [live-20261020-01/manifest.json](live-20261020-01/manifest.json) | 手动下一首、自然结束及手动热缓存对照共 9 笔：队首日志首次可读→首 GET 约 0.8 秒，3 秒轮询发现→GET 可为负；保留实际采样边界、播放器缓存条件和原始来源指纹。实验进程已退出，用户选择保留直连，见[报告](../acceptance/live-acceptance-20261020-01.md) |
| [live-20261019-01/manifest.json](live-20261019-01/manifest.json) | #36 独立正文额度与本轮核心实测：正式事件关联供数区间 >768 KiB、≤1 MiB；1 Mbps 截止后真实重试/恢复、零缓存共享/回收、队列滚动与交接、正预算发布及直连恢复。保留预览干扰、冷 Seek 缺口和历史失败；hosts/进程/监听已恢复。完整脱敏附件本地与备份已验证，未发布，见[报告](../acceptance/live-acceptance-20261019-01.md) |
| [live-20261015-02/manifest.json](live-20261015-02/manifest.json) | 新构建日常运行回溯：43 条正式请求/记录一致、37 完成/6 待复查、38 缓存 MD5 正常、37 个队首交接样本；用户整体正常，保留取消、外围游戏错误和退出标记缺口。十项未整项结项；完整逐请求/事件轨迹已归入版本化 ZIP，Git 留摘要和清单，本地/备份及公开下载回验通过，[版本化附件已发布](https://github.com/SW26010/still-wanna-dance/releases/tag/evidence-20261015-live-02-v1)，见[报告](../acceptance/live-acceptance-20261015-02.md) |
| [live-20261015-01/manifest.json](live-20261015-01/manifest.json) | 本轮联合实机已收尾：CF 冷热/队列/续传、两档每连接限速完整播放、存储故障抑制与恢复；保留卡顿/Seek 长等待及错误提示缺口，十项均未整项结项；hosts/权限和进程清理已验证，见[报告](../acceptance/live-acceptance-20261015-01.md) |
| [live-20261014-02/manifest.json](live-20261014-02/manifest.json) | 正式刷新/切节点、预览拖动、同视频双播放器及预览拖动的 1 Mbps/不限速/缓存十二场景；保留旧请求干扰、实际热缓存重分类和反馈缺口。不限速/缓存主要交互正常，限速未恢复；hosts/进程/监听已恢复。[报告](../acceptance/live-acceptance-20261014-02.md)，LA-008/020/037 仍未结 |
| [live-20261014-01/manifest.json](live-20261014-01/manifest.json) | 真实 VRChat GET 限速观测：前四档顺序播放、128 KiB/s 混合探索、256/64 KiB/s 独立正式播放；保留模式不明和切档重连干扰，hosts/进程/监听已恢复。[报告](../acceptance/live-acceptance-20261014-01.md)，LA-008/037 未结项 |
| [cf-cpu-followup-20261014/manifest.json](cf-cpu-followup-20261014/manifest.json) | #20 无 VRC 的等速 CPU 与完整缓存竞争补测：独立本地 TLS 上游、五轮 105 组、零公网流量；含冷/热连接、首读/Seek、内存、取消后关闭、ETW 和 42 组独立 xperf 校核。一次 UAC 的受限助手已退出；见[报告](../validation/issue-20-cf-cpu-followup-20261014.md)。仅实验，默认值未改，LA-037 待验收 |
| [cf-cpu-20261014/manifest.json](cf-cpu-20261014/manifest.json) | #20 单 IP、最高 32 路的 CPU 调度实验：同时启动、每 250 ms 加一路、逐档增加，三轮共 45 组、13,406 个校验通过的请求；包含逐线程/逻辑处理器证据、峰值及独立 xperf 校核，保留流量上限失败和干扰重测历史。见[报告](../validation/issue-20-cf-cpu-20261014.md)；未运行游戏，LA-037 待验收 |
| [cf-high-concurrency-20261014/manifest.json](cf-high-concurrency-20261014/manifest.json) | #20 固定单 IP、新资源 96 MiB 区间的 8/12/16/24/32 路五轮位置平衡对照；26 组、2,401 请求、2,496 MiB，全部保留，见[报告](../validation/issue-20-cf-high-concurrency-20261014.md)。32 路仍有收益，不代表最优默认值；LA-037 待验收 |
| [cf-multi-ip-20261014/manifest.json](cf-multi-ip-20261014/manifest.json) | #20 三个 DNS IPv4 单独及合用的 4/6/8/12 路同总并发对照；三轮共 54 组、1,446 请求、1,620 MiB，全部保留，见[报告](../validation/issue-20-cf-multi-ip-20261014.md)。仅开发实验，默认设置及 LA-037 待验收状态不变 |
| [cf-concurrency-20261014/manifest.json](cf-concurrency-20261014/manifest.json) | #20 固定 CF 资源/直连地址、30 MiB 区间的 1/2/3/4 路及长 Range 对照；保存全部 27 组、607 请求、首轮干扰排除和补测，主分析四个配对。仅上游吞吐开发实验，不改默认、不替代 LA-037，见[报告](../validation/issue-20-cf-concurrency-20261014.md) |
| [issue20-development/development-checks.json](issue20-development/development-checks.json) | #20 最终源码身份、Windows race、构建检查及[同一回环内容的 Range 并发 A/B](issue20-development/benchmark-final.txt)，保留[旧版基线](issue20-development/benchmark-baseline.txt)和[初版等待退化](issue20-development/benchmark-initial-parallel.txt)；边界见[验证记录](../validation/issue-20-validation.md)，真实线路与游戏仍待 LA-037 |
| [issue14-windows-final/manifest.json](issue14-windows-final/manifest.json) | #14 `2ec1b06` 本地 Windows 发行与 race 收尾：全仓 race、五协议 60 秒负载、SQLite 取消泄漏的初轮失败及上游修复回归、实际发行包/完整源码/离线重建；保留修复前证据，仅开发验证，LA-034/035/036、LA-018/012 仍待实机，见[集成记录](../validation/issue-14-b2-integration.md#windows-发行与-race-收尾) |
| [runtime-mihomo-20261013/manifest.json](runtime-mihomo-20261013/manifest.json) | #14 最新要求的完整内嵌 B / 独立进程 C 对照：69 项开发检查、同一 VMess/WS 节点和视频 Range 三轮取样、各 60 秒连续负载、全局状态/故障边界、失败历史及构建身份；无生产切换或实机通过，见[评估与实施设计](../validation/mihomo-runtime-evaluation-20261013.md) |
| [managed-mihomo-20261013/manifest.json](managed-mihomo-20261013/manifest.json) | 完整 Mihomo 独立进程的 31 项开发检查、30 条原生 VMess/WS 加载、前两节点真实 Monitor/播放/视频取样、固定出口与 Windows 进程边界、3 轮性能比较及失败历史；不是生产切换或实机通过，见[报告](../validation/managed-mihomo-validation-20261013.md) |
| [issue14-development/release-validation.json](issue14-development/release-validation.json) | #14 桌面接入后的本地正式构建、实际附件哈希、96 个依赖及完整源码、独立重建和符号证据；仅开发验证，非发布或实机验收，详见[记录](../validation/issue-14-validation.md) |
| [issue14-development/release-validation-final.json](issue14-development/release-validation-final.json) | #14 评审修复后 `acbb673` 的生产 EXE、完整对应源码、全新缓存离线重建及 Portable 隔离验证；最终附件与源码文件哈希复核，仅开发验证，LA-034/035/036 仍待实机，详见[记录](../validation/issue-14-validation.md#最终产品代码的发行链复验) |
| [issue14-development/subscription-import-check.json](issue14-development/subscription-import-check.json) | 用户提供订阅在 `08ae90f` 的导入兼容检查：30 条 VMess/WS 全部超出当前支持范围；未执行节点连接或游戏验证，不包含私人链接或节点参数 |
| [live-acceptance-20261007.json](live-acceptance-20261007.json) | `8b522c5` 联合实机验收：播放、预缓存途中点播/预览、双连接共享下载、故障与桌面生命周期；相对时间及清理前文件校验，结论见[验收报告](../acceptance/live-acceptance-20261007.md) |
| [current-release-live-20260925.json](current-release-live-20260925.json) | 历史构建 `c69e844` portable 联合实机验收：播放、队列、统计、容量淘汰、桌面退出/重启及真实 hosts UAC 操作 |
| [auto-sha-live-20260925.json](auto-sha-live-20260925.json) | 第二次 StepStash 联合测试的阶段性记录：10271 Auto 本地命中、SHA 解析超时与独立 TCP 连接失败 |
| [mvp-acceptance-20260925.json](mvp-acceptance-20260925.json) | StepStash 实网验收：旧库读取、CF/HKG 冷下载、重启、原版回读；不是原版调查阶段数据 |
| [mvp-stream-acceptance-20260925.json](mvp-stream-acceptance-20260925.json) | 冷缓存流式修复后的同矩阵完整实网回归，含实际二进制哈希和首包时间 |
| [mvp-live-20260925.json](mvp-live-20260925.json) | VRChat 联合验收：精简播放/同步事件、服务请求、用户反馈、落盘校验与 hosts 恢复哈希 |
| [hot-http-results.json](hot-http-results.json) | 原版热缓存 HTTP 行为，包括异常 Range |
| [payload-verification.json](payload-verification.json) | 实际响应内容与本地切片的 SHA256 比对 |
| [cold-http-results.json](cold-http-results.json) | 网络修复后的自动完整冷缓存结果 |
| [live-round1-requests.json](live-round1-requests.json) | 第一轮游戏请求，含故障和客户端已有缓存的干扰 |
| [live-round2-requests.json](live-round2-requests.json) | 第二轮实机完整下载及 HKG/Auto 命中 |
| [api-node-behavior.json](api-node-behavior.json) | 原版 API 热/冷缓存对 node 的处理 |
| [candidate-cdn-api.json](candidate-cdn-api.json) | 候选参数试验，不等于 UI 选项映射 |
| [sha-endpoint-probe.json](sha-endpoint-probe.json) | SHA IP 入口的独立超时样本 |
| [generated-metadata-1343.json](generated-metadata-1343.json) | 自动冷缓存生成的元数据 |
| [generated-metadata-1344.json](generated-metadata-1344.json) | 实机冷缓存生成的元数据 |
| [cache-integrity.json](cache-integrity.json) | 对实际缓存和原库文件重新计算的大小/哈希 |
| [network-retest-summary.json](network-retest-summary.json) | 从工具输出整理的网络修复前后结果，不是原始抓包 |
| [game-events-round1.txt](game-events-round1.txt) | 第一轮关键游戏事件，带原日志行号 |
| [game-events-round2.txt](game-events-round2.txt) | 第二轮路由/解析/播放/同步事件，带原日志行号 |
| [manifest.json](manifest.json) | 来源文件、脱敏说明和仓库证据文件 SHA256 |

## 脱敏与保留范围

- Git 内证据只纳入文本记录、脱敏日志摘要、配置差异、文件哈希及由数据绘制的纯矢量 SVG；不携带图片、录像、程序等二进制。完整脱敏材料按存储约定归档并保留可访问链接和校验，不公开完整游戏日志、用户列表、房间实例标识、实际 hosts 文件或备份。
- 移除响应中的 Cloudflare 报告 URL 等非必要遥测字段，保留协议分析需要的响应头。
- 本机绝对路径和用户名以占位符或 `%USERPROFILE%` 替换；公开上游地址、歌曲 ID 和内容校验值保留用于协议复现。
- auth 查询参数替换为 REDACTED。受替换影响的响应路径/Location 无法直接用于重放。
- 原响应体 SHA256 保留为测量值；涉及动态 auth 的响应体本身未携带，不能通过脱敏文本重新计算该哈希。
- 游戏事件只选路由、解析、开始、错误和同步等行，删除颜色标记；不保留用户歌曲队列 JSON。
- 所有计数是观测请求/事件数，不自动等于独立歌曲数或成功播放次数。

## 原始资料位置

在原测试电脑上：

- `<调查工作目录>\investigation`：原版测试脚本、完整原版日志及隔离缓存。
- `<DancingLog目录>\logs\source-vrc-logs`：历史游戏日志；扫描过 68 份，59 份有相关路由记录。
- `%USERPROFILE%\AppData\LocalLow\VRChat\VRChat\output_log_session-070.txt`：实机两轮的游戏日志。

以上路径已使用占位符脱敏；仓库不要求这些目录存在。关键事实已整理为可移植文件；继续调查时才需要访问原资料。

时间已按下述隐私规则转换，不应再按绝对时区时间解释。


## Issue #14 中立增量评估（2026-10-13，开发检查）

[评估报告](../validation/issue-14-incremental-evaluation.md)、[脱敏证据与校验清单](issue14-incremental-20261013/manifest.json)。保留 B2 生命周期、夹具容量失败及对照，不作最终选型；不是 LIVE 实机批次。

## Privacy normalization

Personal paths and real media signature parameters are redacted. Exact capture/playback timestamps are replaced by T+seconds relative to the earliest timestamp in each file; clock-only excerpts use a separate per-file clock baseline. Relative times across different files are not directly comparable. Source log filenames use stable session aliases. Calendar dates in document names and development history remain for version tracking. HTTP capture dates are redacted. These historical evidence URLs are not reusable credentials.

Numeric epoch timestamps are relative milliseconds from a separate per-file epoch baseline. Embedded epoch identifiers in local run paths are also normalized. Manifest file hashes describe the sanitized Git blob bytes (LF), not Windows checkout newline conversions.
