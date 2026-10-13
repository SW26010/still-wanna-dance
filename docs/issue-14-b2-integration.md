# Issue 14 B2 产品集成

基线 `8ed4043`，Mihomo 固定 `v1.19.32`。开发分批提交，本轮不执行真实游戏验收或 push。

当前产品已切换为单个 B2 完整内嵌运行时；外部 SOCKS5 保留，旧 A 的协议执行、DNS/dialer 桥接和字段归一化已移除。下面按批次保存实现与验证范围，早期“尚未接线”表述仅描述当时提交。

## 导入与身份边界

新增原生 YAML/URI/Base64 输入路径，沿用 2 MiB / 2048 条预算、逐条结果和稳定排序。暂不切换产品入口，避免旧 A 接受它无法执行的节点。

导入仅提取节点，原生转换和 `ParseProxy` 校验不调用 `ApplyConfig`。跨协议能力门禁拒绝链式出口、动态组、文件/接口/路由能力、overlay/IP stack，以及可在原生解析失败时遗留 base adapter 的可选 smux 包装。v1.19.32 的普通 stream adapter 构造不拨号；AnyTLS 可创建空闲维护 worker，校验后立即 `Close`。升级核心须重新审核这些副作用；不维护协议字段白名单。

Mihomo 使用的 logrus 输出在包初始化时永久丢弃，转换前即生效；不订阅其含原始参数的事件。应用自身继续使用 slog 固定诊断。显示名不进入节点数据；不改写凭据，不承诺所有跨格式/默认值语义等价节点都合并。连接参数有变化可以保守重测，不能错误继承另一出口。

自动检查与各批结果在后续提交中补充。该批原生路径尚未接入产品，不新增实机编号；LA-034/035/036 保持待验收。

第一批自动检查：go test -tags=no_easytier ./internal/proxyengine/mihomo 通过（包含新原生导入测试与旧执行层回归）。

## Console 与 Monitor 切换

Console 持有一个 Runtime，仍以现有 `Proxy{ID,Dial}` / `NewMultiPool` 连接业务与 Monitor；仅初始化稳定暂定集合中最多 5 个出口。空库、密文不可读与纯直连不启动完整核心；强制代理无隐式直连。游戏 HTTPS 兼容透传仍独立直连。已有候选/共同预算、第五代理及部分节点已有观测时的剩余活跃回退保持不变。

节点载荷升至 version=2，DPAPI 封套仍为 version=1。旧载荷不迁移、不解释、不覆盖密文，只有用户重新导入并保存才替换；HMAC 域改为 `swd-mihomo-node-v2`，稳定指纹外层格式仍为 `stable-v1-`。旧 `channel.key` 保留，不复用旧节点观测；未知和坏快照同样保留。冻结旧明文夹具 `testdata/proxy-nodes-v1.json` 以验证拒绝和显式替换。

公共请求边界提供 `ErrCapacity` / `ErrRetired`。Monitor 将本地容量、退役和调用者取消保存在独立短期诊断中，保留原质量、来源和历史吞吐，不更新其时间或增加样本；正常超时、HTTP/正文/Range 错误仍按失败处理。UI 显示诊断与既有观测，测速状态也能解释未取得新样本。失败尝试保留原有自动测速冷却，避免繁忙时持续重试。

第三批自动检查：正常 Windows 用户环境中 `go test -tags=no_easytier ./internal/console ./internal/upstreamstate ./internal/upstreamrequest` 全部通过，包含真实 Console 到四协议回环、完整草稿/DPAPI/失败保存、第五候选/部分观测回退；本地质量与资源发现定向回归通过。`node --test scripts/refresh.test.mjs scripts/setup.test.mjs` **210/210** 通过。首次沙箱运行的进程身份/原子替换权限失败以及随后发现的测试多次初始化问题均未计为通过；测试已改为独立进程后重跑通过。LA-034/035/036 同步更新，LA-018/012 的真实退出和持续使用留待集中验收。

## 完整运行时与所有权

新增 `NewRuntime`：受控 `ParseWithBytes → ApplyConfig`，仅传入活跃节点，捕获固定对象后调用 `proxydialer.New(proxy,true)`。每进程只允许一次完整初始化，失败或退役后不能重新启动。目录由调用方传入绝对应用路径；配置无代理入站、TUN、订阅规则或自动更新；核心使用不读 hosts 的证书校验 DoH，标准库默认 resolver/transport 和 SWD 直连解析器保持独立。

每节点 32 个准入槽，分别覆盖未完成拨号、HTTP 空闲/活跃连接及其实际关闭；它与 K=5 无关，也不是性能最优值。默认拨号预算 10 秒，调用者取消不归还仍被后台占用的槽。连接 Close 的单次后台清理也占槽，避免迟到连接关闭卡住时无限增长。关闭立即拒绝新拨号，取消在途工作，整个连接/adapter/provider 关闭与排空共享 3 秒等待预算；超预算返回固定错误，依赖进程退出，不声称完整卸载全局状态。

