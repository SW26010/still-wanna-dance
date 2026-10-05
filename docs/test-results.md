# 原版黑盒测试结果

> 历史记录：CF/HKG 表示当时的界面、日志标签或 API 参数。域名和 IP 仅描述当次观测，不构成固定对应，也不保证继续存在；当前以 API 返回集合为准，见[资源域名规则](resource-domains.md)。

测试时间为北京时间 2026-09-24 晚至 2026-09-25 凌晨。JSON 中的 ISO 时间戳以 Z 结尾，表示 UTC，应加 8 小时后与游戏日志对照。

## 环境与证据分层

- Windows 本机、VRChat WannaDance v1.4、原版 d309146c。
- 自动原版测试使用本地 18080/18443 和独立缓存。
- 实机通过本地 80 的 HTTP 记录器转发到原版 18080；443 只转发原版 18443，不解密 TLS。
- 用户在私人实例中操作并确认音画；客户端日志和原版请求日志作时间关联。
- 记录器会引入一个转发层；时间数据是该环境的观测，不是无开销的原版性能基准。

证据入口：[目录与脱敏说明](evidence/README.md)。

## 第一轮：网络故障期间

| 用例 | 结果 | 证据 |
| --- | --- | --- |
| 已缓存 1343 实机播放 | 用户确认音画正常；HIT；完整传输 32867177 字节 | [请求摘要](evidence/live-round1-requests.json) |
| 1343 常见 Range / 完整文件 | 内容哈希匹配；同时发现后缀、越界 Range 异常 | [HTTP 结果](evidence/hot-http-results.json)、[切片校验](evidence/payload-verification.json) |
| 10414 重播 | 前段能播，但客户端从 25403392 开始取后段，随后发生停滞和续传 | [请求摘要](evidence/live-round1-requests.json) |
| 6927 冷缓存 | 数百 KB 后停滞，多次重试，用户确认失败 | [请求摘要](evidence/live-round1-requests.json) |
| 移除 hosts 后直接播放 6927 | 仍失败；没有新请求进入本地记录器 | [游戏事件摘录](evidence/game-events-round1.txt) |

当时对公开 play 域名和 ud-play 回源域名直接请求中段 1024 字节，能收到正确 206 头，但正文为 0 字节直到超时。用户随后请网络管理员调整分流规则。不能将这一阶段失败固化为原版正常协议行为。

客户端已有数据会掩盖回源问题，因此“播放开始”不等于“冷缓存完整下载成功”。

## 第二轮：网络修复后

| 用例 | 结果 | 证据 |
| --- | --- | --- |
| 6927 公开域名与 ud-play 中段预检 | 均为 206、1024 字节，约 0.538s / 0.756s | [网络复测摘要](evidence/network-retest-summary.json) |
| 自动原版 1343 从空缓存下载 | 32867177 字节，11908ms；内容与原文件一致 | [冷缓存结果](evidence/cold-http-results.json) |
| 自动下载后的中段请求 | HIT，正确 1024 字节，5ms | 同上 |
| 游戏 6927 续传 | 用户确认恢复；起点 1261568，不作为全新冷缓存证明 | [第二轮请求](evidence/live-round2-requests.json) |
| 游戏 CF 1344 全新冷缓存 | `bytes=0-`；40548476 字节；40476ms；落盘并生成元数据 | 同上、[文件校验](evidence/cache-integrity.json) |
| HKG 重播 1344 | 使用 nya 域名，HIT 同一份文件，用户确认正常 | 同上、[游戏事件](evidence/game-events-round2.txt) |
| HKG 切换后恢复进度 | 日志同步至 94.66 秒，用户确认正常 | [游戏事件](evidence/game-events-round2.txt) |
| SHA 1344 | 独立 IP 入口超时，最终视频路径未知 | [独立探测](evidence/sha-endpoint-probe.json) |
| Auto 1344 | 初次切换异常，后续重试恢复；尾段 12220540 字节完整返回 | [第二轮请求](evidence/live-round2-requests.json) |

CF 冷缓存样本 1344 的本地 MD5 与 URL e、原库文件一致。HKG/Auto 的成功证明当前样本跨域复用，不意味着任意版本视频都能仅按 ID 复用。

## 下载元数据

原版成功后生成的文件与元数据样本：[1343](evidence/generated-metadata-1343.json)、[1344](evidence/generated-metadata-1344.json)。临时缓存文件在完成后不再存在，没有生成 download.txt。

## 已知限制

- SHA 的服务响应尚未知，不能声称已支持四条路径。
- HKG 只做过缓存命中和回源小片段，没有独立从零下载整首的实机测试。
- 未通过破坏文件或更换版本证明 e/s 的严格校验行为。
- 有失败、续传和重试样本，但没有完整的断点恢复及并发状态矩阵。
- 未做新的多人中途加入对照；切换 CDN 后恢复进度不能完全替代。
- 未测原版启用鉴权、Redis、RTSP 或 mirror 下载器。

## 环境收尾

两轮结束均恢复了 hosts；第二轮恢复文件与备份 SHA256 完全一致：

```text
2D6BDFB341BE3A6234B24742377F93AA7C7CFB0D9FD64EFA9282C87852E57085
```

原版测试进程与记录器均退出，80/443/18080/18443 无测试监听。这是测试结束时的检查记录，不代表未来机器状态。
