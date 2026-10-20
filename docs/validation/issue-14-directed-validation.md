# Issue #14：B2 定向验证

2026-10-13，在 `3a91bb6` 的中立评估后，补查 Hysteria2 热流取消/恢复及重试职责。固定 Go 1.25.0、Mihomo v1.19.32、Windows amd64、CGO=0、`no_easytier`。只修改实验、测试和文档；没有选定架构、迁移产品、读取私人订阅或启动游戏。

## Hysteria2：可控阻塞、排空与恢复

[初轮结果](../evidence/issue14-directed-20261013/hot/results.json) **24/24**；[单元测试](../evidence/issue14-directed-20261013/hot/tests.json)、[构建身份](../evidence/issue14-directed-20261013/hot/build.json)、[源码及证据哈希](../evidence/issue14-directed-20261013/hot/manifest.json)。随后补强会话身份观测并修正排空辅助层，最终[25/25](../evidence/issue14-directed-20261013/hot-session/results.json)，见下文。先前 256 次随机定时取消的“未触发”记录保留，不能改写成当时已通过。

源码核对发现，固定版本 `sing-quic/hysteria2.Client.DialConn` 在已有会话上调用**非等待式 `OpenStream()`**；额度不足立即报错，没有使用 `OpenStreamSync(ctx)`。返回的 `clientConn` 在首次 Write 发送 TCP 请求、首次 Read 等待 TCP 响应。因此必须区分“本地 QUIC 流对象分配”“Hysteria2 TCP 响应完成”“HTTP 首响应”，不能把 Dial 返回或 `GotConn` 当作后两者成功。

夹具 `hotfixture` 独立进程调用相同依赖图中的上游 `hysteria2.NewService`，不链接进 B2 驱动，不 fork 核心、不注入客户端锁、不替换 OpenStream。服务只绑定回环，目标只允许两个固定 `.invalid:80` 名称，无公网转发。服务端读到真实热流请求后，扣住协议响应，直到控制面显式放行或客户端关闭。取消依据服务器已进入等待的屏障，而不是随机 1 ms 定时器。

| 场景 | 操作及实际结果 |
| --- | --- |
| 阳性对照 | 预热请求成功；单条热流进入响应门闩，显式放行后成功返回 `ok`，证明夹具并非永久坏服务。 |
| 64 槽饱和与取消 | 64 条热流全部到达服务器后再保持至少 100 ms。准入 `occupied=64 / openConnections=64 / pending=0`；额外拨号立即 `dial-capacity`，不新增后台工作。取消后 64 个调用者均在 1 秒门槛内结束；不释放门闩，服务端 held 和本层占用均归零。 |
| 取消后恢复 | 正常请求在同一 QUIC peer 上成功；重新进入受控等待并显式放行后也成功。不是通过更换节点或重启掩盖恢复失败。 |
| 带在途热流退役 | 同样扣住 64 条响应，关闭 Pool 和完整运行时；调用者均在 1 秒门槛内结束，双方等待/占用归零，旧 Dial 拒绝再请求。新进程代次可对同一夹具成功请求；没有声称已退役对象可复活或同进程可卸载重建。 |
| QUIC 流额度耗尽 | 服务端 `MaxIncomingStreams=2`，实际持有 2 条流后下一次 Dial 在 500 ms 门槛内失败，没有排队。关闭持有流后双方排空；额度传播期间恢复探测第 3 次成功，仍为同一 peer。**不能声称释放后首个请求必成功**，工具记录恢复尝试次数。 |

取消及退役轮次各自的 64 条流都已从原生 Dial 返回；这验证的是**热流延迟握手响应中的阻塞与恢复**，不是声称制造了一个不存在的 OpenStream 等待路径。当前实现没有对核心私有互斥锁设置可控阻塞点，所以不把这组结果外推为任意内部锁竞争都可取消。计时采用 Windows 本地时钟，零/亚毫秒记录不代表精确瞬时完成；通过依据是明确的状态屏障和宽松的上限。

### 有界不等于必然恢复

新增 `TestAdmissionBoundedDoesNotImplyRecovery`（release / retire 两个子例）让 4 个上游函数明确忽略 Context，并在可控屏障等待。调用者取消、20 ms 拨号预算到期及额外 50 ms 观察后，4 个 pending/槽位仍占用；健康新请求因容量不足被拒绝。退役可以拒绝新工作，但同样不能强行结束这些函数。只有释放上游屏障、实际返回后，4 个迟到连接才关闭、槽位归零；未退役代次恢复请求，已退役代次保持不可用。

