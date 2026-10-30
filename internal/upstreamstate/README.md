# Canonical 上游检查服务

本包测量固定上游服务，并按操作维护状态与推荐。职责和刷新交接见 [上游通道设计](../../docs/upstream-channels.md)。Monitor 不依赖 console、DNS 或 SOCKS5 实现；它从应用的 upstreamrequest.Channel 获取可执行候选。Console 在条款同意后启动 Monitor，退出时关闭；WebUI 通过 upstreamMonitor 快照展示全部候选的观测，旧健康检查已移除。业务播放和下载仍独立运行。

## 通道与网络模式

基础设施只向 Channel 发布 `upstreamrequest.Candidates`，检查维度统一为操作 × 入口 × 通道。直连展开有效 DNS IP 池；SOCKS5 把原域名交给代理解析。网络模式为 direct（内置 DNS 直连）和 socks5（域名交由代理），两者不混合候选或相互回退。DNS 只保证答案合法和 TTL，不进行性能排序。

直连推荐携带被测 `Transport`，使用它能够固定到被测路径，同时保留 URL、Host、TLS SNI 与证书校验。目标主机不同会拒绝。凭据不进入候选 ID 或结果；代理配置更换使用新的不透明身份。网络配置变更取消旧检查，旧版本迟到结果丢弃；仍允许且 IP 不变的直连通道保留观测。

单候选与多候选使用相同的发现、测量、状态和推荐流程。Channel 尚未发布或已释放时没有可执行候选，检查返回不可用；隔离测试也通过候选接口提供传输。

## 检查对象

播放入口清单取自 [playbacksource](../playbacksource/source.go)，与业务解析和持久化地址筛选共享来源规则。Monitor 保留自己的计时、健康分类和结果交付，不承担业务缓存、下载或查询生命周期。

| 操作 | 入口 | 检查内容 |
| --- | --- | --- |
| Catalog / api | api.udon.dance/Api/Songs/list | 完整 JSON、有效歌曲 ID；正文交给 Console 完整校验，参与歌曲资料、当前成员及共享水位更新 |
| Catalog / kiva | x.kiva.moe/api/v2/wanna/songs | 业务状态码、清单版本、正数歌曲 ID、合法且无冲突的 MD5 |
| Catalog / wanna | wanna.kiva.moe/api/wannaInfo | 同上，独立清单入口 |
| PlaybackURL | 播放 API 的 node=cf | 重定向、目标主机和资源元数据 |
| Resource | API 当前返回的域名集合 | 实际解析出的资源 URL，连续 Range 下载，3 秒、16 MiB 或整首结束，先到即停 |

Catalog 标识为 api、kiva、wanna；PlaybackURL 标识为 cf（固定 node=cf，不查询或回退 node=nya）；Resource 标识直接使用返回 URL 的域名，不预设域名名单或数量。接口节点与资源域名没有固定对应关系。每个接口/通道的新结果替换旧结果，样本到期或对应候选退休后移出集合；空集合不生成资源候选。域名与 IP 地理位置只描述观测，不推断 HKG/CF 身份。详见[资源域名规则](../../docs/resource-domains.md)。Entry 标识服务，资源 Entry 不是任意歌曲的播放 URL。三个清单入口每轮独立检测各自全部候选，按各自响应格式校验，失败不影响其他入口。每轮从有效列表的去重正数歌曲 ID 中均匀随机采样；优先用本轮 Udon 样本，无有效样本时依次用 Kiva、WannaInfo；三个列表均失败可在 SampleLifetime 内复用旧 ID，首次没有样本时不猜测视频地址。每轮按返回域名合并资源样本；同一域名即使由多个接口或 API 通道返回，也仅检测一次该域名的每个资源通道。没有取得某域名的合法 URL 时不改写其它域名的 URL 来补测，也不把 API 的失败归到该域名。同一域名的所有资源候选共用本轮解析出的一个合法样本，不把不同 IP 返回的不同视频混作吞吐比较。

歌曲样本不保证全部歌曲或完整下载可用。业务仍须校验实际资源版本、响应及完整内容。

