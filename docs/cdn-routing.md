# CDN 与网络路由

以下是 2026-09-25 实机测试结果。API 候选参数试验和游戏 UI 的实际映射必须分开。

## 房间 UI 实际映射

| 选项 | LoadRoutedURL 入口 | 本次最终视频路径 | 验证状态 |
| --- | --- | --- | --- |
| CF | `http://api.udon.dance/Api/Songs/play?node=cf&id=...` | `http://play.udon.dance/files/...` | 完整实机冷缓存通过 |
| HKG | `http://api.udon.dance/Api/Songs/play?node=nya&id=...` | `http://nya.xin.moe/files/...` | 跨域复用 CF 缓存通过；回源小段通过 |
| Auto | `http://api.udon.dance/Api/Songs/play?id=...` | 本次为 `http://nya.xin.moe/files/...` | 本地命中及重试后播放通过 |
| SHA | `https://139.196.46.195:51886/Api/Songs/play?id=...` | 未取得有效最终地址 | 游戏及独立 HTTPS 请求超时 |

日志保存的 VideoRoute 值包括 Auto=0、HKG=2、SHA=3。不要将 UI 文本直接转换成 node 值：HKG 使用 nya，SHA 根本不使用 node=sha。Auto 的选择由上游决定，不能永久固定为 nya。

直接试验 `node=hkg`、`node=sha`、`node=auto` 均得到 nya，只能说明当时 API 对这些候选值的结果，可能是回退行为；不能据此声称对应 UI 选项已测试。[候选试验](evidence/candidate-cdn-api.json)

## 原版配置中的域名

| 对外入口 | 原版上游连接目标 | 用途 |
| --- | --- | --- |
| api.udon.dance | ud-orig.kiva.moe | API 的 TLS 透传 |
| play.udon.dance | ud-play.kiva.moe | CF 视频回源、TLS 透传 |
| nya.xin.moe | ud-nya.kiva.moe | HKG 视频回源、TLS 透传 |
| aya.kiva.moe | 不在上述透传映射中 | 游戏日志中的歌曲图片来源 |

本次视频入口为 HTTP 80。原版还配置了 443 SNI 透传，曲目列表本身有 HTTPS 请求，因此不能只配置 HTTP 路径。

连接目标与应用层域名不同。例如原版连接 `ud-play.kiva.moe:80`，HTTP Host 保持 `play.udon.dance`；TLS 透传保留原始 SNI。域名分流和嗅探规则应共同考虑连接目标与 Host/SNI，不能只检查其中一个名字。

SHA 是额外的 **TCP 139.196.46.195:51886** 路径。hosts 不能重映射裸 IP，现有域名映射方案不接管其入口。当前未取得 SHA 的有效响应，不能决定是否还需要接管它最终的视频地址。

2026-09-25 后续 StepStash 联合测试：10271 的 Auto 重载命中本地缓存，SHA 则在解析入口超时。用户转述管理员核查：网关按国内 IP 集合旁路 Mihomo，SYN 被 ACCEPT 并经 pppoe-wan 出站；客户端和网关均为 SYN_SENT / UNREPLIED，另一网络出口也在 TCP 阶段超时。这支持当前入口不可达，不足以确定远端或中间网络的具体故障，也没有验证 TLS 证书行为。即使本地已有视频，解析入口未返回视频 URL 时，现有缓存服务仍无法兜底。见[本轮证据](evidence/auto-sha-live-20260925.json)。

## 两种接入模式的差异

### 只映射两个视频域名

```text
127.0.0.1 play.udon.dance
127.0.0.1 nya.xin.moe
```

这是第二轮实机测试使用的方式。API 仍正常按节点解析，视频请求进入本地原版。先只映射 play 会漏掉 HKG 及本次 Auto 的 nya 视频。

### 同时映射 API 域名

这是原版支持的另一种配置，但当前观测显示：原版 API 未命中时跳到 HTTPS API，并丢弃 node。用户原先选择的 CDN 可能因此改变。这个行为是后续设计时需明确保留或修正的兼容差异。

## 切换时序

选择 CDN 会触发重新加载；随即手动点歌可能遇到约 10 秒加载冷却。本次从 SHA 切到 Auto 后，旧 SHA 解析任务又返回超时。Auto 第一次异常，随后重试恢复；时序支持旧任务干扰的解释，但不能直接证明房间内部取消机制。

对照测试应记录每次选择时间，等前一次成功或明确失败后再切换。不能仅凭当前 UI 所选节点给所有迟到错误归因。
