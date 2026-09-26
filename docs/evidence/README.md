# 证据目录

这里保存精简、脱敏后的测试证据，使新仓库不依赖旧对话即可理解已验证的结论。它们不是完整网络抓包。索引中标明 StepStash 的文件属于历史构建验收，其余为原版调查结果。两者均不能替代当前 StepStash 构建的测试；证据文件名和内容保留采集时的名称与记录。

## 文件索引

| 文件 | 来源与用途 |
| --- | --- |
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

- 不携带原程序二进制、视频、完整游戏日志、用户列表、房间实例标识、实际 hosts 文件或备份。
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


## Privacy normalization

Personal paths and real media signature parameters are redacted. Exact capture/playback timestamps are replaced by T+seconds relative to the earliest timestamp in each file; clock-only excerpts use a separate per-file clock baseline. Relative times across different files are not directly comparable. Source log filenames use stable session aliases. Calendar dates in document names and development history remain for version tracking. HTTP capture dates are redacted. These historical evidence URLs are not reusable credentials.

Numeric epoch timestamps are relative milliseconds from a separate per-file epoch baseline. Embedded epoch identifiers in local run paths are also normalized. Manifest file hashes describe the sanitized Git blob bytes (LF), not Windows checkout newline conversions.
