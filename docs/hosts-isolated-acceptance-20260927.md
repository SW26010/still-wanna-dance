# hosts 接入下的上游探测修复验收

> 历史记录说明（2026-10-06）：文中的 CF/HKG 是当时的界面、日志标签或 API 参数称呼。域名、IP 及地理位置只属于当次观测，不构成节点到域名的固定映射，也不保证这些域名持续存在。当前实现以有效 API 返回的域名集合为准，见[资源域名规则](resource-domains.md)。原始证据保持原样。

2026-09-27，在 API、CF、HKG 三个域名均保留 `127.0.0.1` hosts 映射的 Windows 环境运行。修复前，验收脚本的上游请求使用系统解析，会访问本地服务，把视频响应误判为协议变化。

修复将 HTTP/HTTPS 上游请求改用直接 DNS 查询，解析失败不回退 hosts，过滤回环、内网和本机网卡地址；本地请求继续使用显式回环端口。原始域名用于 Host 和 HTTPS 证书校验。报告新增实际连接地址。

从当前源码构建独立测试程序后执行：

```powershell
go build -o test-runs/acceptance-build/stepstash.exe ./cmd/stepstash
go build -o test-runs/acceptance-build/stepstash-console.exe ./cmd/stepstash-console
node scripts/acceptance.mjs test-runs/acceptance-build/stepstash.exe test-runs/acceptance-build/stepstash-console.exe
```

[完整结果与程序 SHA256](evidence/hosts-isolated-acceptance-20260927.json)：总体 `passed`，退出码 0。

- CF/HKG 上游 API 返回 302，两首歌冷下载、大小及 MD5 校验、跨 Host Range、播放 API、重启 HEAD 均通过。
- 控制台 Auto 冷/热播放，以及失败 SOCKS5 出口触发的 GET/HEAD/Range 本地降级、无缓存歌曲 502 均通过。
- 三次上游探测实际连接 `198.18.7.129`，本地请求连接 `127.0.0.1`。结果仅证明本次配置 DNS/代理环境中的 hosts 隔离与链路通过，不代表绕过系统代理或直接连接公网源站。

新增回归覆盖 DNS 地址过滤、解析失败禁止回退、HTTP/HTTPS 请求使用独立解析且保留 TLS 主机名，以及显式本地端口仍能访问测试服务。已纳入 CI。本轮没有改动 hosts，也未执行游戏联合人工验收。
