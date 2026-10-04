# Canonical 上游检查服务

本包测量固定上游服务，并按操作维护状态与推荐。职责和刷新交接见 [上游通道设计](../../docs/upstream-channels.md)。Monitor 不依赖 console、DNS 或 SOCKS5 实现；它从应用的 upstreamrequest.Channel 获取可执行候选。Console 在条款同意后启动 Monitor，退出时关闭；WebUI 通过 upstreamMonitor 快照展示全部候选的观测，旧健康检查已移除。业务播放和下载仍独立运行。

## 通道与网络模式

基础设施发布的 Transport 实现 `upstreamrequest.Candidates` 时，检查维度为操作 × 入口 × 通道。直连展开有效 DNS IP 池；SOCKS5 把原域名交给代理解析。三种网络模式为 direct（不启用代理）、socks5（强制代理）、auto（两者平等参与检查）。DNS 只保证答案合法和 TTL，不进行性能排序。

推荐携带被测 `Transport`，使用它能够固定到被测路径，同时保留 URL、Host、TLS SNI 与证书校验。目标主机不同会拒绝。凭据不进入候选 ID 或结果；代理配置更换使用新的不透明身份。网络配置变更取消旧检查，旧版本迟到结果丢弃；仍允许且 IP 不变的直连通道保留观测。

不实现 Candidates 的普通 Transport 仍受支持：保留固定七项线路观测，便于已有调用方和隔离测试；此兼容模式不提供候选推荐。

## 检查对象

| 操作 | 入口 | 检查内容 |
| --- | --- | --- |
| Catalog / api | api.udon.dance/Api/Songs/list | 完整 JSON、有效歌曲 ID |
| Catalog / kiva | x.kiva.moe/api/v2/wanna/songs | 业务状态码、清单版本、正数歌曲 ID、合法且无冲突的 MD5 |
| Catalog / wanna | wanna.kiva.moe/api/wannaInfo | 同上，独立清单入口 |
| PlaybackURL | 同一 API 的 node=cf / node=nya | 重定向、目标主机和资源元数据 |
| Resource | play.udon.dance / nya.xin.moe | 实际解析出的资源 URL，连续 Range 下载，满 2 秒且 16 MiB；上限 5 秒或整首 |

线路标识为 api、kiva、wanna、cf、hkg；hkg 对应 node=nya。Entry 标识服务，资源 Entry 不是任意歌曲的播放 URL。三个清单入口每轮独立检测各自全部候选，按各自响应格式校验，失败不影响其他入口。每轮从有效列表的去重正数歌曲 ID 中均匀随机采样；优先用本轮 Udon 样本，无有效样本时依次用 Kiva、WannaInfo；三个列表均失败可在 SampleLifetime 内复用旧 ID，首次没有样本时不猜测视频地址。同一路线的所有资源候选共用本轮解析出的一个合法样本，不把不同 IP 返回的不同视频混作吞吐比较。

歌曲样本不保证全部歌曲或完整下载可用。业务仍须校验实际资源版本、响应及完整内容。

## 接口

- NewMonitor(Options)：只构造，不发请求。
- Start()：幂等启动调度器；何时允许访问上游由对象所有者决定。
- Check(ctx)：检查并等待。并发调用共享一轮，取消一个等待者不影响其它等待者。
- Results(Operation)、Snapshot()：纯内存读取独立副本；包含未知、失败、过期和关闭状态。候选数量随有效 IP 池变化，没有候选时保留 unknown/no_channel 项。
- Recommended(Operation)：返回 `(Selection, bool)`。Selection.Result 给出依据，Selection.Channel.Transport 可供实际请求复用。无有效成功观测时返回 false。读取不测速，也不增加迟滞计数。
- SetPolicy(Policy)：修改刷新、有效期、请求超时和切换策略。
- Close()：取消并等待检查和调度结束；不拥有或关闭应用的连接池。

```go
m, err := upstreamstate.NewMonitor(upstreamstate.Options{})
if err != nil { return err }
defer m.Close()
if err := m.Start(); err != nil { return err }

// 后续读取不触发现场测速。
selection, ok := m.Recommended(upstreamstate.Resource)
if ok {
    // 对匹配 selection.Channel.Host 的真实资源使用该 Transport。
    // selection.Result.Entry 仅是入口，不能当作完整视频 URL。
    _ = selection.Channel.Transport
}
```

