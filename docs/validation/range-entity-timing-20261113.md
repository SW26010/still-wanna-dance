# Range 实体条件与测试同步验证（2026-11-13）

本轮在 `a8681a61` 提交块读取句柄复用后，单独跟进 `TestRangeTailReadableWhilePrefixBlocked` 的 `missing entity condition` 偶发失败。[上一轮记录](block-read-reuse-20261113.md#回归与保留的失败)中的当前源码 2/20、未修改基线 6/50 失败继续保留；本轮完成归因和修正，替代其未解决状态。现行契约见[Range 并发与实体校验](../download-resource-design.md#7-不保留块下载暂停与恢复)。

## 复现与根因

用例在服务端 Flush 前缀响应头后关闭 `prefix` 通道，随即启动四个尾部 reader，并要求所有非零块请求都携带 `If-Match: "stable"`。该信号只证明服务端已发送头，不证明客户端已收到响应并执行 `acceptEntity`。

在请求构造与实体接受处临时记录 `[DEBUG-range-entity]`，30 轮有 7 轮失败。失败顺序为：块 0 构造请求时没有 ETag → 块 2 构造请求时仍没有 ETag、未带 If-Match → 块 0 接受响应并记录 ETag → 服务端检查块 2 请求并报错。此时 `prefix_headers_sent=true`；随后块 1 的请求已携带正确条件。见忽略目录 `artifacts/range-entity-20261113/trace-before.log`。

该顺序符合当前下载契约：冷块不等待其他块的首响应，构造请求时尚未知晓的 ETag 无法成为条件；响应返回后仍需独立验证实体一致性及最终 MD5。不能按块号推断实体建立顺序。

通过现有 ResourceTransports 入口包装真实回环 HTTP transport，暂存块 0 的响应，直到另一个冷块已构造请求才交付。这将原断言的失败固定为 3/3，0.727 秒，不依赖延时或概率。复现源码差异和原始结果保留在 `deterministic-before.patch`、`deterministic-before.log`。

## 修正范围

- `TestRangeTailReadableWhilePrefixBlocked` 保留上述通道屏障，明确覆盖实体建立前的并发冷请求；断言首个冷请求没有未知的条件，拒绝外来条件。原有四读者共享、尾部正确、前缀未完成不得发布、并发与请求次数、最终文件字节校验全部保留。
- `TestParallelRangeEntityValidation` 的强 ETag 场景先通过真实 Range GET 完成首块，并等待该会话收尾，再检查后续并发请求的条件和响应校验。保留 If-Match 必须存在的断言，新增 `strong-stable` 成功场景，与强 ETag 缺失／变弱／改变的拒绝场景配对。
- `TestChangedAddressDoesNotReuseStrongValidator` 同样先在新地址完成首块，再要求后续请求携带新地址的条件。旧条件拒绝、完整发布、旧 reader 可读及最终清理断言继续保留。

最终改动仅涉及测试及契约说明；临时产品探针已移除，下载实现与 `a8681a61` 相同。冷启动并发与已建立实体后的条件请求通过各自真实阶段验证。

## 验证与证据

Windows amd64、Go 1.27.3；race 使用 GCC 16.2.0、`CGO_ENABLED=1`。证据均位于忽略目录 `artifacts/range-entity-20261113/`。

| 检查 | 结果与证据 |
| --- | --- |
| 同一受控复现 | 原断言 3/3 失败；修正后三个顶层用例各运行三次均通过，5.639 秒。`deterministic-before.log`、`targeted-after.log` |
| 条件缺失反向验证 | 临时移除产品构造请求时设置 If-Match 的一行，经 diff 确认后执行 `TestParallelRangeEntityValidation/strong-stable`，出现五次 `strong condition lost`，按预期失败。`missing-condition-mutation.log`；随后恢复产品源码并确认无差异 |
| 连续回归 | 三个修正用例各 20 轮通过，41.654 秒。`repeated-after.log` |
| 完整常规包测试 | cacheproxy 187.438 秒、blocksched 0.171 秒，均通过。`packages-after.log`；可选跨卷、磁盘耗尽及 CPU 实验未启用 |
| 定向 Windows race | 三个修正用例及换址重定向用例通过，96.737 秒，无竞态报告。`race-after.log` |
| 静态与清理 | cacheproxy vet 退出码 0，gofmt、`git diff --check` 通过；`vet-after.log`。产品源码无临时日志或条件缺失变异 |

整包和 race 按已确认的 Windows 沙箱限制，通过隔离临时目录入口在普通用户权限下运行。本轮没有需要 VRC 才能判断的剩余场景。
