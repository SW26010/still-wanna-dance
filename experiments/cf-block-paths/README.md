# CF 候选路径分块下载对照

本实验复用产品 `directDNS`、`upstreamstate.Monitor`、固定候选 Transport 和 `cacheproxy.DiscardLoader`。入口在 `internal/console/cf_block_experiment_test.go`，仅以 `windows && cf_block_experiment` 标签编译；普通构建和测试不包含它。放在 console 测试包是为了访问实际 DNS 实现，避免为实验添加生产 API。

> 首个下午两组窗口完成后，用户要求澄清 IPv4/IPv6 限制，后续执行改用独立[四组协议](FAMILY.md)。本文件作为原始预先方法保留，两种协议不混算。

## 预先固定的方法（2026-10-21）

- 本机当前直连网络；不读取用户代理凭据或改变实际应用配置。基线是当前源码的 DNS 双栈候选、默认测速和推荐策略在独立 Monitor 中产生的推荐，不冒充正在运行应用的历史推荐状态。
- 训练歌曲固定 1343。基线含 DoH 返回的 IPv4/IPv6；额外候选固定 `103.21.244.1`、`104.16.0.1`、`104.24.0.1`、`162.158.0.1`，只用于 `play.udon.dance`。分别使用相同 Monitor 进行三轮训练，保留默认 15%/两轮切换策略，两类训练顺序交替。
- 两组分别固定训练结束时的推荐 IP。额外组只在四个额外候选中选择；它是“扩展候选能否提供更好路径”的对照，不是已经实现的 DNS+额外候选联合回退策略。正式样本不能用于重新挑选赢家。
- 测试歌曲固定 1344、10552、10553，与训练歌曲分离。每时段重新获取 URL；任一不可用、域名改变或大小不在 16–256 MiB 时记录阻塞，不按速度换歌。每时段总资源大小上限 400 MiB。
- 每资源、每组先完整预热一次；之后四轮成对对照，AB/BA 各两次，资源次序轮换。顺序在开始前保存。每次创建新的连接池及产品 DiscardLoader，完整资源、1 MiB 块、默认软上限 16、250 ms 缓增，组间休息两秒。产品固有重试保留，额外的实验重试为零。
- 全程校验原 Host/SNI/TLS、实际远端、206、精确 Content-Range/长度、identity、强 ETag。每个分块做 SHA256，与本时段基线预热逐块比较。没有磁盘缓存、整文件 MD5 发布或 VRC 行为。
- 每组最多 90 秒，每时段最多 30 分钟、正文最多 6 GiB。出现 429/503、内容/远端校验失败立即停止；连续两组失败停止；其余失败仍保留并纳入可靠性统计。未测计划项单独列出，不能静默删除失败或重测慢组。
- 主要指标是同资源同轮完整下载耗时/有效吞吐；辅以首块、前 8 MiB 就绪时间、块延迟 P95、CPU 毫秒/MiB、实际并发、请求/重试数及失败率。预热不进速度统计，训练和预热流量单列。
- 计划三个本地时段：10 月 21 日下午、当晚 20:00 后、10 月 22 日 08:00 后。记录实际时间；错过窗口不能伪造补测时间。后续两个窗口由本聊天定时跟进，每个窗口只运行一次完整计划。
- “稳定可观”工程判据预先定为：三个时段均有正向配对中位收益、整体配对中位吞吐增益至少 15%、至少 80% 配对更快、每资源跨时段中位收益非负、失败率不增加，前 8 MiB 就绪时间中位数不恶化超过 10%。达标也只支持本机/网络/目标域名已测范围；三个时段不支持跨运营商或长期普遍结论。统计以资源和时段分层，不能将数千块视作独立实验样本。

## 运行

```powershell
./scripts/with-test-temp.ps1 go test '-tags=no_easytier,cf_block_experiment' ./internal/console -run '^TestCFBlockHarness$' -count=1
./scripts/with-test-temp.ps1 go test -c '-tags=no_easytier,cf_block_experiment' -o artifacts/cf-block-paths-20261021/probe.exe ./internal/console
& artifacts/cf-block-paths-20261021/probe.exe '-test.run=^TestCFBlockExperiment$' '-test.v' '-test.timeout=35m' -cfexp-out artifacts/cf-block-paths-20261021/afternoon
```

输出目录必须不存在，避免覆盖历史。完整 URL、原始 ETag、逐请求数据及 Monitor 原始快照只在忽略目录。Git 只保留方法、工具、报告、精简统计及哈希。后续窗口使用同一已验证 EXE；若源码变动，不混入新构建。实验期间不并行运行编译、分析或另一项测速。
