# Canonical 上游检查服务

本包独立于现有控制台健康检查、WebUI、播放、缓存和下载。当前没有业务路径接入它。它统一定期检查固定服务，按操作提供所有线路的独立状态、延迟、速度和有效期。选线路、是否等待上游、是否使用本地结果由调用方决定；检查器不排名、不推荐、不维护偏好或切换门槛。

## 使用边界

请求自动使用应用已有的上游请求通道，直连或 SOCKS5 对检查器和调用方透明。调用方只设置检查策略，无须传入 client、transport 或网络参数。控制台初始化和网络设置变更时，由内部基础设施同步通道；检查器自动作废旧观测、取消旧检查，并在已启动时立即重新检查，无须重建对象。通道尚未就绪时，周期检查等待就绪，手动 Check 返回错误，不擅自回退直连。检查器不拥有或关闭共享连接池。

线路由内部固定定义，调用方不提供候选入口或强制请求线路。读取接口提供如下结果：

| 操作 | 线路标识 | 检查内容 | Entry 含义 |
| --- | --- | --- | --- |
| `Catalog` | `api` | `/Api/Songs/list`，校验完整 JSON 和有效歌曲 ID | 完整歌曲列表接口 URL |
| `PlaybackURL` | `cf`、`hkg` | 同一 API，分别请求 `node=cf`、`node=nya`，验证重定向及返回视频主机 | 对应 node 的播放解析接口 URL，业务请求仍需附加自己的 id |
| `Resource` | `cf`、`hkg` | 使用各自播放接口真实返回的视频 URL，进行最多 64 KiB 的 Range 读取 | 对应视频服务的 HTTPS 入口，**不是可直接播放任意歌曲的资源 URL** |

`hkg` 对应 API 参数 `nya` 和资源主机 `nya.xin.moe`；`cf` 对应参数 `cf` 和主机 `play.udon.dance`。`auto` 是业务选择策略，不是检查线路。歌曲列表是一个共享 API 检查，不虚构 CF/HKG 两份列表状态。也不会用边缘诊断页面的延迟推断源站或视频性能。

每轮从歌曲列表选取最小的有效正整数 ID 作为代表样本；列表暂时失败时，可在 `SampleLifetime` 内复用先前的歌曲 ID，但会重新解析真实视频 URL。首次列表失败且没有样本时，两条线路的解析和资源结果均为未知，绝不猜测歌曲或镜像 URL。`SampleSongID` 表示请求播放 API 时使用的歌曲 ID，不是 URL 路径中的资源 ID，也不表示已验证内容归属。多个歌曲可以共享资源；一次成功的片段检查不能保证所有歌曲、完整资源下载或播放器起播成功。

## 对外接口

- `NewMonitor(Options)`：构造对象，不产生网络请求。
- `Start()`：启动周期检查，重复调用不会增加调度器。何时允许访问上游由调用方决定。
- `Check(ctx)`：立即检查并等待结果；并发调用共用一轮检查。取消等待者不会影响其他等待者；请求各自受到检查策略的超时限制。
- `Results(Operation)`：纯内存读取该操作的全部线路，返回独立副本，不现场测速或发起业务请求。Catalog 返回 1 项，PlaybackURL 和 Resource 各返回 2 项；不支持的操作返回空切片。
- `Snapshot()`：纯内存读取全部 5 项结果以及调度状态，返回独立副本。
- `SetPolicy(Policy)`：运行时更改周期、有效期和超时。
- `Close()`：取消并等待在途检查和调度器结束；不关闭共享传输或连接池。

结果按操作和线路定义顺序输出：Catalog/api、PlaybackURL/cf、PlaybackURL/hkg、Resource/cf、Resource/hkg。顺序不代表优先级，不随性能、可用性、读取时机或请求完成顺序改变。未知、失败、过期、关闭的线路也保留结果项，不合并两条线路的失败原因。轮次中的已完成观测可立即读取，未完成线路仍显示此前有效观测或未知状态；每项都有自己的观测时间和有效期。

每条线路保留最多 8 个同操作、同歌曲的连续成功样本，估计值是仍在有效期内的样本平均值。失败或歌曲样本改变会清除先前的平滑窗口；某线路的失败不覆盖其它线路或其它操作的观测。

## 结果字段

