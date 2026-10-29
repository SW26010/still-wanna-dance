# 2026-10-28 CI 稳定性验证

本轮从 `46f67b7` 继续，先汇总失败类型并验证共用测试机制，再推送整批修复。此前工具链、临时目录、调度观察点及浏览器定位的逐轮证据分别见[开发说明](../development.md)、[调度验证](block-scheduler-validation.md)与[WebUI 验证](webui-validation.md)。

## 收齐的平台结果

[`check` #15](https://github.com/SW26010/still-wanna-dance/actions/runs/40716805096)的 Ubuntu 全部通过，包括 326 个浏览器测试、Node、Go 测试、vet、构建、race 和格式检查。Windows 浏览器 326 项、Node、普通 Go 测试、vet 和构建通过，但便携包脚本再次运行 Go 测试时，`TestZeroRetentionReusesExistingQueueVideoUntilHandoffExpires` 在检查文件消失时遇到 `CreateFile: Access is denied`；随后日志记录了成功回收。该轮便携包及烟测不能记为通过。

此前多次矩阵提前取消使另一平台的结果不完整。现保留各平台独立的最终结果，并在浏览器失败时保留 Playwright trace，供检查动作、请求、DOM 和焦点时序；不增加重试或延长断言超时。

## 成组复现与修正

| 类别 | 修复前对照 | 共用修正与验证 |
| --- | --- | --- |
| 保存尚未完成就读取状态或刷新 | 临时让内存接口的 settings 请求处理延后 250ms；7 个套件的 26 项中 7 项失败，均读取到未更新状态或尚未收到的请求 | 成功保存操作共用 `saveSettings`，等待设置操作区的最终状态；同样延迟和 4 workers 连续三轮 **78/78 通过**，26.6 秒 |
| 把异步回收中的首个文件错误当作最终结果 | Windows 用 `FileDispositionInfo` 标记真实文件待删除，保留句柄 100ms；旧 `expectRetained` 立即报与 CI 相同的 Access denied | 84 处调用共用的助手仍以原有 3 秒期限等待明确的文件不存在；持续错误最终仍失败，要求文件保留的断言不变。新平台用例和远端原失败用例各重复 50 次，共 **100 项通过**，9.150 秒 |

保存用例的受控延迟只用于诊断，已恢复夹具；最终测试不依赖固定等待。审查同时区分了本来就在验证禁用状态的失败／校验用例，以及已有“重启按钮启用”等完成条件的用例，没有机械替换所有禁用断言。通知与焦点类已有远端修复证据；其余仅检查弹层内容的用例没有被视为焦点恢复验证。

Windows 删除语义参考 [Microsoft DeleteFile 文档](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-deletefilea)。本机直接调用 DeleteFile 的首个夹具立即返回文件不存在，未复现 CI；改为显式的传统 `FileDispositionInfo` 后才得到相同错误。保留这一过程差异，没有将首次夹具失败算作原故障复现。

## 本地验证与证据边界

- 新工作区使用锁定的 pnpm 11.25.0 安装依赖；前端 `check` 和内嵌资源一致性校验通过。
- 移除延迟后的完整 Edge 回归 **326/326 通过**，1.9 分钟，启用了与 CI 相同的 `retain-on-failure` trace 模式。
- 使用 Go 1.27.3 运行完整便携包入口通过，覆盖 `cmd`、`internal` 全部 Go 测试、vet、内嵌资源校验和桌面构建；便携包烟测通过独立启动目录、配置锁、同意流程、日志、重复实例拒绝、重启、覆盖升级、整目录迁移及并发独立配置。ZIP SHA-256 为 `101074f87c4b3ee0fc9126966328721b345e64db4a5ea6cc9953465a7082ab74`；打包时是本轮未提交工作区，不能当作远端提交的发布产物。
- 本机 Playwright Chromium 在新工作区仍于启动阶段报 `spawn UNKNOWN`，没有进入用例；本机 Edge 结果与远端 Chromium 结果分开记录。
- 受控验证只操作内存 API 和隔离临时文件，不使用个人数据库、修改 hosts 或启动 VRC。

本轮原始证据在忽略目录 `artifacts/ci-stability-20261028/`：`install.log`、`chromium-baseline.log`、`slow-save-probe.diff`、`slow-save-before.log`、`slow-save-after.log`、`full-after.log`、`pending-delete-before.log`、`pending-delete-legacy-before.log`、`pending-delete-after.log`、`package.log`、`portable-smoke.log` 及对应输出目录。此前证据仍保留在原工作区的 `artifacts/ci-fix-20261028/`，未搬入版本库。

## 2026-10-29：异步轮询与短暂通知的成组复核

[`check` #16](https://github.com/SW26010/still-wanna-dance/actions/runs/40721820170)运行 `7a87e91`：Ubuntu 浏览器 325 通过、1 失败，Windows 324 通过、2 失败；两平台的曲库批次进度用例均失败，Windows 另有授权通知链接点击失败。浏览器失败使后续 Go、打包等步骤跳过，不能将该轮记为后端验证通过。该提交的[仓库产物检查](https://github.com/SW26010/still-wanna-dance/actions/runs/40721065127)通过。

- **模拟时钟与异步请求**：原曲库用例本地 30 次中 2 次复现 Windows 同样的“90% 进度未出现”；trace 显示批次停止触发新请求，测试在响应处理及下次定时器注册前就再次快进，后续读取落在断言超时边界。临时对 downloads 响应增加 250ms 延迟、单 worker 运行后，3/3 复现 Ubuntu 的“读取计数仍为 3”；只将 `fastForward` 换成 `runFor` 仍 3/3 失败。改为在默认断言期限内分步推进时钟，同样延迟下 3/3 通过（19.1 秒）。曲库刷新和下载完成用例共用 `expectWithClock`，停止后不应再请求的断言保留指定时长检查；助手不用于证明精确轮询周期。
- **诊断截图与通知消失**：在截图返回前模拟额外 7.5 秒处理时间，授权通知链接按原定时器消失，旧用例无法点击。尝试悬停暂停后 4 项仍失败，因此未采用。将截图移到通知操作和目标状态验证之后，同样慢采集条件下桌面与窄屏重复两轮 **4/4 通过**（28.5 秒）。VRCX 通知同样先操作，保留跳转后页面截图；没有修改产品通知时长或动画行为。
- 临时响应延迟和慢截图探针均已移除。原始结果分别保留在 `clock-baseline-unsandboxed.log`、`clock-delay-before.log`、`clock-runfor-probe.log`、`clock-delay-after.log`、`toast-slow-before.log`、`toast-slow-after.log`、`toast-capture-after-action.log` 及各自 trace 目录。最初沙箱内 Edge 启动失败属于环境限制，不计为原 CI 故障复现；沙箱外的测试仍使用隔离内存 API 和临时目录。
- 首次成组回归曾把时钟助手扩用到要求精确请求次数的错误恢复套件，观察点越过指定计数而失败；该扩用已撤回，保留该套件原有行为。中断的过程记录为 `full-clock-toast.log`，不计为完整回归通过。
- 移除全部临时探针、收窄助手范围后，完整 Edge 套件使用 4 workers 和 `retain-on-failure` 连续三轮 **978/978 通过**（5.6 分钟），记录为 `full-clock-toast-final.log`；前端 `check`、内嵌资源一致性和差异空白检查通过。此处仍是本机 Edge 证据，远端 Chromium 与后端全流程以新提交的 CI 结果为准。

时钟行为依据 [Playwright Clock 文档](https://playwright.dev/docs/clock)：快进只触发已经到期的定时器，不能替代等待异步响应后才注册的后续定时器。上述结论由本地失败 trace 和延迟对照确认；远端 trace 上传成功，但本机 Chrome 的下载接口超时，未声称已读取远端 trace。

## 2026-10-29：合回主线与正式构建验证

[PR #38](https://github.com/SW26010/still-wanna-dance/pull/38)整合主线 `f1cacec7` 的 14 个新提交后合并为 `b48abd0a`。本地 Edge **340/340**、跟踪目录 Go 测试、vet 与前端一致性通过；工具测试首次因子进程使用旧 Go 而失败，显式选择已安装的 Go 1.27.3 后 **46/46** 通过。完整历史检查一次补齐三个旧 WebUI bundle 的遗漏登记，各自字节与原提交构建清单一致；主线资源检查通过。

在 `b48abd0a` 上使用仅本地标签 `v0.0.0-ci-validation.20261029`，正式打包入口完整通过：干净检出的工具/Go 测试及 vet、完整对应源码、离线前端重建、全新缓存与包内 file GOPROXY 的独立 EXE 重建、实际 ZIP 便携包验证。输出复制后的大小和哈希再次核对通过。Portable 为 20,891,689 字节，SHA256 `ef91437eb69af250687af314d101ff91f4d8a43cf1e8db9ab04a47f93de9b41f`；对应源码为 299,831,638 字节，SHA256 `a1a1c4470a031f58f1eea79bff35bad5469fcacf1e554cc799c4bc7b40fdd60d`。未推送标签、发布 Release 或验证公共下载；本轮未指定前一 ZIP，不代表跨版本覆盖验收。

同一提交的 [`check` #18](https://github.com/SW26010/still-wanna-dance/actions/runs/40847203331)收齐两个独立失败：Windows 浏览器 **339 通过、1 失败**，后续步骤未执行；Ubuntu 浏览器、工具、普通 Go、vet 和 build 通过，race 阶段的真实进程重启测试等待首次启动超过 15 秒。此次修正集中在测试的完成条件与验证范围，产品和打包代码不变：

- **图片完成条件**：Logo 元素可见不表示图片已加载。仅给原资源响应增加 250ms 延迟，旧尺寸断言 **3/3 复现**；改为在默认 5 秒断言期限内等待非零 `naturalWidth` 后，同样延迟下文档套件三轮 **15/15 通过**。资源返回 404 的反例仍在原期限内失败；移除探针后，文档及品牌套件 **6/6 通过**。其它图片用例已等待解码，没有新增公共助手。
- **重启测试范围**：首次资源初始化已由 `TestProductionBootstrapSeedsMediaWithoutCatalogOrEngine` 验证，主线新增的万条种子导入不应与进程重启共用 15 秒转换期限。本地单独 race 用例通过；限制本次进程到两逻辑核、与真实初始化测试并行时接近期限，进一步收窄到一逻辑核后复现同样的首次启动超时。现先通过真实 Console 创建并关闭安装配置，再启动子进程验证原有重启流程。同一单核并行 race 对照连续两轮，两项测试全部通过；原有 15 秒期限、配置锁、地址复用、旧令牌拒绝、新令牌和退出断言均保留。未伪造种子状态、跳过初始化覆盖或增加生产配置。

修正后入口包完整 race 回归通过（15.619 秒），原始材料在忽略目录 `artifacts/main-release-20261029/`，包括完整本地回归、首次工具链失败、三个历史资源遗漏、正式构建日志和产物，以及 `logo-*`、`restart-race-*` 对照。所有临时图片探针均已移除；CPU 约束仅用于本次诊断进程并已恢复。本地 Windows race 对照不能替代 Linux 结果，修正后的主线检查以新提交运行记录为准。

## 2026-10-29：配置 v8 与曲库修复合并验证

远端 `2dfb5300` 的 [`check` #19](https://github.com/SW26010/still-wanna-dance/actions/runs/40851974272)已成功完成。将本地 `7782f131` 的十个提交合入时保留双方历史；六处冲突按配置 v8 字段适配远端测试修复，保留调度准入观察、保存完成等待和请求专属字段过滤。自动日志目录在当前 `settings` 中保持空串，不再沿用旧夹具的默认路径回填。产品源码与本地 `7782f131` 一致。

合并结果本机 Edge **345/345**、跟踪目录 Go 全包测试（`-tags=no_easytier -count=1`）、vet、build、前端类型及内嵌资源检查通过。工具测试首次 **45/46**，原因是子进程在 `GOTOOLCHAIN=local` 下选中 PATH 中的 Go 1.27.1；仅为本轮验证显式选择已安装的 Go 1.27.3 后 **46/46** 通过，未修改全局环境或产品代码。

完整历史资源检查发现 `9d9068db`、`e1eb1605` 和 `7782f131` 的三个 WebUI bundle 漏登。分别读取原 Git blob，核对字节数及 SHA256 与对应提交构建清单一致后追加精确例外；已有登记保留，检查 **0 errors**，既有大小提示保留。原始输出在忽略目录 `artifacts/merge-main-20261029/`，包括首次工具链失败、修正后工具测试和资源检查前后结果。本段记录本机合并检查，合并分支的远端平台结果以 PR 所列运行记录为准；未新增游戏实机结论。
