# 游戏接入退出策略与权限助手验证（2026-10-21）

现行行为与安全边界以[hosts 契约](../console.md#hosts)为准；本记录说明本轮实现及验证范围，不替代真实 UAC、系统会话结束或断电验证。

后续补证： [2026-10-26 Windows 原生验收](windows-native-validation.md)已实际完成托盘启动、UAC 取消/同意、默认恢复策略的托盘与网页退出，且修复取消提示乱码。下方保留 2026-10-21 的历史实现与限制；其中引导和保存后重启策略已被现行契约替代，不能沿用为当前验收预期。

## 实现

- `restoreHostsOnExit` 默认开启，缺失/null 采用默认值；显式关闭后退出保留接入。沿用配置格式 5 和 saved/active 分离，保存后重启生效，旧进程按当前策略退出。首次引导保留该高级选项，不增加模式选择步骤。
- 两种策略共用按需创建、会话内复用的助手。接入、恢复或显式启用时先检查实际 hosts；默认策略复用已有托管映射时也准备退出恢复权限。自动启动服务与状态读取不触发授权，页面明确提示未准备恢复的旧映射，并保留可用的接入按钮。
- 退出、托盘清理及重启共用 `Console.Close`，只使用已有助手，不启动新的 UAC。默认恢复后停止网关；持久化保留映射。助手连接丢失、回执无法解析及条目被外部修改均不冒报恢复成功，不通过再次提权兜底。
- 复用已有 go-winio 依赖，实现随机命名管道、当前用户/管理员 ACL、拒绝远程连接和双方进程 ID 核验。协议仅允许固定操作，不接受文件路径、文件内容或任意命令。旧单次 `-hosts-action` 入口保留用于已有人工修复工具，日常控制台改用会话助手；文件转换、备份与精确标记恢复继续共用原实现。

## 已执行

| 检查 | 结果与实际覆盖 |
| --- | --- |
| 控制台与桌面程序入口整包测试 | `go test -tags=no_easytier ./internal/console ./cmd/still-wanna-dance-console -count=1` 通过；覆盖原有设置、引导、独立队列、退出和配置清理行为 |
| 新增助手与配置回归 | 同一助手反复接入/恢复、两种退出策略、空操作不提权、已有托管与用户映射区别、冲突先拒绝、注入 UAC 拒绝、待授权时退出取消、恢复失败保留原内容、丢失/异常回执后不复用旧流、显式网关启动准备权限、saved/active 与缺失/null/false 配置均通过 |
| Windows 真实进程和命名管道 | 非提权测试子进程运行生产会话循环，覆盖两种策略的正常关闭、主端连接断开、助手退出等待，以及错误父进程 ID / 非本机或无效管道名拒绝；只写临时 hosts 文件 |
| Windows race | 使用已有 w64devkit/GCC，`go test -race -tags=no_easytier ./internal/console -run 'Test(Hosts\|WindowsHosts\|RestoreHosts\|ExplicitGateway\|Activation\|OwnedLegacy)' -count=1` 通过；包括取消授权与退出并发、真实子进程及通信 |
| Edge 内存接口回归 | acceleration、settings-help、setup、copy、console、recovery 六组，共 39 项通过；含默认/持久化确认文案、旧映射补授权入口、设置草稿与保存、320/390px、引导和队列独立性 |
| Edge + 真实 Go Console | `TestHostsPolicyBrowser` 通过：页面主动接入私有 hosts 文件，保存关闭自动恢复并刷新，确认本次仍按 active 默认恢复；关闭并从同一磁盘配置新建 Console，确认持久化设置生效且退出保留映射。HTTP 未模拟，助手使用隔离文件会话；这不是桌面入口自动拉起或真实 UAC 验证 |
| 构建与静态检查 | 前端 build/check、内嵌资源一致性、Go vet、Windows 控制台构建和标准桌面脚本构建（`dev-hosts-session`）通过 |

浏览器截图已检查；最初退出截图截到了 Fluent 弹窗过渡，改为禁用截图动画后重新采集，最终对话框、背景遮罩及窄屏换行正常。没有因此修改产品动画。

审查后修正帮助页「连接恢复说明」遗留的“退出不会恢复”表述，明确停止网关保留接入、正常退出按当前生效设置处理，系统注销/关机/断电或强制终止不保证恢复。前端重新 build/check、差异检查及 settings-help/copy 两组 9 项浏览器回归通过。首次沙箱内 Edge 启动因 `TransactNamedPipe` 失败，尚未执行页面断言；保留 `review-copy-sandbox-failure.md` 后在沙箱外以普通用户运行相同隔离回归通过。

## 失败记录与边界

初次沙箱内定向测试出现已有的 `renameat ... The handle is invalid` 配置原子替换错误，以及命名管道 `Access is denied`。沿用仓库临时目录包装器，在普通 Windows 用户权限下执行同一组隔离测试后通过；没有修改产品文件写入或安全策略绕过沙箱。

本轮没有修改系统 hosts、启动游戏、请求真实 UAC 或更改用户设置。真实 UAC 同意/取消、使用另一管理员账户凭据、交互托盘、注销/关机及断电仍未验证，属于 Windows 环境边界，不新增 VRC 台账项。主端管道断开测试证明助手在仍存活时的清理行为，不等于系统同时终止两个进程或断电也能恢复。

本地日志、截图和隔离开发构建保存在忽略目录 `artifacts/hosts-session-validation/`：`go-targeted.txt`、`go-console.txt`、`go-race.txt`、`browser.txt`、`live-browser*.txt`、`live-browser/`、`build.txt`。运行路径和原始资料不入 Git。

## 授权成功后的退出设置提示（2026-10-27）

新助手授权成功并连接后发布公共 hosts 状态中的 `authorizationID`，网页使用现有状态读取显示信息 Toast，并链接到「退出时恢复游戏直连」字段。触发规则与文案以[内容约定](../webui-content.md)为准；没有改变 UAC、hosts 写入或退出策略。每次授权生成独立随机标识，标签页只保存该标识去重，刷新不重放已显示的提示。初版的进程身份与授权序号组合已被该标识替代，不再将鉴权 token 用于通知或写入浏览器存储；Toast 的正文、操作链接、关闭和超时统一由公共 `Notification` 呈现。

- 前端 `build` / `check` 和 `git diff --check` 通过，内嵌资源已重建。
- Go 定向回归 `Test(Hosts|RestoreHosts|ExplicitGateway|Activation|WindowsHosts)` 通过；验证新助手成功授权的记录、复用不重复、拒绝/冲突不报告成功及现有接入/退出行为。包含私有文件和非提权子进程的命名管道检查。
- Edge 四组定向回归共 24 项通过：`hosts-notice`、`settings-help`、`acceleration`、`settings-structure`。新增覆盖成功状态的后台通知、取消后重试成功、操作结果并存、刷新去重、新授权再次提示、320px 深色与 1280px 浅色、跨设置标签定位/焦点与未保存草稿保持；截图已检查。
- `TestHostsPolicyBrowser` 真实 Console 联调通过：HTTP 未模拟，私有 hosts 助手成功建立后展示 Toast，点击直达设置，保存、刷新、重新创建 Console 及两种退出策略均通过；390px 截图已检查。

首次沙箱内 Edge 因 `TransactNamedPipe` 失败而未进入页面断言，Go 回归出现 `renameat ... The handle is invalid` 及命名管道 `Access is denied`。停止本轮残留的测试服务器后，在普通用户环境执行相同隔离检查通过。失败摘要、浏览器原始失败、重试日志及联调截图保存在忽略目录 `artifacts/hosts-notice-validation/`。本轮未启动游戏、修改系统 hosts 或请求真实 UAC；隔离助手成功连接与浏览器联调不等同于新一轮原生 UAC 实测，原生授权路径沿用文首所引既有证据。

同日收敛复验：独立授权标识与公共通知复用完成后，前端 `build` / `check`、上述 Go 定向回归（含真实 Console 浏览器联调）及 24 项 Edge 回归再次通过。额外核对不同助手会话的授权标识不同、复用时保持不变，以及关闭 Toast 后状态轮询不重复展示；联调截图布局保持一致。记录位于 `artifacts/hosts-notice-validation/refinement/`，前次失败和验证记录保留。

同日 P2 修复：对 `sessionStorage` 属性访问、`getItem` 与 `setItem` 分别注入异常，修复前 3 项均失败并复现 WebUI 白屏。通知模块仅在存储读写处局部捕获异常，继续显示 Toast 和操作链接，保留当前页面内去重，仅放弃刷新去重。修复后 `hosts-notice` 全部 7 项 Edge 回归通过，覆盖页面存活、链接定位、轮询不重复、新授权再次提示及刷新降级，且无未捕获页面异常；前端 `build` / `check` 与差异检查通过。失败日志、白屏截图和成功日志位于 `artifacts/hosts-notice-validation/storage-failure/`。本次仅修改前端存储异常处理，沿用既有后端与隔离服务证据，未触及原生授权或系统 hosts。
