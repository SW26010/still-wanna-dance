# #16 安全与真实入口修正：开发检查

日期：2026-10-12。基线 `d21c21c` 加未提交工作区修改，接续[页面反馈检查](20261012-layout-feedback.md)。本记录为代码回归与浏览器夹具检查，不是实机验收报告；LA-029 / LA-030 保持待验收，密钥场景联动 LA-016 / LA-017。

## 变更与自动检查

| ID | 评审问题与修正 | 回归依据与实际结果 |
| --- | --- | --- |
| REV-01 | Settings 裸 SHA-256 可用于离线猜测密码。改为持久 `channel.key` 的 HMAC-SHA256，使用独立用途前缀，保留密码、不可解密凭据及其余配置的变更检测；公开状态、应用响应、回执查询隐藏内部 targetVersion。 | `TestSetupSettingsVersionIsKeyedStableAndCredentialSensitive` 验证同密钥跨 Console 实例稳定，密码/其他配置/不可解密密文/密钥变化使版本改变，且版本不等于旧裸摘要。`TestSetupVersionKeyFailureNeverFallsBackOrResumes` 验证坏密钥原文件保留，状态报错，应用与续接失败，不启动导入。 |
| REV-02 | 真实首次入口使用独立旧模板。对 `/` 与 `/terms` 采用固定视口、正文确认按钮、三态圆点及独立退出；确认后仅交接前两页访问标记，记录门禁不依赖该标记。 | `TestFirstEntryUsesViewportWizardAndBodyConsent` 从真实 ServeHTTP 读取两入口，检查模板及按钮位置；Node 条款/向导测试检查交接、一次性消费及不可授权后六页。浏览器结果见下表。 |
| REV-03 | 共享回执变化导致原应用 ID 丢失。新增只读 POST `/api/setup/receipt`，按原 ID 查询已有回执；读取失败保留 ID，明确不存在也要用户主动退出才清除。 | `TestSetupReceiptLookupPreservesOlderApplicationAndHidesLegacyVerifier` 验证旧 ID 可读、共享文件与当前回执不变、旧裸摘要不外泄且不触发重启。前端覆盖共享回执被替换、读失败退出后重入、404、过期响应及丢失应用响应，均不重新提交应用。 |
| REV-04 | 不存在的目标后缀掩盖 junction 祖先。`CheckFutureDir` 遍历所有已有祖先，拒绝重解析点或非目录，预检不创建目标。 | `TestSetupMissingStorageRejectsExternalJunctionAncestor` 使用测试临时目录内的 junction 与两级不存在后缀；存储检查、向导检查、离页验证及最终应用均拒绝，配置与目标保持不变。实际使用的外置盘与权限组合仍待实机验收。 |

执行结果：

- `node --test scripts/*.test.mjs`：**185/185 通过**。
- `go test ./internal/console ./internal/cacheproxy ./internal/workfiles ./internal/desktop -count=1`：cacheproxy、workfiles、desktop 通过；console 初次仅因旧测试依赖 HTML 属性相邻顺序失败。改为检查欢迎/条款区的可见性语义后，完整 `go test ./internal/console -count=1` 通过（33.605 秒）。
- 最后补齐首次确认的访问标记交接后，重跑全部 Node 测试仍为 185/185；`go test ./internal/console -run 'TestFirstEntry|TestTerms|TestSetup' -count=1` 通过（1.275 秒）。
- `git diff --check` 通过。原有重启匹配、凭据变化、导入幂等与门禁回归继续覆盖。

## 浏览器操作与观测

来源：Codex 内置浏览器的 DOM 尺寸/属性、可访问性树、表单操作和内存夹具动作日志。首次入口 HTML 由上述 Go 测试通过真实 ServeHTTP 导出至忽略目录，浏览器通过夹具加载模板；模板中的令牌被替换，所有 API 仍为内存模拟。

| ID | 操作 | 实际观测 |
| --- | --- | --- |
| UI-R01 | 在 1280×720、1024×600、800×450、640×360、375×667 五种 CSS 视口检查真实首次模板的欢迎、条款两页 | 共 10 组；每组文档尺寸等于视口，底部导航和退出均在视口内；内容区最小高度 225。正文内部可滚动，条款确认按钮属于正文表单，不在页脚。 |
| UI-R02 | 欢迎 → 条款 → 点击正文确认，进入完整向导 | 模拟确认后转入完整向导条款页，按钮显示禁用的“已确认条款”；欢迎圆点灰色，条款圆点绿色。未生成真实 DPAPI 记录。 |
| UI-R03 | 窗口 A 应用无导入清单，窗口 B 应用带模拟导入来源的清单，再刷新 A | A 恢复原无导入结果，B 保持导入已启动结果。夹具日志为两次用户确认对应的 `/api/setup/apply`，随后一次只读 `/api/setup/receipt`；未重放 A 的应用或导入。 |
| UI-R04 | 检查以上操作期间的浏览器错误 | 未观察到脚本错误。 |

首次入口复现方式（PowerShell，仓库根目录）：

```powershell
$env:SWD_UI_TERMS_EXPORT_DIR = Join-Path (Get-Location) 'artifacts/setup-first-entry'
go test ./internal/console -run TestFirstEntryUsesViewportWizardAndBodyConsent -count=1
node scripts/setup-ui-fixture.mjs
```

默认端口 `50416`；`/__fixture/welcome`、`/__fixture/terms` 分别提供真实入口模板，`/#/setup` 提供完整向导。夹具会明确显示“真实入口模板，操作仅修改内存”。重启清空所有模拟同意、回执和任务状态；这不是运行实际应用。

## 被测代码哈希

`hashBasis: working-tree-bytes`，SHA256 基于本地实际文件字节；不是 Git blob、发行包或实机构建哈希。正式验收仍需冻结构建和证据清单。

| 文件 | SHA256 |
| --- | --- |
| `internal/console/setup_apply.go` | `c3e84f6fb0ee59ed9185860aaf2a4ddd23d6e403482bf18bdd56cf51aa6ed188` |
| `internal/console/console.go` | `92897dcaadbc0f64188b89d93690823fdf220084437ff17cf0d3343d6b99d55e` |
| `internal/console/terms.go` | `20f7e4806a3bf2772f92e7f7eb70627fd6ea9215b36a95224c5af796f19977f9` |
| `internal/console/assets/setup.js` | `a73f0e38b64d972fb71cdcfb18a61fc85077d9c5f4c1063a0ccf5411c91d9be8` |
| `internal/console/assets/console.css` | `5c877fad0256796b1ee1db6eb006d10b27f7d3f75a6bc4fa03c4285f78d13392` |
| `internal/cacheproxy/storage_inspect.go` | `8e5b2643493ca062a53f3352eb2bfccb5419fc6a0a16c9a7ad2cabdecd727d37` |
| `internal/workfiles/files.go` | `0021172bf7e37b91d4834cf5d389232abca4d4dfe9d1a1876e27f020f9cc6f1a` |
| `scripts/setup-ui-fixture.mjs` | `506546b770d818a8de35a5747ea71436817e67a1c3511b631824a10e10816fb2` |

未启动游戏、未执行真实 UAC/hosts 修改或视频导入、未验证实际网络出口。未操作真实浏览器缩放菜单或系统 DPI，CSS 视口检查不能替代这些场景。测试仅使用可丢弃临时配置/密钥与内存 API；不保存代理凭据、条款记录、原始私密日志或图片二进制。旧裸摘要续接或密钥更换后的待续接清单会失败并要求重新核对，不自动迁移或导入。
