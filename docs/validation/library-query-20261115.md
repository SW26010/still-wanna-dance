# 曲库查询与分页验证（2026-11-15）

本轮处理“分页只限制返回数量，每次仍扫描目录、读取全部歌曲及资源并构造结果”的 P2。现行规则见[曲库读取与刷新](../webui-design.md#曲库读取与刷新)；本记录只证明以下固定数据和检查范围，不代表任意硬盘、超大库或游戏体验的延迟保证。

## 实现与兼容边界

- 保留独立只读 `ReadLibraryPage(ctx, paths, query)` interface、查询参数和返回结构。歌曲及资源的普通查询在 SQLite 中筛选、排序、计数及分页，再批量补齐可见页；共享资源详情只构造一次，关联歌曲摘要仍限 20 首且总数完整。
- 精确 ID／MD5 查询使用数据库字段条件，不再读取全库歌曲。文字搜索用确定性 SQLite 函数保留 Go Unicode 小写及字面子串规则，包括 `%`、`_`、反斜线、跨字段换行和原有 300 字符截断。子串搜索和未索引排序仍有数据库全候选成本。
- 动态需求分仍由 `effectiveSongPriority` 计算。只读取匹配候选的 ID、需求次数、累积分数和时间；资源视图汇总全部当前关联歌曲。歌曲的真实需求分组优先、双向排序、同分 ID 降序及未知大小末尾等规则不变。
- 运行中的 Console 通过引擎读取现有 `retained` 库存；发布、导入、删除及周期核对使用原机制更新，不新增持久化库存或结果缓存。每次仍复制库存大小映射，资源查询及本地状态筛选需要连接内的临时主键表；精确字段查询仅投影目标本地文件。临时表不写入媒体数据库，连接关闭即丢弃。
- “检查本地视频”通过受保护的 `POST /api/library/scan` 显式调用现有核对机制，再刷新页面，及时发现外部增删或大小变化；不执行清理、需求准入或下载。核对支持请求取消，失败保留原库存。普通查询和封面预取复用库存，外部变化等待显式或周期核对；可见文件的身份戳仍逐次检查。
- 独立只读调用没有运行期库存 owner，仍扫描所选目录以准确返回全目录数量和占用。未启动引擎时的 Console 走同一路径；缺失数据库不创建文件，目录故障和本地筛选拒绝规则保留。

## 基线与性能对照

Windows amd64，AMD Ryzen 7 9850X3D，Go 1.27.3。使用实际创建流程内嵌的 **10,471 首歌曲／10,446 个资源**，统一补简单名称及歌手；空目录或额外创建 10,000 个 5 字节普通文件。额外文件使用独立 MD5，因此资源视图包含 20,446 个候选，覆盖未知本地文件并集。没有读取视频正文或访问真实上游。

旧实现使用修改前相同 benchmark，`-benchtime=3x`；新实现使用 `-benchtime=10x`。这是同机短基准，受缓存、后台任务及采样次数影响，不能以单次差异推导稳定 SLA。MB 使用十进制字节。旧 Console 调用同一独立读取函数；新引擎列使用真实启动的引擎及其已建立库存，启动／首次核对不计入查询计时。

| 数据／查询 | 旧实现 ms／MB | 新独立读取 ms／MB | 新引擎库存读取 ms／MB |
| --- | --- | --- | --- |
| 空目录，默认 50 首 | 32.88／14.15 | 4.74／0.17 | — |
| 空目录，精确 `id:2367` | 34.13／7.15 | 3.18／0.10 | — |
| 10,000 文件，默认页 | 56.58／18.49 | 15.44／3.34 | 6.33／0.59 |
| 10,000 文件，第二页 | 66.33／18.49 | 15.80／3.34 | 7.21／0.59 |
| 10,000 文件，精确 `id:2367` | 75.62／11.50 | 17.14／3.27 | 5.12／0.51 |
| 10,000 文件，名称排序 | 78.30／18.49 | 24.68／3.35 | 9.25／0.59 |
| 10,000 文件，需求分排序 | 83.58／18.49 | 30.90／6.14 | 12.24／3.36 |
| 10,000 文件，资源默认页 | 75.87／19.64 | 49.91／6.50 | 54.10／3.73 |

空目录默认页的分配对象从约 **262,642** 降至 **2,712**；精确 ID 从约 **262,611** 降至 **865**。资源视图保留必要的库存投影／未知文件并集成本，短基准下库存路径与独立路径耗时有波动，不外推为所有资源查询都按同一比例提速。

## 验证与失败记录

- `TestLibraryPageAllocationBound` 在旧实现复现失败：默认页、单 ID 和资源反查均约 26 万个对象，超过每页 20,000 的宽松上限；新实现的默认页、名称排序、单 ID、资源默认页和资源 ID 查询通过。上限用于捕获重新物化全库的回归，不作为时间阈值。
- 临时新旧差分探针通过 **3,528 组**：两种视图、七种排序值、双向、六种筛选、七种查询及三个 offset，使用同一时刻比较完整结果与错误。覆盖共享映射、无映射歌曲、偏好、真实需求与初始分；探针完成后删除，避免长期保留双实现。
- 既有曲库、资源管理、独立存储及库存核对定向回归通过。新增回归覆盖 Unicode 和 SQL 通配符字面搜索、跨字段匹配，以及引擎库存复用、外部文件显式刷新、共享资源删除即时更新和取消。
- 第一版资源 SQL 保留不必要的关联和集合去重，在大本地目录下曾慢于旧实现；用查询计划确认后，改为临时主键库存和已知／未知资源的索引并集。探针中计数约 36 ms → 3.6 ms，首屏候选约 17 ms → 0.5 ms；上表记录修正后的端到端结果，不用局部计划耗时替代总耗时。
- 首轮 Console 配置夹具在沙箱内报 `renameat ... The handle is invalid`，Windows junction 夹具报 `Access is denied`；相同定向测试在普通用户权限下通过，没有修改产品逻辑绕过测试。
- Edge 曲库和封面套件 **79 项通过**，包含主动检查才发送扫描请求、视图切换不重复扫描、分页、搜索、偏好、删除确认、封面预取及布局回归。复核发现主动扫描失败后原实现仅显示错误通知、保留旧占用和已保存状态；新增目录掉线用例在旧实现失败，将页面刷新移至 `finally` 后通过：读取既有 `videoError` 结果、占用显示未知、已保存徽章清空、预览禁用。
- 真实 `TestLibraryBrowser` 通过：隔离 Console、SQLite、实际下载引擎和 Edge，受控外部 GET 完整发生在两次观察之间后自动显示已保存；共享资源只下载一次，主动库存检查返回成功，字段查询、偏好持久化、视图切换和删除通过。输出在忽略目录 `artifacts/library-query-real-console/`。未启动游戏、实际网关或修改 hosts，不新增 VRC 待验项。
- WebUI 重建及一致性检查通过，锁定依赖未变；生成包的精确产物登记已更新。
- `cacheproxy` 与 `console` 两个包的完整 Go 回归通过（分别 260.292 秒、223.301 秒），两包 `go vet` 通过。Windows 定向 race 的 `TestLibraryEngineReusesAndRefreshesInventory`、`TestRetentionReconciliationFailureDoesNotWaitForDecision`、`TestRemovalReservationOnlyBlocksSameVideo` 通过，无竞态报告；使用已有 GCC 16.2.0、`CGO_ENABLED=1`，未执行全包 race。
- `git diff --check` 及跟踪历史的仓库产物检查通过（保留原有大文件提醒）；历史检查不检查未提交工作区。当前 WebUI 则由重建一致性检查与新增精确哈希登记核对。未执行 push。

复跑入口（Windows 测试均使用仓库临时目录包装器；受本机沙箱文件操作限制时按上述失败记录在普通权限下运行）：

```powershell
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run '^TestLibrary' -count=1
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/cacheproxy -run '^$' -bench '^BenchmarkReadLibraryPage(Seeded|MaintainedInventory)$' -benchtime=10x -benchmem
./scripts/with-test-temp.ps1 pnpm --dir webui test library.spec.ts library-covers.spec.ts
$env:SWD_LIBRARY_BROWSER_EVIDENCE = Join-Path (Get-Location) 'artifacts/library-query-real-console'
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/console -run '^TestLibraryBrowser$' -count=1 -v
```
