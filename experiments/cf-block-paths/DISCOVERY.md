# 双域名候选失效定位实验

当前实验构建的出站要求见[网卡绑定规则](network.md)；下文原始命令和冻结批次保留为历史。

2026-10-21，在用户要求继续后追加，独立于四组速度窗口和第一次四分钟 DNS 观察，不补齐或重跑旧速度样本。

目标是记录“解析响应 → 缓存/租约 → 可执行候选 → API 请求 → Monitor 快照”的同轮证据，区分以下情况：

- 某地址族的响应没有进入 200 毫秒合并窗口。
- TTL 为零，或短租约在发现/使用之间到期。
- 所有解析失败、域名地址变化或背景刷新尚未完成。
- API 请求成功后候选过期，导致末尾快照显示 `no_channel`；不能把末尾状态解释为没有发出请求。

复用原产品默认策略与实现，只在显式实验标签中增加观察包装。两个域名固定为 `api.udon.dance` 和 `play.udon.dance`；沿用六个 DoH 提供者入口、200 毫秒窗口、原 TTL 和默认刷新策略。每次合并解析有唯一编号，记录 A/AAAA 提供者响应、TTL（含别名链）、完成时间及脱敏错误。候选发现前后记录缓存、所有租约和后台查询状态；只读候选为空时也记录快照。

预先固定 72 轮，每轮依次发现两个域名候选，轮间五秒；每六轮执行一次产品 `CheckPlayback`，采样歌曲 1343，仅检查 API/播放 URL，不测视频吞吐。产品首次必要的目录请求保留。记录实际请求起止、目标/远端 IP、HTTP 状态，完整瞬态 URL 不写入公开材料。总期限十分钟，外层十二分钟，不因未复现或已复现而增加轮数。

首次取得候选时，为每域名保存各族第一个地址。每域名至多前三次空候选事件，在同轮处理结束后分别对保存的 v4/v6 地址做一次证书验证的 TLS 握手：保留原 ServerName、443、系统信任根，不发送媒体请求、不将成功地址重新加入产品候选池。记录事件和握手各自时间，分析时必须报告延迟；这能区分候选资格与邻近时刻的连通性，不能证明过去某一微秒的连通性。TLS 每次四秒，失败不重试。

报告必须同时保留成功与失败事件、被收集器主动取消的解析、缺测和 API 的实际响应。即使出现零 TTL，也只对有相同时间线的本批事件归因，不能追溯宣称先前四组窗口具有同一根因。

```powershell
./scripts/with-test-temp.ps1 go test '-tags=no_easytier,cf_block_experiment' ./internal/console -run '^TestCF(FamilyDNS|Discovery)Harness$' -count=1
./scripts/with-test-temp.ps1 go test -c '-tags=no_easytier,cf_block_experiment' -o artifacts/cf-family-paths-20261021/discovery-probe.exe ./internal/console
& artifacts/cf-family-paths-20261021/discovery-probe.exe '-test.run=^TestCFDiscoveryExperiment$' '-test.v' '-test.timeout=12m' -cfexp-out artifacts/cf-family-paths-20261021/discovery-followup
```

旧二进制及快照不覆盖，晚间/次晨仍使用此前冻结的四组二进制。本批只回答候选机制问题，不能加入速度收益统计。

## 观察结束后的机制验证

使用独立 `TestCFDiscoveryLeaseShortening` 虚拟时间夹具复核源码行为：初始双族 TTL 60 秒，默认第 30 秒刷新接受 TTL 1 秒的新答案，第 32 秒读取候选；比较“刷新仍返回两族”和“刷新省略 IPv6”两种场景。该夹具只复现当前实现，没有改生产代码，也没有混入已冻结的实网二进制或冒充真实网络故障。
