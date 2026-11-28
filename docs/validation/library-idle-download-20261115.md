# 曲库空闲后的下载观察验证（2026-11-15）

本轮修复曲库首次读取没有任务后，无法发现其他入口启动下载的问题。现行规则见[曲库读取与刷新](../webui-design.md#曲库读取与刷新)；本记录替代 2026-11-12 验证中“无任务停止下载读取”的规则，曲库列表与覆盖率仍不做定时扫描。

## 复现与原因

原复现记录 `artifacts/architecture-review-20261115/library-idle-probe-final.log` 来自前序检出：推进 80 秒、公共状态成功刷新 17 次，下载与曲库各只读取 1 次，行状态始终为“未保存”；手动检查后立即变为“已保存”。这是显示生命周期问题，不是下载失败。

本轮先运行生产嵌入前端的 Edge 回归：

```powershell
./scripts/with-test-temp.ps1 pnpm --dir webui test library-download-completion.spec.ts --grep 'idle library' --workers=1
```

歌曲和资源两个视图均失败：期望“下载中”，实际仍是“未保存”。失败输出保留于本轮忽略目录 `artifacts/library-idle-before.log`。最小场景只有首次空闲读取、外部任务开始和结束，不触发批量任务、设置保存或手动刷新。

原因是空任务快照把下载读取间隔设为 0，而完成刷新又依赖此前见过的任务 ID。公共状态正常更新不能唤醒它；单纯恢复活动任务轮询也不能发现两次读取之间已经结束的短任务。

## 修复边界

本节及“恢复时连续相同错误”记录前两轮实现；其中健康状态下重建基线不补读曲库的策略，已由下文“首次成功建立基线”补充替代。

- `useDownloadObservation` 复用 `useResource` 的单请求、取消、错误保留及可见性控制，每 5 秒读取下载快照，空闲和首次失败后仍继续唤醒。空闲时没有任务标签查询。
- 引擎在现有共享任务登记／移除的锁范围内各推进一次 `revision`，快照在同一锁下读取版本与任务。进度、重复读和共用同一任务的调用者不推进版本；成功、失败与取消都经过原清理出口。没有新增配置、数据库字段或下载执行路径。
- 曲库通过观察 hook 的同一变化回调刷新列表和本地占用，移除页面自己的任务 ID 比较；版本不变时不重读曲库。未见过活动快照的短任务也能触发刷新。
- 首次下载读取失败后，恢复成功会核对曲库；已有成功基线则继续比较版本。离页、保存后的版本变化及标签隐藏会重建基线，复用页面原有恢复读取。

## 检查结果

| 检查 | 结果与范围 |
| --- | --- |
| 生产前端与受控 Edge API | `library-download-completion`、`library-refresh`、`library-refresh-retry`、`library-background-refresh` 共 39 项通过，20.6 秒；`artifacts/library-idle-final-browser-v2.log` |
| 新增生命周期场景 | 空闲后外部开始／完成，歌曲和资源视图、两次读取间完成、首次及后续 503 恢复、隐藏与离页暂停／返回均通过；没有任务变化时曲库读取次数不增加，返回时只读一次 |
| 相邻页面行为 | 其他下载仍活动时及时刷新、批量准备和任务空档、下载进度不重复刷新、导入与覆盖率、后台刷新焦点及 320／390／1280px 布局继续通过 |
| Go 下载与接口回归 | `TestActive*`、`TestDownloads*` 通过；包括共享任务只登记一次、成功／重定向／失败／关闭、并发读取、两次快照间完整任务、关闭不等待生命周期 I/O；`artifacts/library-idle-real-console.log` |
| Windows 定向 race | `TestActiveSharedLifecycle`、`TestActiveRevisionRetainsUnobservedFlight`、`TestActiveSnapshotDoesNotJoinClose` 通过，63.255 秒，无竞态报告；使用已有 w64devkit GCC、`CGO_ENABLED=1`，`artifacts/library-idle-race.log` |
| 真实 Console／引擎／Edge | `TestLibraryBrowser` 在首轮嵌入前端上通过，9.538 秒；`artifacts/library-idle-real-console-final.log` |
| 构建与静态检查 | 前端 build／check、两包 Go vet 通过；前端依赖和锁文件未变；内嵌资源与构建清单同步更新 |

真实联调使用隔离 SQLite、临时视频目录和受控 HTTP 上游。浏览器暂停轮询后，从外部 HTTP 客户端对同一引擎发起实际 GET，收到完整正文并核对下载快照从 `revision:0,tasks:[]` 变为 `revision:2,tasks:[]`；此时页面仍显示未保存。恢复观察后，两首共用同一资源的歌曲自动显示已保存，没有点击本地检查或批量下载。随后搜索、收藏重载与删除继续通过，上游资源只下载一次，页面脚本错误为 0。

首轮真实浏览器结果及已检查截图在 `artifacts/library-idle-real-console-final/`；这些含临时路径的原始材料不入库。当时 `app.js` 的 Git 文本字节 SHA-256 为 `21e02673bd7785acf16760a10ed6ff661014e17b6bbb81efd710a350d6a99e20`，1,322,441 字节；评审补充后的最终产物见下节。

## 评审补充：恢复时连续相同错误

评审指出观察作用域变化和标签隐藏会清空恢复标记，而连续相同错误不改变 effect 依赖。新增三个场景分别经过返回曲库、恢复标签和操作后刷新：下载读取始终返回相同的 503，确认恢复后又失败至少两次，随后让短任务完成并恢复成功响应。补充修复前，三项均在“期望已保存、实际未保存”处失败，保留 `artifacts/library-idle-resume-before.log`。

两个基线重置入口改为在已有读取错误时保留 `null`（成功后需核对），健康状态仍使用 `undefined`（由恢复时的页面读取建立基线）。标签可见性监听随错误变化更新，并在恢复时也核对标记，覆盖隐藏期间收到失败响应的时序；不依赖错误文本变化来重新设置标记。

修复后四份曲库刷新用例共 **42 项通过，19.7 秒**；三个新增场景均断言第一次恢复成功时补读一次曲库，之后版本不变不再重复读取，健康恢复的单次读取断言保持不变。输出为 `artifacts/library-idle-resume-after.log`。前端 build／check 和产物登记检查通过；后端源码未在此次补充中修改，复用上表 Go、vet 与 race 证据。

更新后的生产前端通过 `TestLibraryBrowser` 真实 Console／Edge 联调（8.143 秒），结果位于 `artifacts/library-idle-resume-real-console.log` 和 `artifacts/library-idle-resume-real-console/`。最终 `app.js` 为 1,322,455 字节，Git 文本字节 SHA-256 为 `81f7eb51ac04b41cefd6847929f51f012d7184c322fa8d6790334776b4718fb7`，已登记该精确生成产物例外。连续相同错误的时序由受控浏览器夹具验证；真实服务检查继续证明外部短 GET 的最终状态能自动同步，不把它描述为后端错误注入。

## 评审补充：首次成功建立基线

曲库与下载快照独立读取，仍有此前未覆盖的窗口：曲库先取得未保存，短任务随后开始并完成，首次下载快照才返回新版本号和空列表。原实现只记录首次成功基线，之后版本不再变化，曲库便一直保留旧状态。返回页面、恢复标签或操作后刷新也会重建基线，存在同类交错。

先在歌曲和资源两个视图中阻塞首次下载快照，让曲库完成未保存读取，再完成短任务并释放快照。原生产前端两项均失败，轮询继续但行状态始终未保存。命令为 `./scripts/with-test-temp.ps1 pnpm --dir webui test library-download-completion.spec.ts --grep 'before first observation' --workers=1`；有效失败保留在 `artifacts/library-first-observation-before-unsandboxed.log`。沙箱内首次运行未进入断言，Edge 启动失败后清理未结束，已中断，输出保留在 `artifacts/library-first-observation-before.log`；后续浏览器均以普通用户权限运行。

观察 hook 现在对每个新基线的首次成功快照也调用同一曲库刷新回调。未建立基线只需 `undefined`，不再区分健康与错误重置；错误不建立基线，已有成功版本仍跨普通读取失败保留。首次加载和恢复时会在原页面读取后再核对一次；建立基线后的相同版本继续不刷新，没有新增定时曲库扫描或后端状态。

验证结果：

- 新增交错覆盖歌曲／资源视图及首次进入、返回页面、恢复标签、操作后刷新，共 8 项。每项检查自动已保存、删除可用及占用同步，随后继续轮询 31 秒但曲库不再读取。
- 五份曲库浏览器用例共 **101 项通过（1.1 分钟）**，包括错误恢复、其他任务仍活动、导入、封面及布局。输出为 `artifacts/library-first-observation-regression-final.log`。中间一次为 44 通过／6 失败，保留 `artifacts/library-first-observation-regression.log`：旧断言未计入基线核对的额外一次读取，且新增资源返回场景丢失视图参数；调整后仍精确断言读取次数，返回使用原查询，封面缓存发布由夹具显式控制。
- `TestLibraryBrowser` 在真实 Console／SQLite／下载引擎／Edge 上分别通过 `between-reads`（8.11 秒）和 `before-first-observation`（3.39 秒）。后者先读出未保存并阻塞浏览器首次下载请求，再执行实际外部 GET，核对完整正文及服务端 `revision:2,tasks:[]`，释放首次请求后两首共享歌曲自动已保存。两组均只回源一次，偏好持久化、搜索和删除继续通过，脚本错误为 0；输出位于 `artifacts/library-first-observation-real-console.log` 及同名目录的两个子目录。
- 前端 build／check、产物清单与 Git 差异格式检查通过。最终 `app.js` 为 1,322,355 字节，Git 文本字节 SHA-256 为 `430d3c0fd3a355b9d812f25cfbb5a8759fbd647d15bb0c75764cda3aee47e2fa`，已登记精确产物例外。生产后端逻辑与依赖未变，复用前两轮后端及 race 证据。

后续评审指出，上述 `between-reads` 批次仅等待曲库显示，首次下载快照较慢时也可能落入首次基线窗口，因此不据该批次严格区分两种时序。联调脚本现在先让初始曲库读取结束，再释放浏览器首次下载请求，确认其 `revision:0,tasks:[]` 响应及随后基线核对的曲库响应完成，最后才冻结时钟并发起外部 GET；`before-first-observation` 则仍将首次请求阻塞到 GET 完成之后。两组结果显式记录 `baselineReconciledBeforeDownload`。真实 Console／Edge 复验均通过，分别为 9.01／3.27 秒；日志与结果保留在 `artifacts/library-observation-baseline-barrier.log` 及同名目录，生产前端产物未变。

本补充替代前两轮“健康恢复只读取一次曲库”的策略与断言，历史通过次数和失败记录保留；没有新增 VRC 专属验证缺口。

## 保留的中间失败与限制

首次沙箱浏览器在启动阶段失败且清理未结束，已中断；该次夹具退出后，普通用户权限下的同一回归取得上述有效失败和修复结果。中间补充检查发现标签及页面恢复时，原有页面读取与生命周期通知会重复读取曲库（期望增加 1 次，实际增加 2 次）；保留 `artifacts/library-idle-final-browser.log`，重建观察基线后最终 39 项通过，没有放宽读取次数断言。

本轮是隔离服务与受控浏览器验证，没有启动 VRC、修改系统 hosts，或替换用户正在运行的程序；不对实际游戏音画作结论，没有新增 VRC 专属待验项。观察为 5 秒一次的采样，短于采样周期的下载中间态可能落在两次采样之间；生命周期版本确保其结束后仍能核对最终本地状态。
