# HEAD 元数据与下载解耦验证（2026-11-15）

本轮基于 `9b8034f1` 的工作区修复普通 HEAD 进入下载需求路径的问题。现行规则见[HEAD 元数据请求](../download-resource-design.md#head-元数据请求)。

## 复现与根因

原始架构检查两轮各 100 次冷 HEAD，分别产生 37／11 次上游 Range 尝试；上游未交付正文，因此只证明额外网络尝试，不证明整视频下载。后台已有 2848 个唯一使用块时，另一个 45 块视频的 HEAD 返回 503。原始探针及结果保留在忽略目录 `artifacts/architecture-review-head-20261115/` 的 `probe.go`、`result.log`、`result-with-get-control.log`。

最小回归 `TestHEADDoesNotRequireDownloadCapacity` 仅占满后台使用额度，再发送一个已知大小的冷 HEAD。修复前稳定返回 `503 / block usage demand capacity reached`；无需真实网络或游戏环境即可判断此故障。

普通入口在完成请求解析后仍调用 `obtainDemand`。虽然 HEAD 未登记 GET 时钟，但此调用本身会登记整资源需求并进入下载任务，所以同时解释容量拒绝和可能发生的 Range 尝试。已发布文件打开不申请整文件发布额度；歌曲解析保留原准入、地址与身份规则，本身不登记块下载需求。

## 修复范围

- 普通 HEAD 在请求解析后进入独立元数据路径，只检查已发布文件属性或使用解析得到的大小，不进入下载 interface。
- `media_headers.go` 共用视频类型、ETag 与 Range 能力响应头；GET 与预览复用同一设置，预览 HEAD 仍不需要引擎。
- 普通 HEAD 保留本地 HIT／冷 MISS 标记、ETag／本地日期条件及忽略 Range 的行为；歌曲屏蔽、请求观察和身份校验保留。

## 验证

Windows amd64、Go 1.27.3；定向 race 使用 GCC 16.2.0、`CGO_ENABLED=1`。测试通过仓库 `with-test-temp.ps1` 和 `go-patches.mjs` 入口运行；日志位于上述忽略目录。HEAD 回归可单独执行：

```powershell
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run '^TestHEAD' -count=1
```

| 检查 | 结果与证据 |
| --- | --- |
| 容量最小回归 | 修复前 503，修复后 200、空正文，已有容量不变，无新需求或下载任务 |
| 定向回归 | HEAD 容量、普通 URL／歌曲入口无需求与媒体回源、冷热响应头、ETag／日期条件、无效／多段 Range 忽略，以及歌曲屏蔽、GET、文件句柄、回收及发布额度相关检查通过；`fix-targeted.log` |
| 原始 100 次探针 | 100 次均为 200，媒体上游请求 0；2848 块后台负载下 HEAD 为 200、空正文。GET 对照仍产生一次上游请求，取消后容量从 45 块恢复为 0；`fix-original-probe.log` |
| 缓存代理整包 | 沙箱内完成整包（286.930 秒），13 个用例因硬链接／junction 等权限限制失败，其余通过；这 13 个用例在普通用户权限下复验全部通过（7.557 秒）。分别保留 `fix-cacheproxy.log`、`fix-cacheproxy-failed-unsandboxed.log`，不将首轮改写为通过 |
| Console 预览 | 两个预览入口用例在普通用户权限下通过（0.492 秒），覆盖 HEAD 不启动引擎、签名／来源／身份与歌曲准入；`fix-console-preview-unsandboxed.log` |
| 定向 Windows race | HEAD、冷热 GET、歌曲准入、当前 GET 生命周期及共享文件句柄检查通过（98.974 秒），无竞态报告；`fix-race-unsandboxed.log` |
| 静态检查 | cacheproxy／console 的 vet、gofmt、`git diff --check` 与全部变更文件的仓库产物分类检查通过；`fix-vet.log` |

Console 首轮在沙箱内报 `renameat: The handle is invalid`，race 首轮报 `ThreadSanitizer: failed to restore address`；原始输出分别保留在 `fix-console-preview.log`、`fix-race.log`。按仓库已记录的 Windows 沙箱限制复核，仅改变运行权限，未改测试断言或产品文件操作。

原始探针使用受控回环上游，调用真实 Server 处理器、需求与调度实现；不是公网或 VRC 音画验收。本轮不启动游戏、不修改 hosts，没有新增必须由 VRC 判断的剩余场景。
