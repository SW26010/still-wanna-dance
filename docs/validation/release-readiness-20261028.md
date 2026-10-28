# 发布前复核与修复（2026-10-28）

基于 `f5dda56f` 的工作区，复核 2026-10-27 发布审查中提出的问题并完成下列修复。此次只做代码、依赖和自动回归；按任务边界不修改 GitHub Actions、不开展新的实机/实网验收，也不执行正式发行打包或产品源码的离线重建。历史游戏失败与待验状态仍以[实机台账](../live-acceptance-backlog.md)为准。

## 修复内容

- 桌面入口取得配置锁后，先读取日志级别并暂存配置错误，以遵守级别的 stderr logger 打开并清理固定块工作区，再安装文件日志和处理配置错误，恢复[生命周期约定](../runtime-lifecycle.md#安全与失败)。日志目录被文件占用、配置 JSON 损坏时也能回收已识别块；未知文件保留，错误退出释放工作区锁。新增入口子进程回归先重现两种残留，再确认修复，不启动业务上游。
- Go 最低版本提高到 **1.27.3**，`golang.org/x/net` 更新至 **v0.60.0**，必要关联模块由 Go 解析更新。依据为 [Go 发布记录](https://go.dev/doc/devel/release)、[HTTP/2 修复](https://pkg.go.dev/vuln/GO-2026-6610)及 [X.509 修复](https://pkg.go.dev/vuln/GO-2026-6809)。模块许可核对、声明和内嵌副本一起更新，详见[许可记录](../licenses/README.md)。本机原安装仍为 Go 1.27.1；补丁工具链通过 Go 官方下载和校验机制进入模块缓存，构建/Node 测试只在当前进程调整 PATH，没有修改全局安装。
- 测速的单路保持、自然增长和取消测试改用已有的 `testing/synctest` 模式。原来用主机时间观察异步 transport 回调，受线程抢占影响会压缩相邻采样间隔；现在保留原有时间、并发上限和清理断言，不放宽阈值，不修改生产调度逻辑。

前次浏览器的队列定位和短页布局问题、测速清理夹具的上游模式、旧锁兼容性文档已由后续提交修正，本次不重复修改。

## 自动验证

Windows amd64，Go 1.27.3，桌面相关 Go 命令使用 `-tags=no_easytier`。Windows 测试使用 `scripts/with-test-temp.ps1`，涉及进程和文件句柄的回归在正常用户权限下运行。

| 检查 | 结果 |
| --- | --- |
| `go test ./cmd/... ./internal/... ./data/... -count=1` | 20 个有测试的包全部通过，其余包无测试 |
| 同范围 `go vet` | 通过 |
| 新启动清理回归、原有日志等级和重启回归 | 修复后通过；新增两种失败场景在修复前均失败 |
| `TestCalibrationRunOwnsUpstreamCleanup -count=100` | 100 次通过，确认已有夹具修复有效 |
| `TestCalibrationHoldsSingleThenUsesNaturalGrowthAndCancels -count=100` | 虚拟时钟下 100 次通过；修改后 upstreamstate 整包再次通过 |
| Edge `task-errors.spec.ts`、`ui-layout.spec.ts` | 12/12，通过本地模拟 API 验证 |
| `pnpm --dir webui build` | 类型检查及资源生成通过；JS/CSS 内容未变，仅声明副本和清单更新 |
| Node `release-notices.test.mjs`、`release-source.test.mjs` | 9/9；源码重建用例只覆盖临时夹具，不代表实际产品离线重建 |
| 现有 `build-desktop.ps1` 构建开发 EXE | 通过，使用 Go 1.27.3 和生产构建参数，未发布 |
| `release-notices.mjs` 检查该 EXE | 112 个实际链接模块全部有已复核声明 |
| `go mod verify` | 全部模块校验通过 |
| govulncheck v1.9.0 扫描桌面源码入口 | 当前调用符号和导入包均为 0 条已知漏洞，模块图还有下述 5 条记录 |

扫描中的模块级记录为 `GO-2026-6866`、`GO-2026-6860`（x/net/html）、`GO-2026-6237`（dhcp）、`GO-2026-5932`（crypto/openpgp）、`GO-2026-5841`（compress/s2）。这些包未被此次桌面入口导入，故本轮没有为消除模块图提示扩大升级范围。这是特定入口、构建标签和数据库快照下的静态分析结果，不是对全部协议、平台或未来调用的安全保证。

## 失败与边界

- 初次扩大测速重复检查时使用了过宽的 `TestCalibration.*(Cleanup|Release|Cancel)` 正则，包含每次约 5 秒的自然增长用例；100 轮无法在设定的 5 分钟内完成，最终超时。该轮另观察到一次 `177.1156ms < 200ms` 的时间断言失败。随后该用例单独运行 10 轮通过；改用虚拟时钟后 100 轮及所属整包通过。保留失败事实，不把超时称为产品死锁或全部重复测试通过。
- 第一次桌面构建因刚更新的第三方声明尚未同步到内嵌资源而被新鲜度检查拦截，生成资源后通过。Node 回归初次为 8/9：旧本机 Go 1.27.1 在 `GOTOOLCHAIN=local` 下被最低版本检查拒绝；当前进程改用 1.27.3 后 9/9。
- 后续 P2 复核发现：首次调整启动顺序时在读取日志级别前传入 nil logger，残留块清理会绕过 `off/warn/error`。扩展入口回归，放入可回收块和同名异常目录，先确认这三个级别失败；随后提前读取级别、暂存配置错误并传入遵守等级的 stderr logger。`info/debug` 记录回收和跳过事件，`warn` 仅记录跳过警告，`error/off` 不输出这两类事件，非法级别按 Info 记录后报错；每种情况仍回收普通块并保留异常目录。桌面入口及 applog 整包测试、定向 vet 通过，日志为 `cleanup-log-level-before.log`、`cleanup-log-level-after.log` 和 `cleanup-log-level-vet.log`。本次未重跑上表中无关的全量、浏览器和构建检查。
- GitHub Actions 中的 Go 1.25.x 和 `GOTOOLCHAIN=local` 保持原样，尚不能构建本次代码；后续需要单独对齐。日常桌面构建/打包同样要求 PATH 中的 Go 至少为 1.27.3。没有以普通命令的自动工具链切换冒充本机或 CI 已升级。
- 上述验证期间没有执行 VRC、真实磁盘耗尽、托盘/跨账号、实网上游或完整发行验收，也没有触发远端 CI、提交或推送；后续本地提交遵循用户另行授权。

本轮原始日志、模块差异和许可比较、构建身份及开发 EXE 保存在本地忽略目录 `artifacts/release-fixes-20261028/`；初次扩大测速重复的超时堆栈保留在本次工具输出，本页摘录其命令和失败症状。前轮证据继续保存在 `artifacts/release-review-20261027/`，不删除或改写。
