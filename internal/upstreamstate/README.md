# 直连通道监测

产品使用 `NewMonitor` 检查歌曲列表与播放地址。视频资源统一由[块采样器](../../docs/download-upstream-routing.md)读取。本文涉及的逐 IP 资源探测、吞吐推荐与单字节探测仅适用于显式创建 `NewExperimentMonitor` 的离线对照实验。

本包只维护直连的操作 × 入口 × IP 观测、历史和推荐。Console 仅在直连且同意条款后启动，退出时关闭。代理不使用本包；所有上游的自动／手动轻量流程在 `upstreamcheck`，Console 统一调度并复用响应入库，`catalogsync` 管理业务选歌进度。现行边界见[上游设计](../../docs/upstream-channels.md)。本模块保留原有能力，不扩大选路职责。

## 通道与网络模式

基础设施只在直连向 Channel 发布 `upstreamrequest.Candidates`；DNS 展开有效 IP 池，仅维护合法答案与 TTL，不排序性能。代理使用普通 HTTP Transport，不创建候选或 Monitor。

直连推荐携带被测 `Transport`，使用它能够固定到被测路径，同时保留 URL、Host、TLS SNI 与证书校验。目标主机不同会拒绝。网络配置变更取消旧检查，旧版本迟到结果丢弃；仍允许且 IP 不变的直连通道保留观测。

单候选与多候选使用相同的发现、测量、状态和推荐流程。Channel 尚未发布或已释放时没有可执行候选，检查返回不可用；隔离测试也通过候选接口提供传输。

## 检查对象

