# 上游协议策略实施验证

日期：2026-10-29，北京时间。基线为 `7faf1c3f`，验证对象为本次未提交工作区。现行规则见[上游通道](../upstream-channels.md#业务请求的协议策略)，历史连接调查见[观测汇总](kiva-connectivity-20261029.md)。本次只实施 HTTP/HTTPS 及相关候选执行规则，不实施另行确认的曲库主表／资源映射改造，也不提前合并资源 Monitor 与下载模块。

## 实现范围

- 清单、播放地址和资源请求共用有界协议尝试：HTTPS 优先，传输故障可回退 HTTP；证书错误、业务状态／校验错误和本地生命周期错误不授权降级。同端点协议变化及升级跳转不重置候选预算。
- 固定 IP 的冷候选交错覆盖 IPv6/IPv4；已测推荐、DNS 有效期、Host/SNI、证书校验、强制代理隔离不变。不固定历史 IP、不永久禁用地址族。
- 完整清单读取和 Range 正文失败参与相同游标。顺序 200 正文保持不在中途重开的契约。显式 HTTP 回环测试配置继续保留，不增加用户设置。
- Monitor 记录执行协议；不同协议的延迟／吞吐不混合，HTTPS 恢复后不因等待新吞吐而被 HTTP 推荐挤掉。原始资源 URL 与 API 来源身份保持不变。
- 不新增协议健康缓存、独立恢复轮询或资源调度状态。后续请求和既有监测重新确认 HTTPS。

## 自动与隔离验证

实现阶段通过以下检查；审查修正后的补充验证见本节末尾：

| 检查 | 范围 |
| --- | --- |
| 全项目 Go 回归 | `go test -p 1 -tags=no_easytier ./cmd/... ./internal/...` |
| 静态检查 | `go vet -tags=no_easytier ./cmd/... ./internal/...` |
| 只读依赖构建 | `go build -mod=readonly -tags=no_easytier ./cmd/...` |
| Windows race | `upstreamrequest`、`playbacksource`、`upstreamstate`、`cacheproxy`、`console` 的协议、完整读取、升级、冷候选、HTTPS 恢复、超时及业务重试定向用例 |

使用开发文档规定的测试临时目录包装器；race 使用仓库已有便携 GCC。真实本地 TLS 服务覆盖正常 HTTPS、主动断开、正文截断、超时、证书拒绝及错误内容，验证实际 HTTP 请求是否出现。资源用例覆盖初始与后续 Range、丢弃下载器、重定向目标回退、错误 Range／超长正文拒绝，以及证书错误恰与期限重合时仍不降级。已有测试继续验证代理身份、固定 IP、取消释放和顺序响应生命周期。

迭代中发现并修正：升级请求未计入游标、Monitor 重定向后未再次应用回退、截止掩盖不可降级错误、恢复的 HTTPS 被缺吞吐条件过滤、重定向计时在零时钟增量时漏算，以及后续 Range 单通道重试次数变化。旧测试中的固定两次全失败预期更新为两条 HTTPS 加两条合格 HTTP 回退，仍受共同六次上限及原父期限约束。

历史失败保留：首轮沙箱内持久化测试触发已知 Windows 原子重命名限制；沙箱外复验通过。一次全项目检查在编辑期间读到不同版本的包，出现新增函数未定义；停止修改源码后的完整复验通过，不将该次失败计为通过。原始日志在忽略目录 `artifacts/protocol-regression.log`、`protocol-regression-final.log`；最终结果为 `protocol-regression-verified.log`、`protocol-vet.log`、`protocol-build.log`、`protocol-race.log`。

审查补充：新增回归用例先复现了两项重定向边界问题——后续 Range 在目标端点耗尽尝试后将其错误记到起始端点，导致健康起点被降级；Monitor 遇到同端点 303 升级或 A→B→A 回环时重新创建游标，出现八次请求。修正后只将本层收到的正文／校验失败反馈给响应端点，303 升级沿用共享游标，Monitor 拒绝再次进入已访问的资源端点。用例分别断言健康起点没有 HTTP 请求、升级最多出现 HTTPS→HTTP→HTTPS，以及回环不重复请求端点；不新增公共接口、持久状态或调度机制。

修正后 `cacheproxy`、`upstreamstate`、`upstreamrequest`、`playbacksource`、`console` 五个包的完整测试与同范围 vet 通过，命令行只读依赖构建通过；前三个包的上述重定向／升级用例 race 检查通过。补充日志为 `artifacts/protocol-review-regression.log`、`protocol-review-vet.log`、`protocol-review-build.log`、`protocol-review-race.log`。本轮没有重跑公网探测，以下实网记录仍只对应原观测时段。

## 本机直连实网复测

使用产品独立 DoH、候选池、共同 Fetch 与播放解析器，不使用系统 hosts 或环境代理；HTTPS 保留证书验证。每个操作总期限 30 秒，清单完整读取与结构校验，资源仅读取 1 KiB；不保存或输出签名资源 URL。

14:51 的首轮中，Udon 六次请求均取得 200，但正文被按候选数分配的短期限截断，整轮未取得完整清单。由此调整为：等待响应头时预留候选时间，收到响应头后在原单次尝试及调用者期限内读完正文；不额外延长期限。新增隔离测试重现并覆盖该问题。

14:53 的复测结果：

| 目标 | 实际路径与结果 |
| --- | --- |
| Udon 清单 | 首个 HTTPS/IPv6 完整 200，2,885,371 bytes，10,470 首，版本 `20261028232411` |
| Kiva 清单 | HTTPS/IPv6 EOF，随后 HTTPS/IPv4 完整 200，5,992,687 bytes，10,470 首，同上版本 |
| WannaInfo | HTTPS/IPv6 EOF，随后 HTTPS/IPv4 完整 200，5,768,543 bytes，10,076 首，版本 `20260215004959` |
| 播放地址 | HTTPS/IPv6 返回合法 302，按原始 Location 解析资源 |
| 资源片段 | HTTPS/IPv6 返回 206，Content-Range 与 1,024 bytes 校验通过 |

原始两轮记录和带 `ignore` 构建标记的复跑程序位于 `artifacts/protocol-policy-live/`。最终短测无需触发 HTTP 回退；受控服务验证其行为。这些结果只证明本机当时的直连样本，不是长期可用率、代理实网或完整播放验收。未启动 VRC、未改 hosts、未修改日常实例配置及数据，也未新增必须交由游戏验证的台账项。
