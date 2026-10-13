# Issue #14：候选方案增量评估

本轮按用户 2026-10-13 的要求，仅逐项验证候选方案并记录证据，不作最终选型、不迁移正式后端、不关闭 issue、不 push。历史报告的 C 建议保留为历史意见，不作为本轮的结论或停止依据。

读取 issue #14 正文（2026-10-13 15:16:54 北京时间更新）及全部三条评论；起始源码 `6b212e0e4b00efa7371c0da131098b78ebbea98f`，工作区干净。

| 路线 | 本轮处理 |
| --- | --- |
| A 逐节点 Adapter | 引用既有实现、实验及订阅兼容失败记录，不扩张协议白名单 |
| B1 完整核心 + 内存入口 | 保留 69/69 历史 B1/C 矩阵，仅补必要的共同负载对照 |
| B2 完整核心 + 原生固定对象直拨 | 新增实验入口、通用所有权与有界准入；优先验证取消、固定身份和协议会话 |
| C 独立核心 + 固定认证 SOCKS | 使用既有核心夹具做共同负载对照，不进入正式迁移 |
| D 同进程完整核心 + 固定认证 SOCKS | 在需要区分入口与进程影响时做小范围诊断，单列证据 |

## 实现与约束

实验仍固定 Go 1.25.0、Mihomo v1.19.32、`no_easytier`、同一独立模块依赖图。B2 沿用原生输入门禁和完整 executor 初始化，仅替换业务入口：初始化时捕获允许的固定 `C.Proxy`，调用上游 `proxydialer.New(proxy,true)`。不从运行中的全局名称表重新绑定。`NewByName` 的缺名/内置出口负例使用只含本代节点的映射。

通用准入的 64 个槽位覆盖在途拨号和已交付连接；满额立即拒绝。每次拨号有 3 秒实验上限，前后检查取消，迟到连接关闭。即使上游忽略取消，仍占原槽位，不能无限增建后台任务。退役取消在途拨号、关闭已交付连接，随后关闭核心并有限等待。此包装不追踪核心私有协议 socket，不声称全局状态可卸载。

本轮只改实验工具、测试和文档，不改变产品运行行为，因此不新增实机编号。LA-034/035/036、LA-018、LA-012 及其他既有待验项目保持原状态；未启动游戏或修改 hosts。

## 验证记录

分阶段记录实际运行、失败、证据与限制。自动开发检查与实机验收分别管理。最终结果在后续评估提交中补齐。

第一阶段：`incremental.ps1 -Phase build` 成功。`go test -mod=readonly -tags=no_easytier ./probe -count=1` 通过（含迟到结果、容量、退役、200 次取消交付竞争、无效目标与原生输入门禁）；内嵌和 `external_only` 两种 vet 通过，驱动/核心/夹具构建成功。尚不能用这些单元测试证明真实协议生命周期。

### B2 实际生命周期

[最终矩阵](evidence/issue14-incremental-20261013/lifecycle.json) **51/51**。SS、VLESS/TLS、AnyTLS、Hysteria2 均经过真实回环协议握手、预热后预取消、HTTP 请求取消与恢复、退役关闭；SOCKS5/TLS/QUIC 无响应握手各经过 6×32 次 HTTP 取消压力。拨号峰值上限 64，等待 4 秒后在途拨号为 0；退役后本层占用和核心统计均为 0，采样无 TCP 监听和 UDP 端口。AnyTLS 退役即时 goroutine 为 4，31 秒后为 3。这里未观测私有协议对象数量，网络表与任务数不能代替“所有内部会话瞬时销毁”的证明。

两个带标记出口完成 40 路并发与 Range 正文哈希验证。实验在请求批次之间交换全局 `n0/n1` 映射并关闭空闲连接，B2 捕获的固定对象仍保持原出口。缺名、空名、DIRECT/GLOBAL/REJECT 被本代映射拒绝，失败代理没有请求可直连陷阱。订阅顶层 TUN/规则/端口未执行，业务目标未交本地 DNS。`executor.Shutdown` 后仍能新建请求，完整封装退役后旧 Transport 拒绝新请求；标准库 Resolver/Transport 未变，但核心全局 resolver/inner 保留。合成后台 panic 终止宿主，不声称发现上游 panic 漏洞。

