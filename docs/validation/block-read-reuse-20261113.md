# 临时块读取句柄复用验证（2026-11-13）

基线为 `157e2512b15e`。原 `readBlockFile` 每次调用都执行 `os.Open`、`ReadAt`、`Close`；流读取与完整合并快照均经过该函数。32 KiB 分段读取一个 1 MiB 块，会打开、关闭文件 32 次；快照的文件 pin 没有消除重复打开。现行接口见[本地资源管理接口与所有权](../download-resource-design.md#6-本地资源管理接口与所有权)。

## 修复与生命周期

`local_block_reader.go` 的 `blockReader` 统一提供跨块 `ReadAt` 和 `Close`。每个 reader 最多保留一个当前块句柄，同块小段读取复用句柄；读完块、切换块、跨块 Seek、读取出错或关闭时释放句柄。普通 reader 在句柄存续期间固定对应文件，先关闭句柄再 unpin；完整 snapshot 仍原子固定全部文件至 Close，仅按需逐块打开。

`spoolReader` 持有自己的本地 reader，缺块等待继续独立登记；请求取消会关闭本地 reader，显式 Close 会先撤销取消回调。读取与关闭通过 reader 自己的互斥串行，不在存储元数据锁内执行文件 I/O。旧文件退出可用集合后仍可由持有者读完，最后文件引用释放后删除，不影响替换块。

## 性能对照

Windows amd64、Go 1.27.3、Ryzen 7 9850X3D。`BenchmarkBlockRead` 创建 64 个 1 MiB 块，以 32 KiB 缓冲顺序读满 64 MiB，计时前预热系统缓存；分别通过真实 spool reader 和合并 snapshot 读取。每组三轮，每轮五次，表中为三轮中位数。

| 路径 | 修复前 | 修复后 | 改善 | 分配次数／64 MiB |
| --- | --- | --- | --- | --- |
| 流读取 | 85.160 ms | 7.736 ms | 约 11.0 倍 | 6151 → 204 |
| 合并快照 | 84.222 ms | 8.374 ms | 约 10.1 倍 | 6151 → 196 |

复跑入口：

```powershell
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run '^$' -bench '^BenchmarkBlockRead$' -benchtime=5x -count=3
```

原始结果保留在忽略目录 `artifacts/block-read-reuse/benchmark-before.log` 和 `benchmark-final.log`。这是系统热缓存下的本地读取开销，不代表冷磁盘、网络或游戏首帧收益。

## 回归与保留的失败

| 检查 | 结果与证据 |
| --- | --- |
| 分配规模回归 | `TestBlockReadAllocationsScaleWithBlocks` 比较同样 2 MiB 内容的 32 KiB／1 MiB 读取。原流读取分别为 197／11 次分配，快照为 196／10，均失败；修复后通过。见 `before.log`、`targeted-after.log`、`benchmark-final.log` |
| 文件引用与读取 | 新增当前块边界释放、同块／跨块 Seek、关闭、空闲取消、打开／读取失败、多读者独立引用、存储关闭后快照读取、并发 ReadAt/Close 检查；既有跨块、短尾块、快照退休、替换文件及删除计量断言继续通过。见 `lifecycle-after.log`、`targeted-after.log` 与最终整包／race 记录 |
| 最终定向 Windows race | 通过，30.898 秒，无竞态报告。覆盖新增生命周期用例、块回收、独立缺块等待、取消、并行 Seek、流交付及旧 reader 退出。Go 1.27.3、GCC 16.2.0、`CGO_ENABLED=1`；`race-final-unrestricted.log` |
| 静态检查 | cacheproxy、blocksched 的 vet，以及 gofmt、`git diff --check` 通过；初轮 vet 无输出，收尾的 cacheproxy 复验记录退出码 0，见 `seek-cancel-vet.log` |
| 整包回归 | blocksched 通过；cacheproxy 运行完整套件，只有未修改的 `TestRangeTailReadableWhilePrefixBlocked` 报 `missing entity condition`，因此不记整包通过。见 `packages-final-unrestricted.log`，cacheproxy 163.730 秒 |

分配断言初次在 race 插桩下也运行，出现随读取次数增加的额外分配，见 `race-final-lifecycle.log`。该性能断言现通过 `!race` 单独运行，不以插桩结果衡量文件打开成本；文件生命周期和并发检查仍在 race 范围内。

首轮整包在沙箱内受到硬链接、目录联接及相关子进程限制，记录在 `packages-after.log`；最终定向 race 的沙箱运行也遇到 `ThreadSanitizer: failed to restore address`，记录在 `race-final.log`。随后依照开发约定在普通用户权限下执行同一隔离目录入口，文件系统限制解除，定向 race 通过；整包仍保留上述 Range 用例失败。

针对 Range 失败，当前源码连续 20 次有 2 次失败，见 `range-tail-recheck.log`。另用 `git archive` 从未修改的 `157e2512b15e` 导出隔离检出，连续 50 次有 6 次相同失败，见 `range-tail-baseline.log`；因此它在本次改动前已存在。用例以服务端发送前缀响应头作为同步点，不能保证客户端已记录 ETag；这解释了实体条件断言存在时序窗口，后续修正应明确同步客户端实体建立。此次保持该用例及下载策略原样，保留既有失败，不通过跳过或放宽断言取得整包通过。

## 评审收尾：取消后的 Seek

评审指出取消回调先关闭本地 reader 后，Seek 会返回 `os.ErrClosed`，丢失来源到期原因。新增 `TestStreamResponsePreservesCancellationDuringSeek`，在来源以 `blocksched.ErrExpired` 结束并等待关闭回调完成后，调用真实 `serveStream`／`http.ServeContent`。普通／Range × GET／HEAD 四种组合在修复前均提前提交 500，正文为 `seeker can't seek`，见 `seek-cancel-before.log`。即使 Seek 同时记录错误，已提交的响应也无法由外层改成 504。

修复保留原 spool Seek 的元数据语义：逻辑偏移查询／更新不因本地 reader 已关闭而失败，仍在跨块时释放旧句柄；实际 Read 负责返回已有的 context cause。通过移除 `blockReader.seek` 对 closed 状态的拒绝完成，没有增加错误状态或响应缓冲。GET 的到期错误留给外层按既有规则处理，HEAD 仍可读取不可变元数据。

修复后四个回归子场景通过：GET 保留 `ErrExpired` 及尚未提交的响应，现有状态映射为 504；HEAD 普通／Range 分别返回 200／206 且无正文。连同块读取、引用释放、缺块取消、错误 Range、冷流式交付等定向检查通过，3.675 秒，见 `seek-cancel-after.log`。相关流响应、Seek／关闭、取消等待及请求到期用例在普通用户权限下连续三轮 race 通过，20.539 秒，无竞态报告，见 `seek-cancel-race.log`；vet、gofmt 和 `git diff --check` 通过。原整包失败和热缓存性能证据按上述范围保留，本次未重跑整包或性能基准。

可选跨卷、磁盘耗尽和 CPU 实验未启用。本次读取问题可由隔离文件 I/O、受控 HTTP 与 race 判断，无新增必须依赖 VRC 的验收项。
