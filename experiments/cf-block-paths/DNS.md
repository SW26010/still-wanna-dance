# 四组实验中断后的 DNS 边界观察

2026-10-21 四组下午窗口以 `test_candidate_removed` 停止。保留全部原始样本和缺测，不重跑该窗口，不把未完成的顺序平衡当成完整实验。

本补充只定位候选发现边界，不追加速度样本。旧进程没有在失败点保存 DNS 池，因此不能追溯断言中断原因是 TTL 为零、AAAA 太晚或权威记录变化。

使用测试标签下的 `TestCFFamilyDNSExperiment`，对同一域名复用产品 `queryProvider`、`collectDNS`、`lookupShared`、默认 DNS 刷新策略、租约存储和 `upstreamrequest.Pool`。包装器只记录每个提供者 A/AAAA 响应的地址、TTL、用时及脱敏错误；不改 200 毫秒收集窗口，不延长 TTL，不固定 DNS 结果，不做媒体下载。

预先固定为 120 次候选发现，每次间隔两秒，共约四分钟，内层上限六分钟。保留当前可执行候选、缓存地址及过期时间，以及前台/后台解析记录。失败和少于六个候选的样本照常保存，不为复现期望结果延长观察。DNS 查询仍由原缓存和刷新机制决定，不是每两秒强制向所有提供者请求。

独立夹具验证两个机制：A 和 AAAA 任一方比首个可用答案晚到超过 200 毫秒，都可能被截掉；TTL 为零的有效答案在当前候选接口中没有可用租约。夹具只能证明机制存在，不能代替实网故障归因。

```powershell
./scripts/with-test-temp.ps1 go test '-tags=no_easytier,cf_block_experiment' ./internal/console -run '^TestCFFamilyDNSHarness$' -count=1
./scripts/with-test-temp.ps1 go test -c '-tags=no_easytier,cf_block_experiment' -o artifacts/cf-family-paths-20261021/dns-probe.exe ./internal/console
& artifacts/cf-family-paths-20261021/dns-probe.exe '-test.run=^TestCFFamilyDNSExperiment$' '-test.v' '-test.timeout=7m' -cfexp-out artifacts/cf-family-paths-20261021/dns-followup
```

后续跨时段四组对照保留原有冻结 EXE 和停止规则；中断属于可用性证据，不能通过临时放宽候选成员或延长 TTL 把它改写为成功下载。
