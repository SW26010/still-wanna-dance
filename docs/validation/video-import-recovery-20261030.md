# 视频导入恢复验证（2026-10-30）

现行行为见[视频导入与恢复](../video-import.md)。本轮验证当前未提交工作区的实现；未启动 VRC、修改系统 hosts 或操作用户已有视频。

## 范围与结果

- `internal/cacheproxy`、`internal/console`、`internal/desktop`、`internal/workfiles` 四包完整测试通过；定向检查覆盖固定清单、文件身份、任务恢复、配置工作槽与管理入口校验。使用仓库 `scripts/with-test-temp.ps1` 和 `no_easytier` 构建标签。
- 真实 Console 重建验证：重启只加载任务状态，不启动引擎或扫描；清单新增文件不纳入，移动、删除、修改、同内容同时间戳但身份不同的替换文件均跳过。成功回执可在下一次重启后确认清理；普通启动不删除源文件。
- 固定原子工作槽占用验证：发布后保存失败，后续文件不处理，最后可靠记录保持原样。重新启动后已存在目标按跳过处理，不补发清理回执；清理不会删除该源文件。
- 隔离子进程在暂存校验、已发布但未保存回执、源文件已删除但未记账三处被强杀。重启不自动工作；继续后正确清理暂存与处理原清单，缺失源文件不计为本程序删除成功。
- 真实跨卷检查：在 D 盘新建隔离来源目录，C 盘为目标目录；复制了部分内容后强杀子进程，检查暂存长度大于零且小于原文件，重启后重新处理成功。再次重启并确认后成功清理源文件，目标为独立文件。已有跨卷发布、目标保留元数据和回收检查同时通过。新建目录已清理。
- 浏览器夹具 29 项通过：新增暂停／继续／放弃交互、跨库拒绝及关联任务错误、归属、路径选择；覆盖 390 和 1280 像素宽度。
- 浏览器连接隔离真实 Console 验证通过：首次重建后点击继续，完成导入；再次重建后勾选并确认清理，再结束批次并刷新。记录实际 POST 操作，确认页面加载、刷新不会自动继续或删除。390 像素截图已人工检查，长目标路径完整换行。
- 前端 TypeScript、资源构建与构建清单校验通过；受影响四包 `go vet` 通过。桌面入口以 `no_easytier` 标签构建成功，开发二进制保存在忽略目录 `artifacts/import-recovery/SWD.exe`，未启动或替换用户现有程序。

原始输出与浏览器截图保存在忽略目录 `artifacts/import-recovery/`：`go-initial.log`、`go-recovery.log`、`go-packages.log`、`go-final.log`、`cross-volume.log`、`browser.log`、`browser-final.log` 和 `browser/`。这些运行材料含本地测试路径，不入库。

## 本轮失败与修正

1. 首次沙箱内测试遇到硬链接 `Access is denied`、原子替换 `The handle is invalid`，并使配置清理子进程失败；按仓库既有 Windows 验证约定转沙箱外运行同类隔离测试，没有修改产品代码绕过文件系统错误。沙箱外定向和四包完整回归通过。
2. 沙箱外测试发现新增身份检查持有源文件句柄，使 Windows 删除报“文件正在被另一个进程使用”。在最终路径身份复查前关闭句柄，保持内容、身份及取消检查；原失败日志保留，修复后正常清理、替换拒绝、目标缺失保留和跨卷清理检查均通过。
3. 首轮窄屏截图中长目标路径被裁切；复用现有 `.wrap` 样式，缩短恢复说明，并重新运行真实服务浏览器验证、检查截图。

## 验证边界

上述强杀证明进程中断恢复，不代表真实断电、硬盘损坏或物理磁盘耗尽已经验证；保存失败使用真实原子工作槽占用复现。未运行本轮 Linux 或 Windows race 检查。源文件变化检测以文件身份、大小和修改时间为依据，内容校验仍用于导入和清理；不承诺发现刻意伪造身份及属性的外部修改。

发布成功但回执未保存的那一项保留源文件、不补发清理回执，是已确认的功能取舍。没有新增需要 VRC 才能判断的场景。

## 同日补充：导入前空间预估

空间预估只读取来源与本次目标目录的卷信息。Windows 使用打开的目录句柄查询卷序列号，不按盘符比较；Linux/macOS 使用文件系统设备号。检测不保证单个文件的硬链接权限或子目录卷边界，最终处理仍由原有导入决定；本轮没有实测网络共享或 Linux/macOS。

- Windows 只读检测、目录不存在／文件路径返回待确认、当前生效目标与已有批次目标、Token／Origin／条款准入及方法边界通过。真实同卷和跨卷目录联接通过：表面同为 C 盘路径，联接实际指向 D 盘时正确返回跨卷；D 盘只读，联接在工作区临时目录创建并移除。
- 浏览器夹具 19 项通过，覆盖空间提示、导入暂停／继续和路径选择。320／1280 像素下未选择、检测中、同卷、跨卷、待确认和重置均不改变导入按钮的相对位置；旧请求迟到不会覆盖新来源，检测失败不禁用导入。悬停、键盘聚焦及 Escape 关闭 Tooltip 通过。
- 最终提示紧跟“外部目录”标签，320／1280 像素均在同一行，去掉独立提示行；保留输入框名称，点击提示不打开选择器，Shift+Tab 可从输入框进入提示。同行调整后复核路径／导入相关 19 项，并重跑真实 Console 选择流程。键盘用例曾在点击已聚焦提示后重复调用 `focus()`，没有新聚焦事件而失败；改为真实 Shift+Tab 进入后通过，未修改组件行为。输出见 `browser-inline.log`、`browser-inline-fixed.log`、`real-browser-inline.log`。
- 浏览器连接真实隔离 Console：选择来源后得到同卷预估、键盘提示及恢复默认通过，没有创建导入任务或启动引擎；已有批次重启后显示预估并继续、清理、结束通过。已查看 320／390／1280 像素截图。
- `go test -tags=no_easytier ./internal/cacheproxy ./internal/console -run 'Test.*Import|TestManagement' -count=1`、受影响两包 `go vet`、前端 TypeScript、资源重建及清单校验通过。真实浏览器检查通过对应测试的环境变量启用。

