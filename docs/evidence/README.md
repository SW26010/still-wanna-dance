# 证据目录

后续每轮实机验收必须按[实机验收机制](../live-acceptance-process.md)提交报告及 `live-YYYYMMDD-NN/` 脱敏证据目录（含 `manifest.json`），在本索引增加入口，并更新[待验收台账](../live-acceptance-backlog.md)。历史证据保持原样，不补造缺失数据或将历史通过外推到当前构建。

这里保存精简、脱敏后的测试证据，使新仓库不依赖旧对话即可理解已验证的结论。它们不是完整网络抓包。索引中标明 StepStash 的文件属于历史构建验收，其余为原版调查结果。两者均不能替代当前 StepStash 构建的测试；证据文件名和内容保留采集时的名称与记录。

## 文件索引

#14 B2 后续定向验证：[热流取消/排空/恢复清单](issue14-directed-20261013/hot/manifest.json)，24 项真实回环检查及有界但不保证恢复的通用模型，见[报告](../issue-14-directed-validation.md)。仅开发测试，不代表实机通过。

同轮[重试/失败反馈清单](issue14-directed-20261013/retry/manifest.json)：19 项受控入口检查、3 包定向回归、初轮反例与沙箱失败；明确本地容量/退役分类仍有接入缺口，不提前决定 B2/C。

收尾复核：[热流会话身份与辅助层修正](issue14-directed-20261013/hot-session/manifest.json)最终 25 项通过，保留实际 WaitGroup panic 和修正后测试；[最终驱动重试复测](issue14-directed-20261013/retry-final/manifest.json)19 项通过。历史轮次未覆盖重写。

