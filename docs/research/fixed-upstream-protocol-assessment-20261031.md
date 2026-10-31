# 固定上游协议与可选清单来源评估

2026-10-31，北京时间。源码基线 `996e07ab4fd66842f1b464e67ad7e8400ff89cc9`。本文保留实施前评估及其证据。用户随后接受 HTTP 清单的安全代价，确认暂保留两备用完整清单并改为按需尝试；实施和验证见[固定协议验证](../validation/fixed-upstream-protocol-20261031.md)，现行规则见[上游通道](../upstream-channels.md)。

## 结论与边界

**可行，建议将 SWD 主动请求固定为：Udon 清单、播放 API、视频资源使用 HTTP；Kiva、WannaInfo 使用 HTTPS，删除自动升降级。** 现有实网证据支持这一工程取舍，但不能证明 HTTP 在每个 IP、网络或未来资源域名上都可用。固定策略意味着遇到只支持 HTTPS 的资源或强制 HTTPS 的上游时明确失败，后续再修改规则，不在运行时恢复一套协议协商机制。[历史 HTTP 批次](upstream-http-20261028.md)、[同 IP 协议对照](issue-21-cf-triad-20261022.md#协议差异及其阶段)。

“所有视频资源”应限定为本产品已支持、经有效播放 API 及资源规则确认的资源。它不扩大到任意外部视频、SHA 入口，也不把动态域名改成固定白名单。DoH、Mihomo 自身连接及客户端 HTTPS 透传保留原协议职责；本地接管仍按已有 HTTP API 契约工作。[server.go](../../internal/cacheproxy/server.go) 的非播放 API 入口 307 与客户端 443 透传属于入站兼容，不是本轮删除的主动请求协议协商，也不因此增加本地清单代理。[资源域名规则](../resource-domains.md)、[请求与连接](../upstream-channels.md#请求与连接)。

HTTP 不提供 HTTPS 的服务器认证及传输保密、完整性保障；来自同一 HTTP 链路的 URL/MD5 不能补回这些保障，在途者可同时改写数据及摘要。这是采用方案的已知取舍，不需要为此新增 Kiva/WannaInfo 认证链或将它们重新设为必需依赖。[RFC 9110 §4.2.2](https://www.rfc-editor.org/rfc/rfc9110.html#section-4.2.2)、[RFC 9530 §6.1](https://www.rfc-editor.org/rfc/rfc9530.html#section-6.1)。

## 已有证据与本次小样本

| 证据 | 支持的结论及限制 |
| --- | --- |
| [10-28 HTTP 批次](upstream-http-20261028.md) | Udon HTTP 清单完整成功；6 首播放 API 返回合法 HTTP 视频地址，12 个前/中部 1 KiB 片段均为有效 206。Kiva/WannaInfo 的 HTTP 返回同端点 HTTPS 301，因此直接 HTTPS 更简洁。 |
| [10-29 清单窗口](../validation/kiva-connectivity-20261029.md) | Udon HTTP 双栈合计 60/60 完整成功；同期 HTTPS 0/60，稍后 HTTPS 又完整成功 12/12。不能称 HTTPS 永久不可用。 |
| [10-29 Wi-Fi 窗口](../validation/upstream-wifi-windows-20261029.md#持续-wi-fi-观察)及[六轮留出](../validation/wifi-ip-selection-20261029.md#同-ip-的-http--https-对照) | 前者 Udon 播放 API 与视频请求合计 HTTP 97/102、HTTPS 0/101；后者三个相同 DNS 视频 IP 的 HTTP 18/18、HTTPS 0/18。仅对应窗口、网络及片段，不是清单成功率。 |
| [10-22 同 IP、同视频、同 Range 对照](issue-21-cf-triad-20261022.md#协议差异及其阶段) | 固定 104 地址池两窗分别有 2 个“仅 HTTPS 完成”的配对；HTTP 并非覆盖 HTTPS 所有可用路径。保留该反例，不由总体 HTTP 优势抹去。 |

本次在 **10-31 10:59:49–10:59:54** 补做三个 HTTP GET，未扩大到 Kiva/WannaInfo 或做吞吐比较：

| 请求 | 公网远端 | 结果 |
| --- | --- | --- |
| Udon 清单 | `104.26.5.28` | 200，完整 `2,886,450` bytes，`10,474` 个正数且不重复 ID，版本 `20261030115114`，约 2.698 秒 |
| `node=cf`、歌曲 6254 播放 API | `104.26.5.28` | 302，Location 为 `http://play.udon.dance` 的合法视频路径及大小/MD5 参数，约 0.768 秒 |
| 同次视频前 1 KiB | `172.67.69.58` | 206，`Content-Range: bytes 0-1023/52379700`，恰好 1,024 bytes，约 0.469 秒 |

方法为 Node v24.22.1 原生 HTTP 客户端，`dns.resolve4` 取得当次公网 IPv4 后固定 lookup，绕开系统 hosts；不使用代理、不跟随重定向，每请求上限 20 秒，视频最多读取 1,025 bytes。签名 URL 仅在内存中用于当次取样，没有输出或保存。`resolve4` 与系统 hosts 的区别见 [Node DNS 文档](https://nodejs.org/api/dns.html#dnsresolvehostname-rrtype-callback)。这不是产品 DoH/候选调度的集成验证，也不证明完整视频、代理、IPv6 或未来域名可用。

本次完整清单 SHA-256 为 `ec7f0376a1236f640010ff6623d2ee1ead42cd2bbb92c15a6c4783ac9c14cb47`；视频片段为 `11902492b9b6ab804707c9558dd5ab44ca2635201dea524e0de3d8c01123d1a3`。数据是同次只读探针的脱敏输出摘要，不包含正文或可复用签名 URL。未启动 VRC、改 hosts 或日常实例数据。

## 需要成套清理的实现

协议可以使用现有 [catalogmeta](../../internal/catalogmeta/sources.go) 与 [playbacksource](../../internal/playbacksource/source.go) 的入口定义，不新增通用策略框架。仅修改 URL 常量不够：

- 移除 [Attempts](../../internal/upstreamrequest/attempts.go) 内仅服务升降级的状态、错误资格分类、升级处理、协议时间预留及批内协议记忆；保留候选尝试上限、完整正文失败后的重试、调用者取消与期限。资源 Range 和顺序 200 的生命周期仍有独立价值，不能随协议状态一起删掉。[现行尝试契约](../upstream-channels.md#业务请求的协议策略)。
- 一并检查 Console 与 CLI 的默认基址、资源请求、丢弃下载测速及 Monitor。[server.go](../../internal/cacheproxy/server.go) 中存在 HTTP 上游仅允许 loopback 的旧限制；[probe.go](../../internal/upstreamcheck/probe.go)、[routes.go](../../internal/upstreamstate/routes.go)及[network_access.go](../../internal/console/network_access.go)仍有 HTTPS 默认。检测、测速与真实下载必须使用相同固定协议。
- 删除 Monitor 的 HTTPS 恢复优先、跨协议推荐/样本分流等专属逻辑；旧 HTTPS 吞吐不能当成新 HTTP 样本。旧样本兼容应最小化处理，不能为了沿用历史速度重新保留协议自适应。[Monitor 现行规则](../upstream-channels.md#直连测量和推荐)。
- API 返回 HTTPS 视频 URL 只代表原始地址，不等于已证明 HTTP 不可用。保留原始 URL 与来源，执行请求统一应用 HTTP；Host、路径、参数、资源成员、大小及校验规则不变。正常 API→视频及合法资源重定向仍保留；同端点 HTTP→HTTPS 强制升级明确拒绝，不能改回 HTTP 后循环请求。[资源规则](../resource-domains.md)、[视频 URL 校验](../../internal/videometa/playback.go)。HTTP 与 HTTPS 本身不是可直接假定等同的标准 origin，执行归一是本产品明确选定的策略。[RFC 9110 §4.2.2](https://www.rfc-editor.org/rfc/rfc9110.html#section-4.2.2)。
- 不清空业务数据库。清单身份已有按 host/path 判断的机制，但 [song_metadata_candidates.go](../../internal/cacheproxy/song_metadata_candidates.go)按完整 API 字符串匹配，`song_urls` 又以 `(song_id, api, node)` 为键；新 HTTP 基址须兼容已知 Udon HTTPS 来源，避免已有映射失配、重复写入或整库重新补齐。[存储约定](../storage.md)。

上述清理减少的是协议分支及其测试组合；DNS 有效期、代理隔离、候选选择、合法重定向、内容校验和取消仍是主链路需要的机制。不能将“固定 HTTP”扩张成删除这些职责。

## Kiva/WannaInfo 的非必要化

当前数据模型已以 Udon 为主清单，MD5 缺项可由资源快照与播放 API 补齐；但执行流程还没有完全消除备选来源对主链路的影响。[曲库设计](../song-catalog-design.md)、[资源快照](../../data/media-seed/README.md)。

| 现有耦合 | 建议边界 |
| --- | --- |
| [CheckConnectivity](../../internal/upstreamcheck/connectivity.go)先串行请求三清单，全部结束才交付清单并检查播放/资源；每个清单调用可用约 30 秒。[请求组装](../../internal/console/network_access.go)、[清单读取](../../internal/console/catalog_checksums.go)。 | Udon 主链先完成并发布结果。两备选移出强制每轮主检查，不为可选失败增加约 60 秒前置等待。 |
| [Monitor](../../internal/upstreamstate/check.go)也在清单候选遍历完成后继续 API；[refreshLocalCatalog](../../internal/console/catalog_checksums.go)在 Udon 失败或映射有缺项时串行拉备选。 | 保留现有薄适配和共用解析/同步；按需尝试，不为两备选扩展专属选路、退避、恢复轮询或调度框架。 |
| [catalog_sync_data.go](../../internal/console/catalog_sync_data.go)可将备选解析/冲突写为全局曲库检查错误。 | 可选来源失败与主清单可用性分开，不能让 Udon 已成功的主链显示整体失败；真实本地存储错误仍须暴露。 |

实施前待确认、现已确认的业务边界是：**Udon 不可用时，Kiva/WannaInfo 是否继续具有完整清单兜底资格，还是以后仅允许补 MD5？** 用户确认暂保留现有完整清单兜底资格并改为按需尝试；后续再随边缘化安排删除。它们不能成为启动、正常播放或主清单更新的必要前提。

## 后续验证范围

实施时由开发者验证固定协议在清单、播放、下载、测速、Monitor、Console/CLI 和代理出口上的一致性，覆盖不再升降级、强制升级不成环、合法资源跳转、Range/正文失败/取消、旧映射复用，以及备选超时不阻塞主链。公网只需补相关路径的小样本，失败按实际阶段记录；不能用本次短样本代替改动后的产品集成检查。

本评估不新增游戏台账项。若实现保持客户端 HTTP API、缓存响应与 TLS 透传契约，协议策略本身可由非游戏检查完成；只有出现确实影响播放器行为且无法用受控服务验证的剩余风险，才按[验证机制](../live-acceptance-process.md)合并到既有游戏场景。
