# CF 单视频 Range 并发实验

开发实验，复用 `upstreamrequest.NewPool` 的固定直连 HTTP Transport 和 `console.NewUpstreamDialer` 的独立 DNS。产品代码、配置、库、hosts 和游戏均不改动。

## 问题与范围

比较同一 `node=cf` 返回资源、固定远端 IP、相同前缀的 1/2/3/4 路 1 MiB Range，以及单个长 Range。第一阶段隔离网络请求吞吐，不调用整文件缓存引擎、播放调度或最终 MD5 发布。网络请求无实验层重试；Go Transport 的内部透明重试没有独立计数。结果不可直接外推到首播、Seek、跨歌曲竞争或完整缓存完成时间。

没有跨代理选路或回退。保留 URL 的公开域名作为 Host/SNI 并验证证书，HTTP URL 仅按产品约定升级为 HTTPS。首次通过独立 DNS 实际连接取得远端 IP，整个批次固定该地址。显式 HTTP 代理不使用；无法据此排除系统 TUN/透明代理。记录 `CF-Ray` 的节点后缀，固定 IP 不保证固定物理节点。

## 执行

在仓库根目录，用 Go 1.25.0：

```powershell
go test -race -tags=no_easytier ./experiments/cf-concurrency -count=3
go vet -tags=no_easytier ./experiments/cf-concurrency
$env:CGO_ENABLED = '0'
go build -tags=no_easytier -trimpath -o artifacts/cf-concurrency/probe.exe ./experiments/cf-concurrency
& artifacts/cf-concurrency/probe.exe -mode prepare -song 1343
& artifacts/cf-concurrency/probe.exe -mode measure -mib 30 -out artifacts/cf-concurrency/results.json
```

先完成测试、vet、编译，再执行测量；测量期间不并行编译、测速或运行其他实验。`prepare` 保存的 `target.json` 是私密输入，可能含完整资源参数，只保留于忽略目录，不提交。终端及结果中不输出完整 URL、原始 ETag 或 CF 请求标识。

初次长 Range 为预热与内容参照，不进入性能汇总。参照保存每个 1 MiB 块的 SHA256，每次测量都检查精确 206/Content-Range/长度、identity 编码、强 ETag（存在时）、正文终止和逐块哈希。仅证明测试区间一致，不冒充整文件 MD5。错误不能作为有效吞吐；每次失败保存原状态，不自动重跑覆盖。

四轮顺序如下，`L` 表示长 Range：

| 轮次 | 顺序 |
| --- | --- |
| 1 | L、1、2、4、3 |
| 2 | 2、L、3、1、4 |
| 3 | 3、4、2、L、1 |
| 4 | 4、1、3、2、L |

四档分块在相对顺序上平衡；长 Range 插入不同位置，不声称五组构成完整 Latin square。每组之间休息两秒，重新创建连接池，连接建立计入总耗时；仅 CDN 内容经过预热，不是假设所有连接已热。上游缓存状态无法强制清空，逐请求记录实际状态及 Age。

最多四路、区间最大 32 MiB、每组最多两分钟、批次最多三十分钟；429/503 或连续两组失败停止。默认四轮正文上限约 `21 × 区间大小`，另有 1 KiB 预检、失败响应及网络协议开销；30 MiB 时约 630 MiB。限制的是应用读取的测试正文范围，不是网卡全部流量上限。

`-rounds 1..4` 可限制顺序表使用的行数，默认 4；单轮用于明确标记的补测，不独立声称顺序平衡。每次调用均重新预热并建立内容参照，因此单轮正文为 `6 × 区间大小`。若某轮有已知本机干扰，保留全部原始数据及排除原因，补测相同顺序，并确认资源、地址、逐块哈希、协议及节点仍可比较。

## 指标与分析

- 有效吞吐 = 验证成功的唯一区间字节数 / 测量耗时。所有分块必须成功才有吞吐值。
- `elapsedMS` 包含连接、响应、读取、逐块校验和调度；不含测量结束后的结果文件写入及连接池清理。
- 记录响应头与首次正文等待、每请求起止、实际峰值/时间平均在途请求数、连接标识/复用、协议、远端、CDN 节点与缓存状态。
- 长请求和分块请求使用同一传输设置，标准库连接池策略也相同。HTTP 请求并发不等于 TCP 连接数；连接 ID 仅在单组内有效。
- 每轮以同轮一路作配对，展示全部采样、每档中位数、范围及配对加速比。只有四个配对，不给出稳定 p95 或普遍性结论。
- 一路小分块慢但长 Range 快，只支持“请求等待/传输粒度值得进一步研究”的线索；不能直接证明 CF 按连接限速。

