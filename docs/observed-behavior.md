# 原版已知协议与行为

> 历史记录说明（2026-10-06）：文中的 CF/HKG 是当时的界面、日志标签或 API 参数称呼。域名、IP 及地理位置只属于当次观测，不构成节点到域名的固定映射，也不保证这些域名持续存在。当前实现以有效 API 返回的域名集合为准，见[资源域名规则](resource-domains.md)。原始证据保持原样。

状态词：**已实测**表示有请求、文件或游戏日志证据；**推测**表示与证据相符但未证明；**待验证**表示不能写成兼容承诺。

原版：`wanna-cdn.exe`，启动版本 `d309146c`，SHA256：

```text
A3C7C554E5C2B0B43EC8E9480BEA200504C19A8EFA897DA8F6F6315CCF7AE429
```

测试使用 `NO_AUTH=true`。鉴权开启时的行为不能从这些结果外推。

## 房间与播放器链路

```text
WannaDance USharpVideo 原始歌曲 URL
    → 按 CDN 选项执行 LoadRoutedURL
    → VRChat 启动 Tools/yt-dlp.exe
    → 得到实际视频 URL
    → 视频客户端读取文件或 Range
    → OnVideoReady / OnVideoStart 与房间进度同步
```

本次 CF/HKG/Auto 的原始 API 是 HTTP：`/Api/Songs/play?id=<id>`。SHA 的改写例外见 [CDN 文档](cdn-routing.md)。曲目列表还存在 HTTPS `/Api/Songs/list`，与视频文件请求分开处理。

实机中先出现两次浏览器风格 GET，无 Range，读取少量数据后断开；随后出现 `NSPlayer/12.00.26100.9457 WMFSDK/12.00.26100.9457` 请求。时序与解析和播放阶段吻合，但不能仅凭 User-Agent 证明请求进程身份，也不应将这些版本字符串写死。

实际播放器请求包括：

```http
GET /files/2403/1344-660524b4ebadb.mp4?e=REDACTED&s=REDACTED HTTP/1.1
Host: play.udon.dance
Range: bytes=0-
Cache-Control: no-cache
Pragma: no-cache
User-Agent: NSPlayer/12.00.26100.9457 WMFSDK/12.00.26100.9457
```

恢复请求可能带 `If-Match`、`If-Unmodified-Since` 和非零起点 Range。`no-cache` 不会阻止本次样本命中原版本地文件。

## 视频 URL 参数

样本路径：`/files/<目录>/<歌曲ID>-<内容标识>.mp4?e=<值>&s=<值>`。

| 歌曲 | e / 实测 MD5 | s / 字节数 |
| --- | --- | --- |
| 1343 | `28711962048bed664c98f27e1d9d5842` | 32867177 |
| 1344 | `8f26d29704140a431dea0c1374f2eeba` | 40548476 |

以上两个文件的 MD5 和大小均已实际校验。`e` 与 MD5 相符、`s` 与长度相符；不能将其称为已确认的临时签名。原版是否严格使用二者验证下载、如何处理错误值，仍待测试。

## HTTP 路由实测

证据：[热缓存请求](evidence/hot-http-results.json)、[API node 参数](evidence/api-node-behavior.json)。

| 请求及条件 | 原版响应 |
| --- | --- |
| GET `/v/1343`，有文件 | 200，完整视频 |
| HEAD `/v/1343` | 400，无响应正文 |
| GET `/v/1343`，普通单区间 Range | 206，切片内容与本地文件一致 |
| GET 或 HEAD `/Api/Songs/play?id=1343`，有文件 | 302 到相对 `/v/1343.mp4?auth=...&t=wd` |
| 跟随上述本地 URL | 按请求返回视频/Range |
| API 带 `node`，有文件 | 仍转到本地 `/v/` |
| API 带 `node`，无文件 | 302 到 `https://api.udon.dance/Api/Songs/play?id=<id>`，未保留 node |
| GET `/v/6927`，无文件 | 400，`AreYouTryingToHackMe` |
| `/files/...mp4` 带正确 e、s，命中 | 返回本地视频，支持普通 Range |
| `/files/...mp4` 缺少 e 或 s | 400，`BadToken` |
| HEAD `/files/...mp4`，带 e、s，命中 | 200，有 Content-Length，无正文 |

热缓存视频响应有 `Content-Type: video/mp4`、`Accept-Ranges: bytes`。原版在完整 200 响应上也发出全文件 `Content-Range`。API 的 auth 每次可不同；NO_AUTH=true 并不意味着不生成 auth，验证范围和有效期未知。

一个 `/files/` 样本即使使用 `Host: api.udon.dance` 也命中；这不足以证明任意 Host 均可接受。

## Range 异常，不视为推荐实现

| 输入 | 原版观测 |
| --- | --- |
| `bytes=0-1023` | 正确返回 1024 字节 |
| `bytes=20000000-20001023` | 正确返回中段 1024 字节 |
| `bytes=-1024` | 错误地返回从第 1024 字节到文件尾 |
| `bytes=40000000-`，文件仅 32867177 字节 | 先返回 206、倒置 Content-Range 和巨大 Content-Length，随后中断并记录工作线程 panic；进程继续服务 |
| `bytes=0-15,32-47` | 400 MethodNotAllowed |
| `bytes=abc` | 400 MethodNotAllowed |

完整文件及相关切片已经 SHA256 比对：[校验结果](evidence/payload-verification.json)。后续 StepStash 对无效、越界、多区间请求应明确自己的行为，不自动复制原版缺陷。

## 缓存生命周期

已观测到：

1. 仅访问无缓存歌曲的 API，只产生跳转。
2. 请求视频路径后记录 MISS，经对应回源域名获取内容，保留视频 Host。
3. 下载过程中使用 `cache/<文件名>.mp4`。
4. 完整成功后出现 `songs/<id>/video.mp4` 和 `metadata.json`，临时缓存路径消失。
5. 后续请求记录 HIT；CF 下载的 1344 可在 HKG/Auto 下复用。

最终文件状态不能证明内部一定用了原子重命名。文件系统轮询看到的临时文件大小也不能替代完整请求及落盘验证。

原版代理下载生成的是简化元数据，例如：

```json
{
  "id": 1344,
  "category": 114514,
  "title": "1344",
  "categoryName": "",
  "titleSpell": "",
  "playerIndex": 0,
  "volume": 0.0,
  "start": 0,
  "end": 0,
  "flip": false,
  "skipRandom": false,
  "originalUrl": null,
  "checksum": "8f26d29704140a431dea0c1374f2eeba"
}
```

没有生成 `download.txt`。这与已有镜像库里的完整曲目信息不同，不能将后者假定为服务运行的必要输入。

## TLS 与进度同步

- Node HTTPS 客户端连接原版本地 18443，SNI/Host=`api.udon.dance`，保持证书验证，成功取得上游 302；已证明该配置的 TLS 透传可工作。
- 本机 curl 的一次 TLS 测试报 Schannel `SEC_E_NO_CREDENTIALS`，不代表上游或原版 TLS 失败。
- 旧游戏日志有中途进入后跳到 176.57 秒的样本；本次切换 HKG 后实际恢复至 94.66 秒并正常播放。
- 这些证据不能代替独立的多人中途加入验收。
