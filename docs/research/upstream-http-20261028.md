# 上游 HTTP 可用性观测 2026年10月28日

2026-10-28 23:45–23:48（北京时间），本机直连实测，源码 `13d59bc8f7883dc5e4643ca98eb8c0f622c793c3`。目标是判断 SWD 自己发起的业务请求能否统一改回 HTTP；本轮只核对代码和请求上游，没有修改产品协议、配置或 hosts，没有启动 VRC。

这是固定批次的历史记录。后续持续监测和最新复核统一见[三个清单入口的连接观测](../validation/kiva-connectivity-20261029.md)：Udon HTTPS 在 10-29 10:16–10:31 仍全部失败，但 11:42–11:43 的 curl 与原 Node 客户端双栈复核均完整成功。因此，本文的失败不能用于断言 HTTPS 持续不可用；协议选型与清单来源改造仍以现行设计和对应决策为准。

**不能把全部业务上游统一改成纯 HTTP。** Udon 歌曲列表和抽样播放链路可直接通过 HTTP 使用；Kiva 与 WannaInfo 的 HTTP 入口返回 301，要求继续访问 HTTPS。视频资源域名由播放 API 动态提供，本次成功的样本不能证明未来返回的所有域名均支持 HTTP。

## 入口与结果

| 采样时用途 | HTTP 入口 | 本次结果 | 判断 |
| --- | --- | --- | --- |
| Udon 歌曲列表（仅监测与检测样本） | `api.udon.dance/Api/Songs/list` | IPv4、IPv6 均为 200；正文 2,885,371 bytes，解析出 10,470 条 | HTTP 直接可用，不用于本地曲库同步 |
| Udon 播放地址 | `api.udon.dance/Api/Songs/play?node=cf&id=…` | 6 首全部返回 302，Location 为合法的 HTTP 视频地址 | HTTP 直接可用；302 是正常的视频地址解析 |
| 视频正文 | 播放 API 本次返回 `play.udon.dance` | 6 首各取前部、中部 1 KiB，12 次全部为 206，Content-Range 与字节数校验通过 | 本次抽样 HTTP 可用 |
| Kiva 曲库与 MD5 清单 | `x.kiva.moe/api/v2/wanna/songs` | IPv4 返回 301，Location 指向同主机、同路径的 HTTPS 地址 | HTTP 只提供升级跳转，业务正文仍需 HTTPS |
| WannaInfo 曲库与 MD5 清单 | `wanna.kiva.moe/api/wannaInfo` | IPv4 返回 301，Location 指向同主机、同路径的 HTTPS 地址 | HTTP 只提供升级跳转，业务正文仍需 HTTPS |

样本歌曲为 1、1343、1344、6254、10058、10946。视频 HTTPS 对照 12 次中 7 次成功，与对应 HTTP 片段的 SHA-256 均相同；其余 5 次发生响应头超时或正文读取失败。播放 API 的 HTTPS 对照 3 次正常返回 302，另 3 次为 EOF 或 TLS 握手超时。Udon 列表 HTTPS 双栈对照超时，IPv4 对照收到 200 后只读到 8,192 bytes，在 25 秒期限内未读完。

Kiva 同期 HTTPS 对照出现 500，独立 curl 固定 IPv4 复核仍为 500；WannaInfo HTTPS 对照返回 200，正文 5,768,543 bytes，经产品 `catalogmeta.Parse` 解析得到 10,076 条。保留上述失败，不作协议性能归因；暂时故障不改变已经观察到的 HTTP 升级跳转，也不能用来断言服务永久不可用。

