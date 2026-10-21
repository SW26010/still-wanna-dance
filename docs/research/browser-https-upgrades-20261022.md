# 浏览器 HTTPS 升级与本地 HTTP 视频服务

2026-10-22，Windows Chrome **155.0.8059.85**。目的：判断将视频资源域名指向本地、HTTP 提供缓存、HTTPS 透传或拒绝时，浏览器会实际选择哪条路径。当前产品规则仍以[资源域名规则](../resource-domains.md)为准，本次没有修改产品、hosts、证书或浏览器安全设置。

**HTTPS 透传成功，会让浏览器的尝试性升级停留在 HTTPS，从而绕开本地 HTTP 缓存。拒绝 TLS 可以触发部分导航回退，但不能普遍“强迫 HTTP”。** 当前 Chrome 的普通 HTTP 链接和 HTTP 重定向已实测回退；明确 HTTPS 链接失败后没有 HTTP 请求。真实资源域名还受 HSTS 和 DNS HTTPS 记录影响，不能直接套用隔离域名的结果。

## 当前请求链与历史证据

现有 WannaDance 网页的歌曲链接指向 `https://api.udon.dance/Api/Songs/play?id=1`。直连 API 的默认请求及模拟浏览器导航请求均得到 `302 Location: http://play.udon.dance/files/...mp4`；独立在 Chrome 打开同一 API，最终地址却为 HTTPS 资源。HTTP、HTTPS 资源各取 1 KiB Range 均返回 206，抽样响应没有 HTTP→HTTPS 服务端重定向，也没有 HSTS/CSP 响应头。请求头 `Upgrade-Insecure-Requests: 1` 不等于响应中的 CSP 指令，不能仅凭它判定原因。

这证明“API 返回 HTTP”与“浏览器最终使用 HTTPS”可以同时成立。没有完整浏览器网络轨迹，尚未区分这次真实导航的具体内部升级来源：普通 HTTPS Upgrades、DNS HTTPS 记录、动态 HSTS 或已有重定向状态。[协议与浏览器观测摘要](browser-https-upgrades-20261022/protocol-summary.json)保留了这个边界。

`<原版调查目录>/测试记录.txt` 的 2026-03-29 记录包含明确对照：资源域名映射到本地时，HTTPS API 跳到 HTTP 资源，原版记录歌曲 3114 的 `[HIT]`；只映射 API 时，同样跳到 HTTP 资源，但没有本地命中。原版启动日志同时存在资源域名的 443 SNI 透传。因此，过去本地播放成功是有证据的，但记录没有浏览器版本、升级设置及异常名单，不能推断当时成功是因为“拒绝了 HTTPS”。

当前 [`hosts.go`](../../internal/console/hosts.go) 仅管理 API 域名；[`https.go`](../../internal/console/https.go) 转发原始 TLS，不解密视频内容。即使补上资源域名映射，HTTPS 透传本身仍不会读取本地 HTTP 缓存。

## 本机隔离实测

使用当前 Chrome 配置，HTTP 服务监听 `127.0.0.2:80`；独立 TCP 服务监听 `127.0.0.2:443`，收到 TLS ClientHello 后关闭连接。使用新的 `*.swd20261022.127.0.0.2.sslip.io` 域名访问本机，不改 hosts。入口页为 `http://127.0.0.2/`，通过点击链接发起导航；没有在地址栏手输目标 HTTP URL，以免落入显式 HTTP 输入例外。

| 场景 | 服务端观测 | 浏览器结果 |
| --- | --- | --- |
| 首次点击 HTTP 链接 | 2 次 TLS 尝试被关闭，然后 GET HTTP `/result` | 显示本地 HTTP 页面，无确认页 |
| 入口 302 到新的 HTTP 域名 | 2 次 TLS 尝试被关闭，然后 GET HTTP `/result` | 显示本地 HTTP 页面，无确认页 |
| 明确的 HTTPS 链接 | 6 次 TLS 尝试被关闭，整个观测窗口没有该域名 HTTP 请求 | `ERR_CONNECTION_CLOSED` |
| 再次点击第一条 HTTP 链接 | 直接 GET HTTP，没有新增 TLS 尝试 | 显示本地 HTTP 页面 |
| 用另一新域名重测 302 | 2 次 TLS 尝试被关闭，然后 GET HTTP `/result` | 再次自动回退成功 |

