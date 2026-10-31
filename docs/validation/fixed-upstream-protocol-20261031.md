# 固定上游协议与按需备用清单验证

日期：2026-10-31，北京时间。源码基线 `996e07ab4fd66842f1b464e67ad7e8400ff89cc9`，验证对象为未提交工作区。用户已接受 HTTP 清单的安全代价，并确认 Kiva/WannaInfo 暂保留完整清单兜底、改为按需尝试。现行规则以[上游通道](../upstream-channels.md#业务请求的协议策略)和[曲库设计](../song-catalog-design.md#清单同步与增改不删)为准；实施前证据见[评估记录](../research/fixed-upstream-protocol-assessment-20261031.md)。本记录替代旧 HTTPS 优先规则，历史实测结果保留。

## 实现与边界

- Udon 清单、播放 API 与已验证资源的执行固定 HTTP；Kiva/WannaInfo 固定 HTTPS。删除自动升降级、降级资格分类、Session 批内协议记忆、OriginScheme 配置及 Monitor 的跨协议推荐逻辑。CLI、Console、丢弃下载测速及探测共用固定规则。
- 清单按 Udon → Kiva → WannaInfo 顺序尝试，首个可用清单接受后停止。缺 MD5 不触发备用请求；旧的合法清单保留本地水位即可结束本轮。备用清单继续使用原完整解析、版本裁决和资源补缺事务，不增加独立调度或专属健康状态。Monitor 只检查 Udon；手动测速仅在样本准备不足时尝试备用。
- 候选重试、响应头与正文期限、独立 DNS、代理隔离、资源域名/MD5/大小校验、Range 正文重试和顺序 200 的生命周期保留。原始返回 URL 不改写；资源执行按 HTTP 归一后拒绝回访同一地址，不循环跟随强制 HTTPS 升级。
- 旧 Udon HTTPS API 记录仍可用于元数据完整性判断和有效地址复用；新的 HTTP 观察通过原时间顺序检查后，事务替换相同歌曲/节点的旧行。原始视频 URL、映射和数据库格式保留，不清库。历史 HTTPS 或缺协议的吞吐样本不恢复为 HTTP 速度，合法冷却时间保留。
- 清单写入与播放观察共用本地事务错误处理。存储故障终止本轮，不能误触发备用来源；内容冲突仍属于清单拒绝。网络页未尝试的备选显示“未使用（已有可用清单）”，不生成测量时间或延迟。

DoH、Mihomo 自身传输、客户端 HTTPS 透传及入站非播放 API 的 307 兼容响应保持各自职责；不新增清单代理。没有启动 VRC、修改系统 hosts、写入日常实例数据或执行 commit/push。

## 自动化与浏览器结果

使用 Windows、Go 1.27.3、pnpm 11.25.0、Edge。Windows Go 测试经 `scripts/with-test-temp.ps1` 使用隔离临时目录。

| 检查 | 结果与范围 |
| --- | --- |
| `go test -p 1 -tags=no_easytier ./cmd/... ./internal/... ./data/... ./experiments/...` | 全部通过；包括 CLI、Console、资源加载、Monitor、代理与数据库回归。Console 42.517 秒，cacheproxy 75.150 秒。 |
| 同范围 `go vet -tags=no_easytier` | 通过。 |
| 末次整理后的清单/事务/旧记录定向检查 | 通过；覆盖备用接受后停止、写库错误终止、迟到旧观察和旧 API 记录复用。 |
| `pnpm --dir webui build`、`pnpm --dir webui check` | 通过，已更新嵌入资源与构建清单。 |
| 两个 acceptance Node 测试文件 | 10/10 通过；验收脚本不再跟随 API HTTPS 升级，视频执行归一 HTTP。 |
| CLI 与桌面 `go build -tags=no_easytier -mod=readonly` | 均通过，产物位于本次忽略目录。 |
| 隔离真实 Console + Edge | `TestNetworkConnectivityReusesResponsesToInitializeStorage` 与浏览器脚本通过；验证 5 行结果、两个备用跳过、HTTP 标识、曲库入库、390 px 窄屏无溢出及无页面异常。已检查截图。 |

网络故障夹具覆盖固定协议的候选切换、HTTPS 备用正文截断不降级、HTTP 升级不跟随、合法资源重定向、Range/正文失败与期限、取消与连接释放。旧三来源必测和协议切换断言已改为新的业务契约；相关正文校验与故障测试保留。

首轮失败未隐去：沙箱内原子替换报 `renameat: The handle is invalid`，与仓库已有环境记录一致；使用同一临时目录入口在沙箱外复验，未修改产品文件操作绕过错误。后续回归暴露旧请求次数/协议预期、代理端口夹具及测试中错误使用候选禁止桩；逐项修正。清单 SQL 失败原先只保留在 Store 状态、返回值缺少 ErrLocalStorage 标识，导致来源尝试继续；复用事务错误处理补齐此边界后，故障注入和全包复验通过。

日志和截图保留在忽略目录 `artifacts/fixed-upstream-protocol/`：`targeted.log`、`focused.log`、`focused2.log`、`console-focused.log`、`full-go.log` 保留阶段结果；`full-go-final.log`、`final-boundaries.log`、`vet-final.log`、`live-browser.log` 与 `browser/result.json` 记录最终检查。截图 `browser/active-proxy.png` 的文件 SHA-256 为 `7f24449b0aabd799883bdc5522da37b8cebbfd137573aeb26ebbc5ab6fc309e6`。原始路径及测试库信息不复制到提交材料。

## 公网产品路径小样本

10-31 11:30，通过 `SWD_LIVE_FIXED_PROTOCOL=1` 运行 `TestLiveFixedUpstreamProtocols`，使用产品 DoH、候选选择、响应校验及隔离存储，没有固定历史公网 IP：

| 请求 | 结果 |
| --- | --- |
| Udon HTTP 完整清单 | 200，2,886,450 bytes，版本 20261030115114，通过完整解析并入库 |
| 歌曲 6254 的 CF 播放 API | HTTP 302，合法资源域名 play.udon.dance，原始播放证据成功保存 |
| 同次返回资源 | HTTP 206，Range bytes=0-0，正文及大小校验通过 |

产品路径检查用时 11.58 秒，签名 URL 未输出。该样本不代表所有网络、资源域名或未来上游均支持 HTTP；没有做公网全视频下载、吞吐比较、Kiva/WannaInfo 可用性或 VRC 体验验证。只支持 HTTPS 的上游明确失败属于已选定固定策略的边界，不恢复运行时自动升降级。本次无需新增 VRC 台账项。

## 拆分提交验证

按用户要求分为两个提交：`e1ebb2bb`（`refactor(upstream): use fixed protocols for upstream requests`）只包含固定协议、旧播放证据兼容和吞吐样本规则；后续 `refactor(catalog): fetch backup catalogs only when needed` 包含按需清单、Monitor 范围、存储故障终止兜底及界面跳过提示。共同研究和本综合记录放入第二个提交。

第一提交使用隔离源码快照验证，全部 1,514 个文件的 Git blob 与实际提交树一致；它保留三清单检查及原清单兜底行为。CLI 和桌面程序的 `-tags=no_easytier -mod=readonly` 构建、`cmd/internal/data` 全包测试、同范围及实验包的 vet、前端产物一致性检查和 10 项验收脚本测试通过。第二提交源码的 CLI、桌面构建、`cmd/internal/data/experiments` 全包测试、同范围 vet 及 `pnpm --dir webui check` 均通过。

拆分过程的失败记录保留：首次隔离归档受 `export-ignore` 影响缺少实验目录，两个测试文件中的辅助函数重命名及候选数量断言还混入第一提交；补齐全部跟踪文件，并将这些断言与对应行为一起移入第二提交后，产品包回归通过。随后未改动的 `experiments/cf-concurrency` 在完整回归中报 Windows 本地套接字缓冲区／队列不足；单独以 `-count=1` 重跑整包通过（1.976 秒），不将单次复验外推为已定位系统资源问题的根因。

分提交日志保留在忽略目录 `artifacts/commit-split-20261031/`：`first-go.log` 记录首次分界检查，`first-go-final.log` 与 `first-experiments-retry.log` 保留第一提交的完整回归及实验包复验，`second-go.log` 记录第二提交的完整回归，`first-vet.log` 和 `second-vet.log` 记录静态检查。本轮未重新执行公网或 VRC 验收，前面的实网结果仍只适用于原采样时段。
