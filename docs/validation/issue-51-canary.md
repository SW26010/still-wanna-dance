# Issue #51 桌面 canary 接入验证

2026-11-17，基于 `75c093f1` 的实验与实机证据、`2502b1bb` 的工程取舍，完成 `issue51_canary` 桌面接入。本页保留当时构建、命令及验证结果。后续默认产品规则见[正式播放需求与 GET 入口](../download-resource-design.md#正式播放需求与-get-入口)。

## 实现与边界

Console 将已有 VRChat 日志目录交给资源引擎，canary 启动即监控，关闭引擎时一并停止。没有新增环境变量、用户设置或界面开关。模型固定 0 秒余量，非 owner 有条件入口 504；已确认 owner 的有效媒体流提前发送合法 200／206，随后仍按原截止断流。未识别角色不推断为 owner。

歌曲、角色只读取模型实际接受的事件状态，避免过旧日志让适配器与模型各自保留不同身份。目录关联按资源版本和大小匹配，临时块就绪读取现有注册表；只保留最后一次获准正式视频响应的实际交付进度。模型拒绝发生在新 GET 需求登记之前，原有交接不因拒绝续期。2／10 秒寿命复用原交接机制，不更改来源时钟、范围筛选和下载准入。

## 自动检查

全部经 `scripts/with-test-temp.ps1` 使用隔离临时目录，输出保存在忽略目录 `artifacts/issue51-canary/`。

| 检查 | 结果与覆盖 |
| --- | --- |
| canary 接入首轮 | `targeted-first.log` 通过；真实引擎交付首块后取消，未就绪接续收到一次 504，随后 206 正常供数；真实 HTTP owner 提前响应头；日志追加、截断及关闭；分段交接 |
| canary 扩展回归 | `targeted-final.log` 通过；缓存、日志及模型包，包括 owner 超时中止 206 且正文不混入错误、角色未知／过旧、元数据不匹配、预览／浏览器、条件请求、Range、原截止及交接回归 |
| 普通构建回归 | `ordinary.log` 通过；相同缓存核心范围未启用 canary 时的 HTTP、等待截止、取消、交接和播放时钟行为 |
| Console 回归 | `console-native.log` 通过；播放、队列、预取及引擎相关测试，保留原配置语义 |
| 并发检查 | `race-native.log` 通过；canary、播放相关缓存测试及日志／模型边界，无数据竞争报告；缓存包耗时 146.923 秒 |
| 静态与产物检查 | `vet.log` 通过；前端嵌入资源一致性与 Git 差异格式检查通过。没有前端变更 |
| 版本及日志追加验证 | `logging-canary.log`、`logging-ordinary.log` 通过；canary 强制 Debug（包括已保存 `off`），普通构建保留级别过滤；零总量预算保留所有轮转片并跨重启续写 |
| 实际 EXE 日志检查 | `logging-exe-result.json`、`logging-exe-check.log` 通过；隔离配置为 `off`，预置 21 MiB 日志，实际 EXE 连续两次在占用的回环端口处结束，均输出 Debug 并逐字节保留旧片，配置保持原样；没有进入业务服务 |

首次 Console 沙箱运行在既有 `terms.json` 原子替换处报 `renameat: The handle is invalid`；首次 race 的缓存包报 `ThreadSanitizer: failed to restore address`，日志分别保留在 `console.log`、`race.log`，不计为通过。按[开发约定](../development.md)使用同一隔离入口在普通用户权限下复验，没有修改文件操作或放宽断言。并发复验记录在 `race-native.log`。

## 历史构建与执行记录

桌面产物为 `bin/SWD-canary.exe`，内置显示版本 `v1.3.0 canary`，Windows amd64、Go 1.27.3、开发构建。源基线为 `2502b1bbc4a783a94d3081c78af780a6cdf543a7`，另含当时未提交的 canary 实现与测试，VCS 身份为 dirty。通过 `-build-info` 和 `go version -m` 核对，实际标签包含 `no_easytier,issue51_canary`。

11 月 17 日交付的 EXE SHA256 为 `44df00d6c772ca14e8464f8b8465b51924c73c58eff00847de2d733113cbc8cc`，构建输出和身份为 `build-v130.log`、`build-info-v130.json`。首轮 `dev-issue51-canary` 的 SHA256 为 `E8320F2B7CC6E4106B247D08C81B4CF5BB96D87FA43DDD0B6668BC1C731E9C67`，保留 `build.log`、`build-info.json`；该轮尚未强制 Debug 或取消保留上限。

下列命令记录当时的操作。复现需要同时恢复对应的未提交源码、构建标签实现及 `TestCanary` 用例。当前工作区运行该正则筛选会匹配零项测试。现行产品的验证见[正式播放与 GET 入口](block-scheduler-validation.md#2026-11-18-正式播放与-get-入口)。

```powershell
node scripts/check-webui.mjs
node --input-type=module -e "import {desktopBuildFlags,goEnv,desktopEntry} from './scripts/release-source.mjs'; import {runGo} from './scripts/go-patches.mjs'; const flags=desktopBuildFlags({version:'dev-issue51-canary'}).map(v=>v.startsWith('-tags=')?v+',issue51_canary':v); runGo(['build',...flags,'-buildvcs=true','-o','bin/SWD-canary.exe',desktopEntry],process.cwd(),goEnv());"
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test '-tags=no_easytier,issue51_canary' ./internal/cacheproxy -run '^TestCanary' -count=1
```

未启动 VRC 或修改 hosts。11 月 17 日构建时，真实日志到达顺序、自动／手动恢复及恢复后旧请求成本没有本轮实机结果；历史 owner 自动回零和过早刷新失败保持有效，不将代码接入称作已解决。相关游戏剩余范围并入既有 [LA-040](../live-acceptance-backlog.md#la-040慢网拖动多播放器与后台竞争)，不另列逐改动验收矩阵。
