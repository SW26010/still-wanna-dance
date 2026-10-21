# 四组实验：修复后的 Wi-Fi 批次

本修订于 2026-10-22、新批次采样前记录。用户先要求查清并修复 `test_candidate_removed`，随后要求按最新方法使用本地 Wi-Fi 继续。旧三次失败全部保留；新方法不重跑、补齐或混算旧窗口。

## 方法变化及解释边界

- 出站使用[显式网卡绑定](network.md)。启动前确认本地配置选中在线的 Wi-Fi 且具备两族源地址；实际 GUID、地址及逐连接证据只留在忽略目录。
- 产品 DNS 修复同 IP 的短 TTL 刷新提前缩短旧租约的问题，遵循[现行 TTL 契约](../../docs/upstream-channels.md)。保留原到期时间，不使用任意保底 TTL；首次零/短 TTL、全部租约真正过期、解析失败或地址消失仍可能导致固定 IP 不可用。
- 实验不再统一报告 `test_candidate_removed`：区分绑定失败、取消、候选发现失败、空候选集和选定地址未返回，保存失败前后的 DNS 状态及底层原因。历史错误缺少现场记录，不能倒推为同一个原因。
- 新窗口为 schema 3 / `bound-interface-retained-dns-leases-v1`；分析器拒绝与旧方法合并。网络绑定和 DNS 修复同时变化，因此新旧差异不是租约修复的实网因果估计。受控回归单独证明租约行为。
- 原诊断 `TestCFDiscoveryLeaseShortening` 只用于旧行为复现，源码与结果仍在旧 provenance / Git 历史中；现行普通回归改为 `TestDNSRefreshRetainsOriginalLease`，覆盖两族、零/短 TTL、遗漏地址、长 TTL 及真实到期边界。

## 次数和停止规则

在既有失败之后显式开始新方法，本身是方法修订后的探索，不能称为原始预注册样本。最多三个新窗口：

| 窗口 | 北京时间允许启动区间 | 输出目录 |
| --- | --- | --- |
| 本次用户即时继续 | 10-22 修复与验证完成后立即一次 | `overnight` |
| 原有次晨机会 | 10-22 08:00–12:00 | `morning` |
| 原有晚间机会 | 10-22 20:00–24:00 | `evening` |

沿用 [FAMILY](FAMILY.md) 的固定资源、四组、训练规则、平衡顺序、组间隔、候选资格、40/45 分钟与 6 GiB 限制。每个机会最多一次；保留全部失败与缺测，不换固定地址、不重跑慢组、不新增第四个机会。错过如实缺测，不能在另一时段追补。采样期间不编译、分析或运行另一测速。不得覆盖旧二进制或已有输出目录。

使用 `artifacts/cf-family-wifi-20261022/family-probe.exe`，构建指纹固定在[验证记录](../../docs/validation/cf-family-wifi-20261022.md)与本地 manifest 中。先通过该构建的 Wi-Fi 双栈小流量检查，再开始四组窗口。

```powershell
./scripts/with-test-temp.ps1 ./artifacts/cf-family-wifi-20261022/family-probe.exe '-test.run=^TestCFFamilyExperiment$' '-test.v' '-test.timeout=45m' -cfexp-network local/cf-network.json -cfexp-out artifacts/cf-family-wifi-20261022/overnight
```

后续只替换输出目录名。每批结束后使用 `analyze_family.py` 汇总本批所有窗口，包括失败；旧三次结果另列引用。分别比较 extra/auto、extra/v4、extra/v6，报告同 IP 波动、训练成本、失败及缺测。

公开采集时间遵循[脱敏约定](../../docs/evidence/README.md#privacy-normalization)：`analyze_family.py` 输出时以最终汇总文件的最早非零时间为基准，递归转换为 `T+seconds`，未设置的时间输出 `null`，不公开绝对基准。另行整理的复核 JSON 在输出后执行 `python experiments/cf-block-paths/evidence_time.py <复核文件>`，只处理已筛选的公开材料；报告中的实测时刻也使用文内最早事件作为独立基准。文件之间的相对时间不能直接比较，计划启动区间和开发日期保留。原始绝对时间仅留在忽略目录，私有源文件/EXE 哈希仍按实际字节，提交材料的清单哈希按脱敏后的 Git blob 字节。

判据仍为三个完整时段各自正向、总体中位吞吐增益至少 15%、配对胜率至少 80%、每资源中位收益非负、失败率不增加、前 8 MiB 中位时间不恶化超过 10%。块请求不作独立样本。三个机会用完即收尾；不足三个完整时段为证据不足，完整但未达标为未支持稳定可观收益，均不等于证明所有额外 IP 无效。即使达标，仅适用本机 Wi-Fi 网络、已测资源及候选，不自动实现候选扩展。绑定证据不证明路由器绕过透明代理。
