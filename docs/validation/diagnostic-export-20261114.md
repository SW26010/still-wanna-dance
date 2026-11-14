# 诊断包导出验证（2026-11-14）

现行规则见[日志说明](../logging.md#收集诊断信息)。本轮验证工作区源码与重建后的内嵌 WebUI；验证后按用户授权提交，未推送。

## 实现与范围

唯一入口位于使用帮助的「反馈问题」卡片。默认最近 24 小时，可选问题说明不阻塞下载；三类资料按时间收集为 ZIP，缺失和读取错误写入 manifest。不新增数据库或配置字段，不启动加速、游戏或改 hosts。

## 已通过

- `pnpm --dir webui build`、`pnpm --dir webui check`：类型检查和内嵌产物一致性通过；更新了 Fluent Textarea 引入的依赖许可声明及对应大文本产物登记。
- 通过临时目录包装器执行 Go 定向回归：`Test(ExportRequestHistory|History|RequestHistory|DiagnosticArchive|DiagnosticExport|DiagnosticTimestamp|Log|Text|Writer|Segment)`，包为 `internal/cacheproxy`、`internal/console`、`internal/applog`，使用 `-tags=no_easytier -count=1`。三个包通过；未开启已有历史浏览器专用环境变量，其专用用例仍跳过，不计为本轮浏览器覆盖。
- 数据库测试实际写入 604 条事件，导出区间内 602 条，验证超过页面上限仍完整、HEAD／失败／取消保留及两端时间边界。
- 打包测试覆盖跨午夜、VRC 多行堆栈归属、结束边界排除、SWD 文本及旧 JSON 时间格式、显式时区、数据库缺失时继续收集、取消；HTTP 测试从 Console 路由验证 Token、方法和参数拒绝、ZIP 可读取及不启动引擎。
- 设置 `SWD_DIAGNOSTIC_BROWSER_EVIDENCE` 后，`TestDiagnosticExportBrowser` 启动隔离真实 Console 和 Edge，无内存 API 替代。实际下载留空说明和填写说明的两份 ZIP，再由 Go 打开校验 VRC/SWD 内容与可选说明文件。验证默认区间恰为 24 小时、错误时间反馈、320px 无横向溢出、按钮可见、Escape 关闭及入口焦点恢复、无页面运行错误。最终产物再次联调通过。
- 人工查看工具生成的 320px 截图，字段、说明、错误和操作均完整可见。截图及合成资料 ZIP 位于忽略目录 `artifacts/diagnostic-export/`，不入库。
- `git diff --check` 通过；当前 `app.js` 按仓库产物策略分类无错误。

## 失败与边界

首次沙箱回归在既有 `testConsole` 创建条款夹具时发生 `renameat .terms.json.swd-tmp terms.json: The handle is invalid.`，新旧 Console 用例均未能完成初始化；数据库及纯打包测试通过。普通用户权限下复验消除该环境限制，定向回归及真实浏览器通过。未修改条款或文件重命名实现。

只读取本机已有 VRC 日志的时间前缀，核实 `yyyy.MM.dd HH:mm:ss` 格式，未复制私人内容到仓库。VRC 无偏移量时间仍按本机时区解释，历史时区变更无法恢复。未验证并发轮转时每个文件均可完整取得；实现按打开时长度读取，轮转导致打开失败会登记，不能承诺跨来源原子快照。未运行游戏、辅助技术或完整发布预检；本功能没有需要新增的游戏专属待验项。

## 复跑

### 提交前评审修正

统一 logging.md 与 webui-design.md 中遗留的目录入口及“仅展示路径”描述。取消按钮卸载后，仅当焦点落回正文且下载按钮可见时恢复焦点；用户主动聚焦其他控件时不抢焦点。真实 Edge 检查键盘取消后返回下载按钮、请求结束时保留已移到常规设置链接的焦点，通过。额外焦点场景使用受控 HTTP 响应；原有两份 ZIP 仍经真实 Console 下载验证。

首次“不抢焦点”用例在移除并重挂请求拦截后未取得受控错误响应，实际请求完成了真实 ZIP 下载。改为单次安装拦截并等待各处理函数明确到达后释放响应，两个焦点用例通过；不通过固定等待掩盖竞态。证据目录 `artifacts/diagnostic-export-focus/`。前端重建、一致性检查与 diff 空白检查通过。

### 后续界面调整：卡片内表单

再次按用户反馈整理布局与文案：首行左侧开始／结束时间，右侧“最近 X 小时”；问题说明独占下一行，标签不显示时区文字，不显示快捷范围解释提示。用途说明与分享提醒各保留一处。真实 Edge 联调增加宽屏左右坐标检查，通过下载及 320px 回归，并检查 `artifacts/diagnostic-export-layout/form-desktop.png`。

按用户反馈移除弹窗，表单直接置于「反馈问题」卡片。复用共用 NumberInput（Fluent SpinButton）增加“最近 X 小时”，默认 24；快捷范围在导出时刷新到当前时间，手动修改起止时间清空快捷值并使用自定义范围。问题说明仍可留空。

重新通过前端 build/check 及真实 Console／Edge 联调，覆盖默认 24、输入 2 小时同步时间范围、递增按钮到 3、两种说明状态的 ZIP 下载、自定义时间及无效小时数校验、无弹窗和 320px 无横向溢出。截图与 ZIP 在 `artifacts/diagnostic-export-inline/`，已检查窄屏截图。上文 Escape／焦点恢复仅描述被替代的弹窗版本。

```powershell
$env:SWD_DIAGNOSTIC_BROWSER_EVIDENCE = Join-Path (Get-Location) 'artifacts/diagnostic-export'
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestDiagnostic(Archive|Export|Timestamp)' -count=1 -v
```