首轮沙箱内遇到目录联接拒绝访问、既有原子保存 `The handle is invalid`；浏览器首轮启动阶段失败后未正常结束，已中止并转沙箱外验证。测试自身先后修正了对空任务的解引用，以及将页面既有 VRCX 检测误计为导入操作的断言，未修改产品行为绕过失败。相关失败与最终输出保存在忽略目录 `artifacts/import-space/` 的 `go-initial.log`、`go-native.log`、`go-native-fixed.log`、`go-final.log`、`browser-native.log`，联调证据在 `browser/`、`recovery-browser/`，夹具截图在 `artifacts/webui-tests/`。未新增 VRC 待验项。

## 2026-11-15：移除目标卷的硬链接前提

旧实现有两处依赖：源到暂存只对跨卷错误回退复制，暂存到正式目标则无条件再次硬链接。使用真实导入流程、只替换硬链接系统调用的回归 `TestImportWithoutHardLinks`，同卷注入 `ERROR_NOT_SUPPORTED` 时在暂存失败，跨卷注入 `ERROR_NOT_SAME_DEVICE` 后复制成功但在发布失败。仅扩大暂存回退条件后，两项仍在发布失败；改用禁止覆盖的重命名后通过，另补 `ERROR_INVALID_FUNCTION` 用例。该过程证明不能仅增加复制兜底而保留原发布方式。

Windows 采用不含替换／跨卷复制标志的 [MoveFileExW](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-movefileexw)，空间预估通过打开的目录句柄读取 [FILE_SUPPORTS_HARD_LINKS](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-getvolumeinformationbyhandlew)。Linux 使用 `renameat2(RENAME_NOREPLACE)`，不支持该原语时保留原硬链接发布兼容路径。现行范围以[视频导入规则](../video-import.md)为准。

- Windows 定向回归通过：不支持硬链接的两种错误、跨卷复制路径、独立副本清理、访问拒绝与磁盘满错误保留、取消后暂存清理、导入途中出现的同名目标不覆盖且不发回执；原生发布同时覆盖已有文件、目录及超过 260 字符的路径。
- 原硬链接优化保持：5 MiB 视频导入和清理的实际进程读取量各为 5 MiB。真实两卷 NTFS 复制、目标保留元数据、清理后内容保持、立即容量淘汰和跨卷目录联接识别通过；测试创建的外部临时目录已清理。
- 两包关联回归通过：`./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy ./internal/console -run 'Test.*Import|TestPublishImportedVideo|TestTemporaryMediaAfterProcessTermination' -count=1`，覆盖固定清单、暂停恢复、成功回执和进程中断清理。未设置环境变量的可选浏览器／跨卷检查仍跳过；真实跨卷两项随后单独启用验证。
- `webui/e2e/import-space.spec.ts` 的 3 项 Edge 检查通过，包括 320／1280px 下同卷需要复制的空间提示、布局稳定和旧请求不覆盖新选择。前端构建、TypeScript 与构建清单检查、两包 `go vet`、Linux 测试二进制交叉编译通过。

本轮初次真实硬链接检查在沙箱中收到 `Access is denied`；Edge 也在启动时退出（`TransactNamedPipe`）。按开发约定转沙箱外后通过，没有将访问拒绝加入 Windows 复制兜底。浏览器失败资料保留于忽略目录 `artifacts/webui-tests/`，通过截图使用单独的 `artifacts/import-hardlinks-browser/`。尝试 macOS 交叉编译时遇到仓库已有的 `lockDirectory` 缺失，本轮没有扩展平台支持。

验证边界：本机可用卷均为 NTFS，未实际挂载 exFAT/FAT 或网络共享；不支持硬链接的系统错误为受控注入，复制、校验、发布、回执清理使用真实实现。Windows 卷能力查询的支持路径和浏览器的复制提示已验证，无硬链接卷的真实查询结果尚未实测。Linux 只完成编译，未运行其内核／文件系统路径。未启动 VRC、修改 hosts 或操作用户视频，不新增 VRC 待验项。

同日合并 `codex/workspace-3`（`a4091757`）到 `main`（`4f4c8fb1`）时，仅生成的 `app.js` 与构建清单冲突，已从合并源码重建并登记产物。上述两包导入关联回归、两包 `go vet`、前端构建与一致性检查，以及导入空间／历史统计的 5 项 Edge 检查通过。首轮沙箱仍出现硬链接／目录联接 `Access is denied` 和原子改名 `The handle is invalid`；相同 Go 回归在普通用户权限下通过，输出保存在忽略目录 `artifacts/merge-workspace-3-20261115/import-native.log`。可选真实浏览器、跨卷检查未在本次合并时重新启用，沿用上文证据及边界。
