# 复现操作手册

本仓库还没有可启动的 StepStash 服务。下面描述原版黑盒测试环境与验收方法；原版程序、视频及测试工具需在本地另行准备。

现有本机调查工具保留在 `<reference-workspace>\investigation`，未迁入本仓库。它们含与该电脑绑定的路径和原版依赖，不应被当成通用的项目启动命令。

## 自动测试环境

1. 固定原版二进制版本并记录 SHA256。
2. 每组冷缓存使用新的空目录；热缓存只复制一首明确样本。不要直接修改常用歌曲库。
3. 检查 18080/18443 是否空闲，原版只监听 127.0.0.1。
4. 记录生效的进程环境。已有 RUST_LOG=warn 会覆盖 dotenv，需要在子进程显式设置日志级别。
5. 通过带 Host 的本地请求测试；避免在自动阶段修改系统 hosts。
6. 保存请求、状态、响应头、实际字节数、耗时、哈希、文件变化和原版日志。
7. 结束后只关闭本次创建的进程，验证监听已释放。

本次配置模板：

```dotenv
VIDEO_PATH_UD=./songs
CACHE_PATH_UD=./cache
NO_AUTH=true
LISTEN=127.0.0.1:18080
BUILTIN_SNI_LISTEN=127.0.0.1:18443
BUILTIN_SNI_PROXY=api.udon.dance=ud-orig.kiva.moe:443,play.udon.dance=ud-play.kiva.moe:443,nya.xin.moe=ud-nya.kiva.moe:443
RUST_LOG=info,wanna_cdn::http=debug,wanna_cdn::cdn=debug,wanna_cdn::cdn::proxy=info
```

此模板属于原版测试配置，不是 StepStash 已定义的配置接口。

## 网络预检

先从 API 获取当前地址，避免直接假定历史 URL 仍然有效。不要只看 302/206 头；必须检查响应正文实际字节数。

```powershell
curl.exe --noproxy "*" --max-time 15 -sS -D - -o NUL "http://api.udon.dance/Api/Songs/play?node=cf&id=1343"
```

测试时的 CF 回源样本：

```powershell
curl.exe --noproxy "*" --max-time 15 -sS -D - -o NUL -w "received=%{size_download} time=%{time_total}\n" -H "Host: play.udon.dance" -H "Range: bytes=20000000-20001023" "http://ud-play.kiva.moe/files/2403/1343-660524b4eb86f.mp4?e=REDACTED&s=REDACTED"
```

预期为 206、正确 Content-Range、received=1024。分别检查公开视频域名和原版回源域名，不能互相替代。`--noproxy "*"` 只排除 curl 显式代理，不能保证绕过透明代理或 TUN。

HKG 应使用实际 node=nya 获取 URL，并对照 ud-nya.kiva.moe + Host: nya.xin.moe。SHA 独立检查 139.196.46.195 的 TCP 51886；保持 TLS 验证，不用不明的成功/失败结果外推视频响应。

## 无需 hosts 的本地请求

在原版或观测入口已经就绪时，用 curl 单次定向连接：

```powershell
curl.exe --noproxy "*" --max-time 10 -sS -D - -o NUL --connect-to "play.udon.dance:80:127.0.0.1:18080" -H "Range: bytes=0-1023" "http://play.udon.dance/files/2403/1343-660524b4eb86f.mp4?e=REDACTED&s=REDACTED"
```

URL 中的 Host 仍是播放域名。真实 TLS 透传测试也必须保留原始 SNI 和证书校验，不能用直接请求上游替代域名来替代。

## 游戏实测

1. 用户进入 WannaDance 私人实例，先固定 CF，记录游戏日志文件和开始时间。
2. 先确认无本地接入时，同一歌曲的正常播放与网络预检。
3. 启动原版和 HTTP 记录器，验证本地热缓存小片段；再启用接入。
4. 备份 hosts，保留所有已有条目；管理员权限仅添加带唯一标记的 play/nya 两行。刷新 DNS，并用请求日志确认游戏确实进入本地。
5. 热缓存样本：确认 HIT、音画、实际返回字节。
6. 冷缓存样本：选本次客户端会话未播放过的歌曲。检查 Range 是否从零开始；若客户端只取中后段，就不能把它称为全新下载。
7. 等原版确认完整下载与元数据生成，检查落盘大小、MD5，再测相同文件命中。
8. 依次比较 CDN。切换会自动重载，避免马上重复点歌；遇到 SHA 超时后应等旧请求结束再单独验证 Auto。
9. 单独测试恢复进度、多人中途加入和切歌中断。
10. 恢复只由本次添加的 hosts 条目，校验恢复前后文件；关闭本次进程并确认端口释放。

不要把请求日志里 `resolved to` 视为成功播放：解析失败时也可能输出原 URL。至少结合 OnVideoReady/OnVideoStart、用户音画反馈和实际传输结果。

## Windows 操作经验

- 本机普通权限绑定 80/443 曾成功，不能据此保证所有机器都相同；hosts 修改仍需要管理员权限。
- 隐藏启动后台服务，记录 PID 和进程路径；清理时核对路径，不能按宽泛进程名结束用户的其他程序。
- Windows PowerShell 5 的 Invoke-WebRequest 不允许直接通过 Headers 设置 Range；测试工具曾改用 HttpWebRequest.AddRange。
- UAC 辅助程序必须将失败与退出码反馈给调用端；退出码 0 不足以替代对实际 hosts 状态的检查。
- 如果另有程序同时修改 hosts，恢复时仅移除本次标记行，不应覆盖其修改。没有并发变化时再用备份哈希确认完全恢复。