初筛后再决定是否测多个视频、第二时段、热连接条件或实际缓存引擎。若四路收益已趋于平台，优先避免扩大并发上限。实机 LA-037 状态不因本工具结果改变。

分析脚本只依赖 Python 标准库；可选 `--chart` 需要另备 Matplotlib，不加入产品依赖。示例中仅排除事先记录为可能受静态检查干扰的首轮，原始数据保留，原因需在报告中说明：

```powershell
python experiments/cf-concurrency/analyze.py artifacts/cf-concurrency/run-01.json artifacts/cf-concurrency/run-02.json --exclude-round run-01:1 --out artifacts/cf-concurrency/summary.json --chart artifacts/cf-concurrency/throughput.png
```

脚本要求每个纳入的配对轮次包含全部五组且全部成功；内容参照、固定地址、歌曲和区间必须一致。失败组或不完整轮次必须另行报告，不能静默删除后计算仅成功样本的加速比。

## 扩展：4/6/8/12 路与多 IP

`measure` 保留上面的四路实验。`sweep` 是独立的开发实验，最多 12 个在途请求、三个 IPv4 地址；不修改产品的 3 路默认值或全局 6/4 配额。先用 `Resolve-DnsName -Type A -DnsOnly -NoHostsFile` 核对并单独保存本次系统 DNS 答案，再以 `-ips` 显式固定地址；工具不会自动发现、替换或删除失败地址，也不在实验中按 DNS TTL 换址。TLS 域名、Host、资源 URL 均相同。

```powershell
Resolve-DnsName play.udon.dance -Type A -DnsOnly -NoHostsFile
# 完成上述测试、vet 和编译后运行；ips 必须来自本次 DNS 核对。
& artifacts/cf-concurrency/probe.exe -mode prepare -private artifacts/cf-concurrency/sweep-target.json
& artifacts/cf-concurrency/probe.exe -mode sweep -private artifacts/cf-concurrency/sweep-target.json -ips '104.26.4.28,104.26.5.28,172.67.69.58' -mib 30 -out artifacts/cf-concurrency/sweep.json
python experiments/cf-concurrency/analyze_sweep.py artifacts/cf-concurrency/sweep.json --out artifacts/cf-concurrency/sweep-summary.json --chart artifacts/cf-concurrency/sweep.png
```

每个 IP 都先执行一次完整长 Range 预热，必须有强 ETag，并逐块匹配第一个地址的 SHA256；失败即保留结果并停止，不能把不可达地址静默剔除。每个分块也检查实际远端 IP 和目标一致。此处“跨 IP 校验”只验证同一区间的表示一致，不证明三个地址对应不同物理服务器。

三个 IP 各自单独运行 4/6/8/12 路，再比较三 IP 合用的相同四档**总并发**；另有 IP1 单个长 Range 对照，共 17 组。每轮包含所有组一次，共三轮，用固定种子 `20261014` 事先打乱顺序；完整计划在初次结果文件中保存。这是随机完整区组，三轮不构成完整顺序平衡。

合用时每个工作者固定使用一个 IP，各 IP 共享待下载块队列；快 IP 自然领取更多块，没有失败重试或迁移。12 路为每 IP 4 路，6 路为每 IP 2 路；4 路的 2/1/1 与 8 路的 3/3/2 额外名额在三轮轮换。比较结果时同时展示各单 IP，避免把选到较快地址误认为多 IP 聚合带宽。保持新连接池、1 MiB 块、同一前缀、2 秒间隔、120 秒组超时、30 分钟批次超时和停止条件。

三个地址时共 `3 次预热 + 3 × 17 次测量 = 54` 个区间，30 MiB 时正文上限为 1,620 MiB（另有 API/1 KiB 预检及协议开销）。只有 30 个分块，12 路只有约 2.5 波请求，短任务的建连与尾部空闲都计入，不能直接外推到长视频稳态吞吐。

分析要求每轮完整、无失败、执行顺序与计划一致，所有区间、逐块哈希、远端身份和实际峰值均符合声明；不提供排除慢样本的选项。逐轮配对比较 6/4、8/6、12/8、同并发多 IP / 各单 IP，以及多 IP / 当轮最快单 IP。后者只是观测上界对照，不是事先可知的最优选路策略。图中误差线是三次观测范围，不是置信区间；保留每次原始结果，不凭三个样本声称统计显著性。
