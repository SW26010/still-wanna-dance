# 需求调度验证（issue #22）

2026-10-11，Windows 本地自动化验证。实现范围和运行语义见[队列预缓存](queue-prefetch.md#资源需求与优先级)。这些结果不代表当前构建已完成 VRChat 实机验收。

## 自动化结果

| 检查 | 结果 |
| --- | --- |
| `go test ./cmd/... ./internal/... ./data/... -count=1 -timeout=240s` | 全部通过 |
| `go vet ./cmd/... ./internal/... ./data/...` | 通过 |
| `go build ./cmd/...` | 通过 |
| 新增缓存调度与 GET 生命周期回归，`-count=20` | 通过 |
| 控制台删歌、换房、移出窗口、新队首重排回归，`-count=10` | 通过 |

新增缓存回归覆盖：

- 多个队列 ID 共享 MD5；删除一份需求不取消另一份仍需要的内容。
- GET 已收到前段、等待新字节期间保持 P0；队列变化优先抢占其他低等级下载，最后一个 GET 结束后重新评估。
- 两个 GET 引用分别释放；请求取消在回调执行前也立即影响有效等级。
- 无竞争时，取消预取调用不浪费已启动的下载，最终仍完整校验落盘。
- 人为延迟网络正文的 `Close`，确认旧 worker 清理前后台计数及真实额度不释放，也不发布部分内容。
- 同 MD5 的旧 worker 正在取消时收到 GET，等待清理后再启动；P0 先于等待的 P3 获取额度，不重复下载。
- HEAD 与已完成的短 Range GET 不留下 P0；后续 GET 可以抢占已无需求的旧下载。
- 结束后没有遗留 `.part` 文件。既有共享下载、Range、选路、缓存交接保护、淘汰与关闭测试随正式包测试通过。

可重复执行核心缓存回归：

```powershell
go test ./internal/cacheproxy -run 'TestDemandRanks|TestQueueDemandPreemption|TestNoCompetition|TestPreemptionWaits|TestGETReattaches|TestHEADAndCompleted' -count=20 -timeout=60s
go test ./internal/console -run TestQueueInvalidation -count=10
```

## 环境边界

本机直接运行 `go test ./...` 会纳入 Git 忽略的历史 `artifacts/` 和 `test-runs/` 探测源码，其中存在重复 `main` 和旧 `stepstash/internal/cacheproxy` 引用，导致总体失败；正式代码包通过。保留这些本地文件，使用上表明确的正式包路径验证。

受限 Windows 沙箱阻止部分原子重命名、文件身份和链接测试；完整正式包测试在正常权限下使用隔离临时目录执行，没有修改真实 hosts 或启动游戏。仅新增缓存回归在仓库内专用临时目录也通过。本机未配置 Windows race 所需的 C 编译器，因此未运行 `-race`，应由已有 Linux CI 补充；本次按要求没有 push 或触发远程 CI。

## 延后的真实游戏检查

1. 两首慢速预缓存时删除旧队首并加入新队首，确认新队首及时开始，日志记录抢占原因，旧资源没有半成品落盘。
2. 游戏 GET 正等待后续 Range 块时删歌或换队首，确认当前响应不断流；播放器停止或断开后，旧下载按剩余需求降级。
3. 快速离队、重新入队、换房和停止监听，检查没有旧队列错误复活；普通离队的 30 秒文件保护仍有效。

本阶段先完成代码与可控网络回归；以上实机检查尚未执行。