这是**不配合取消的上游函数模型**，不是本轮发现 Hysteria2 有永久挂起故障。它明确了通用封装的保证范围：限制任务增长和迟到连接所有权，不保证不返回的任意 Go 函数可被终止。若未来实际协议存在这种长期不收敛，不能通过提前归还槽位、不断创建新任务来“恢复”；应保留失败并据真实故障讨论上游修复或更大故障域的恢复。本文不据此决定 B2/C。

### 收尾复核发现及修正

初轮用 UDP peer 地址观察会话延续，不能独立排除同地址重新握手。夹具补用上游 TLS `GetConfigForClient` 公共回调只计数、不改变配置：最终[热流矩阵](../evidence/issue14-directed-20261013/hot-session/results.json) **25/25**，取消、额度释放和再次请求期间握手数始终为 1；退役后的替换进程请求使握手数增为 2。由此区分原会话恢复和新代次重连，仍不推断所有内部协议对象的数量。

这次复核的单元测试实际触发 `sync: WaitGroup is reused before previous Wait has returned`。原因是实验 `admission.wait(timeout)` 在超时后仍遗留后台 Wait；活跃代次排空后接受新工作时，旧 Wait 尚未返回。原运行时正常只在退役后调用 wait，但恢复模型对活跃代次的限时观测暴露了这个辅助层问题，不能忽略失败。修正为锁保护的工作计数及每一批工作的完成 channel，限时观察不启动后台等待者；准入数量、拨号预算和协议路径不变。

[测试证据](../evidence/issue14-directed-20261013/hot-session/tests.json)中 `unit` 保留 panic 导致的包失败，`unit-fixed` 是修正后完整实验单测通过，`recovery-repeat` 是同一确定性屏障模型连续 10 次通过（每次 release/retire 两个子例），不是用随机增加 Hysteria2 次数补覆盖。复核中修改辅助层前的一轮 25 项结果保存在 [history.json](../evidence/issue14-directed-20261013/hot-session/history.json)，最终二进制和源码以 [manifest](../evidence/issue14-directed-20261013/hot-session/manifest.json) 为准。

## 重试、预算与失败反馈

[受控矩阵](../evidence/issue14-directed-20261013/retry/results.json) **19/19**，使用第一提交 `7e669f6` 对应的同一个驱动二进制，不修改核心重试或协议实现。观测对象是回环 SOCKS peer 的实际请求次数，不靠 HTTP 耗时推断重试。

辅助层修正后，使用最终驱动再跑同一套小矩阵，结果仍为[19/19](../evidence/issue14-directed-20261013/retry-final/results.json)。前后构建分别保留，以下表格的具体次数与 5.031 秒取自首轮，不能混为同一轮测量。

| 注入 | B1 | B2 |
| --- | --- | --- |
| 第一次握手在回复前 EOF，之后健康 | 一个 HTTP 请求成功，peer 实收 2 次拨号握手 | 第一个 HTTP 请求失败，peer 实收 1 次；显式下一次 HTTP 请求成功，总计 2 次 |
| 每次握手 EOF | 一个 HTTP 请求失败前实收 10 次，受上游 5 秒上下文和次数限制 | 一个 HTTP 请求失败，实收 1 次 |
| SOCKS 回复码 5 后关闭 | 第一个 HTTP 请求失败，实收 1 次；下一请求可恢复 | 同左；额外 raw Dial 确认原生接口此时返回成功 |
| HTTP 100 ms 超时、peer 扣住握手 | HTTP 返回后仍有 1 个核心任务；约 5.031 秒后核心及 peer 归零 | HTTP 返回后仍有 1 个 pending；实验拨号预算统一设 5 秒，约 5.031 秒后排空 |
| 预取消请求 | peer 未收到拨号 | peer 未收到拨号；另直接传入原生 Dial 的 100 ms Context 能结束本轮慢握手，pending 归零 |

代码依据：[Mihomo tunnel 的 `retry` / `handleTCPConn`](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/tunnel/tunnel.go)、[`proxydialer.New`](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/component/proxydialer/proxydialer.go)、[`socks5.ClientHandshake`](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/transport/socks5/socks5.go)。B1 的循环最多 10 次，共用 `DefaultTCPTimeout=5s`，退避间隔带抖动；特定 resolver/loopback 错误直接停止。B2 不经过这层循环，但仍包含各协议自身的实现。此处的 10 次是一次受控结果，不外推为所有协议、所有错误都重试 10 次。

发现了必须保留的语义差异：固定版本原生 SOCKS `ClientHandshake` 读取 `VER/REP/RSV` 后未检查 REP，继续读取绑定地址。夹具完整返回 REP=5 后关闭，原生 Dial 已认为成功，随后 HTTP 失败，因而不能触发上述 Dial 错误重试。首次实验错误地预期它会触发 B1 重试，得到 **10/12**；该反例与初次脚本变量遮蔽的启动失败均保存在 [history.json](../evidence/issue14-directed-20261013/retry/history.json)。随后增加明确的握手 EOF 对照，没有为通过检查更改上游协议。这个结果也再次表明不能以 Dial/GotConn 成功给节点记业务成功。它不证明历史 backlog 的差异来自重试。