| 文件 | 来源与用途 |
| --- | --- |
| [cf-cpu-followup-20261014/manifest.json](cf-cpu-followup-20261014/manifest.json) | #20 无 VRC 的等速 CPU 与完整缓存竞争补测：独立本地 TLS 上游、五轮 105 组、零公网流量；含冷/热连接、首读/Seek、内存、取消后关闭、ETW 和 42 组独立 xperf 校核。一次 UAC 的受限助手已退出；见[报告](../issue-20-cf-cpu-followup-20261014.md)。仅实验，默认值未改，LA-037 待验收 |
| [cf-cpu-20261014/manifest.json](cf-cpu-20261014/manifest.json) | #20 单 IP、最高 32 路的 CPU 调度实验：同时启动、每 250 ms 加一路、逐档增加，三轮共 45 组、13,406 个校验通过的请求；包含逐线程/逻辑处理器证据、峰值及独立 xperf 校核，保留流量上限失败和干扰重测历史。见[报告](../issue-20-cf-cpu-20261014.md)；未运行游戏，LA-037 待验收 |
| [cf-high-concurrency-20261014/manifest.json](cf-high-concurrency-20261014/manifest.json) | #20 固定单 IP、新资源 96 MiB 区间的 8/12/16/24/32 路五轮位置平衡对照；26 组、2,401 请求、2,496 MiB，全部保留，见[报告](../issue-20-cf-high-concurrency-20261014.md)。32 路仍有收益，不代表最优默认值；LA-037 待验收 |
| [cf-multi-ip-20261014/manifest.json](cf-multi-ip-20261014/manifest.json) | #20 三个 DNS IPv4 单独及合用的 4/6/8/12 路同总并发对照；三轮共 54 组、1,446 请求、1,620 MiB，全部保留，见[报告](../issue-20-cf-multi-ip-20261014.md)。仅开发实验，默认设置及 LA-037 待验收状态不变 |
| [cf-concurrency-20261014/manifest.json](cf-concurrency-20261014/manifest.json) | #20 固定 CF 资源/直连地址、30 MiB 区间的 1/2/3/4 路及长 Range 对照；保存全部 27 组、607 请求、首轮干扰排除和补测，主分析四个配对。仅上游吞吐开发实验，不改默认、不替代 LA-037，见[报告](../issue-20-cf-concurrency-20261014.md) |
| [issue20-development/development-checks.json](issue20-development/development-checks.json) | #20 最终源码身份、Windows race、构建检查及[同一回环内容的 Range 并发 A/B](issue20-development/benchmark-final.txt)，保留[旧版基线](issue20-development/benchmark-baseline.txt)和[初版等待退化](issue20-development/benchmark-initial-parallel.txt)；边界见[验证记录](../issue-20-validation.md)，真实线路与游戏仍待 LA-037 |
| [issue14-windows-final/manifest.json](issue14-windows-final/manifest.json) | #14 `2ec1b06` 本地 Windows 发行与 race 收尾：全仓 race、五协议 60 秒负载、SQLite 取消泄漏的初轮失败及上游修复回归、实际发行包/完整源码/离线重建；保留修复前证据，仅开发验证，LA-034/035/036、LA-018/012 仍待实机，见[集成记录](../issue-14-b2-integration.md#windows-发行与-race-收尾) |
| [runtime-mihomo-20261013/manifest.json](runtime-mihomo-20261013/manifest.json) | #14 最新要求的完整内嵌 B / 独立进程 C 对照：69 项开发检查、同一 VMess/WS 节点和视频 Range 三轮取样、各 60 秒连续负载、全局状态/故障边界、失败历史及构建身份；无生产切换或实机通过，见[评估与实施设计](../mihomo-runtime-evaluation-20261013.md) |
| [managed-mihomo-20261013/manifest.json](managed-mihomo-20261013/manifest.json) | 完整 Mihomo 独立进程的 31 项开发检查、30 条原生 VMess/WS 加载、前两节点真实 Monitor/播放/视频取样、固定出口与 Windows 进程边界、3 轮性能比较及失败历史；不是生产切换或实机通过，见[报告](../managed-mihomo-validation-20261013.md) |
| [issue14-development/release-validation.json](issue14-development/release-validation.json) | #14 桌面接入后的本地正式构建、实际附件哈希、96 个依赖及完整源码、独立重建和符号证据；仅开发验证，非发布或实机验收，详见[记录](../issue-14-validation.md) |
| [issue14-development/release-validation-final.json](issue14-development/release-validation-final.json) | #14 评审修复后 `acbb673` 的生产 EXE、完整对应源码、全新缓存离线重建及 Portable 隔离验证；最终附件与源码文件哈希复核，仅开发验证，LA-034/035/036 仍待实机，详见[记录](../issue-14-validation.md#最终产品代码的发行链复验) |
| [issue14-development/subscription-import-check.json](issue14-development/subscription-import-check.json) | 用户提供订阅在 `08ae90f` 的导入兼容检查：30 条 VMess/WS 全部超出当前支持范围；未执行节点连接或游戏验证，不包含私人链接或节点参数 |
| [issue-16-ui/20261012-single-confirm.md](issue-16-ui/20261012-single-confirm.md) | #16 直连范围固定标注、单次应用直达控制台、修复入口/导入条件显示及空间文案；211 项前端、Go 与内存浏览器开发检查，LA-029/030 待验收 |
| [issue-16-ui/20261012-picker-focus-dpi.md](issue-16-ui/20261012-picker-focus-dpi.md) | #16 目录窗口归属与 Per-Monitor V2 DPI；原生 API 定向测试、预览取消清理，窗口视觉与多屏缩放仍待 LA-029 |
| [issue-16-ui/20261012-local-feedback.md](issue-16-ui/20261012-local-feedback.md) | #16 目录框局部提示、hosts 成功文案去重、存储/网络刷新稳定布局；208 项前端测试、Go 定向回归和延迟内存预览，仍待实机验收 |
| [issue-16-ui/20261012-dialog-controls.md](issue-16-ui/20261012-dialog-controls.md) | #16 条款弹窗及失效记录重确认、直接恢复 hosts、导入目录文本框、网络/接入统一刷新；自动回归和浏览器内存检查，LA-029/030 待实机 |
| [issue-16-ui/20261012-layout-spec.md](issue-16-ui/20261012-layout-spec.md) | #16 按用户四页稿调整：同意状态、单行存储/容量、导入复选框、分项网络重测、接入状态与应用摘要；202 项前端测试和 Go/浏览器模拟检查，LA-029/030 待实机 |
| [issue-16-ui/20261012-four-step-setup.md](issue-16-ui/20261012-four-step-setup.md) | #16 四步引导、现代 Windows 目录选择器、单行存储控件与同页接入/完成；199 项前端回归、Go 与浏览器文字记录，原生窗口视觉确认及 LA-029/030 待验收 |
| [issue-16-ui/20261012-six-step-setup.md](issue-16-ui/20261012-six-step-setup.md) | #16 六步引导、目录选择接口、合并配置及完成状态；198 项前端回归、Go 与模拟浏览器文字记录，原生窗口及 LA-029/030 仍待实机 |
| [issue-16-ui/20261012-ux-copy.md](issue-16-ui/20261012-ux-copy.md) | #16 新用户文案精简与网络结果层级；自动检查和模拟浏览器文本记录，LA-029/030 待实机验收 |
| [issue-16-ui/20261012-same-page-consent.md](issue-16-ui/20261012-same-page-consent.md) | #16 首次与完整向导合并：同页确认、阅读位置保留、业务门禁和加载失败恢复；自动测试及内存夹具，仍待实机验收 |
| [issue-16-ui/20261012-review-fixes.md](issue-16-ui/20261012-review-fixes.md) | #16 评审修正：HMAC 配置版本、真实首次入口模板、原 ID 回执查询和 junction 祖先预检；自动测试与内存浏览器检查，仍待实机验收 |
| [issue-16-ui/20261012-layout-feedback.md](issue-16-ui/20261012-layout-feedback.md) | #16 页面反馈修正：五种 CSS 视口 × 八步、正文条款按钮、当前页网络结果、hosts 按钮、三态圆点；仅内存夹具开发检查，LA-029/030 待实机验收 |
| [issue-16-ui/README.md](issue-16-ui/README.md) | #16 最终浏览器内存夹具检查：条款确认、重入测速、草稿挽留及重启超时恢复；仅文字结果，LA-029/030 仍待实机验收 |
| [live-acceptance-20261007.json](live-acceptance-20261007.json) | `8b522c5` 联合实机验收：播放、预缓存途中点播/预览、双连接共享下载、故障与桌面生命周期；相对时间及清理前文件校验，结论见[验收报告](../live-acceptance-20261007.md) |
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

- 证据只纳入文本记录、脱敏日志摘要、配置差异和文件哈希；不携带图片、录像、程序等二进制、完整游戏日志、用户列表、房间实例标识、实际 hosts 文件或备份。
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

[评估报告](../issue-14-incremental-evaluation.md)、[脱敏证据与校验清单](issue14-incremental-20261013/manifest.json)。保留 B2 生命周期、夹具容量失败及对照，不作最终选型；不是 LIVE 实机批次。

## Privacy normalization

Personal paths and real media signature parameters are redacted. Exact capture/playback timestamps are replaced by T+seconds relative to the earliest timestamp in each file; clock-only excerpts use a separate per-file clock baseline. Relative times across different files are not directly comparable. Source log filenames use stable session aliases. Calendar dates in document names and development history remain for version tracking. HTTP capture dates are redacted. These historical evidence URLs are not reusable credentials.

Numeric epoch timestamps are relative milliseconds from a separate per-file epoch baseline. Embedded epoch identifiers in local run paths are also normalized. Manifest file hashes describe the sanitized Git blob bytes (LF), not Windows checkout newline conversions.