完整清单的来源资格、版本裁决及映射优先级以[曲库规则](../../docs/song-catalog-design.md#清单同步与增改不删)为准，资料候选及写入事务见[存储契约](../../docs/storage.md)。

## 接口

- NewMonitor(Options)：只构造，不发请求。
- Options.OnCatalog：交付成功的 Udon、Kiva、WannaInfo 清单及来源，在 Monitor 锁外复用正文而不增加请求，取消或已退休网络配置的批次不交付。Console 完整解析候选，按版本从新到旧、同版 Udon 优先同步，仅使用 `songs` 水位；存储失败单独记录，不改变探测结果或测速耗时。
- Options.OnPlaybackURL：候选探测在播放 API 返回有效地址后、资源采样前交付完整 URL、请求歌曲 ID、无查询参数的 API、实际节点值 cf 及原请求起止时间。自动与手动检测复用同一回调，节点/通道间可并发调用，均在 Monitor 锁外；取消或已退休批次不交付，Close 取消并等待回调。Console 复用 `ObserveSongURL` 在同一事务保存地址、资源大小和当前歌曲映射，按同一歌曲跨节点的查询发起时间拒绝迟到观察，不增加解析或下载请求；存储失败单独记录，不算作上游故障，也不计入探测耗时。每轮只更新所选歌曲，不在单轮遍历全库；完整签名 URL 不进入状态快照。网络页连通性检测注册同一清单及播放地址回调，写入当前生效存储库；未应用的存储目录草稿不提前生效。
- Options.OnProgress：在锁外交付检测阶段、本轮 Started 及当前观测，Operation 为空表示本轮结束；同轮串行调用，取消或退休批次不再交付。进度中没有清单正文或签名 URL，展示层只汇总结果，不自行发起探测；网络页检测按 Started 排除旧轮观测，防止保留实例后将未重测线路显示为本轮成功。
- Options.PlaybackSongCandidates：歌曲清单交付完成后、播放探测前，在锁外取得本地缺项候选，单候选/多候选及自动/手动播放检测共用一次选择。Console 从应用持有的业务库读取缺映射、当前 MD5 的 cf URL 或正资源大小的歌曲，不启动视频引擎；按歌曲自身有效分数降序排列（真实需求覆盖先验并按 60 天半衰期衰减，同分按歌曲 ID 降序），不累加共享资源的其他歌曲分数。一批仍只探测一首、不增加 HTTP；已尝试但未补齐的歌曲本遍跳过，全部剩余候选尝试完再从最高分重试，避免失效歌独占；已补齐项自动退出。遍历状态只在当前 Monitor 内存中，网络配置切换清空，重启按数据库缺项重新排序。没有候选或本地读取失败时保留原抽样/样本复用；只检测清单不触发补齐选择。Console 的后台与网络页检测实例均由同一入口注册清单同步、缺项候选及播放地址同步三个回调；网络页检测跨轮保留实例及遍历状态，每个网络作用域独立推进，共享数据库缺项结果。URL 单纯过期不算元数据缺失，播放可用性仍由 `SongURLs` 的独立规则判断。
- Start()：幂等启动调度器；何时允许访问上游由对象所有者决定。
- Check(ctx)：检查并等待。并发调用共享一轮，取消一个等待者不影响其它等待者。
- Results(Operation)、Snapshot()：纯内存读取独立副本；包含未知、失败、过期和关闭状态。候选数量随有效 IP 池变化，没有候选时保留 unknown/no_channel 项。
- Recommended(Operation)：返回 `(Selection, bool)`。Selection.Result 给出依据，Selection.Channel.Transport 可供实际请求复用。无有效成功观测时返回 false。读取不测速，也不增加迟滞计数。
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

已有业务 raw dial 不含操作上下文，只在配置模式内保留连接容错，**不表示已采用 Monitor 推荐**。Monitor 没有自动替换现有业务请求；由业务明确使用推荐的 Transport，才能使用被测通道。

## 结果与推荐

CatalogTime 保留上游响应原始 time 字符串（Udon 顶层 time，其余两个入口 data.time），与 ObservedAt 区分；缺失时为空，不伪造时间。WebUI 按清单分别显示，并保留不同候选的时间差异。

结果包含 Operation、Route、Entry、ChannelID、Mode、IP、Scheme、State、Reason、Stage、HTTP、ObservedAt、ValidUntil、Samples、SampleSongID。Scheme 是实际执行协议，Entry 继续标识服务。无有效估计时数值为 nil，不是零耗时。

- EstimatedLatencyMS：最近连续成功且未过期样本的平均首字节响应时间，可能包含此前轮次的样本，不是本轮检测耗时。单次样本从请求开始计时，包含连接、握手；资源包含此前重定向的耗时。网络诊断每项显示本轮可用线路中的最低估计值，标为「估计响应时间」。
- EstimatedSpeedBPS：资源为有效字节数 / 正文下载耗时（排除建连、握手和响应头）；列表仍为有效字节数 / 完整探测耗时。不是持续满速带宽。PlaybackURL 不提供吞吐估计。
- ThroughputObservedAt：独立的吞吐采样时间。轻量首字节检测不更新吞吐值与此时间；直连通道的最近成功样本持久化并随重启恢复；代理手动样本不持久化；配置的吞吐间隔内且通道当前健康时可参与推荐，过期后仅作为历史样本展示。
- ProbeDurationMS：代表性探测平均完成耗时。TransferDurationMS 与 TransferredBytes 是最近一次正文下载的实际时长及字节数。

每个操作/入口/通道保留最多 8 个同歌曲的连续成功样本。失败或样本变化重置该窗口，移除的候选历史会回收。一项完成立即发布，不等待整轮；失败只影响该项。

直连候选的各操作先优先健康 HTTPS；代理模式只观测单一逻辑出口，不对内部节点排名。Catalog 和 PlaybackURL 要求有效首字节耗时，同协议内优先低耗时；Resource 同协议内优先有未过期、协议一致的有效吞吐者，再比较吞吐高低。HTTPS 仅有有效轻量检查时也可作为候选，不借用历史 HTTP 吞吐；这样协议恢复不必等待下轮完整测速。有当前有效吞吐的健康路径继续使用默认连续两轮至少改善 15% 的迟滞；失败、失效或恢复 HTTPS 时及时切换。只有历史吞吐而没有当前健康观察时不提供推荐。推荐只代表当前已观测集合，不保证全网或所有资源的全局最优。

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

Interval 是候选检查的最大常规间隔；新 DNS 候选事件提前唤醒，接近成功观测到期时也提前检查，留出请求余量。最短重新调度间隔为 1 秒，避免极短 TTL 导致忙循环。DNS 的新旧地址在原 TTL 内重叠，相同 IP 的 TTL 刷新不清空测量。推荐有效期取测量有效期与当前 DNS 租期的较早者。刷新失败不能延长 TTL；启动、全体失败或过短 TTL 仍可能没有可用推荐。

## 探测边界

应用主 Monitor 可通过 `Options.CatalogReady` 确认完整清单已被消费者接受；回调在 Monitor 锁外执行，必须遵守取消。未就绪时沿用同一调度器作有上限的初始化退避，额外自动批次只检查清单；成功后恢复常规周期。诊断 Monitor 不提供该回调，保持原有行为。`Status.CatalogInitializing` 与既有 `NextCheck` 供界面展示，具体节奏、完成依据与首次覆盖率规则统一见[曲库设计](../../docs/song-catalog-design.md#清单同步与增改不删)。

歌曲列表及播放地址最多 4 个并行候选探测；自动资源检查按原调度频率请求 Range bytes=0-0，读到一个正文 byte 即关闭，只记录延迟和可用性。直连自动吞吐检查另有可配置的吞吐间隔冷却；代理模式不运行周期性吞吐测试，手动测速仍可测当前出口。直连冷却按首次资源尝试开始计时；Options.ThroughputStatePath 可将时间持久化，重启读取后按剩余时间等待，没有历史或已到期则启动首轮测速。DNS 变化及短 TTL 不绕过冷却。视频资源吞吐跨线路、跨候选串行执行，每次仅一个资源传输，避免争抢本地带宽。列表最多 16 MiB；资源连续读取，满 3 秒、16 MiB 或整首资源结束，任一条件满足即停止（Range 限定最多 16 MiB，不额外读取）；3 秒内有有效正文就保留实测吞吐，没有正文记超时，必须是正确的 206、Content-Range 和未压缩正文。资源重定向最多三次，必须保持主机、大小及校验标识。播放 URL 不自动跟随重定向。视频元数据统一用 internal/videometa 校验，HTTP 视频 URL 默认优先 HTTPS，传输失败按[共同协议策略](../../docs/upstream-channels.md#业务请求的协议策略)有限回退。监测记录实际执行协议，重定向后的已验证目标也使用同一尝试规则。

超时记录 timeout；仅明确收到 524 才记录 origin_timeout。不把取消当作通道失败，不泄露底层可能带认证信息的错误。

自动调度的资源探测让出业务下载带宽：共享 Channel 的 BeginResourceLoad 在业务开始时取消正在进行的自动资源采样，并阻止后续采样，所有业务加载结束后对被打断的候选做轻量首字节检查，完整测速等待下个配置周期。被打断的样本丢弃，不更新通道失败状态。显式 Check 新建的检测批次保持原有行为；并发请求仍合并现有批次。Status.ResourcesPaused 供界面展示自动资源测速暂停状态。

## 验证

`go test ./internal/upstreamrequest ./internal/upstreamstate ./internal/console`；`go vet` 同上。测试覆盖候选固定 IP、Host/SNI、SOCKS5 远端 DNS、两种模式隔离、TTL 交接、配置退休、无 I/O 读取、逐候选失败隔离、推荐迟滞、动态候选唤醒、旧配置迟到结果丢弃，以及既有正文与重定向边界。

CheckSelected(ctx, kind) 支持 catalog、playback、latency、throughput 四类手动检查，取消自动轮次并优先执行，自动调度等待手动轮次结束。依赖请求仅用于取得合法样本，不替换无关操作的观测。资源检查仍串行，手动吞吐绕过冷却和业务暂停，手动延迟只读取一个字节。

CheckSelected 的调用者取消时，取消并排空所预留的批次及回调，随后释放手动席位，Monitor 保持可复用；其它完整手动检查及自动调度在此期间等待。普通 Check 的共享等待者仍只取消自己的等待，不取消共享工作。

另支持 connectivity：复用完整 Monitor 检测流程，每轮检查三个清单入口、cf 播放 API 及按实际返回域名去重的资源候选；资源只请求 `bytes=0-0`，不更新吞吐样本或冷却时间。网络诊断保留直连 Monitor 和最近一份 SOCKS5 草稿的 Monitor；相同代理身份重测沿用实例，地址/用户名/密码变化后替换并关闭旧作用域。构造不发请求、不启动自动调度、不发布业务选路；每轮完成或取消后清理闲置连接及未使用的拨号，保留选歌进度。退出时关闭全部实例与连接池。有效 Udon/Kiva/WannaInfo 正文按统一清单规则直接复用入库；已完成的元数据同步不会随取消撤销。更换存储目录后仍由新实例按正常监测填充新库，不搬运旧库或诊断内存观测。CF 并发测速测量单路/满路速度与 CPU，继续使用独立 Calibration。

手动吞吐检测（含全量手动检测中的吞吐阶段）在首个资源通道开始测速前，重置并持久化自动吞吐的配置周期计时；同轮后续通道不重复重置。手动视频列表、播放地址解析、响应延迟检测不改变该计时，尚未取得合法资源而未开始测速也不重置。

吞吐持久化文件采用 schemaVersion=2，保存最近尝试时间和直连通道的最近成功样本（歌曲编号、采样时间、字节数、正文耗时、执行协议），不保存资源 URL 或凭据；代理样本不保存或恢复。恢复样本不恢复通道健康状态。缺少协议字段的历史 v2 样本视为 HTTPS；不同协议的吞吐不混用，HTTPS 轻量检查恢复后可在等待新吞吐样本期间参与协议优先。直连资源区显示下次自动吞吐测速时间，以及等待间隔、业务暂停、准备样本、等待有效播放地址、自动调度未启动等状态；历史样本过期会明确标注，保存失败也会提示。

外部 SOCKS5 身份使用[配置实例数据根](../../docs/storage.md)的 `channel.key` 中的本地随机密钥，对代理地址、用户名、密码计算 HMAC-SHA256。相同配置在重建连接池及重启后身份保持一致，任一字段变化则生成新身份；身份不包含明文凭据。当前不读取或保存任何 SOCKS5 吞吐样本；直连样本及测速计时仍正常恢复。保留该密钥可延续通道身份；它不是代理凭据解密密钥。

吞吐状态读取或解析失败时记录 warning，以空状态启动；原文件保留至下一次成功的原子保存，不生成 `.corrupt-*` 副本。代理密钥损坏时原地保留，不自动生成新密钥；密钥无法读取或写入时同样记录 warning，连接池使用进程内临时身份，代理仍可使用。内嵌 Mihomo 会提示节点选择本次不会保存；恢复原密钥后可重新使用原通道身份，并在原生缓存仍有效时恢复选择。组件缓存初始化、重建及打开失败的边界见 [Mihomo 选择缓存](../../docs/mihomo-dashboard.md#配置和运行)。原子写入使用固定工作槽，由配置锁持有者启动时回收；详见[运行文件生命周期](../../docs/runtime-lifecycle.md)。

直连模式的自动吞吐检测间隔可在 WebUI 设置中调整，默认 20 分钟，允许 1～1440 分钟整数，对应 throughputIntervalMinutes。策略在构造时固定，保存后重启生效，无需先停止网关或任务；重启按当前格式保存的最近尝试时间计算到期时间。此设置同时决定吞吐样本参与推荐的有效期，首字节检测频率不变；生效边界统一见[配置生命周期](../../docs/settings-lifecycle.md)。

资源观测、推荐和持久化吞吐键统一使用 `resource/<domain>/<channel>`。无版本、旧版本、旧 cf/hkg 键和缺少通道身份的聚合样本不恢复、不迁移；当前格式只恢复有效直连样本及冷却时间。历史记录不会建立域名成员关系或恢复健康状态，只有 API 再次返回该域名后才展示相关历史。

资源域名去重不会合并掉 API 来源：Result.Sources 保留各接口/通道返回的来源摘要，ResourceSources(domain) 返回包含完整原始 URL 的独立值副本；空域名可查询全部。记录实际 API、node、歌曲 ID、API 通道、域名/路径和有效期。完整播放 URL 不序列化到状态 JSON 或吞吐文件。普通播放解析同样将来源写入 upstreamrequest.Channel，多个 API 返回同一 URL 时仍能按实际 node 追溯。