**保留的失败与归因：**

- [沙箱轮次](evidence/issue14-incremental-20261013/history-sandbox.json)：网络表看不到本轮已知监听，阳性对照失败，停止；没有把空表当成无监听证据。正常用户上下文读取后通过。
- [首轮生命周期](evidence/issue14-incremental-20261013/history-lifecycle-backlog5.json)：49/51，两项 40 路并发失败，其余通过。首轮没有逐请求保存这两项的细节，此限制保留。
- [专项复现](evidence/issue14-incremental-20261013/history-bindings-backlog5.json)：Python 默认 accept backlog=5 下 B2 两批分别 23/40、19/40 请求失败，约 1–3 毫秒内、尚未取得 HTTP 连接；B1/C 两批均通过。只改为 backlog=128、保持相同驱动二进制后，[三路线同负载全部通过](evidence/issue14-incremental-20261013/bindings-backlog128.json)。这支持夹具接入容量影响结果，不能推断 B2 公网稳定性差；首轮未细分底层 errno，不能把“连接拒绝”写成已直接测得。之后新增固定 errno 分类以便后续记录。
- 相关 `upstreamrequest` 测试在沙箱通过；`upstreamstate` 在沙箱临时目录原子 rename 被拒绝。正常用户上下文 `go test -mod=readonly -tags=no_easytier ./internal/upstreamrequest ./internal/upstreamstate -count=1` 两包通过，不改产品代码来适应沙箱故障。

复跑：`incremental.ps1 -Phase lifecycle`；专项比较用 Python 运行 `incremental.py --bin <本轮绝对输出目录> --phase bindings --backlog 5` 或 `128`。需要网络表能看到阳性对照。构建身份见 [lifecycle-build.json](evidence/issue14-incremental-20261013/lifecycle-build.json)，文件校验见 [manifest.json](evidence/issue14-incremental-20261013/manifest.json)。正常结束已验证本轮全部进程退出、provider 明文文件为 0、hosts 哈希不变。原始夹具清单留在受限的忽略目录。

### D 与共同负载工具

为区分历史 B1/C 合成吞吐差异中的入口与进程因素，增加 D 的小范围诊断：同一完整 executor，使用上游导出的 `inbound.NewSocks`、`BaseOption.SpecialProxy` 和随机认证，绑定 `127.0.0.1:0` 后读取实际端口，每入口固定一个本代出口。没有自制 SOCKS 服务或协议实现。只验证认证、固定出口、Range、失败无直连和监听清理，不复制完整协议矩阵。

初轮本地对照 [20/20](evidence/issue14-incremental-20261013/comparison-before-trace-lock.json) 包含 D 边界、四种入口三轮共同 SS 负载及 B2 60 秒混合取消负载。随后代码审查发现 HTTP trace 回调可能异步写测量字段，已加互斥保护；此前未运行 race，不能声称观察到了数据竞争。保留原轮次，正式汇总使用修正测量器后的复测，不把前后数字混为同一构建。

B2/D 在性能阶段使用 5 秒拨号预算，与上游 tunnel 的 5 秒连接预算对齐；生命周期阶段保留更紧的 3 秒预算。所有方案的请求超时、节点、目标和字节数相同，但准入含义仍不同：B1 限制完整 tunnel 操作，B2 限制原生拨号及交付连接，D 只限制本机 SOCKS 前端，C 沿用原实验的入口。正常负载低于 64，不用这些计数作对等压力容量排名。D/C 的客户端连接关闭不意味着核心在途握手立即结束。

`-Phase compare` 可独立执行全部本地成本检查；`-Phase real -PrivateSubscription <本地文件>` 只补公网验证，避免重复本地套件。用户已明确授权本轮复用既有私人订阅的前两个节点进行有限 API/视频取样；不拉取/更新订阅，不保存签名资源 URL。
