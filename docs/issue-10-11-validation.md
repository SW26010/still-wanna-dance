# Issue 10 / 11 本地实现与验证

日期：2026-10-07。环境：Windows amd64，Go 1.27.1，Node.js 24.19.0。

## 实现范围

- 配置运行中可保存，保存与当前运行快照分离，统一在应用重启后生效；失败保存不更新内存快照，修改回当前值清除待重启提示。
- CDN、Queue 独立启停，共用由 Console 持有的缓存引擎；队列开启意图与实际运行分别表示。下载补齐暂停队列，期间关闭后不再恢复。
- 独立自动启动配置与旧配置迁移，不把旧的默认队列开关解释为应用启动即下载。
- 立即重启在旧实例完成有序清理、释放锁后启动新进程；保留配置路径、控制台地址与托盘模式，网页检测新会话后刷新。
- 删除保存时切换存储的版本号与读取重试逻辑，保留退出、扫描及缓存文件身份检查。

具体语义见[配置与运行生命周期](settings-lifecycle.md)。全部提交留在本地，未 push，未修改或关闭 GitHub issue。

## 自动验证

| 检查 | 结果 |
| --- | --- |
| `go test ./cmd/... ./internal/... -count=1 -timeout=120s` | 全部通过 |
| `go vet ./cmd/... ./internal/...` | 通过 |
| `go build ./cmd/... ./internal/...`，Windows amd64 | 通过 |
| `GOOS=linux GOARCH=amd64 go build ./cmd/... ./internal/...` | 交叉构建通过，未在 Linux 上运行测试 |
| `node --test scripts/refresh.test.mjs scripts/navigation.test.mjs scripts/terms.test.mjs scripts/acceptance-protocol.test.mjs scripts/acceptance-request.test.mjs` | 117 项通过 |
| `git diff --check`、Go 格式检查、JavaScript 语法检查 | 通过 |

真实子进程测试运行程序入口的无托盘模式：使用带空格的配置路径和动态端口，确认初始配置锁、重启响应、旧进程退出、新实例使用同一地址和新令牌、拒绝旧令牌、最终正常退出与锁释放。测试未同意条款，不启动上游监测或游戏下载。

后端回归覆盖运行中保存、网络请求不中断、库存扫描不切库、密码省略与清除、四种 CDN/Queue 组合、CDN 故障、队列启动失败后重试、补齐成功/失败/取消/退出后的队列意图、配置迁移和重启请求鉴权。前端覆盖保存草稿与轮询顺序、独立队列开关、当前/已保存配置分开显示、重启响应丢失、等待旧实例退出及连接超时提示。

本地忽略目录 `artifacts/`、`test-runs/` 中有旧实验 Go 文件，因此全量检查采用便携打包脚本同样的正式源码目录范围。Windows junction、端口归属及实例发现测试需要在沙箱外运行，已通过。没有修改这些旧实验文件。

## 后续人工验收

按本次要求延后真实 VRChat 联动：队列同步、预缓存命中、播放期间保存设置及重启后的实际游戏恢复。托盘重启的实际桌面体验也待人工确认；配置锁与 Windows 单实例基础测试已通过。

本机 `CGO_ENABLED=0` 且没有 GCC，未运行 race detector。没有把自动测试通过等同于真实游戏或桌面视觉验收通过。
