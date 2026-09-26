# 播放域名实测：2026-09-26

本次以用户指定的 `https://wanna.kiva.moe/`、实时播放请求、用户提供的游戏日志和随包 README 为依据。没有使用 StepStash 的 Origins 映射，没有修改 hosts 或业务代码。

结论：本次 WannaDance 播放证据支持使用 `api.udon.dance`、`play.udon.dance`、`nya.xin.moe` 这些公开入口。没有观察到客户端需要主动访问 `ud-play.kiva.moe` 或 `ud-nya.kiva.moe`。不过 `ud-play.kiva.moe` 确实出现在原版随包 README 的回源日志示例里，因此“完全没有来源”不准确；它的历史用途与是否应成为本项目默认依赖是两件事。

## 观察到的域名

| 域名 | 本次证据 | 用途与边界 |
| --- | --- | --- |
| `wanna.kiva.moe` | 用户指定入口；浏览器资源清单含 `/api/wannaInfo`、JS、CSS、字体 | 曲库网页及网页自己的数据接口 |
| `api.udon.dance` | 网页歌曲链接；实时 API 302；游戏日志 | 播放地址解析；另有曲目列表接口 |
| `play.udon.dance` | CF 实时 API 返回；206 视频片段；2,028 条历史解析记录；公网 DNS + HTTPS 成功 | 公开视频入口 |
| `nya.xin.moe` | Auto/HKG 实时 API 返回；两首网页实际播放；公网地址 HTTPS 返回跳转 | 另一公开视频入口；不保证始终在同一域名提供正文 |
| `aya.kiva.moe` | 网页封面实际加载；历史日志 17,209 次 URL 提及 | 图片资源，不能据此当成视频回源地址 |
| `static.cloudflareinsights.com` | 浏览器资源清单中的 beacon 脚本 | 网站统计脚本；不是视频源 |
| `a.nel.cloudflare.com` | API/视频响应的 `Report-To` 头 | 被服务器声明的错误报告地址；本次未证明浏览器实际发送报告 |

`github.com` 是网页展示的源码外链，未点击，不列为播放网络依赖。`dns.google` 是本次调查主动使用的 DNS 查询服务，不是从歌曲播放链路发现的视频域名。不要因为 `wanna.kiva.moe` 和 `aya.kiva.moe` 有直接证据，就推定同一根域名下所有 `ud-*` 地址都必须依赖。

## 游戏请求模拟

### HTTP / HTTPS 补测

随后对上述六个域名的实际样本路径分别发起 HTTP、HTTPS GET，记录首跳，不自动跟随。HTTPS 均通过证书验证；网络仍使用本机当前路径。证据见 [protocol-check.json](evidence/playback-domain-20260926/protocol-check.json)。

| 域名及样本 | HTTP 首跳 | HTTPS 首跳 |
| --- | --- | --- |
| `wanna.kiva.moe/` | 301 到同地址 HTTPS | 200 网页 |
| `api.udon.dance/Api/Songs/play?node=cf&id=1` | 302 到 HTTP 视频地址 | 302 到 HTTP 视频地址 |
| `play.udon.dance` 歌曲 1 | 206，1,024 字节 | 206，1,024 字节 |
| `nya.xin.moe` 歌曲 1 | 206，1,024 字节 | 206，1,024 字节 |
| `aya.kiva.moe/images/small1.jpg` | 301 到 HTTPS | 206，1,024 字节 |
| `static.cloudflareinsights.com` 实际 beacon 脚本 | 301 到 HTTPS | 200 脚本 |

因此均有 HTTP/HTTPS 入口响应，但不能笼统说均通过两个协议直接提供内容。HTTPS API 返回 HTTP Location，也不意味着整条链保持 HTTPS。`nya.xin.moe` 的当前网络路径与前述公网 IP 对照行为不同，仍应保留按网络/节点变化的边界。

样本歌曲为 1、1343、6927。每首分别测试 Auto（不带 node）、CF（node=cf）、HKG（node=nya），同时测试 HTTP 和 HTTPS API，共 18 组。脚本使用原生 Node HTTP/HTTPS，无自定义回源映射；HTTPS 保留证书校验。请求 User-Agent 取自仓库既有播放器观察记录，不声称本次启动了 VRChat。

| API 选择 | 三首歌本次返回的 Location 主机 | 后续片段 |
| --- | --- | --- |
| Auto | `nya.xin.moe` | 均 206，实际 1,024 字节 |
| CF | `play.udon.dance` | 均 206，实际 1,024 字节 |
| HKG / nya | `nya.xin.moe` | 均 206，实际 1,024 字节 |

HTTP 和 HTTPS API 的这批原始 302 Location 都是 `http://...`。探测脚本忠实跟随，没有偷偷升级。另测歌曲 1 的中段 `bytes=20000000-20001023`，得到匹配 Content-Range 和 1,024 字节。浏览器风格 UA 的 HTTPS Auto 入口也得到相同域名关系。

