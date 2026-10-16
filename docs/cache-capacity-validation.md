# 缓存容量零值与独立不限选项：开发验证

日期：2026-10-16。实机状态：**待验收 LA-047**。

## 行为与实现

- 设置页与首次引导共用 `webui/src/cache-capacity.tsx`，只提供「不限制／限制为」。限制值允许 0，最多两位小数；切换选项保留数值。默认不限，容量输入预填 40 GiB。
- `cacheUnlimited` 独立表示不限；`maxCacheBytes` 表示字节预算。限制为 0 时复用现有引用、队列及交接保护，保护解除后回收正式视频；已有受管视频也按该预算处理。允许临时写盘，保留歌曲资料、统计及未知文件。不提供旧配置兼容或迁移逻辑。
- 保存和引导应用均携带两个字段，保存后仍需重启生效。引导确认页和缓存页正确区分零容量与不限。
- 零预算不接纳需要新增空间的补齐下载；已有受保护文件可复用。网页禁用补齐并解释原因，仍允许检查缺失视频和队列预缓存。
- CLI 显式 `-max-cache-bytes=0` 使用零预算，正数使用有限预算，`-cache-unlimited=true` 独立选择不限。两种显式选择互斥；不传参数仍不限。

主要代码：`internal/cacheproxy/{config,retention,cache,batch_budget,server}.go`、`internal/console/{console,setup_apply}.go`、`cmd/still-wanna-dance/main.go`、`webui/src/{cache-capacity,settings,setup,cache,home-library}.tsx`、`webui/src/types.ts`。嵌入前端与 `webui/build-manifest.json` 已重新生成。

## 自动检查

以下是开发检查，不代表真实游戏、托盘或跨进程重启场景通过。

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

## 下一轮实机验收

步骤、预期及证据要求见[LA-047](live-acceptance-backlog.md#零容量缓存与独立不限选项)。重点覆盖真实播放/拖动、完整队列及交接保护、退出与异常重启后的文件生命周期，以及保存后完整应用重启的生效边界。未启动本轮实机验收。