第二批自动检查：`go test -tags=no_easytier ./internal/proxyengine/mihomo` 通过；补充 `TestAdmission|TestRuntime` 定向检查通过（含阻塞 Close、未响应取消、成功/取消竞争、不同节点容量隔离、四协议固定出口、名称重绑和初始化失败不可重试）。完整核心测试使用独立测试进程，无重置生产单例后门。`scripts/build-desktop.ps1 -Output artifacts/issue14-b2/SWD-stage2.exe` 成功；新增完整核心依赖已通过 `go mod tidy` 固定。产品接线尚未切换，本批不新增实机项，既有台账继续积攒。

## 旧实现清理与发行输入

第四批删除旧 A 的 Node、协议字段表、DNS/socket 所有权桥和相应旧测试。保留原实验目录与历史证据；依赖旧 API 的 managed-mihomo/probe 标为 historical_a，仅在原提交重现，不作为第二条产品路径。原生导入恢复唯一的 Import/Normalize 入口，说明和设置页同步原生节点范围。增加实际 VMess/WS 回环服务，覆盖它经过 Console 和生产固定对象的执行，而不只证明导入接受。

准入交付进一步统一由调用者收尾拨号 context，接收端复查本层 deadline，防止原生成功与超时竞争时交付迟到连接。新增回归验证迟到成功关闭并归还额度。核心 DNS 使用 AliDNS 与 Cloudflare 两个独立供应商的 literal-IP DoH，证书校验保持启用，无系统 hosts/UDP bootstrap；本地请求 AliDNS 的公开 example.com 查询得到 200，未使用私人节点。这项端点取样不承诺其他网络可达性，也不是实机验收。

新增实际链接模块及 Wintun 原始源码/签名文件来源核对见[第三方声明](licenses/README.md)。111 项复核清单保留旧记录；签名 DLL 的独立条款与 GPLv2 驱动源码、MIT Go 绑定分别保存。没有更换核心、关闭校验或省略源码输入以绕过发行检查。

清理阶段自动检查：Go 1.25.0 / Windows amd64 / CGO=0 的全部 cmd/internal 测试与 vet 通过；前端与发行脚本 **242/242** 通过。最初脚本测试受沙箱临时 Git 目录写入限制，提升至正常用户测试环境后通过。一次全量 Go 测试与无节流持续负载相邻执行，出现 Windows 临时端口耗尽；失败日志保留，未计为通过。

四协议有固定频率的 60 秒负载完成 21,430 次成功请求，穿插每十次一次响应取消并检查退出全部额度排空。两轮无节流约十余万请求后的回环连接失败和端口耗尽单独保留，不把固定频率通过外推为无限连接能力。随后增加 VMess/WS 的最终五协议结果、干净源码发行链结果在后续验证记录中补充。

第五协议补充：包含 VMess/WS 的完整核心测试通过，60 秒十个 worker 共 26,757 次正常请求，每十次穿插一次取消，最终全部准入槽与拨号 worker 归零；Console 五协议冷请求专项通过。没有使用原始私人订阅或公网代理。

本机没有已安装 C 编译器；仅在忽略目录解压官方 Zig 0.17.0，补充 Windows 系统同步库后 Go 1.25/1.27 的 race 二进制可链接，但 ThreadSanitizer 启动时均报地址分配失败（错误 87），固定映像基址复查也未解决。**不声称 race 通过**；现有 Linux CI race 检查仍保留，本轮不 push、不触发远端 CI。该限制不等同发现业务数据竞争。

## 最终开发验证与提交

实现按顺序提交为 `9c1c294`（原生导入边界）、`4383a84`（有界固定出口核心）、`c74ba50`（Console/Monitor 接线）、`d7132b3`（移除旧 A 与五协议回归）、`5af35b4`（声明和对应源码输入）。后续 `56c9967` 修正历史工具中重复的构建约束，仅工具变化，不新增实机项。

最终干净副本 `56c9967` 使用 Go **1.25.0**、CGO=0 完成 `go test -tags=no_easytier ./...`、`go vet -tags=no_easytier ./...` 和 Linux amd64 桌面入口交叉构建。不是 Linux 执行验证。结构化[开发检查及失败范围](evidence/issue14-b2/development-checks.json)与[证据哈希清单](evidence/issue14-b2/manifest.json)保存了失败、复测和未覆盖项；完整原始工具日志仅在忽略目录中。

实际发行链绑定 **`5af35b4806a85d1e36049e443072a1cf7380c6e5`**（后续仅历史工具/本报告变化，产品代码未变），Go 1.25.0 / Windows amd64 / CGO=0 / no_easytier：真实桌面 EXE、109 个实际链接模块的声明、109 份完整模块 ZIP、195 个模块图条目的元数据及 SQLite/Wintun 原始输入全部核对。导出的源码无 Git 和本地 artifacts，无 EasyTier 模块或其已知 WASM。空缓存、仅 file GOPROXY 的独立重建通过，不承诺逐字节相同；真实 Portable ZIP 的隔离条款门禁、实例身份和搬迁 smoke 通过，未进入游戏或修改 hosts。见[发行证据](evidence/issue14-b2/release-validation.json)。

