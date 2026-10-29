# 文档导航

`docs/` 根目录维护现行使用说明、设计和开发约定；按下表选择入口。历史报告只证明其记录的构建和条件，不能作为当前默认值或验收要求。

## 使用与现行设计

| 内容 | 入口 |
| --- | --- |
| 使用、兼容范围与排障 | [WebUI 与桌面程序](console.md)、[命令行服务](service.md)、[功能范围](scope.md)、[日志](logging.md) |
| 下载架构与运行参数 | [下载需求与资源管理设计](download-resource-design.md)是规则、接口及验证边界的统一依据；[块调度设计](block-scheduler-design.md)解释实现，[运行规则](download-concurrency.md)汇总参数 |
| 视频导入、暂停与恢复 | [固定清单、恢复与源文件清理](video-import.md) |
| 队列与视频保留 | [队列预加载](queue-prefetch.md)、[视频存储预算](cache-retention.md) |
| 歌曲屏蔽与收藏 | [需求准入、队列计时与资源保留规则](download-resource-design.md#8-歌曲屏蔽与收藏)、[偏好表与版本边界](storage.md#歌曲偏好存储) |
| 配置、存储与生命周期 | [配置生命周期](settings-lifecycle.md)、[存储契约](storage.md)、[运行文件生命周期](runtime-lifecycle.md)、[曲库与需求统计](song-catalog-design.md) |
| 持久化整理目标与迁移 | [职责与边界](persistence-design.md)维护分类原则；[迁移计划](persistence-migration-plan.md)记录实现范围、逐项核对与剩余验证 |
| 网络 | [资源域名规则](resource-domains.md)、[上游通道](upstream-channels.md) |
| WebUI | [WebUI 目标设计](webui-design.md)为页面与交互的 canonical 文档；现有界面见[内容约定](webui-content.md)，组件与验收见 [Fluent UI](fluent-ui-migration.md)、[可访问性](webui-accessibility.md) |
| 开发与分发 | [开发环境](development.md)、[测试维护与 CI 修复](development.md#测试维护与-ci-修复)、[Portable 打包](packaging.md)、[对应源码](corresponding-source.md)、[许可资料](licenses/README.md) |

## 验证与历史资料

| 位置 | 用途与边界 |
| --- | --- |
| [验证机制](live-acceptance-process.md)、[VRC 台账](live-acceptance-backlog.md)、[非 VRC 跟进](validation-followups.md) | 当前验证分工和真正剩余的场景；不在其他文档重复维护待验清单 |
| [validation/](validation/) | 开发、浏览器、受控服务与性能验证记录；保留每轮失败、修复和环境限制，阅读现状时回到对应设计 |
| [acceptance/](acceptance/) | 已实际开展的游戏/实网验收报告；新游戏批次使用[报告模板](live-acceptance-report-template.md) |
| [history/](history/) | 原版调查、被替代的设计、阶段审计和旧台账快照；文件名中的 `original-` 指原版调查 |
| [research/](research/) | [视频保留收益研究](research/cache-priority.md)、[等权初始表半衰期回测](research/equal-requester-priority-20261026.md)、[媒体元数据研究](research/video-metadata-20261015.md)、[CF 候选收益与搜索成本](research/issue-21-cf-selection-20261022.md)、[HTTP/HTTPS、104 地址与 Speedtest 对照](research/issue-21-cf-triad-20261022.md)、[浏览器 HTTPS 升级与本地 HTTP 服务](research/browser-https-upgrades-20261022.md)及复算数据 |
| 清单内容与使用核对 | [Udon/Kiva/WannaInfo 内容核对](research/catalog-sources-20261029.md)记录固定批次的字段、版本及采样时业务用途，不代表后续实现 |
| 曲库改造决策 | [Udon 曲库与资源映射决策](research/udon-catalog-decision-draft-20261029.md)保存目标、实施边界及验证；现行规则回到[曲库设计](song-catalog-design.md)，预置维护见[资源快照](../data/media-seed/README.md) |
| 上游连接观测 | [三个清单入口的连接与更新排查](validation/kiva-connectivity-20261029.md)汇总各窗口、错误阶段和 Udon HTTPS 后续成功；[10 月 28 日 HTTP 批次](research/upstream-http-20261028.md)保留清单、播放与视频片段的原始结论。均为固定条件下的历史观测，不代表永久可用性或协议选型 |
| 上游协议实施 | [HTTPS 优先与有界 HTTP 回退验证](validation/upstream-protocol-20261029.md)，现行规则见[上游通道](upstream-channels.md#业务请求的协议策略) |
| Wi-Fi 上游与 IP 优选 | [初始故障窗口](validation/upstream-window-20261029.md)、[连续观察及多路对照](validation/upstream-wifi-windows-20261029.md)、[329 地址单路优选](validation/wifi-ip-selection-20261029.md)：保留失败、中断和独立六轮验证；同 IP 协议对照单列，未接入产品 |
| [evidence/](evidence/README.md) | 脱敏证据和清单；大附件的下载、校验及保留规则见[证据存储](evidence-storage.md) |

## 仓库文件归属

| 位置 | 内容 |
| --- | --- |
| `cmd/`、`internal/` | 可执行入口、产品实现及同包 Go 测试；固定夹具放包内 `testdata/` |
| `webui/src/`、`webui/e2e/`、`webui/test-support/` | 前端源码、浏览器用例、内存服务器与 Go 联调浏览器驱动 |
| `scripts/` | 构建、发布、检查及维护入口；研究工具按主题放子目录 |
| `experiments/` | 可复跑的独立实验及工具，不作为产品运行依赖 |
| `data/initial-priority/`、`data/media-seed/` | 产品内嵌的初始优先级及版本化资源/映射快照；其余运行数据不入库 |
| `artifacts/`、`test-runs/`、`local/`、`bin/` | 忽略的验证输出、隔离会话、私人材料及构建产物；保留所需证据后再按具体用途清理 |

文档采用小写连字符命名；日期用于固定批次、观测和快照，持续维护的规则不在文件名中加日期。沿用 Go 的下划线及 `_test.go`、浏览器的 `.spec.ts`、Node 的 `.test.mjs` 约定。新增文件放入已有类别，移动时同步相对链接、脚本入口和直接收录它的清单。历史证据中的源码路径与哈希仍指采样时快照，不机械改为当前路径。

许可审计 [issue-30-license-audit.md](issue-30-license-audit.md) 的路径已写入发行清单并参与严格校验，因此保留在原位置，由许可索引引用。

生成的 WebUI、产品图标、研究图表运行副本和许可声明有明确构建或分发用途；以[资源策略](evidence-storage.md#git-历史和防回归)及生成工具为准，不能按同内容或“生成文件”一概删除。
