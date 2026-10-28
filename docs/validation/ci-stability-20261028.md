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