本地包保留在 `artifacts/issue14-b2/release-check/`：Portable ZIP 19,362,806 字节，对应源码 194,582,359 字节。EXE SHA256 为 `7af46bcbe3ed80079cd5ac5e1ea0499bb354498ad916e182c5dfece5a29da5de`，包哈希与源码哈希见证据。`v0.0.0-issue14-b2-validation` 只存在于忽略目录内的本地验证副本；未给主仓库创建标签、未发布 GitHub Release、未 push。包内生成的发布 URL 是待发布占位链接，并非可下载的远端资产。

开发实现完成；实机 **LA-034/035/036、LA-018/012** 保持待验收。下一轮仍需真实节点、游戏播放/下载、桌面重启/退出、跨用户或机器快照，以及约定时长的持续运行证据。60 秒回环和 Portable smoke 不替代这些场景。

## 审查收尾

2026-10-13 在 `786b4a1` 基础上修复两项审查发现。`ErrUnavailable` 现归类为 `channel_unavailable`，直连 DNS 候选过期/移除不再显示“代理运行时已退出”；只有 `ErrRetired` 使用该退出诊断。独立诊断继续保留已有质量、吞吐与资源来源，不更新原样本时间或延长有效期。行内诊断及自动测速状态同步显示“通道已失效”。

原生导入仅在提供 `port` 时执行通用规范化和范围校验，必填字段、替代字段和互斥关系交由 Mihomo 原生解析器判断。Mieru 仅含合法 `port-range` 的节点可接受，不补造单端口；单端口字符串仍规范化，同时提供两者、缺少两者和反向范围仍拒绝，没有新增逐协议字段规则。

本轮自动检查使用 Go **1.25.0 / Windows amd64 / CGO=0**：`go test -count=1 -tags=no_easytier ./internal/proxyengine/mihomo ./internal/upstreamstate ./internal/upstreamrequest` 三包通过，相同三包的 `go vet -tags=no_easytier` 通过，覆盖原生导入、错误分类、历史观测保留与候选过期/关闭；`node --test scripts/refresh.test.mjs` **128/128** 通过，包含直连通道失效与真实运行时退出的不同文案，以及已有延迟/吞吐仍可见。Mieru 此处只验证本地导入语义，不声称真实服务端连通。

台账 **LA-034/035** 已补充范围端口导入、保存/重启和通道诊断步骤，联合 LA-036 核对界面；状态继续待验收。上述旧发行链及 `5af35b4` 包不包含本次产品修复，本轮未重新构建发行包、启动游戏或修改 hosts；原有 race 未覆盖范围继续保留。

## Windows 发行与 race 收尾

2026-10-13 至 14 日，先对 `8e3eef9` 完成干净副本发行、离线源码重建与 Portable 检查。使用经发布方 SHA256 校验的便携 w64devkit 2.10.0（GCC 16.2.0 / MinGW-w64 14.0.0）配合 Go 1.25.0，race 检测器可以启动；有锁最小对照退出 0，故意无锁对照报告 DATA RACE 并退出 66。此前 Zig 启动失败继续保留为工具链失败历史。

首轮全仓 race 没有 DATA RACE 报告，但 `TestLiveTailCoalescesSnapshotsBeforeDownload` 因关闭后 SQLite 文件仍被占用失败，随后 10 次原样复测中 2 次失败。独立受控查询确认取消后 `DB.Close` 返回成功仍可残留文件句柄，与[SQLite 上游 !81](https://gitlab.com/cznic/sqlite/-/merge_requests/81)一致。采用已发布的 `modernc.org/sqlite v1.40.1` 修复，保留 SQLite 3.50.4 引擎及其他依赖版本，同步许可证和对应源码输入，不在 SWD 中复制驱动修补。新增 Windows 文件释放回归在旧驱动失败、新驱动连续 10 次通过；原 Console 失败测试在修复版本也连续 10 次通过。带关闭日志的诊断副本未复现，不能以该诊断运行代替最终版本回归。

`8e3eef9` 的 B2 五协议 60 秒 race 负载通过；这个结果和该版本的发行包均保留为修复前证据。修复后的全仓 `go test -count=1 -tags=no_easytier -race -json ./...` 通过（14 个包、1,188 个测试/子测试通过、15 个跳过，无失败或 DATA RACE）。发行声明及源码策略的 9 个脚本测试通过。最终发行产物和精确版本在后续归档中记录。产品行为涉及取消和退出，已补充 LA-018/012；其余实机项继续积攒，没有游戏、hosts 或跨用户验收结论。
