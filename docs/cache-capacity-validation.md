# 视频存储预算零值与独立不限选项：开发验证

2026-10-19 起按[术语与命名](download-resource-design.md#术语与命名)使用“视频存储预算”“本地视频”“房间队列预加载”。下方既有验证结果保留当时的名称和环境；本轮术语检查见末节。

初次验证：2026-10-16；默认值于 2026-10-18 调整为零预算。实机状态：**待验收 LA-047**。

## 行为与实现

- 设置页与首次引导共用 `webui/src/cache-capacity.tsx`，只提供「不限制／限制为」。限制值允许 0，最多两位小数；切换选项保留数值。全新配置与引导「恢复本页默认」均限制为 0 GiB，已保存的明确容量选择保留。
- `cacheUnlimited` 独立表示不限；`maxCacheBytes` 表示字节预算。限制为 0 时复用现有引用、队列及交接保护，保护解除后回收正式视频；已有受管视频也按该预算处理。允许临时写盘，保留歌曲资料、统计及未知文件。不提供旧配置兼容或迁移逻辑。
- 保存和引导应用均携带两个字段，保存后仍需重启生效。引导确认页和本地视频页正确区分零预算与不限。
- 零预算不接纳需要新增空间的补齐下载；已有受保护文件可复用。网页禁用补齐并解释原因，仍允许检查缺失视频和队列预加载。
- CLI 显式 `-max-cache-bytes=0` 使用零预算，正数使用有限预算，`-cache-unlimited=true` 独立选择不限。两种显式选择互斥；不传参数使用零预算。

主要代码：`internal/cacheproxy/{config,retention,cache,batch_budget,server}.go`、`internal/console/{console,setup_apply}.go`、`cmd/still-wanna-dance/main.go`、`webui/src/{cache-capacity,settings,setup,cache,home-library}.tsx`、`webui/src/types.ts`。嵌入前端与 `webui/build-manifest.json` 已重新生成。

## 自动检查

以下为 2026-10-16 的开发检查，当时默认不限、容量输入预填 40 GiB；不代表真实游戏、托盘或跨进程重启场景通过。

| 命令 / 范围 | 结果与覆盖 |
| --- | --- |
| `go test -tags=no_easytier ./internal/cacheproxy ./internal/console ./cmd/still-wanna-dance -count=1 -timeout=180s` | 三包通过。新增覆盖零预算冷播完整响应、现有文件启动回收、未知文件保留、多引用保护、队列预取后两次播放仅下载一次、交接计时到期后自动回收、零预算补齐拒绝新资源且允许现有资源复用；设置与引导保存 0/不限并保持 active 配置；已有正数淘汰、补齐与保护回归通过。 |
| 在 `webui/` 执行 `pnpm build` | TypeScript 检查和嵌入资源构建通过。 |
| 在 `webui/` 执行 `node node_modules/@playwright/test/cli.js test e2e/cache-capacity.spec.ts e2e/setup.spec.ts e2e/console.spec.ts e2e/recovery.spec.ts e2e/copy.spec.ts --workers=4` | 27 项通过，含新增 4 项：保存 0/不限、刷新与放弃草稿、正数草稿、active 保持、引导加载零值/确认/提交、两入口非法输入、零预算展示与补齐门禁。包含窄屏、导航、草稿生命周期及条款回归。 |
| `node scripts/check-webui.mjs` | 源码与嵌入产物一致。 |
| `node --test scripts/check-webui.test.mjs` | 2 项产物校验工具测试通过。 |
| `go vet -tags=no_easytier ./internal/cacheproxy ./internal/console ./cmd/still-wanna-dance` | 通过。 |

首轮沙箱内 Go 测试被临时视频发布和配置文件原子重命名的权限限制阻断，工具测试被临时目录链接权限限制阻断；均在正常权限下重跑通过。首次沙箱浏览器运行未成功完成并已中止，之后使用无界面 Edge 与本地模拟 API 完成上述 27 项。自动测试不接入游戏，不修改 hosts。

已查看自动测试生成的 390 像素宽首次引导存储页与深色设置页截图：两个入口使用相同选项、容量标签及说明，没有横向溢出。截图及测试日志仅在忽略目录 `artifacts/` 中用于开发核对，不作为实机验收证据。

## 2026-10-18 默认零预算

桌面新配置、命令行默认配置及引导「恢复本页默认」统一为 `cacheUnlimited=false`、`maxCacheBytes=0`。已有配置的明确值继续保留；缓存复用测试和实网冷/热缓存验收脚本显式选择不限，保持原场景的保留条件。

首轮 `go test -tags=no_easytier ./internal/cacheproxy ./internal/console ./cmd/still-wanna-dance -count=1 -timeout=180s` 中，缓存引擎与命令行包通过；控制台的覆盖率重算、导入清理、共享资源扫描和扫描快照 4 项因隐含依赖默认保留视频而失败。已在这些夹具中显式启用不限，继续验证其原有业务目的；控制台整包按相同参数重跑通过（58.553 秒）。默认配置与零预算回收仍由首次启动持久化和零容量测试覆盖，已有 0/正数/不限保存与重新加载回归通过。

`pnpm --dir webui build`、`node scripts/check-webui.mjs` 与相关三包的 `go vet -tags=no_easytier` 通过；`node --check scripts/acceptance.mjs` 通过，未执行公网验收脚本。Edge 的 `e2e/cache-capacity.spec.ts` 和 `e2e/setup.spec.ts` 共 9 项通过，覆盖恢复零值、保存与刷新、已有不限设置、非法输入和引导提交。首次沙箱运行因 Edge 临时配置替换失败而未进入页面测试，正常权限重跑通过。浏览器使用内存 API，不代表真实进程重启或游戏体验。

## 下一轮实机验收

游戏专属步骤见[LA-047](live-acceptance-backlog.md#零容量缓存与独立不限选项)：只补真实播放器重连/拖动与队列交接之间的保护风险，并复用 LA-040/041 的操作。设置/CLI、保存重启、精确回收和异常进程清理由 agent 在开发环节验证；已有上表结果按范围复用，不重复塞进游戏轮次。分流不表示未覆盖部分通过；本次未启动游戏验收。

## 2026-10-19 存储术语统一

设置和引导共用“视频存储预算（GiB）”，说明以“本地视频持久化存储预算”开头；完整视频列表、目录选择、删除/预算提示、队列预加载、命中统计、帮助、README、命令行说明和现行设计同步采用新术语。HIT 仅称完整本地视频命中，运行期块复用仍按原请求分类记录。配置键、接口路径、日志事件名、文件布局和预算行为保持不变，历史基准与验证证据保留原文。条款仅统一相关用词，版本更新为 `2026-10-19.1`，原版本/摘要门禁会要求重新确认。

验证结果：

- `pnpm --dir webui build`、`pnpm --dir webui check` 通过，嵌入资源和构建清单已更新。
- 经 `scripts/with-test-temp.ps1` 运行 Edge 内存夹具全套 45 项通过；两个预算入口均检查标签和全称说明，含零值/不限、草稿/保存、导航、删除、帮助、320/390px 与深浅主题。已查看引导存储、设置和本地视频页截图，文案换行正常，无横向溢出。英文错误夹具同步后，存储故障定向 1 项再次通过。
- 经同一入口运行 Go 存储、预算、配置、引导和条款定向回归通过；最终英文错误提示同步后，`internal/cacheproxy`、`internal/legal`、`cmd/still-wanna-dance` 整包通过（42.031 / 0.097 / 1.756 秒）。错误链及原存储行为未变。
- 首轮沙箱内 Edge 在启动阶段因进程/命名管道限制失败，Go 配置原子替换报 `renameat ... The handle is invalid` 等错误；随后在普通权限环境用同一隔离入口通过，不修改产品逻辑绕过环境限制。浏览器首轮错误上下文保留于忽略目录 `artifacts/storage-terminology-browser-sandbox/`，成功日志为 `artifacts/storage-terminology-{browser,go,go-final,browser-final}.log`。

本轮只验证术语与既有行为回归，未启动游戏、修改 hosts 或操作用户库；未新增 VRC 待验项。
