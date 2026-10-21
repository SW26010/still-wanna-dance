# CF 实验的网卡绑定

当前 `cf_block_experiment` 测试构建必须用 `-cfexp-network` 显式提供本地配置。适用于 `TestCFBlockExperiment`、`TestCFFamilyExperiment`、`TestCFFamilyDNSExperiment`、`TestCFDiscoveryExperiment` 和小流量检查 `TestCFNetworkSmoke`。它只约束这些入口创建的连接，不是进程沙箱，也不自动约束其他测试或旧实验工具。

网卡选择、配置读取、Windows socket 绑定、监测和记录均位于 `cf_*_test.go`，受 `windows && cf_block_experiment` 限制。普通测试和正式 EXE 不包含这些入口，也不读取本地实验配置。产品只提供可注入的 DNS 流拨号器和固定候选池拨号器；默认实现、产品设置及 UI 不变。

本规则替代上述入口**新构建**的默认网络使用方式；README、FAMILY、DNS、DISCOVERY 中旧命令、冻结 EXE 及其结果保留为历史。不要覆盖旧 EXE，或把绑定前后的数据当作同一网络批次。

## 固定规则

- API 普通请求、媒体预检、Monitor、DNS/额外固定候选、DoH 查询及 provider 引导、候选失效后的 TLS 探测共用同一个绑定拨号器。继续复用产品 DNS、Happy Eyeballs、候选租约、Host/SNI、证书验证、连接池、重试和下载调度。不接入系统代理、系统 DNS 或 hosts。
- 配置只指定持久网卡 GUID；启动时读取当前名称、IPv4/IPv6 接口索引及地址。每次连接重新读取该 GUID，从有效且处于 preferred 状态的地址中选择对应地址族的源地址，同时用 `IP_UNICAST_IF` / `IPV6_UNICAST_IF` 指定出口并读回核对。仅绑定源地址不代替指定出口接口。
- 目前这些 CF 实验要求可用的 IPv4 和公网 IPv6 源地址。缺配置、未知字段、无效 GUID、找不到网卡、断线、缺地址族、运行中接口索引改变、socket 绑定失败或连接源地址不属于该网卡，均失败关闭；不选择另一网卡，不恢复这个已失败的批次。IPv6 ULA/link-local 不能充当本实验的公网源地址。某个远端超时仍按原实验规则计为该请求失败，不把它等同于整个网卡失效。
- 每次建连检查网卡，空闲期间每秒检查一次；发现失效即取消整个批次。不是瞬时链路抓包，不能声称捕获了所有短于检查间隔的掉线。已设置出口的 socket 不会由实验代码重建成未绑定连接。
- 正文、目标 IP、Host/SNI/TLS 和既有协议校验继续生效。配置变化需新进程、新连接池及新输出目录；不热切换出口。
- 绑定仅影响这些 socket，不修改系统默认路由、网卡状态、hosts 或路由器。普通用户可以使用此 Windows socket 机制。路由器是否绕过透明代理、IPv4/IPv6 是否保留目标地址，仍需独立的路由器侧证据；不能由 TLS 成功或连接远端地址推断“裸连”。

Windows 依据：[IPv4 socket 选项](https://learn.microsoft.com/en-us/windows/win32/winsock/ipproto-ip-socket-options)、[IPv6 socket 选项](https://learn.microsoft.com/en-us/windows/win32/winsock/ipproto-ipv6-socket-options)。IPv4 设置接口索引用网络字节序，IPv6 使用主机字节序；两种读回均为主机字节序。使用现有 `x/sys/windows` 和标准库，不引入新的网络栈或依赖。

## 本地配置与证据

仓库保留本规则、实现、测试和 [空白示例](network.example.json)。示例的全零 GUID 会被拒绝，必须替换为本机实际选择。具体网卡、MAC/IP、路由器规则/放行说明、运行偏好和原始资料均放在已有 Git 忽略目录 `local/`、`artifacts/`；不要写入产品设置、AGENTS.md 或提交真实配置。

用普通 PowerShell 只读列出网卡（无需 CIM 或管理员权限）：

```powershell
[System.Net.NetworkInformation.NetworkInterface]::GetAllNetworkInterfaces() |
  Select-Object Name, Id, OperationalStatus
```

把选定 `Id` 填入 `local/cf-network.json` 的 `interfaceGUID`。运行目录、时间安排等个人偏好也可保存在 `local/` 的运行脚本/笔记中。歌曲、候选、轮数、流量上限及收益判据仍按各实验协议固定，不通过本地网卡配置偷偷改变。

每批新目录内保存：

| 文件 | 内容 |
| --- | --- |
| `network-private.json` | 生效配置、启动时接口身份/地址和致命绑定错误；结束时更新 |
| `connections-private.jsonl` | 每次 TCP 尝试的起止时间、地址族、接口索引、目标、本地/远端地址、底层错误；包含 DoH 和失败/取消的尝试 |
| 原有实验输出 | 计划、训练、请求结果及 Monitor 快照；继续遵循原有私密材料规则 |
| `smoke.json` | 小流量检查的阶段完成情况和双栈正文哈希；仅 smoke 入口产生 |

连接日志记录每次新建 TCP，不为复用连接重复造记录；日志不包含 HTTP URL、请求正文或凭据。保存失败同样取消实验，退出前等在途拨号记录完成。原始地址属于本地资料，公开报告只引用脱敏结论和必要哈希。

## 构建与运行

从仓库根目录执行。完整下载仍需按原实验协议另行安排；本节的 smoke 只做 DoH/bootstrap、普通 API/媒体预检、两个地址族各 1 KiB 的固定候选读取及 Monitor 播放 API 检查，不训练吞吐或启动游戏。

```powershell
./scripts/with-test-temp.ps1 go test '-tags=no_easytier,cf_block_experiment' ./internal/console ./internal/upstreamrequest -run '^Test(CFNetwork(Config|FailClosed|BindFailure|RejectsImplicitDNS|WatchStopsIdleBatch|SocketOptions|DNSInjection|EvidenceFailureStopsBatch)|CF.*Harness|DirectPoolInjectedDialer)$' -count=1
New-Item -ItemType Directory -Path artifacts/cf-network -Force | Out-Null
./scripts/with-test-temp.ps1 go test -c '-tags=no_easytier,cf_block_experiment' -o artifacts/cf-network/network-probe.exe ./internal/console
./scripts/with-test-temp.ps1 ./artifacts/cf-network/network-probe.exe '-test.run=^TestCFNetworkSmoke$' '-test.v' '-test.timeout=2m' -cfexp-network local/cf-network.json -cfexp-out artifacts/cf-network/smoke-01
```

输出目录必须不存在；已存在时检查历史结果，不覆盖、不自动重测失败批次。若直接用 `go test ... -args` 运行，Go 会把工作目录设为包目录，配置和输出参数须使用绝对路径；运行已构建 EXE 时相对路径以启动目录为准。

正式实验使用经过检查的新 EXE，记录实际 SHA256，并在既有实验命令中追加 `-cfexp-network local/cf-network.json`。完整窗口的资源、时间/流量上限、停止条件和统计方法仍以对应协议为准。绑定功能验证通过不等于路由器绕过验证或完整测速完成。

首次实现的自动检查、Windows 实网覆盖及剩余边界见[2026-10-22 验证记录](../../docs/validation/cf-network-binding-20261022.md)。