原始 URL、请求头、响应头、Location、时刻、字节数及片段 SHA256 见 [summary.json](evidence/playback-domain-20260926/summary.json)。可复现脚本为 [probe.cjs](evidence/playback-domain-20260926/probe.cjs)。

另按既有游戏请求特征模拟歌曲 1 的三个节点：浏览器风格无 Range GET 先读前缀，然后播放器 UA 携带 `Range: bytes=0-`、`Cache-Control: no-cache` 和 `Pragma: no-cache`。三组均分别收到 200 与 206，媒体每次读到至少 64 KiB 后主动断开；这是解析/起播请求模拟，不是完整下载。见 [game-sequence.json](evidence/playback-domain-20260926/game-sequence.json) 和 [game-sequence.cjs](evidence/playback-domain-20260926/game-sequence.cjs)。

`https://api.udon.dance/Api/Songs/list` 返回 200、2,866,503 字节；网页的 `/api/wannaInfo` 返回 200、5,768,543 字节。后者的 `originalUrl` 字段还包含 `www.bilibili.com`、`song.xin.moe`、`www.youtube.com`、`youtu.be`、`youtube.com` 的 URL，见 [metadata-link-examples.json](evidence/playback-domain-20260926/metadata-link-examples.json)。这只说明接口数据引用了这些原始来源地址，不能算浏览器已经请求它们，更不能自动列成内置视频播放依赖。

## 网页实际播放

在 Chrome 打开用户提供的曲库首页，点击歌曲链接；第二首通过网页搜索框搜索 1343 后点击。不是直接把猜测的 MP4 地址塞进播放器。

| 歌曲 | 点击的地址 | 浏览器最终 video.currentSrc 主机 | 播放证据 |
| --- | --- | --- | --- |
| 1，CH4NGE | `https://api.udon.dance/Api/Songs/play?id=1` | `https://nya.xin.moe` | 时间从 19.59 到 40.68 秒；readyState=4、paused=false、error=null |
| 1343，Modern Loneliness | `https://api.udon.dance/Api/Songs/play?id=1343` | `https://nya.xin.moe` | 时间从 11.00 到 83.03 秒；readyState=4、paused=false、error=null |

浏览器最终采用 HTTPS，而独立 API 请求的原始 Location 为 HTTP。本次没有完整浏览器 HAR，不能确定其间是浏览器升级、缓存还是其他跳转；不把推测写成观测。网页封面 `https://aya.kiva.moe/images/small1343.jpg` 已加载，naturalWidth=480。

浏览器工具输出的整理见 [browser-observations.json](evidence/playback-domain-20260926/browser-observations.json)，明确标注为人工转录，不冒充原始抓包。

## 独立 DNS 与 HTTPS 对照

本机 hosts 在测试开始时无上述域名的有效映射。但普通连接显示 `198.18.*` 地址，网络存在 fake-IP/透明转发迹象，不能把该地址当作远端真实地址。

另经证书验证的 Google DoH 查询 A 记录，并将连接明确指向返回的公网 IP，保持原 URL、Host、TLS SNI 和证书验证。输入是已观察到的视频路径的 HTTPS 版本，本步骤明确做了协议升级，区别于上面的忠实跳转测试。

- `play.udon.dance`：连到 `172.67.69.58`，证书验证成功，206，实际 1,024 字节；证书覆盖 `*.udon.dance`。
- `nya.xin.moe`：连到 `38.147.189.128`，证书验证成功，302 到 `http://play.udon.dance/...`；随后跟随公开域名收到 206、1,024 字节。证书覆盖 `*.xin.moe`。
- 两条对照链拿到的歌曲 1 首段 SHA256 相同。

记录见 [public-doh-https.json](evidence/playback-domain-20260926/public-doh-https.json)，包含 DNS 答案、所选 IP、响应及证书。测试没有调用任何 `ud-*` 入口。这证明公开入口在本次样本中可用；不保证绕过了网络中的所有透明设备，也不能据此推断服务端内部架构。

SHA 裸 IP 入口沿用历史游戏证据中的 `https://139.196.46.195:51886/Api/Songs/play?id=1`，20 秒无响应，没有发现后续域名。见 [sha-entry.json](evidence/playback-domain-20260926/sha-entry.json)。它不是这次网页发现的新入口。

## 用户提供的历史游戏日志

来源：`<log-collector-directory>/logs/source-vrc-logs`。扫描全部 70 个文件，文件日期覆盖 2026-07-27 至 2026-09-26。保留每个源文件的 SHA256、文件名和证据行号，不复制完整日志或玩家信息。其他视频网站 URL 的查询参数已移除。