### 本轮核对后的职责约定

以下是后续接入任一候选时应遵守的边界，不代表本轮已迁移 B2 或建设另一套请求/协议引擎。

| 所有者 | 保留职责与预算 |
| --- | --- |
| 完整核心 | 节点协议、认证、TLS/QUIC 握手、会话恢复和协议内部机制。SWD 不复制 B1 的 10 次循环，不添加按协议重试。固定出口不能因失败换节点或 DIRECT。 |
| SWD 请求层 | 只对允许重放的现有 GET/已校验 Range 继续现有候选回退；同一业务操作共用父 Context，不因下一候选或下一层重新获得整份时间。API `channelAttempt` 为剩余候选分配剩余时间、单候选上限 10 秒；清单上限 30 秒。候选清单最多 6 项，该数不是底层握手次数。多 URL/多 Range 的外层流程仍由既有调用者约束，不能宣称全操作只会产生 6 次物理连接。 |
| SWD 下载层 | 复用 `resourceAttempts`、正文/Range 校验和已有任务总期限。多候选游标不因正文失败复位；单通道的块级重试、最多 4 次 Range 循环仍是既有独立约束，不另建重试器。完整正文已接受后保留任务期限，不因切换通道重新发送已交付内容。 |
| Dial/连接所有权层 | 有界准入、原生 Context、迟到连接关闭、退役及**独立的后台收敛上限**。`net/http` 可把 Dial 从请求取消/期限分离，所以“请求 100 ms 返回”不能冒充“后台 100 ms 已回收”。本次比较两入口都保留 5 秒核心预算；正式接入必须记录请求上限与后台回收上限，不在未结束时归还槽位来绕过预算。 |
| Monitor | 观测完整 API/资源操作的成功、首响应、正文、吞吐；保持固定 channel 身份。`connect` / `headers` 仅为当前入口的 trace 阶段，不能把 B1 提前 GotConn 与 B2 协议完成时间直接比较为节点质量。业务失败不自动覆写独立测速历史。 |

**失败反馈仍有明确接入缺口**：当前 `errorState` 将 `context.Canceled` 记为 canceled，`record` 不覆盖已有健康观测；timeout 为超时，无成功延迟。但是实验容量错误 `dial-capacity` 和现有 `ErrUnavailable` 都归为 `network_error`，若作为 Monitor 测速错误输入，就会产生 unavailable。不能把这称为节点质量已正确分类。正式接入前应提供协议无关、可判别的本地容量/退役/调用者取消状态，使本地背压和代次失效不污染节点观测；真实业务超时仍如实保留为端到端失败，不擅自归因远端。此处只记录需求及当前行为，没有修改生产分类或以重试掩盖它。

针对性回归新增 `TestEntryFailureFeedbackBoundaries` 和 `TestBusinessRetryDoesNotResetBudgetOrRewriteMonitor`：覆盖 GotConn 前后相同错误、失败无有效延迟、取消不覆盖既有健康观测、容量/退役的当前分类缺口；两个业务候选的成功回退、全失败与父取消，没有刷新总期限或改写 Monitor。连同既有慢候选预算预留、Range 头/正文失败回退、完整正文任务期限、4 次块失败上限及取消/代次丢弃，正常用户上下文 **8 个顶层测试、32 条测试/子测试通过，3 个包通过**。见 [tests.json](../evidence/issue14-directed-20261013/retry/tests.json)，`retry-tests-normal` 为最终轮次；首轮 `retry-tests` 是沙箱记录，Console 隔离配置权限和 CacheProxy 临时 rename 被拒绝，未将其归为产品失败或删除记录。

## 复现与验收边界

构建与运行命令见 [实验 README](../../experiments/runtime-mihomo/README.md)。`directed.py` 每次创建新忽略目录并保存成功/失败，使用 Windows Job 约束子进程；最终正常轮次的进程全部退出，hosts 哈希不变。单元测试及两种入口 vet 通过。构建时工作区包含本轮代码，所以保留实际 `vcs.modified=true`；源码字节由本轮独立 manifest 固定，不复用旧实验构建身份。

仅开发验证。未验证所有协议/内部锁、真实游戏/音画/多人、长时运行、跨用户 DPAPI、发行包或完整生产故障恢复，未运行 race。本轮只改实验与测试，不改变产品行为，因此不新增实机项；LA-034/035/036、LA-018、LA-012 继续待验收。
