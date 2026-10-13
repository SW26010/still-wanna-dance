# 完整运行时 B/C 有限对照

2026-10-13 后续中立增量评估见[新报告](../../docs/issue-14-incremental-evaluation.md)。`incremental.ps1 -Phase build` 构建 B1/B2/D 共用驱动与 C/核心/夹具；`-Phase lifecycle` 执行 B2 新增门槛，`-Phase compare` 执行必要本地对照，`-Phase real -PrivateSubscription <文件>` 在明确授权后单独做有限公网补测。参数 `-GoExe`、`-PythonExe` 与下述一致，输出另存 `artifacts/issue14/incremental/`，不覆盖历史实验结果。B2/D 性能阶段拨号预算 5 秒，生命周期 3 秒；D 只做入口诊断，不是新增正式后端。

`-Phase attribution` 执行受控 DNS 延迟和热会话取消竞争。本轮 Hysteria2 热建流快于 1 ms 定时器，未触发所需取消场景，因此该命令返回非零；不可据其他通过项将其标为通过。`export_incremental.py` 只导出脱敏 JSON、拒绝覆盖旧证据；`summarize_incremental.py` 生成含样本数/失败数的描述性统计；`verify_incremental.py --staged --bin <输出目录>` 核对 Git 暂存字节、二进制哈希、101 个共同链接模块和已评估 Go 源码。

对应 [issue #14 当前要求](https://github.com/SW26010/still-wanna-dance/issues/14)，比较 **B：完整 executor + 原生内存入口** 和 **C：同版本完整核心进程 + 固定 SOCKS5**。不调用旧 `Node.New`，不扩展旧协议白名单，不接入产品。结论、失败历史和生产设计见[评估报告](../../docs/mihomo-runtime-evaluation-20261013.md)。

## 复跑

Windows amd64、Go **1.25.0**、Python 3 标准库；`CGO_ENABLED=0`、`GOTOOLCHAIN=local`、`no_easytier`。独立实验模块固定 Mihomo **v1.19.32**，B 和 C 核心均用本目录同一依赖图编译，不继承上游 `replace`，不改变根模块。首次获取依赖后使用 `-mod=readonly`；`go.sum` 纳入版本控制。实验模块通过 `replace still-wanna-dance => ../..` 使用本工作区的 Pool、Monitor 和 SOCKS Dialer。

```powershell
./experiments/runtime-mihomo/run.ps1 -GoExe <Go1.25.0的go.exe> -PythonExe <Python3的python.exe>
```

可选 `-PrivateSubscription <本地私有订阅文件>`：只读取该文件，不拉取或更新订阅；原生加载全体节点后，仅取前两个做真实 Monitor 和最多 4 MiB/请求的视频 Range 取样。B/C 三轮交替访问**同一完整资源 URL、同一 Range、同样节点**，核对正文哈希。签名 URL 只通过进程管道传递，写证据前移除。不要提交原始文件。

```powershell
<Python3的python.exe> experiments/runtime-mihomo/soak.py --bin <仓库绝对路径>/artifacts/issue14/runtime-comparison --seconds 60
```

连续负载补测复用同一批二进制，每种模式默认 60 秒；每秒 8 次并发正常请求和一次 50 ms 超时请求，采集资源与任务数，最后退役。仅是分钟级开发检查，不代表长时游戏运行。

## 实验边界

- `probe/native.go` 仅调用上游 YAML 和 `ConvertsV2Ray` 提取节点，在构造 adapter 前做跨协议能力拒绝。它保留 WS/TLS 参数，拒绝依赖/动态出口、文件/插件/发现/overlay 能力及其下划线别名；不是完整生产导入器。生产需恢复逐条诊断、2048 节点预算、DPAPI 和稳定 HMAC 身份；这里的 256 节点预算与序号 ID 仅用于夹具。
- B 使用 `executor.ParseWithBytes → executor.ApplyConfig`，与 `hub.Parse` 的完整执行器相同；不调用启动控制面的路由部分。一个进程只初始化一个核心，状态不恢复到初始化前。`inner.HandleTcp` 返回的是内存管道，不证明远端已连通；错误不转普通拨号。
- 导出 `Tunnel.HandleTCPConn` 外包一层通用计数，最多 **64** 个未结束操作，满额立即失败。该上限独立于节点 K。不能排队等待：Go HTTP Transport 的拨号可以脱离原请求取消，早期 32 槽排队版曾产生积压；失败证据保留。
- `Close` 先拒绝新入口并关闭本层业务管道，再关闭核心代理、可关闭 provider 和 executor；最多观察 7 秒排空。没有追踪或改写核心内部各协议 socket。全局 resolver/inner 仍存在，不能当作多租户、热替换或可卸载运行时。
- C 复用旧实验的 `Core`、受限目录和 Windows Job：stdin 主配置、认证回环入口、单成员固定组及 `MATCH,REJECT`。正文先经过**与 B 相同的能力门禁**。正常退出清理本轮 provider；生产崩溃遗留清扫仍需实现。
- B/C 驱动共用 `main.go`，C 的 `external_only` 构建不链接完整 executor；两者都引用现有 console SOCKS 工厂，因而仍链接现行 adapter 的一部分依赖。这是实验二进制成本，不是最终桌面大小。B/core 的共同依赖版本以构建信息核对。
- 所有子进程在执行前加入本轮 Job，隐藏启动。只清理本轮进程和已知 provider 文件，不启动游戏、改 hosts 或操作用户媒体。输出位于忽略的 `artifacts/issue14/runtime-comparison/`；每次随机目录限制为当前用户/SYSTEM。
- 监听表先用本轮 HTTP 夹具做阳性对照。沙箱若返回空表，整轮标失败；空结果不能证明没有监听。需要正常用户上下文的只读网络表访问，不需要管理员改网络。
- 合成基准：3 轮交替、每轮 1 次冷请求、40 次热请求、8 × 16 MiB 正文 SHA256。工作集/CPU 是驱动加核心，排除夹具；Windows QPC 计时。C 的启动包含控制 API 轮询，B 为内存 ready，启动数字不宜解释为纯解析耗时。

```powershell
# 在本目录
go test -mod=readonly -tags=no_easytier ./probe -count=1
go vet -mod=readonly -tags=no_easytier ./probe
go vet -mod=readonly '-tags=no_easytier,external_only' ./probe
```

不需要启动正式 SWD；不自动运行实机验收或发布链。选型后的迁移仅保留一个正式 Mihomo 后端，比较工具可以继续作为历史证据，不成为永久双后端产品。