**用户确认：这些日志大多数是在 `<reference-workspace>/wanna-cdn.exe` 开启时采集。** 尚未逐个会话确认 CDN 状态。因此它们只能支持“游戏当时看到/使用的 URL”，不能当成未接入 CDN 的原站响应证据。本地 CDN 的缓存命中、重定向和内部回源可能影响结果，以下统计不用于证明原站不依赖某个后台地址。

- `api.udon.dance` 在播放相关 URL 中出现 8,520 次。
- `play.udon.dance` 在播放相关 URL 中出现 2,028 次，对应 2,028 条 `api.udon.dance → play.udon.dance` 解析记录。
- `aya.kiva.moe` 在日志中出现 17,209 次 URL 提及，不属于所筛选的播放解析行。
- 这批日志未提取到 `nya.xin.moe`；它的当前可用性来自本次实时请求和浏览器实播。
- 全文检索 `ud-play.kiva.moe`、`ud-nya.kiva.moe` 均为 0。

以上是字符串/URL 提及数，不是独立请求数、歌曲数或成功播放次数。游戏日志不记录所有 DNS、重定向和代理内部连接，因此“没出现”不能证明网络深处不存在该域名。

代表证据：`output_log_session-002.txt:1094` 记录 CF 入口选择；同文件 `:1128` 记录解析到 `play.udon.dance`。完整统计与索引见 [game-log-domains.json](evidence/playback-domain-20260926/game-log-domains.json)，提取器见 [extract-game-logs.cjs](evidence/playback-domain-20260926/extract-game-logs.cjs)。

日志还含其他播放内容的域名：DuduFit 的 `api.dudufit.dance`、`api-ddfd.imkiva.com`、`global-cdn.dudufit.dance`，PyPy 的 `api.pypy.dance`、`cdn.pypy.dance`，以及 YouTube、Twitch、Bilibili 和若干解析入口。索引保留了这些域名，但没有证据将它们纳入本次 WannaDance 内置曲库的默认回源范围。

## 回源域名来源的修正

`<reference-workspace>/README.md:62` 的日志示例直接包含：

```text
re-caching via ud-play.kiva.moe (Host: play.udon.dance)
```

因此 `ud-play.kiva.moe` 有原版随包说明中的历史依据。该行说明的是缓存工具回源，不是游戏播放链接；也没有承诺这个域名长期稳定。本 README 未出现 `ud-nya.kiva.moe`。对随包文件内容的确认不等于独立验证它的发布者身份。

调查结论支持将默认回源改成“公开域名 + 独立 DNS + 原域名 TLS 校验”，按经过验证的实际重定向继续访问。无需以 `ud-play.kiva.moe` / `ud-nya.kiva.moe` 作为必需的中转名。上述调查阶段未修改业务行为；随后按用户要求完成了以下实现与验证。

## 调查后的实现与验证

默认视频连接目标改为 `play.udon.dance:443` 和 `nya.xin.moe:443`；TLS 透传按三个托管域名自身连接 443。两者继续使用独立 DoH，不读取本机 hosts，不以历史 `ud-*` 地址作为兜底。CLI 显式自定义连接地址的能力保留，原域名 Host、SNI 与证书验证仍然生效。已有重定向白名单、资源校验及下载时 HTTP Location 升级为 HTTPS 的逻辑继续使用。

2026-09-26 23:43（北京时间）验证：

- `go test ./...`、`go vet ./...` 通过。针对性测试覆盖默认公开地址、显式覆盖地址保持 TLS 身份、无效配置、不可信证书、连接失败不回退、合法跨域跳转及非法跳转拒绝；TLS 透传测试同时检查真实传入 dialer 的目标为公开域名。
- `STEPSTASH_LIVE_UPSTREAM=1 go test ./internal/console -run '^TestLiveHTTPSUpstreams$' -v -count=1`：CF/HKG 均通过临时本地 TLS 透传获取 1,024 字节视频片段并完成原站证书校验。测试定向连接本地端口来模拟 hosts 接入，没有修改 hosts，也未安装证书。这是协议级验证，不声称本轮重新启动了游戏或浏览器。
- 新构建 CLI 使用默认配置进行隔离冷缓存验收：1343（CF 入口）完整收到 32,867,177 字节，MD5 `28711962048bed664c98f27e1d9d5842`；1344（HKG 入口）完整收到 40,548,476 字节，MD5 `8f26d29704140a431dea0c1374f2eeba`。实际大小和落盘 MD5 都与 API 返回值一致，跨 Host Range 命中和重启 HEAD 命中均通过。入口选择不等于最终视频服务器，允许上游有效跳转。

完整下载原始报告见 [public-origin-acceptance-20260926.json](evidence/public-origin-acceptance-20260926.json)。使用 `node scripts/acceptance.mjs bin/stepstash-public-origin.exe` 运行，测试媒体和进程日志留在忽略目录 `test-runs/acceptance-0`；测试进程已由脚本关闭，原版歌曲库和常用缓存未改动。实网结果仅覆盖当时网络和这些样本，不承诺长期可用性。