已有业务 raw dial 不含操作上下文，仍保留连接容错；auto 的普通拨号采用直连失败后代理回退，**不表示已采用 Monitor 推荐**。Monitor 没有自动替换现有业务请求；由业务明确使用推荐的 Transport，才能使用被测通道。

## 结果与推荐

CatalogTime 保留上游响应原始 time 字符串（Udon 顶层 time，其余两个入口 data.time），与 ObservedAt 区分；缺失时为空，不伪造时间。WebUI 按清单分别显示，并保留不同候选的时间差异。

结果包含 Operation、Route、Entry、ChannelID、Mode、IP、State、Reason、Stage、HTTP、ObservedAt、ValidUntil、Samples、SampleSongID。无有效估计时数值为 nil，不是零耗时。

- EstimatedLatencyMS：从请求开始到最终有效响应的首字节，包含连接、握手；资源包含此前重定向的耗时。
- EstimatedSpeedBPS：资源为有效字节数 / 正文下载耗时（排除建连、握手和响应头）；列表仍为有效字节数 / 完整探测耗时。不是持续满速带宽。PlaybackURL 不提供吞吐估计。
- ProbeDurationMS：代表性探测平均完成耗时。TransferDurationMS 与 TransferredBytes 是最近一次正文下载的实际时长及字节数。

每个操作/入口/通道保留最多 8 个同歌曲的连续成功样本。失败或样本变化重置该窗口，移除的候选历史会回收。一项完成立即发布，不等待整轮；失败只影响该项。

Catalog 和 PlaybackURL 优先低首字节耗时，Resource 优先高有效吞吐。当前候选仍健康时，挑战者默认需连续两轮改善至少 15% 才替换；当前候选失败、过期或移除时立即选其它有效候选。推荐是当前已观测集合的最优，不保证全网或所有资源的全局最优。

## 刷新与策略

| 参数 | 默认值 |
| --- | --- |
| Interval | 5 分钟 |
| Lifetime | 6 分钟 |
| FailureLifetime | 30 秒 |
| SampleLifetime | 10 分钟 |
| RequestTimeout | 30 秒 |
| ResourceMinDuration | 2 秒 |
| ResourceMinBytes | 16 MiB |
| ResourceTimeout | 5 秒（正文下载上限） |
| SwitchImprovement | 0.15 |
| SwitchSamples | 2 |

零值采用默认值；无效值拒绝。SetPolicy 完整替换策略，新超时用于下一轮，有效期仍基于原始观测时间。序列化的 time.Duration 单位为纳秒。

Interval 是候选检查的最大常规间隔；新 DNS 候选事件提前唤醒，接近成功观测到期时也提前检查，留出请求余量。最短重新调度间隔为 1 秒，避免极短 TTL 导致忙循环。DNS 的新旧地址在原 TTL 内重叠，相同 IP 的 TTL 刷新不清空测量。推荐有效期取测量有效期与当前 DNS 租期的较早者。刷新失败不能延长 TTL；启动、全体失败或过短 TTL 仍可能没有可用推荐。

## 探测边界

歌曲列表及播放地址最多 4 个并行候选探测；视频资源吞吐跨线路、跨候选串行执行，每次仅一个资源传输，避免争抢本地带宽。列表最多 16 MiB；资源连续读取，至少 2 秒且 16 MiB 后收尾，5 秒或整首资源结束则优先停止（最多额外读取 1 字节检测越界）；5 秒内有有效正文就保留实测吞吐，没有正文记超时，必须是正确的 206、Content-Range 和未压缩正文。资源重定向最多三次，必须保持主机、大小及校验标识。播放 URL 不自动跟随重定向。视频元数据统一用 internal/videometa 校验，HTTP 视频 URL 升级为 HTTPS。

超时记录 timeout；仅明确收到 524 才记录 origin_timeout。不把取消当作通道失败，不泄露底层可能带认证信息的错误。

## 验证

`go test ./internal/upstreamrequest ./internal/upstreamstate ./internal/console`；`go vet` 同上。测试覆盖候选固定 IP、Host/SNI、SOCKS5 远端 DNS、三模式、TTL 交接、配置退休、无 I/O 读取、逐候选失败隔离、推荐迟滞、动态候选唤醒、旧配置迟到结果丢弃，以及既有正文与重定向边界。