第二轮 302：以首次关闭 TLS 为 `T+0.000s`，在 `T+0.051s` 收到 HTTP GET，相隔 51 ms；只说明本次本机快速失败，没有测公网延迟或视频首帧。实验返回 HTML，验证的是导航与回退，不能替代完整 MP4 播放验收。隔离入口本身是 HTTP；HTTPS 入口→302 HTTP 的情况另有真实 API 的升级观测及 Chromium 对应用例 `HttpsToHttpRedirect_ShouldUpgrade`，但没有在真实资源域名上执行拒绝 TLS 的对照。[实验摘要](browser-https-upgrades-20261022/summary.json)

## 哪些升级允许回退

| 机制 | 升级何时发生 | HTTPS 失败后的默认行为 |
| --- | --- | --- |
| Chrome HTTPS Upgrades | 主页面 GET 导航及重定向中的 HTTP 目标 | 可退回原 HTTP；是否提示取决于 HTTPS-First/警告策略 |
| 明确 `https://` URL | 原始链接、程序或服务端 Location 已指定 HTTPS | 不因 TLS 失败自动改用 HTTP |
| HSTS，包括预加载与父域规则 | 在发出 HTTP 前强制使用 HTTPS | 不回退；拒绝 TLS 导致失败 |
| DNS HTTPS 记录 | 解析器取得兼容 HTTPS RR 后，可合成 HTTPS 重定向 | 这是另一层升级；不能依赖普通 HTTP 例外来消除它 |
| HTTPS 页面里的 HTTP 音视频子资源 | 混合内容规则自动升级 | HTTPS 不可用则资源失败，不采用主页面的尝试性回退 |
| CSP `upgrade-insecure-requests` | 页面策略适用的请求发出前改写 URL | 升级失败即请求失败，无 HTTP 回退 |
| 服务端或缓存中的 HTTP→HTTPS 重定向 | 浏览器收到或复用重定向 | 与主动升级是不同来源；不能用 API 的一个 HTTP Location 排除后续升级 |

