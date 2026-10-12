# #16 同页条款确认：开发检查

日期：2026-10-12。基线 `d21c21c` 加未提交工作区修改，接续[评审修正](20261012-review-fixes.md)。用户反馈确认前后文档切换割裂；本次合并首次入口和完整向导。LA-029 / LA-030 继续待实机验收。

## 实现与边界

- `/` 与未确认时的 `/terms` 直接加载共同的 `index.html` / `setup.html`。入口只携带步骤和页面存活 ID，不带业务状态或同意凭据。
- 首次确认不调用文档跳转或整页刷新，也不通过 sessionStorage 交接访问圆点；后续步骤保留在同一文档内。直接 `/terms` 初次进入时用历史 API 规范地址，保证以后刷新仍恢复向导。
- 未确认时前端不轮询业务 API，后端对状态、进度、缓存和功能 API 继续返回 428；静态资源可加载。条款成功保存后才读取正式设置、建立草稿并开放后六步。读取暂失败保留已确认按钮，恢复连接后继续。
- 欢迎/条款自由往返不制造待保存草稿或离开提示。未确认时的退出、恢复 hosts、停止服务、关闭后的退出倒计时及返回取消机制保留。安全操作区在确认后仍挂载，避免条款正文尺寸变化。
- 首次诊断仍由服务端成功保存同意记录后触发；前端同页初始化不再触发一轮。只读的已确认 `/terms` 保留独立法律文本页。

## 自动回归

- `node --test scripts/*.test.mjs`：**187/187 通过**。覆盖同一正文节点与阅读位置保留、前两页往返不提交、原地确认/重试、另一窗口同意后读取、状态读取失败后恢复、进度保存失败、关闭/返回存活通知、安全操作、前端轮询门禁、直接 `/terms` 地址规范及原有草稿/回执/诊断回归。
- `go test ./internal/console -count=1`：**通过，34.009 秒**。新增首次共同模板与静态资源检查；业务读取仍被拦截，页面加载不创建通道密钥。保留 DPAPI、倒计时、失败拒绝、HMAC、回执与存储等回归。
- 首次沙箱执行聚焦 Go 测试遇到 Windows `Access is denied`，未计为通过；宿主机重跑完整 console 包通过。测试使用可丢弃临时配置。
- `git diff --check`：通过。

旧模板中的首次确认脚本测试移入共同向导测试；`scripts/terms.test.mjs` 现在仅验证已确认的独立条款页安全操作，不保留被删除的跳转行为断言。

## 浏览器文字观测

使用 `TestFirstEntryUsesViewportWizardAndBodyConsent` 经真实 ServeHTTP 导出的 HTML，替换测试令牌后由 `scripts/setup-ui-fixture.mjs` 的内存 API 提供。所有同意、诊断、存储和安全操作均为模拟；没有实际生成用户的 DPAPI 同意记录。

| ID | 操作 | 实际结果 |
| --- | --- | --- |
| SP-01 | 从导出的首次欢迎入口进入条款，滚动完整条款，再点击确认 | 确认前后 URL 均为 `/__fixture/welcome#/setup`，保持同一条款步骤。条款可视高度均为 104 CSS px，scrollTop 均为 1898.5；按钮原地显示禁用的「已确认条款」。欢迎圆点保持灰色，条款圆点变绿，下一步可用。 |
| SP-02 | 确认后点击下一步 | 正常进入「存储设置」，默认路径为夹具 Portable 内 `data/storage`，未开启手动指定时输入禁用。无配置应用、导入或重启。 |
| SP-03 | 在 1280×720、640×360、375×667 检查确认后的条款页 | 文档宽高均等于视口；底部导航与退出均在视口内；长条款仍在内部滚动。临时尺寸覆盖已恢复。 |
| SP-04 | 检查浏览器与动作日志 | 无脚本错误；本轮确认只有一次 `/api/terms/accept`，没有 `/api/setup/network` 二次诊断请求。前序为了加载修改后的脚本做了两次主动刷新，对应两条 `/api/terms/leave`；它们发生在确认之前，不是确认触发跳转。 |
| SP-05 | 更新预览后，旧页面发送过期令牌；随后检查拒绝路径与页面可达性 | 发现夹具在错误响应已写出头部后再次设置 Content-Type，导致模拟进程退出。修正公共 JSON 响应顺序和异常兜底后，过期令牌返回 403，未同意状态读取返回 428，随后首次页面仍返回 200；重新打开的浏览器标签页显示欢迎与可用的下一步。该修正只影响开发夹具。 |

复现：设置 `SWD_UI_TERMS_EXPORT_DIR` 为仓库内忽略目录 `artifacts/setup-first-entry`，运行 `go test ./internal/console -run TestFirstEntryUsesViewportWizardAndBodyConsent -count=1`；在同一环境设置 `SWD_UI_PORT=50416` 后运行 `node scripts/setup-ui-fixture.mjs`，访问 `/__fixture/welcome` 或 `/__fixture/terms`。未设置端口时使用动态端口，以终端的 `FIXTURE_URL` 为准。重启夹具清空模拟同意和任务。

## 被测文件

SHA256 使用 `hashBasis: working-tree-bytes`，不是发行构建或 Git blob 哈希；以下是本次开发检查代码。

| 文件 | SHA256 |
| --- | --- |
| `internal/console/terms.go` | `038c78e0a3a9994ac54d36ad60048fb449888b58a68f5545d3c8cbdcdd6ba0c3` |
| `internal/console/index.html` | `8402453f569bcc97324a364d9277a72989d3868e0650bd90f13aedba5de644b3` |
| `internal/console/setup.html` | `e4cd8930fcee609f77ae0fbe41eddf6468bdefbaee8217b313b2e7ed444eea23` |
| `internal/console/assets/setup.js` | `518f89669b48758e1355d4055d530fc127ac975c1123e1859ef8b4fbb4eefd35` |
| `internal/console/assets/navigation.js` | `5f5c234ee77009aa870a1f5e76167dfdf83bf8884f6e545f2179322df910b562` |
| `internal/console/assets/console.js` | `3d960e0a404619562e95472849607ebec6c31a95a9c4564f02c7eb471d7ac707` |
| `internal/console/assets/console.css` | `5464357e7fc479229a747d746bae9cb3a4105cdb86db5118cfa105b3cfdc67f4` |
| `scripts/setup-ui-fixture.mjs` | `90eb125ed7b83c25cdd0972c08ce4952d1c0d4c7cbee678a1d0f6e6ff294c3d1` |

未启动游戏、未执行真实 UAC、hosts、导入或出口测速，未操作系统 DPI 或真实浏览器缩放菜单。正式实机步骤与文本证据要求见 [LA-029 的 29.1/29.3/29.10 和 LA-030 的 30.2](../../issue-16-validation.md)。此前历史证据保留，不将本次夹具结果标为实机通过。没有 Git commit 或 push。
