# 设置编辑与存储边界重构验证（2026-11-08）

本记录对应 `83302e33` 之后的工作区改动，按后端设置编辑器、前端设置控制器、Store／Server 边界的顺序实施。现行行为以[配置生命周期](../settings-lifecycle.md)和[下载需求与资源管理设计](../download-resource-design.md)为准；没有配置或数据库格式升级。

## 实施边界

- `settingsEditor` 独立管理保存、导入草稿、资源位置及路径选择，正常应用与恢复窗口组合使用；恢复窗口不再构造部分初始化的 Console。正常应用仍提供共享事务锁，并原子发布已保存配置与立即生效字段。
- WebUI 控制器统一管理草稿、保存和异步状态。位置检查／数据库准备及代理导入拥有各自的操作入口；表单通过注册的校验／聚焦适配器参与保存。保留正常页分分类保存、恢复页统一保存及各自的主题生效规则。
- Server 显式持有 Store，数据库操作归 Store；Store 通过同步绑定通知资源决策变化，不持有 Server 指针或操作引擎锁。事务提交、通知与回收／发布决策保留原互斥范围；偏好写入失败在解锁前记录，映射写入失败由外层在解锁后记录，曲库读取到资源保护登记保留原映射锁范围。
- 大量测试文件的修改是显式成员路径与夹具归属调整。引擎和 Store 的关闭由各自持有者负责，测试不再通过 Store 的内部 Server 指针代为关闭引擎。

## 验证

所有 Go test、vet、build 均经 `scripts/go-patches.mjs`，使用 `no_easytier`；Windows 测试经 `scripts/with-test-temp.ps1`。原始日志及浏览器证据位于忽略目录 `artifacts/`，不入库。

| 检查 | 结果与证据 |
| --- | --- |
| Go 全量业务及入口测试 | `./internal/... ./cmd/... -count=1` 全部通过；`architecture-go-full.log`，cacheproxy 177.409 秒、Console 179.659 秒 |
| 设置相关浏览器回归 | 92 项通过；`settings-editor-browser.log`，包含分类、校验、位置、存储故障、下载、日志及保留设置 |
| 最终字段及位置回归 | 44 项通过；`architecture-final-browser.log`，覆盖校验聚焦、目录确认、超时、迟到响应及保存基线变化 |
| Windows 定向 race | cacheproxy、Console 均通过，无竞态报告；`architecture-race.log`，分别用时 185.396 秒和 26.390 秒。覆盖 Store 生命周期、引擎借用、提交到通知的排他性、偏好写入失败的记录顺序、映射／偏好与回收／发布互斥、恢复退出取消、字段省略语义及主题保存 |
| 真实 Console 双页面保存 | 通过；`architecture-settings-live.log` 中 `TestSettingsSeparateBrowsers`，覆盖旧快照部分更新、无关字段合并及过期目录策略确认拒绝 |
| 真实字段校验与存储修复 | `TestSettingsValidationBrowser`、`TestStorageRepairBrowser` 的确认／故障分支通过；同上日志 |
| 真实恢复页共享操作 | 修复后 `TestRecoverySharedSettingsBrowser` 通过；`architecture-recovery-after.log`，包含新建数据库、Mihomo 导入、授权取消及重试、全部分类保存、跨分类错误聚焦、主题和退出 |
| 真实 Mihomo 配置导入 | `TestMihomoConfigurationBrowser` 通过；`architecture-mihomo-browser.log`，文件、URL、Base64、自动检查、加密保存和待重启边界 |
| 前端构建及一致性 | `pnpm --dir webui build`、`check` 通过；`architecture-webui-check.log`；内嵌资源 2,529,789 字节 |
| Go 静态检查及桌面入口构建 | vet `./internal/... ./cmd/...` 和桌面入口 build 通过；`architecture-vet.log`、`architecture-build.log` |

上述重构验证时 `app.js` 的 SHA-256 为 `ed0ef9664f20e48b8cf5cb692adceac4ee6c0c5e03e2041cad09a8d5405e12d0`，1,285,283 字节；已按精确哈希登记仓库产物策略。后续草稿保留修复的产物见下文。

定向 race 使用 `CGO_ENABLED=1` 和本机 w64devkit GCC，测试选择表达式为 `Test(PreferenceCommit|PreferenceFailure|RetentionDecision|PublicationDecision|MappingWrite|PreferenceWrite|Store|EngineBorrows|RecoveryExit|RecoveryShutdown|SettingsRequestOmission|ThemeAutosave)`；未运行正式发行要求的全包 race 分组。`git diff --check` 和修改产物的仓库策略检查通过。

## 保留的失败及处理

1. Windows 沙箱内原子文件替换报 `renameat: The handle is invalid`；隔离目录入口不能消除此限制，沙箱外基线验证正常。后续通过同一隔离目录入口在沙箱外执行，没有修改产品文件写入逻辑。
2. 设置测试夹具原先直接修改 Console 的路径、身份和当前生效配置；拆分后需同时初始化编辑器的对应基线。保留原断言并更新夹具，相关定向测试及最终全量均通过。日志：`settings-module-tests.log`、`settings-module-full.log`、`architecture-settings-live.log`。
3. 恢复页真实联调发现数据库准备提示同时出现在存储和下载分类。将提示限制在存储分类，按原表单顺序进行聚焦；保留失败日志 `architecture-settings-live.log`，成功复验见 `architecture-recovery-after.log`。
4. Store 整包回归发现可选观测标签查询遗漏无用量数据时的空状态检查；补回检查且置于超时上下文创建之前。旧测试只嫁接 usage 的夹具也同步改为完整 Store。保留 `store-module-full.log`、`store-module-full-after.log`、`store-module-targeted.log`；修复后定向回归和最终全量通过。

## 评审收尾：恢复页读取失败保留草稿

评审通过隔离 Edge 对照发现：保存失败后的配置读取再失败时，重构前保留 `debug` 草稿，重构后恢复成已保存的 `info`。原始对照保留在 `artifacts/review-recovery-draft-20261108.json`。原因是读取失败清空数据后卸载 `RecoveryEditor`，丢失其控制器内的草稿。

恢复页启用现有 `useResource` 的失败保留数据选项，让编辑器持续挂载；错误仍显示，旧快照不解除编辑和保存禁用。未增加第二份草稿状态或改变通用读取行为。新增 `recovery-settings.spec.ts` 覆盖正常首次读取与首次读取失败两种路径，均检查保存失败、随后读取失败时的草稿和禁用状态、读取恢复及成功重试保存。

修复前两项新增测试均在编辑器消失处失败（`recovery-draft-before.log`）；修复后连同相邻字段校验共 11 项通过（`recovery-draft-after.log`）。前端 build、check 通过（`recovery-draft-build.log`、`recovery-draft-check.log`）。这是浏览器与受控 API 的状态生命周期回归，不新增后端持久化或实机验证结论，既有真实服务证据仍按原范围引用。

修复后 `app.js` SHA-256 为 `f81caa6fd757b4a3c914e02c252d0a6ff32a6f0afbe042972a9ff4426dc9dffa`，1,285,286 字节；内嵌资源总计 2,529,792 字节，已登记精确产物例外。

## 范围限制

浏览器使用真实 Edge、真实 HTTP 保存和隔离配置／数据库；路径选择结果由隔离目录替代，授权取消及重试使用私有 hosts 夹具。已检查恢复页 320px 和存储分类截图。未修改系统 hosts、启动 VRC 或进行真实 UAC 交互，不把夹具结果解释为游戏或系统提权验收；本次没有新增必须依赖 VRC 的待验项。此次是开发验证，不是正式发行的完整冻结预检。