Monitor 的歌曲清单检查只使用 [catalogmeta](../catalogmeta/sources.go) 定义的 Udon 入口（api），候选发现与实际探测使用同一份定义。Kiva、WannaInfo 的完整清单兜底由 Console 通过普通请求路径按需尝试，不进入 Monitor 的定期检查、健康或推荐，触发规则见[上游检测与测速](../../docs/upstream-channels.md#上游检测与测速)。播放入口清单取自 [playbacksource](../playbacksource/source.go)，与业务解析和持久化地址筛选共享来源规则。Monitor 保留自己的计时、健康分类和结果交付，不承担业务缓存、下载或查询生命周期。

| 操作 | 入口 | 检查内容 |
| --- | --- | --- |
| Catalog / api | api.udon.dance/Api/Songs/list | 完整正文及业务清单校验；结果交给 Console 复用，参与歌曲资料、当前成员及共享水位更新 |
| PlaybackURL | 播放 API 的 node=cf | 重定向、目标主机和资源元数据 |
| Resource | API 当前返回的域名集合 | 实际解析出的资源 URL，连续 Range 下载，3 秒、16 MiB 或整首结束，先到即停 |

Catalog 标识为 api；PlaybackURL 标识为 cf（固定 node=cf，不查询或回退 node=nya）；Resource 标识直接使用返回 URL 的域名，不预设域名名单或数量。接口节点与资源域名没有固定对应关系。每个接口/通道的新结果替换旧结果，样本到期或对应候选退休后移出集合；空集合不生成资源候选。域名与 IP 地理位置只描述观测，不推断 HKG/CF 身份。详见[资源域名规则](../../docs/resource-domains.md)。Entry 标识服务，资源 Entry 不是任意歌曲的播放 URL。Udon 主清单按[有界抽样规则](../../docs/upstream-channels.md#直连测量和推荐)检查候选，从有效列表的去重正数歌曲 ID 中均匀随机采样；无有效样本时可在 SampleLifetime 内复用旧 ID，首次没有样本时不猜测视频地址。每轮按返回域名合并资源样本；同一域名即使由多个接口或 API 通道返回，也仅检测一次该域名的每个资源通道。没有取得某域名的合法 URL 时不改写其它域名的 URL 来补测，也不把 API 的失败归到该域名。同一域名的所有资源候选共用本轮解析出的一个合法样本，不把不同 IP 返回的不同视频混作吞吐比较。

歌曲样本不保证全部歌曲或完整下载可用。业务仍须校验实际资源版本、响应及完整内容。

完整清单的来源资格、版本裁决及映射优先级以[曲库规则](../../docs/song-catalog-design.md#清单同步与增改不删)为准，资料候选及写入事务见[存储契约](../../docs/storage.md)。

## 接口

持久化地址采用[数据库 v9 媒体地址模型](../../docs/song-catalog-design.md#媒体地址模型)，合法观察按事务提交顺序覆盖，不持久化查询/失败时间。Monitor 的计时、批次取消和回调生命周期保持原职责，诊断时间不因地址表精简而删除。

- NewMonitor(Options)：必须传入所属实例的 Channel；只构造，不发请求。
- ReadCatalog(ctx) 供直连主清单读取使用。有有效观测时使用已验证线路，没有时发起或加入清单探测，首份有效正文直接返回。手动检测接替进行中的清单探测时，前台在原期限内等待接替结果。播放检测已取得的前置清单在该批次内也可供冷启动读取。按需读取只启动清单探测，定期检查由 Start 启动。
- Options.OnCatalog：交付通过基本结构检查的 Udon 清单、来源及缓存的完整校验结果，在 Monitor 锁外复用，取消或已退休网络配置的批次不交付。Console 复用校验错误做同步反馈，按版本从新到旧同步有效清单，仅使用 `songs_list` 水位；存储失败单独记录，不改变探测结果或测速耗时。
- Options.OnPlaybackURL：候选探测在播放 API 返回有效地址后、资源采样前交付完整 URL、请求歌曲 ID、无查询参数的 API、实际节点值 cf 及原请求起止时间。自动与手动检测复用同一回调，节点/通道间可并发调用，均在 Monitor 锁外；取消或已退休批次不交付，Close 取消并等待回调。Console 复用 `ObserveSongURL` 在同一事务保存地址、资源大小和当前歌曲映射，按实际事务提交顺序覆盖本次歌曲映射，不增加解析或下载请求；存储失败单独记录，不算作上游故障，也不计入探测耗时。每轮只更新所选歌曲，不在单轮遍历全库；完整签名 URL 不进入状态快照。网络页连通性检测直接调用同一业务清单及播放地址同步入口，写入当前生效存储库；未应用的存储目录草稿不提前生效。
- Options.OnProgress：在锁外交付检测阶段、本轮 Started 及当前观测，Operation 为空表示本轮结束；同轮串行调用，取消或退休批次不再交付。进度中没有清单正文或签名 URL，展示层只汇总结果，不自行发起探测。该接口只属于直连观察者，通用手动诊断不订阅 Monitor。
- Options.PlaybackSongCandidates：仅保留独立直连实验的固定样本覆盖入口，取第一个合法 ID；生产曲库补齐不注册该回调，公平遍历与存储读取完全归 `catalogsync`。

```go
// channel 由所属实例持有，且已发布直连候选；ctx 由调用方提供。
m, err := upstreamstate.NewMonitor(upstreamstate.Options{Channel: channel})
if err != nil { return err }
defer m.Close()
if err := m.Start(); err != nil { return err }
// 等待一轮检测完成；没有成功且有效的候选时仍可能没有推荐。
if err := m.Check(ctx); err != nil { return err }

// 读取推荐不触发现场测速。
selection, ok := m.Recommended(upstreamstate.Resource)
if ok {
    // 对匹配 selection.Channel.Host 的真实资源使用该 Transport。
    // selection.Result.Entry 仅是入口，不能当作完整视频 URL。
    _ = selection.Channel.Transport
}
```

Console 的主清单和播放地址请求各自使用对应推荐的 Transport。直连 `api.udon.dance:443` 的新 TLS 透传连接使用主清单推荐 IP，连接失败时回退普通拨号，详见[业务请求的协议策略](../../docs/upstream-channels.md#业务请求的协议策略)。

## 结果与推荐

CatalogTime 保留 Udon 清单响应的顶层 time 原始字符串，与 ObservedAt 区分；缺失时为空，不伪造时间，保留不同候选的时间差异。

结果包含 Operation、Route、Entry、ChannelID、Mode、IP、Scheme、State、Reason、Stage、HTTP、ObservedAt、ValidUntil、Samples、SampleSongID。Scheme 是实际执行协议，Entry 继续标识服务。无有效估计时数值为 nil，不是零耗时。

- EstimatedLatencyMS：最近连续成功且未过期样本的平均首字节响应时间，可能包含此前轮次的样本，不是本轮检测耗时。单次样本从请求开始计时，包含连接、握手；资源包含此前重定向的耗时。这只用于直连当前连接观测；手动连通性检测不使用此值。
- EstimatedSpeedBPS：资源为有效字节数 / 正文下载耗时（排除建连、握手和响应头）；列表仍为有效字节数 / 完整探测耗时。不是持续满速带宽。PlaybackURL 不提供吞吐估计。
- ThroughputObservedAt：独立的吞吐采样时间。轻量首字节检测不更新吞吐值与此时间；配置的吞吐间隔内且通道当前健康时可参与推荐，过期后仅作为历史样本展示。
- ProbeDurationMS：代表性探测平均完成耗时。TransferDurationMS 与 TransferredBytes 是最近一次正文下载的实际时长及字节数。

每个操作、入口和通道保留最多 8 个连续成功样本。清单计时覆盖整份清单，各轮抽出的播放样本歌曲可以不同。播放地址和资源探测在请求的歌曲变化时重新累计。线路探测失败会重置连续成功窗口，移除的候选历史会回收。一项完成立即发布，失败只影响该项。

Catalog 以平均完整探测耗时排序，PlaybackURL 比较首字节耗时。Resource 优先有未过期有效吞吐的健康候选，再比较吞吐高低。推荐迟滞、失效切换及样本有效性以[直连测量和推荐](../../docs/upstream-channels.md#直连测量和推荐)为准；固定协议及吞吐样本的协议约束以[业务请求的协议策略](../../docs/upstream-channels.md#业务请求的协议策略)为准。

## 刷新与策略

| 参数 | 默认值 |
| --- | --- |
| Interval | 5 分钟 |
| ThroughputInterval | 20 分钟 |
| Lifetime | 6 分钟 |
| FailureLifetime | 30 秒 |
| SampleLifetime | 10 分钟 |
| RequestTimeout | 30 秒 |
| ResourceMaxBytes | 16 MiB |
| ResourceTimeout | 3 秒（正文下载硬上限） |
| SwitchImprovement | 0.15 |
| SwitchSamples | 2 |

策略通过 Options.Policy 在创建时复制、规范化并校验，Monitor 存续期间固定不变；零值采用默认值，无效值拒绝。有效期基于原始观测时间。应用设置的保存与重启规则统一见[配置生命周期](../../docs/settings-lifecycle.md)。序列化的 time.Duration 单位为纳秒。

Interval 是候选检查的最大常规间隔；新 DNS 候选事件提前唤醒，接近成功观测到期时也提前检查，留出请求余量。主清单大池已有推荐时，提前复测只看当前推荐的观测到期时间，详见[上游通道](../../docs/upstream-channels.md#请求与连接)。最短重新调度间隔为 1 秒，避免极短 TTL 导致忙循环。DNS 的新旧地址在原 TTL 内重叠，相同 IP 的 TTL 刷新不清空测量。纯 DNS 候选的推荐有效期取测量有效期与当前 DNS 租期的较早者；租约到期后，预制地址仍可在适用端口凭有效观测获得推荐。刷新失败不能延长 TTL；启动、全体失败或过短 TTL 仍可能没有可用推荐。

## 探测边界

本包不判断完整清单是否已经入库，不负责曲库初始化重试，也不向界面提供初始化状态。这些职责由 Console 的统一上游检测调度和曲库入口承担，具体规则见[曲库设计](../../docs/song-catalog-design.md#清单同步与增改不删)。

歌曲列表及播放地址继续自动检查。实验工具可显式执行旧协议探测用于对照。

完整探测超时记录 timeout，主清单的短探索窗口耗尽则保留原观测和到期时间。仅明确收到 524 才记录 origin_timeout。不把取消当作通道失败，不泄露底层可能带认证信息的错误。

自动资源采样与业务下载共用[下载线路观测](../../docs/download-upstream-routing.md)。产品的手动资源采样也使用该执行层。

## 验证

`go test ./internal/upstreamrequest ./internal/upstreamstate ./internal/console`；`go vet` 同上。测试覆盖候选固定 IP、Host/SNI、SOCKS5 远端 DNS、两种模式隔离、TTL 交接、配置退休、无 I/O 读取、逐候选失败隔离、推荐迟滞、动态候选唤醒、旧配置迟到结果丢弃，以及既有正文与重定向边界。

CheckSelected(ctx, kind) 支持 catalog、playback、latency、throughput 四类手动检查，取消 Monitor 自动轮次并优先执行，Monitor 自动调度等待手动轮次结束。依赖请求仅用于取得合法样本，不替换无关操作的观测。手动资源检查串行执行，延迟检测只读取一个字节。

CheckSelected 的调用者取消时，取消并排空所预留的批次及回调，随后释放手动席位，Monitor 保持可复用；其它完整手动检查及自动调度在此期间等待。普通 Check 的共享等待者仍只取消自己的等待，不取消共享工作。

`CheckConnectivity` 保留直连内部的仅响应检测方式。网络页通用自动／手动检测已移至 `upstreamcheck`，使用应用组装的当前上游请求能力，不创建诊断 Monitor 或诊断 Channel。

资源线路历史由块采样器和业务下载维护。旧 throughput.json 已停用。

外部 SOCKS5 不创建通道身份。实例 channel.key 仅继续服务 Mihomo 节点名称和选择缓存，规则见[Mihomo 配置和运行](../../docs/mihomo-dashboard.md#配置和运行)。

资源域名去重不会合并掉 API 来源：Result.Sources 保留各接口/通道返回的来源摘要，ResourceSources(domain) 返回包含完整原始 URL 的独立值副本；空域名可查询全部。记录实际 API、node、歌曲 ID、API 通道、域名/路径和有效期。完整播放 URL 不序列化到状态 JSON 或吞吐文件。普通播放解析将来源写入应用独立持有的 upstreamrequest.Resources，多个 API 返回同一 URL 时仍能按实际 node 追溯。