- `Operation`、`Route`、`Entry`：检查对象的固定身份，在所有状态下都保留。Entry 仅标识服务，**不表示服务当前可用，也不是推荐**。
- `State`：`available`、`unavailable`、`unknown`、`stale`、`closed`。调用方应检查状态及有效期后再决定业务行为。
- `EstimatedLatencyMS`：歌曲列表和播放 URL 解析为请求开始到该响应首字节的预计耗时，包含连接及握手；资源检查为初始请求开始到最终资源响应首字节的预计耗时，包含之前所有重定向，不是重定向链中第一个响应的首字节耗时。
- `EstimatedSpeedBPS`：正文有效字节数 / 请求总耗时的平均值，单位字节/秒。资源检查包含连接和片段请求开销，是短样本有效速率，不能当成持续满速带宽。URL 解析是重定向操作，不提供无意义的吞吐估计，返回 null。
- `ProbeDurationMS`：代表性探测的平均完成耗时。对资源仅表示限量片段，不表示完整视频耗时。
- `ObservedAt`、`ValidUntil`、`Samples`、`SampleSongID`：估计的时间、样本数量和适用范围。
- `Reason`、`Stage`、`HTTP`：各线路自己的失败原因及阶段（连接、TLS、响应头、正文）和响应码；不返回底层可能包含认证信息的原始错误。

没有有效的成功观测时，估计为 null，而不是零延迟或零速率。超时只记为 timeout，不能由此断言是源站问题；收到 524 才记为 origin_timeout。播放 URL 解析失败时，其对应资源检查为 resolution_unavailable，表示无法取得资源样本，不等同于实测资源连接失败。未进行过检查为 unknown，观测超出有效期为 stale。检查器没有最佳项、汇总可用性或排序依据字段。

## 可手动设置的策略

| 字段 | 默认值 | 含义 |
| --- | --- | --- |
| `Interval` | 5 分钟 | 每轮完成到下一轮开始的间隔 |
| `Lifetime` | 6 分钟 | 成功观测有效期 |
| `FailureLifetime` | 30 秒 | 失败观测有效期 |
| `SampleLifetime` | 10 分钟 | 先前歌曲 ID 可用于新一轮检查的期限 |
| `RequestTimeout` | 30 秒 | 每个歌曲列表 / 播放 URL 请求的超时 |
| `ResourceTimeout` | 4 秒 | 每个视频片段请求含重定向的超时 |

均可在构造时配置或运行时调用 SetPolicy 修改，不设固定 5 分钟上限。零值使用默认值，负值拒绝。SetPolicy 是完整策略替换，若只改一个字段，应先取 Snapshot().Policy。有效期根据原始观测时间重算，不把旧结果变成新结果；刷新周期立即重新调度，在途检查不重叠，请求超时的新值用于下一轮。允许有效期短于刷新间隔，此时读者会看到过期状态。策略的持久化由外部负责；JSON 中 time.Duration 为纳秒。

```go
m, err := upstreamstate.NewMonitor(upstreamstate.Options{
    Policy: upstreamstate.Policy{
        Interval: 2 * time.Minute,
        Lifetime: 3 * time.Minute,
    },
})
if err != nil { return err }
defer m.Close()
if err := m.Start(); err != nil { return err }

// 不发起网络请求；始终同时返回 CF、HKG 两项，包括未知或失败状态。
for _, r := range m.Results(upstreamstate.Resource) {
    // 展示 r.Route、r.State、r.EstimatedLatencyMS、r.EstimatedSpeedBPS。
    // 使用本地结果或请求哪条线路，由业务自行决定。
    _ = r
}

p := m.Snapshot().Policy
p.Interval = 45 * time.Second
if err := m.SetPolicy(p); err != nil { return err }
```

## 限量与资源一致性

视频路径、非空 RawPath 拒绝规则、唯一 e/s 参数、MD5 和大小校验统一由 internal/videometa 提供，缓存模块复用同一解析函数。大小上限由调用方传入；主机、HTTPS 升级、重定向和缓存键仍由各模块负责。

歌曲列表最多读取 16 MiB；视频每条线路每轮最多读取 64 KiB（另读 1 字节用于检查越界），拒绝压缩正文和错误的 Content-Range。服务器忽略 Range 时直接关闭响应，不下载完整资源。视频地址只允许已知视频主机及合法大小 / 校验标识，HTTP 地址升级为 HTTPS。资源重定向最多三次，必须保留主机、大小和校验标识；播放 URL 接口不自动跟随重定向。

状态结果不授权改写某个视频的主机或替换不同版本的资源。未来接入业务加载时仍需独立验证歌曲、资源版本和实际响应，并保留真实请求失败时的处理。

## 验证

`go test ./internal/upstreamstate ./internal/upstreamrequest`、`go vet ./internal/upstreamstate ./internal/upstreamrequest`。测试覆盖完整线路输出、操作和线路间隔离、独立失败原因、固定顺序、首字节与正文耗时分离、只读快照、样本复用和过期、参数动态更新、并发合并、取消与关闭、通道变更自动重查、旧结果作废、迟到结果丢弃、连接池所有权、无配置泄露、解析返回校验、重定向边界和限量读取。控制台中的 TestUpstreamChannelFollowsSettings 验证请求通道跟随现有 SOCKS5 设置并在退出时释放。