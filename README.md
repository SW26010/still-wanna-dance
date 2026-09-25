# StepStash

面向 VRChat WannaDance 的本地视频缓存服务项目。

已有可运行的 Go MVP：HTTP 视频缓存、冷缓存边下边播、完整性校验、Range/HEAD、同曲并发合并及现有歌曲库直接复用。使用方式见 [MVP 手册](docs/mvp.md)，实网、原版回读及 VRChat 联合测试的结果和边界见[验收记录](docs/acceptance.md)。

截至 2026-09-25，已跑通原版的实机 CF 冷缓存完整下载、落盘及文件校验，并确认 HKG、Auto 可以命中同一份缓存。SHA 使用独立的 HTTPS IP 入口，目前超时，尚不能承诺兼容。

## 文档入口

| 文档 | 内容 |
| --- | --- |
| [本地控制台](docs/console.md) | Windows 托盘、单实例避让、CDN 开关、hosts、目录及全曲目下载 |
| [日志队列预缓存](docs/queue-prefetch.md) | 低频增量监听、房间前 3 首预缓存和历史日志回放 |
| [MVP 使用手册](docs/mvp.md) | 构建、启动、接入、配置、缓存规则与验证边界 |
| [MVP 验收记录](docs/acceptance.md) | 真实歌曲库、实网下载、原版回读、联合游戏测试和修复结果 |
| [范围与实现方向](docs/scope.md) | 项目目标、第一版范围、提议中的设计与尚未决定的事项 |
| [存储格式与现有库兼容](docs/storage-compatibility.md) | 直接复用 wannadance-song，保留现有文件并沿用目录格式 |
| [已知协议与行为](docs/observed-behavior.md) | 播放链路、HTTP 接口、Range、元数据和原版异常 |
| [CDN 与网络路由](docs/cdn-routing.md) | UI 选项的真实映射、域名/端口、Host/SNI 与分流要求 |
| [测试结果](docs/test-results.md) | 两轮原版及游戏测试、证据编号和结论边界 |
| [复现操作手册](docs/testing-runbook.md) | 隔离环境、网络预检、游戏操作与清理要求 |
| [待验证清单](docs/open-questions.md) | 开始实现前应补的黑盒测试与最终验收矩阵 |
| [证据目录](docs/evidence/README.md) | 可移植的精简测试数据、原始资料位置与脱敏说明 |

## 结论速览

- 房间先改写播放 API 地址，VRChat 启动 yt-dlp 解析，再由视频客户端发起实际取流请求。
- 本次捕获到浏览器风格的短暂 GET，以及携带 Range 的 `NSPlayer/WMFSDK` 请求；短连接和提前断开不一定是故障。
- CF 与 HKG 的视频域名不同；本次样本可共用同一歌曲文件。Auto 的上游选择不能写死为 HKG。
- 本地命中和完整下载已实测；校验失败、并发、跨重启残留下载等规则尚未完整确认。
- 接入 API 域名与只接入视频域名不等价：原版 API 未命中时会丢弃 `node` 参数。

## 当前仓库内容

包含 Go 服务、本地网页控制台、自动化测试、CI 配置、文档和精简证据，无第三方运行依赖。原版二进制、视频、完整游戏日志和实际 hosts 文件不在仓库中。控制台启动方式见上方手册；打包方式和项目许可证尚未选择，尚未创建 GitHub 远程仓库。

开发环境安装与检查见[开发环境](docs/development.md)。

证据来自 2026-09-24 至 2026-09-25 的 Windows 本机测试，原版版本为 `d309146c`。时间、CDN 地址及上游行为是当时的观测，不是长期服务保证。