原始实测记录已移到仓库外归档根的 `artifacts/upstream-http-20261028/`，文件为 `results.json`、`ipv4-catalogs.json`、`kiva-control.txt` 和探针源码；具体位置与复跑边界见[证据与复跑](../validation/kiva-connectivity-20261029.md#证据与复跑)。本次只恢复文档，未搬回原始输出，也未保存完整视频或公开签名 URL。

## 方法与限制

正式探测复用产品独立 DoH 与 `DialContext`，不使用系统 hosts，不使用环境代理，不自动跟随重定向。每个视频样本仅请求前部和中部各 1 KiB；HTTP 的 200/206 成功与 HTTP→HTTPS 的 301 分别记录。结果只说明本机当前直连网络、当前时段和对应样本的可用性；未验证代理路径，未穷举所有可能的动态资源域名，不是吞吐比较或完整播放验收。

首次双栈请求中，Kiva 与 WannaInfo 经 IPv6 的 HTTP/HTTPS 均发生 EOF；这一失败不足以判断为 HTTP 协议不受支持，因此补做固定 IPv4 对照，才得到上表的 301 结论。初步使用系统解析的 curl 请求曾连接 Udon 的本地 `127.0.0.1:80` 网关并得到 307；该结果已明确排除，不计为公网 API 行为。正式探测记录公网远端地址，绕开了这一混淆。

## 代码范围与实施含义

三种清单入口对应两种格式。采样时的本地曲库只接受 Kiva 与 WannaInfo，它们使用同一含 MD5 的结构，以 `data.time` 比较版本，共用 `songs` 水位；Monitor 优先提交较新候选，旧版本不能覆盖本地新版本，同版本内容冲突时保留现状。Udon 的顶层 `time` 只用于监测展示，正文不交给业务同步。依据为上述源码快照中的 [`monitor_catalog.go`](../../internal/console/monitor_catalog.go)、[`catalog.go`](../../internal/cacheproxy/catalog.go) 与[曲库设计](../song-catalog-design.md#udon-清单边界2026-10-06)。本次 WannaInfo 的 `data.time` 仍为 `20260215004959`；Kiva 返回 500，无法取得其本次版本。10-29 的内容比较及使用情况另见[清单来源核对](catalog-sources-20261029.md)。

采样版本的播放来源由 [`playbacksource/source.go`](../../internal/playbacksource/source.go) 单点定义，仅查询 `node=cf`。Console 的 Udon 与 Kiva 基址见 [`console.go`](../../internal/console/console.go)，Monitor 的三个清单入口见 [`upstreamstate/routes.go`](../../internal/upstreamstate/routes.go)。视频域名根据有效播放 API 响应动态确认，规则见 [`upstreamrequest/resources.go`](../../internal/upstreamrequest/resources.go)；不应把本次出现的 `play.udon.dance` 当作永久完整名单。

本批次支持“所测 Udon 列表、播放地址和视频片段可以通过 HTTP 获取”，不构成统一降级 HTTP 的实施决定。采样版本的下载层默认 HTTPS，限制 HTTP 只能连接回环测试上游（[`config.go`](../../internal/cacheproxy/config.go)、[`server.go`](../../internal/cacheproxy/server.go)），Monitor 也将资源地址改为 HTTPS（[`probe.go`](../../internal/upstreamstate/probe.go)）。后续 Udon HTTPS 完整成功已补充到连接观测汇总；若调整协议，仍需独立决定并保持检测与下载一致、重新验证重定向处理。本报告没有实施这些调整。

独立 DoH 是 DNS 基础设施，其 HTTPS 入口见 [`dns_providers.go`](../../internal/upstreamnet/dns_providers.go) 和 [`dns.go`](../../internal/upstreamnet/dns.go)；内嵌 Mihomo 另有 HTTPS DNS 配置（[`runtime.go`](../../internal/proxyengine/mihomo/runtime.go)）。客户端 HTTPS 兼容连接由 [`https.go`](../../internal/console/https.go) 透传 TLS 密文字节。这两类不属于本次业务 HTTP 可用性测试，不能由视频或清单的 HTTP 成功推导出应改为 HTTP。

## 历史证据边界

[2026-10-22 浏览器调查](browser-https-upgrades-20261022.md)曾确认 `play.udon.dance` 的 HTTP/HTTPS 1 KiB Range 均返回 206；[同日 CF 对照](issue-21-cf-triad-20261022.md)的两个窗口中，DNS 基线的七条路径均完成 HTTP 与 HTTPS 2 MiB 读取。它们支持当时对应视频可通过 HTTP 访问，不代替本次 API 核对，也不能保证其他时间、网络或后续动态资源域名的行为。
