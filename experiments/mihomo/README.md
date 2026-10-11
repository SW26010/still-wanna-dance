# Mihomo 最小嵌入实验

2026-10-11，针对 [issue #14](https://github.com/SW26010/still-wanna-dance/issues/14) 的第一道验证门槛。

**结论：可以继续开发进程内薄适配层。不能把原始 Proxy 直接视为无状态、可随意关闭重用的 Dialer。** 本目录是独立实验模块，尚未把 Mihomo 加入正式应用的依赖或发行包，也不代表 issue #14 已完成。

## 实际运行结果

- 固定 `github.com/metacubex/mihomo v1.19.32`，Go **1.27.1**，Windows amd64，`CGO_ENABLED=0` 构建并运行成功。声明的最低 Go 版本未单独验证。
- 通过本地 replace 与主项目共享依赖选择；baseline 同时引用应用 console 和 upstreamrequest，验证链接兼容性，不启动 console。
- 客户端直接 `adapter.ParseProxy` → `Proxy.DialContext` → HTTP 请求/完整正文校验，无 SOCKS5 中转，无完整 Mihomo 初始化。
- `controlled-lifecycle` 运行中 **43 项必需检查通过**。原始取消行为单独记录，不与保护层的通过结果混淆。

| 协议配置 | 真实 HTTP、4 路并发 | 3 次顺序请求的底层连接数 | 错误认证 | TLS 错误指纹 |
| --- | --- | --- | --- | --- |
| SS / aes-128-gcm | 通过 | 3 | 拒绝业务 | 不适用 |
| VLESS / TCP + TLS | 通过 | 3 | 拒绝业务 | 拒绝 |
| AnyTLS / 默认复用 | 通过 | 1 | 拒绝业务 | 拒绝 |
| Hysteria2 / QUIC + TLS | 通过 | 1 个 UDP peer | 拒绝业务 | 拒绝 |

服务端位于独立进程，通过真实 loopback TCP/UDP socket 交换协议数据；不是 mock 或 net.Pipe。服务端仅允许转发到实验 HTTP origin 和指定链式父节点目标。证书临时生成并以指纹校验，没有跳过证书校验。

会话计数来自服务端 TCP Accept / UDP 源地址计数。顺序复用检查使用 net/http 编解码并同步关闭每条业务连接，再开始下一次请求。早期使用 HTTP Transport 的紧密循环出现过 AnyTLS 3 次请求建立 2 条会话：Transport 的连接关闭与返回响应不同步，不能据此要求始终恰好一条会话。这里验证的是连接归还后的协议会话复用，不是 HTTP keepalive。

## 发现的必要适配边界

1. **初始化不能省略。** 单独导入 adapter 后，SystemResolver 已初始化，但 ProxyServerHostResolver 和 DefaultResolver 为 nil。实验显式安装只解析 `proxy.invalid` 的代理服务器 resolver，并用拒绝型 resolver 拦截默认/系统查询，禁用系统 hosts。真实请求使用业务域名 `business.invalid`，未触发本地业务或系统 DNS 查询。此结果验证了这些路径可注入 resolver，不代表已验证生产 DNS 服务或全部特殊协议路径。
2. **原始取消语义不一致。** 热会话下 AnyTLS、Hysteria2 对已取消 context 仍可能返回连接；VLESS 返回含 `context canceled` 的错误，但 `errors.Is(err, context.Canceled)` 不成立。实验 gate 在拨号前后检查 context，并统一错误归因。
3. **Close 不等于永久退役。** SS、VLESS、Hysteria2 在 Close 后还能完成业务。尤其 parser 包装的 `autoCloseProxyAdapter` 只执行一次 Close，而 Hysteria2 可重新建立会话；再次 Close 无法回收它。独立的原始生命周期探针观察到了残留 QUIC socket 和 goroutine，直到进程结束。正式适配层必须禁止退役后的新拨号。
4. **资源属于节点生命周期。** 实验 gate 登记在途拨号和已建立连接，退役时取消拨号、关闭自有连接，再关闭 Proxy。四种协议均通过预取消、冷 DNS 100ms 超时、退役关闭活跃流、退役拒绝新拨号检查；业务无响应时 200ms 超时也通过。gate 是验证原型，尚非可直接投产的刷新/并发关闭实现。
5. **链式依赖需绑定对象。** 无 tunnel 环境下，裸 `dialer-proxy` 名称不能工作。移除名称并以 `WithDialerForAPI(proxydialer.New(parent, false))` 绑定 SS 父对象后，VLESS 子节点的真实 HTTP 请求成功。这只覆盖 SS → VLESS 的 TCP 链，不证明任意链或 QUIC 链成立。

上述行为可在固定版本的源码核对：`adapter/parser.go`、`adapter/outbound/base.go` 的 autoCloseProxyAdapter、`transport/anytls/session/client.go`、`component/resolver`，以及固定 sing-quic 依赖的 `hysteria2/client.go`。

## 系统副作用与清理观察

正常运行对客户端 PID 采样，没有 TCP Listen；QUIC 需要出站 UDP socket，最后三次客户端仍存活的采样中 UDP socket 已清空。原始 Close 后重拨探针单独运行，不用于声称资源释放通过。

执行前后比较 Windows 用户代理设置、WinHTTP 代理、hosts 文件哈希、网卡列表和路由表，均未变化。服务端 fixture 自己的 loopback 监听与客户端 PID 分开核对。采样不能证明没有极短暂监听，也未覆盖每一个系统设置。

导入库仍会启动少量全局后台 goroutine，例如统计和 observable；AnyTLS 清理循环可能在 Close 后等待其 30 秒 sleep 才退出。因此不能声称“导入没有任何全局副作用”或“Close 后立即零 goroutine”。

## 复现

在 Windows PowerShell 中，使用可访问 Go module 下载源的 Go 工具链：

```powershell
Set-Location experiments/mihomo
./run.ps1
./run.ps1 -OutputDirectory ../../artifacts/mihomo-embedding-raw -ExerciseAfterClose
```

第一条是受控生命周期门槛，必需检查、系统快照或清理采样不通过会返回失败。第二条故意违反原始 Proxy 的生命周期边界，记录重拨行为及残留资源；它的 Passed 仅指观察实验执行通过，不能解释为资源释放通过。不要把它并入正常应用路径。

脚本构建 baseline、fixture、probe 三个程序。fixture 隐藏运行且有五分钟存活上限；脚本 finally 清理自己创建的进程。客户端启动及结束前各等待默认 6 秒供系统采样，可用 `-Hold 15s` 延长。产物写入 ignored `artifacts/`；包括 results、summary、build-info、系统快照、socket 采样、goroutine dump 和日志。

`evidence/` 保存本次精简证据，不含可执行文件、私钥或系统网络清单。完整本机结果位于 `artifacts/mihomo-embedding-controlled/` 和 `artifacts/mihomo-embedding-raw/`。

## 依赖与验证范围

实验没有继承 Mihomo go.mod 的 protobuf replace，当前选择 `google.golang.org/protobuf v1.34.2`，上述构建和协议路径通过。不能据此推断所有协议都不需要上游替换项。正式引入仍需审查实际依赖图、许可证与构建信息，不能把实验编译成功视作发行条件已满足。

未验证公网节点、第三方服务端互操作、VRChat、Reality/WS/ECH、混淆/端口跳跃、任意订阅导入、UDP 业务、复杂链、长时间重连、刷新竞争、规模测量和 race detector。服务端也使用固定 Mihomo 协议实现，互操作覆盖有限。实验 gate 采用立即关闭在途连接的策略，生产刷新是否宽限仍需明确。

现有 `c86a21a` 的固定多出口 upstream Pool 可以保留。后续工作应接入受控 Mihomo adapter，并用上游 convert/节点解析替换旧的手写协议 schema/URI 方案；无需推翻“一节点一 channel”的架构。