主页面导航包括“点击后单独打开一个视频地址”，与网页内部 `<video src="http://…">` 不同。当前观测到的 WannaDance 入口属于前一种，不能拿内嵌视频的规则直接解释它。[Chromium 的导航说明](https://chromium.googlesource.com/chromium/src/+/main/docs/security/ask-before-http/ask-before-http-adoption-guide.md)、[混合内容升级说明](https://blog.chromium.org/2019/10/no-more-mixed-messages-about-https.html)、[CSP 失败处理](https://www.w3.org/TR/upgrade-insecure-requests/#failed-upgrade)

### Chrome 155 的尝试性升级

核对与安装版本一致的源码：`kHttpsUpgrades` 默认启用；自动尝试升级与失败后是否显示警告是独立逻辑。因此关闭“始终使用安全连接”不等于关闭所有自动升级。实际设置、企业策略、站点状态及功能分组还会影响行为，本次没有读取或调整这些设置。[功能默认值](https://chromium.googlesource.com/chromium/src/+/refs/tags/155.0.8059.85/chrome/common/chrome_features.cc)

[`HttpsUpgradesInterceptor`](https://chromium.googlesource.com/chromium/src/+/refs/tags/155.0.8059.85/chrome/browser/ssl/https_upgrades_interceptor.cc) 在导航及每个重定向目标上检查，符合条件时合成 307，原因标记为 `HttpsUpgrades`。主要边界如下：

- 处理最外层主页面 GET；明确 HTTPS 不会被登记为它发起的升级。
- HSTS 交给更严格的网络层处理。URL 主机为 localhost/回环 IP 会被排除；公开域名解析到回环地址仍可能升级，已由本次实验确认。
- 非严格模式下，非默认端口、非唯一主机名、显式输入 HTTP 和已有 HTTP 许可等可成为例外；部分例外只适用于用户最初输入的主机，不能推广到跨域重定向。
- 对被它登记为“已升级”的导航，TLS/连接错误可以触发原 HTTP 回退；警告策略启用时可能先显示确认页。客户端主动阻断和部分网络故障另有例外。
- 当前版本的响应处理先检查网络错误。**有效 TLS 上返回 HTTP 404/502 本身不触发这条回退路径**，不能把旧版博客中的行为当作现版本保证。

默认尝试超时为普通升级 3 秒、需要询问时 5 秒，可能被功能参数覆盖。快速关闭 TLS 与丢弃连接等到超时的用户体验不同。成功回退会记住 HTTP 许可；源码的默认期限是 15 天，再次使用可续期，有效 HTTPS 访问又可能撤销它。因此“刚才能用、后来又升级”也可能由站点状态改变造成。[导航计时器](https://chromium.googlesource.com/chromium/src/+/refs/tags/155.0.8059.85/chrome/browser/ssl/https_upgrades_navigation_throttle.cc)、[许可期限](https://chromium.googlesource.com/chromium/src/+/refs/tags/155.0.8059.85/components/security_interstitials/content/stateful_ssl_host_state_delegate.cc)、[上游回归用例](https://chromium.googlesource.com/chromium/src/+/refs/tags/155.0.8059.85/chrome/browser/ssl/https_upgrades_browsertest.cc)

### 资源域名不能一概而论

| 域名 | Chrome 155 预加载表 | 本次公网 DNS HTTPS RR | 对方案的意义 |
| --- | --- | --- | --- |
| `play.udon.dance` | 未找到本域或父域匹配 | 存在，含 `alpn=h3,h2` | 普通升级可回退，但还需确认实际解析路径和动态站点状态 |
| `nya.xin.moe` | 父域 `xin.moe` 为 `force-https`，包含全部子域 | 未返回 HTTPS RR | 默认 HSTS 约束下，拒绝 HTTPS 无法迫使浏览器使用 HTTP |
| `api.udon.dance` | 未找到本域或父域匹配 | 存在，含 `alpn=h3,h2` | 网页入口本来就是明确 HTTPS；拒绝其 TLS 会使入口失败 |

HSTS 表来自同版本的[预加载源文件](https://chromium.googlesource.com/chromium/src/+/refs/tags/155.0.8059.85/net/http/transport_security_state_static.json)。没有预加载匹配，并不证明用户配置中没有动态 HSTS；本次抽样响应未发送 HSTS，也不能清除浏览器此前保存的父域或本域规则。[HSTS 机制](https://www.chromium.org/hsts/)

DNS 结果是当次 Google DoH 公网查询快照，不能证明当前 Chrome 实际使用了它。在同版本网络代码中，兼容的 HTTPS 记录可产生 `ERR_DNS_NAME_HTTPS_ONLY`，随后合成原因为 `DNS` 的 307。源码中的 `allow_fallback=false` 是**禁止改用不查询 HTTPS 记录的 DNS 回退**，不能误读成一条涵盖所有浏览器导航的回退结论。[DNS 处理](https://chromium.googlesource.com/chromium/src/+/refs/tags/155.0.8059.85/net/dns/host_resolver_dns_task.cc)、[合成重定向](https://chromium.googlesource.com/chromium/src/+/refs/tags/155.0.8059.85/net/url_request/url_request_http_job.cc)

对于 hosts 方案，同版本解析器先执行本地解析任务，hosts 命中可直接返回地址，不再走公网 DNS 任务；缓存检查也可能先发生。因此不能说“公网有 HTTPS RR，就必定绕过 hosts”，也不能只改 A 记录就假定 HTTPS RR 已消失。hosts、DNS 应答替换、浏览器缓存及已有连接是不同条件。此处为[解析器源码](https://chromium.googlesource.com/chromium/src/+/refs/tags/155.0.8059.85/net/dns/host_resolver_manager.cc)推导，未修改真实域名的 hosts 做现场对照。

## 对本地服务方案的判断

资源域名 HTTPS 透传成功时，链路可能是：

```text
HTTPS API → 302 HTTP 资源 → 浏览器升级 → 本地 443 透传 → 上游 HTTPS 视频
```

此时 HTTP 缓存没有机会接到请求。拒绝资源 TLS 时，只有允许回退的情形才能走到：

```text
HTTPS API → 302 HTTP 资源 → 尝试 HTTPS 失败 → 回退 HTTP → 本地 80 缓存
```

所以，对 `play.udon.dance` 的主页面导航，拒绝 TLS 是有实测机制依据的研究方向；成立条件包括资源请求确实到达本地、浏览器允许回退、没有 HSTS 或其他再次升级的约束。**尚未证明真实域名的完整播放链路满足这些条件。** 对 `nya.xin.moe` 则已有预加载 HSTS 这一明确障碍。

仅凭 ClientHello/SNI，服务端没有可靠标记区分“浏览器从 HTTP 自动升级”与“调用者明确请求 HTTPS”。因此不能仅靠同一个域名的 SNI 分流，同时保证前者拒绝以促回退、后者成功透传。API 与资源域名可以分开处理，但资源域名内部的两种 HTTPS 来源不能这样区分。

当前证据支持保留“HTTPS 透传保障可访问”的语义，同时明确它不保证浏览器命中 HTTP 缓存；不支持将所有资源 443 一律拒绝作为透明兼容方案。若继续验证拒绝策略，应只针对选定资源域名，保留 HTTPS API，并以浏览器内部重定向原因、本地 80 请求和实际视频播放共同判定成功。这个判断是方案评估，没有新增配置或改变默认行为。

## 复核与证据边界

隔离实验脚本：[probe-server.mjs](../../experiments/browser-https-upgrades/probe-server.mjs)。每轮用新域名，避免上一轮记住的 HTTP 许可影响首次访问。使用标准 80/443 端口，因为随机端口可能触发升级例外。

```powershell
node experiments/browser-https-upgrades/probe-server.mjs swd-<新的批次标识>.127.0.0.2.sslip.io
```

在同一浏览器访问脚本显示的回环入口，依次点击三个链接，返回入口后再次点击 HTTP 链接。日志写入 `artifacts/browser-https-research/`；服务只绑定回环地址，10 分钟自动退出，交互终端也可输入 `q` 退出。遇到浏览器安全确认页应记录并停止该步骤，不用绕过确认页来制造“自动回退成功”。

[精简观测和来源哈希](browser-https-upgrades-20261022/summary.json)保存在仓库；原始事件、本次运行脚本副本与完整公开源码保存在忽略目录 `artifacts/browser-https-research/`，未发布外部附件。协议探测原始输出在 `artifacts/api-location-protocol/`。没有保存或上传浏览器私人配置、历史或完整视频。

仓库内本机绝对路径使用来源目录占位符；精确采集时刻转换为文件内相对时间，保留研究日期和测量间隔。两个 JSON 摘要各以该文件最早采集时刻为基准，报告正文中的时间对照以第二轮首次关闭 TLS 为基准；不同文件的 `T+` 不直接比较。本地忽略目录内原始记录及其哈希保持不变。

保留的失败与限制：首轮 TLS 域名标签采用子串匹配，把 `redirect` 错标成 `direct`，HTTP Host 与时间仍可识别；修正匹配顺序后用全新域名补测成功。第二轮初次启动遇到旧监听占用，停止第一轮后重启；CIM 进程读取受环境权限限制。退出时普通 stdin 已关闭，改用本实验会话中断，随后分别成功绑定并关闭 `127.0.0.2:80/443`，确认端口已释放；研究标签页已关闭。内部 HSTS 查询页受浏览器工具策略限制，未尝试其他通道绕过，因此动态 HSTS 状态仍未知。

没有做 Edge/Firefox/Safari 的本机矩阵，也没有把 Chromium 源码中的测试当成本机已执行测试。跨浏览器、真实资源域名拒绝 TLS、HTTPS 页面内嵌视频及 VRC 播放均不在本次实测覆盖范围。

研究文件复核：实验脚本语法、7 个本地链接、两轮日志总计（TLS 尝试 10/2，HTTP 结果请求 3/1）及差异空白检查通过。仓库附件扫描在现有 HEAD `b10886464b075cfe0108515b1df894918454adbb` 的历史范围报错：已有 `internal/console/assets/webui/app.js` blob `2a35943163822872ad59aa9ec70bd426d10794e2` 为 1,191,805 字节，缺少精确例外登记；本次未修改该文件或放宽附件策略。
