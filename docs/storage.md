# 统一存储

> 当前采用[曲库、资源关联与歌曲需求统计](song-catalog-design.md)模型。主库为 `storage.sqlite`，SQLite `application_id=0x53574443`（SWDC）、`user_version=2`。这是发布前冻结格式（版本 1 为试验格式），不实现旧格式兼容或迁移。

MD5 文件身份和下载保护仍遵循 [MD5 视频资源管理](md5-resource-design.md)。无版本旧配置及旧数据库请保留备份，使用独立的新配置和存储目录；不要通过更名或修改版本号复用。

```text
data/storage/
  videos/{md5}.mp4
  tmp/download-*.part
  tmp/download-publish-*.part
  tmp/import-*/video
  storage.sqlite
  .lock
```

本地视频唯一身份是小写 MD5。歌曲 ID、URL 中的资源编号、版本、目录、线路和大小均不参与文件键。同 MD5 只保存一份文件，共享下载、容量记账和淘汰；多个歌曲 ID 可以指向同一个 MD5。不另建文件名映射表。

下载时计算一次 MD5，完整接收并校验长度和 MD5 后发布正式文件。后续假设文件不被外部修改，普通播放和扫描不再计算 MD5，也不依据属性变化重新校验。活动响应共用已打开的文件句柄、各自独立读取位置，最后一个使用者结束后关闭句柄。

歌曲清单是 ID → MD5 的权威来源：新增和更新均写入，清单中消失的旧 ID 不删除。只有 ID 请求可以由上游解析结果补充缺失关联，纯 URL 请求不猜测歌曲 ID。已有映射与播放 URL 不一致时只记录日志，不回滚清单映射。

ID 播放命中本地时立即返回，同时后台对照上游 URL 的 MD5；后台检查不替换文件、不打断播放。缺少映射或文件时解析 URL 并冷加载。URL 播放直接取查询参数 e 的 MD5 查本地，缺失时用该 URL 冷加载。

独立的 CheckReferencedFiles 函数把映射中的 MD5 去重，一次枚举目录后返回存在集合和缺失 MD5 及其歌曲 ID。忽略孤儿资源，不检查视频大小或内容。上层使用快照，不重复逐项检查。引擎的 CheckReferences 包装函数在任务期间保护引用资源，任务结束释放；每次新任务重新检查，不缓存跨任务结果。外部文件删除仍可能导致实际播放打开失败。

| 表 | 内容 |
| --- | --- |
| songs | 正整数歌曲 ID、可空标量资料及三个 JSON 数组列 |
| song_media | 歌曲 ID → 唯一当前 MD5，外键关联歌曲和资源 |
| media | MD5、可空字节数和来源路径，不作为本地文件存在性依据 |
| song_usage | 明确歌曲 ID 的累计有效需求、指数分数及最后计分时间 |
| catalog_state | 已接受的上游版本、规范化摘要、来源及接受时间 |
| request_events | 请求结果、耗时和字节数 |
| media_access | 最近 GET 时间，独立于歌曲热度和请求明细保留期 |
| song_urls | 按 API/节点保存播放 URL 证据及纳秒查询顺序 |
| song_playback | 完成传输次数及去重水位 |
| playback_latency | 按缓存分类累计首字节耗时 |
| catalog_checks | 最近清单验证时间与诊断 |
| catalog_members | 当前完整清单成员 |
| traffic_totals | 命中、未命中与节省字节累计 |

清单沿用现有固定 MD5 接口；解析后在同一事务中写歌曲资料、资源、当前映射和时间水位。响应时间按年月日时分秒解析，旧候选不覆盖新候选；同时间同内容不重复写，同时间不同内容报告冲突。仅 Kiva 与 WannaInfo 完整 MD5 清单参与同步；来源是溯源信息，不改变共用的内容身份。获取失败时保留已接受的本地清单。

补齐每次获取清单并更新映射，只为缺失 MD5 解析和下载。同内容多歌曲只安排一个任务；映射没变但文件已被淘汰仍会补齐。解析结果与目标 MD5 不一致时报告失败，不把其他内容当作完成。

资源分数是当前关联歌曲分数之和，孤儿资源为零分。映射改变不立即删除旧文件；容量超限时按分数淘汰，活动请求、下载和队列保护保持有效。淘汰只删除文件，保留关联和统计。文件发布与数据库提交不是跨资源原子事务，已发布的 MD5 文件即使没有元数据也可命中。

存储目录写入由 `.lock` 独占。初始化先取得目录锁，复核数据库身份及版本，在同一事务中建表、建索引和写入格式标记，成功后再清理临时下载并启动业务。离线删除也先取得目录锁，再检查数据库。备份请退出程序后保存整个目录。

缓存列表和最近请求只展示当前歌曲关联，不保存历史归属。列表最近访问保持原语义：长期保存的最后 GET 时间与保留期内 HTTP 请求明细取较新值；清理明细不丢失最后 GET 时间或歌曲需求累计值。

## 数据库打开约定

各类文件的备份、重建、损坏处理和安全回收条件见[运行文件生命周期](runtime-lifecycle.md)。

引擎启动和离线删除在取得 `.lock` 后，先检查已知旧存储标记：根目录存在 `stepstash.sqlite`（不论内容、文件类型，或是否同时存在 `storage.sqlite`），或 `videos` 中存在旧 64 位十六进制组合键文件名，均拒绝继续并提示使用独立新目录。检查旧库标记只看目录项，不打开旧库；拒绝发生在创建主库、清理下载临时文件和容量淘汰之前。除获取锁可能创建或保留 `.lock` 外，不改动数据库、恢复文件、视频或临时下载。此规则不承诺识别任意历史目录，也不包括从外部向运行中目录添加旧文件；使用中的存储目录不应由其他程序改写。

所有数据库连接通过同一入口建立，路径在一个位置定义。缓存列表、曲库、扫描目标、最近请求及流量统计使用只读连接，统一验证应用身份与格式版本；缺库时不创建文件，读取页面不补表或补索引。

只为不存在且没有既有 videos 目录的数据库初始化；已有空文件也拒绝。当前身份、当前版本的库仅验证完整表、列、索引和约束，不补建结构。不识别的身份和不支持的历史版本报错；遇到未来版本，明确提示更新程序。失败不会改写格式标记或清理临时下载。当前没有迁移链；今后的真实升级需要在这个受控入口中明确实现，不能散落在业务读取函数中。

不再生成或依赖 `verification.sqlite`。下载大小和 MD5 校验、显式完整检查、活动文件句柄和并发保护保持有效，检查结果仅在进程内用于本次操作。盘上碰巧存在的旧校验库被忽略，不自动删除。

## Portable 文件契约与生命周期

本节是应用落盘位置和备份范围的统一清单。路径由 `internal/apppaths` 解析；软件版本变化不改变文件名。当前配置格式为 `schemaVersion=3`，媒体 SQLite 为上述版本 2。旧测试配置版本 1、2 不兼容、不迁移，不应手工改版本号继续使用。

### 交付与按需生成

```text
SWD/                              # ZIP 内固定目录，不含版本号
  SWD.exe                         # 产品显示名仍为 Still Wanna Dance
  README.txt                      # 暂保留，取消依赖 #16 内置帮助及 #26
  build-info.json                 # 构建溯源，交付简化由 #26 决定
  licenses/
    LICENSE
    TERMS.txt
    THIRD-PARTY-NOTICES.txt
  data/                           # 不随 ZIP 交付
    config.json                   # 首次成功打开控制台时写入
    config.lock                   # 启动获取，退出释放，文件可保留
    logs/app.log                  # 启动创建，最多另有 .1 / .2 / .3
    terms.json                    # 用户确认当前条款后才写入
    channel.key                   # 使用 SOCKS5 通道身份时才生成
    state/                        # 相关操作首次保存结果时才创建
      throughput.json
      inventory.json
      batch.json
      attempt.json
    storage/                      # 媒体引擎首次使用时创建，允许外置
      storage.sqlite
      videos/{md5}.mp4
      tmp/
      .lock
```

首次打开未确认条款的应用只生成 `config.json`、`config.lock` 和 `logs/app.log`。路径解析和保存设置不预建 `state/` 或 `storage/`；保存目录只在已有祖先目录做临时写入探测。只读页面不建媒体库，显式扫描在需要清单数据库时才初始化引擎。空库存扫描不必创建媒体目录；发现已观察到视频的根目录消失，或现存非空根缺少 videos 时，保留旧库存并报错。系统运行身份和安全清理分别按 #24、#25 已收口，交付文件进一步精简仍由 #26 负责。

### 配置实例与路径解析

库存扫描与批量扫描共用使用证据判断。遇到缺失的媒体根时，先检查当前引擎、恢复的库存（视频数、字节数或已知清单覆盖）、成功批量结果及已结束的尝试记录。有使用证据就诊断为失联库，在启动引擎前拒绝，不创建空库、不更新任务或库存快照；只有没有这些证据的新路径才允许初始化。单纯对未初始化路径做过零库存扫描，不视为已经建立媒体库。

令 `C` 为主配置的绝对路径，`B` 为其父目录，`R` 为实例数据根，`S` 为媒体根：

- 默认 `C=<EXE目录>/data/config.json`，`R=B`，默认媒体设置为相对值 `storage`。
- 自定义 `-config <目录>/config.json` 同样以该目录为 R；其他文件名采用 `R=C.d`。例如 `profiles/a.json` 的条款、状态、日志和默认媒体都放在 `profiles/a.json.d/`，媒体设置为 `a.json.d/storage`。
- 同目录 `config.json`、`a.json`、`b.json` 的锁、日志、密钥、确认记录和状态独立。不要把另一个配置嵌套在已有实例的数据根内；共享同一实例根就是共享其附属状态。
- `-config` 参数本身的相对路径基于启动工作目录；配置内相对媒体路径和手动 VRChat 日志路径始终基于 B。保存时位于 B 内的路径转为相对路径，外部路径保留绝对路径。默认 VRChat 日志目录随当前用户环境解析。
- 主配置不能命名为保留的 `terms.json`、`channel.key`、`config.lock`、`state`、`logs`、`storage`，也不能使用 `.swd-tmp` 后缀，避免覆盖规范配置和原子写入工作槽。
- 多个配置主动选择同一个媒体根时，仍受媒体 `.lock` 独占保护；配置隔离不允许同时写同一媒体库。

配置版本独立于软件发行版本。读取拒绝缺失、不支持或未来版本；保存前再次检查磁盘版本，并保留当前版本中的未知字段。设置 API 不能指定磁盘格式版本。配置、条款及库存/任务/吞吐快照使用每目标固定的 `.<文件名>.swd-tmp` 工作槽，排他创建、同步并原子替换；持有配置实例锁的启动流程仅清理本实例已知的六个遗留工作槽，失败不覆盖已提交文件。通道密钥仅缺失时排他创建，损坏原文保留并降级为临时通道身份；不承诺所有文件构成跨文件事务。

默认启动前若 EXE 旁存在 `still-wanna-dance-console.json`、`stepstash-console.json`、`still-wanna-dance-data` 或 `stepstash-data`，会在创建 data、日志和配置锁前拒绝并要求独立新目录。自定义旧配置由版本检查拒绝。这里只诊断已知布局，不扫描任意历史文件、不复制或自动迁移；存储根的旧库保护仍遵循上文。

### 文件所有权、恢复与备份

| 路径 / 数据 | 生成、更新与退出行为 | 备份与重建 |
| --- | --- | --- |
| EXE、licenses、README、构建信息 | 由交付包拥有；升级仅覆盖这些路径 | 可从相应构建重新取得，许可随分发保留 |
| `C` | 主设置；首次成功打开和保存设置时写入 | 必须备份；代理密码为 CurrentUser DPAPI 密文，换用户/机器可能需重配 |
| `R/terms.json` | 用户主动确认后写入；条款变化要求重新确认 | 随配置备份；缺失则重新确认，不是身份或内容授权证明 |
| `R/channel.key` | 32 字节通道身份密钥；仅缺失时生成，损坏则保留原文件并使用临时身份 | 随配置备份；缺失重建会改变 SOCKS5 稳定身份和旧样本关联 |
| `R/state/throughput.json` | 测速尝试冷却与历史样本；网络监测需要持久化时更新 | 可重建；丢失旧样本和冷却时间，不包含凭据或播放 URL |
| `R/state/inventory.json` | 最近成功的库存快照；扫描失败保留上次结果 | 可重新扫描 |
| `R/state/batch.json`、`attempt.json` | 成功批量任务摘要、最近尝试摘要 | 可重新运行；不是断点续传记录，丢失的历史摘要不可恢复 |
| `R/config.lock` | 整个配置实例生命周期持锁，退出释放 | 不必备份；存在不代表被占用，不能删文件代替释放锁 |
| `R/logs/app.log[.1-.3]` | 启动、退出及操作诊断，5 MiB × 最多 4 份，重启追加 | 可选备份，丢失后无法重建历史日志 |
| `S/storage.sqlite`、SQLite 日志文件、`S/videos/` | 引擎/导入/下载/统计写入；独占根，MD5 去重 | 退出后备份整个 S；统计不能靠视频重建，不在运行中只拷主库 |
| `S/.lock` | 媒体写入与离线删除的目录锁 | 不必单独备份；保留锁文件不等于仍占用 |
| `S/tmp/download-<数字>.part`、`download-publish-<数字>.part` | 下载、Range 与发布暂存；媒体独占锁及数据库格式校验通过后，仅回收符合 Go 临时文件数字名称且为普通文件的已知产物 | 不必单独备份，不承诺断点续传；符号链接、重解析点和未知名称保留，删除失败有明确诊断 |
| `S/tmp/import-<数字>/video` | 单次导入硬链接或跨卷复制暂存；操作结束只删除自己的 `video` 及空目录，崩溃后仅清理空目录或恰有一个普通 `video` 文件的已知目录，绝不递归删除陌生内容 | 不必备份；合法库启动时按媒体锁及格式校验后清理；源视频和完整缓存不受影响 |
| `B/.<配置文件名>.swd-tmp`、`R/.terms.json.swd-tmp`、`R/state/.<状态文件名>.swd-tmp` | 主配置、条款与四类状态的固定原子工作槽（各目标至多一个）；配置锁持有者启动时只清理本实例已知工作槽，不全盘扫描 | 非备份对象；未知文件及旧随机命名不泛化删除，工作槽占用/不安全时拒绝清理并保留最后有效数据 |
| 可写性探测目录中的 `.swd-probe-*` | Windows 使用内核 delete-on-close 创建临时探测文件，正常关闭或进程终止后删除；不扫描用户外部目录 | 非备份对象 |
| `R/channel.key` 损坏、`R/state/throughput.json` 损坏 | 密钥损坏时原文件原地保留、告警并使用进程内临时身份；测速状态损坏时跳过载入并在下次成功保存时原子替换，均不再新增 `.corrupt-*` 副本 | 密钥需要恢复原始备份才能恢复历史身份；旧试验版的 `.corrupt-*` 不自动清理 |

库存和任务快照独立使用 `schemaVersion=1`，只保存 `storageDir` 与 `result`。相对库路径以 B 为基准，整体移动后可用；切换库不加载另一库的结果。损坏或不支持的快照跳过，不引入迁移链。设置、密钥和状态属于配置实例，不塞入可外置的媒体 SQLite。未来 #14 节点/订阅持久化及 #15 法律凭据属于 R，须遵守各自格式合同；#16 引导应复用配置与条款状态。

### Portable 外部状态

| 外部位置 | 所有权 / 生命周期 | 备份约定 |
| --- | --- | --- |
| Windows `%LOCALAPPDATA%/SWD/console-session-<id>.json` | 托盘单实例发现；包含回环地址、SWD 协议身份、实际 EXE、配置、PID 和进程创建时间；激活前校验进程/端口归属，退出删除本会话记录，异常退出后由新锁持有者清理；无托盘模式不创建 | 不备份，可重新发现，不是用户配置 |
| Windows `%SystemRoot%/System32/drivers/etc/hosts` 及 `hosts.swd-backup`（已有 `hosts.stepstash-backup` 原样保留） | 仅用户显式接入修改 hosts；首次备份保留。恢复精确移除托管映射，不覆盖整个 hosts；退出不恢复 | 系统管理员管理，不自动删备份或覆盖其他映射 |
| VRChat 日志目录、用户选取的导入源 | 只读输入，不归 SWD 所有 | SWD 不创建、修改或清理 |
| 用户选择的外部 S | 仍由媒体目录锁保护，路径留作绝对引用 | 必须另行完整备份；移动 Portable 不自动搬外置库 |

命令行服务是独立开发/部署入口：`-storage-dir` 决定 S，相对路径基于 CLI 工作目录，默认值见[服务手册](service.md)。它不创建桌面配置实例。便携包只交付 SWD.exe 桌面入口。非 Windows hosts 路径为 `/etc/hosts`，不纳入 Windows Portable 验收。

完整备份单位是 C + R + 整个 S（默认退出后直接复制 data）；外置存储单独备份。整体移动前先退出，升级仅覆盖交付文件，不覆盖 data。修改存储路径只选择另一套库，不搬运任何数据。操作步骤见[便携交付说明](portable-readme.txt)。

## 清单内容摘要 v1

`catalog_state.digest` 保存 `sha256-v1:<小写十六进制摘要>`。摘要输入固定为 UTF-8 前缀 `still-wanna-dance/catalog/v1\n`（末尾一个换行），后接规范化歌曲列表的紧凑 JSON。字段由独立的 `catalogDigestSongV1` 明确投影，不直接序列化业务类型 CatalogSong。

歌曲按 ID 升序；MD5 转小写；可空字段保留 null 与零值的区别。三个数组按现有规范化规则重编码，对象键排序，数组元素顺序保留；ayaId 和数组内 JSON 数值保留数字文本形式，未承诺把 1 与 1.0 合并。字段顺序、名称及 Go encoding/json 编码规则由固定样例摘要测试约束。来源、上游时间和未知业务字段不参与摘要。

摘要覆盖当前全部歌曲资料和 ID → MD5 关系。给业务结构新增字段不会隐式改变摘要；若要把新语义纳入摘要，必须显式定义新版输入，并同时决定数据库格式策略，不能无意改动 v1。

## 持久化格式与语义（首发基线）

版本号不随软件版本递增，也不为了称作“v1”而重用试验期编号。首发基线是 SQLite **2**、配置 **3**、库存/批量/尝试快照 **1**、吞吐快照 **1**；不迁移此前试验格式。SQLite 的完整 DDL 固定在 `internal/cacheproxy/schema/storage-v2.sql`，有效二进制样本在 `internal/cacheproxy/testdata/storage-v2.sqlite`；JSON 样本在 console、upstreamstate 的 testdata 中。今后改变契约必须保留旧样本、明确新版本和升级决策，不能重新生成旧样本来消除回归失败。

### SQLite 字段清单

以下为完整表清单；类型、NULL、CHECK、外键和索引以冻结 DDL 为准。无显式默认值的必填列必须由写入方提供，未声明为 NOT NULL 的资料列允许 NULL。数据库的 INTEGER 为有符号 64 位，REAL 为双精度；业务端仅写有限分数和合法正整数歌曲 ID。数据库不承诺任意手工写入均有效。

| 表 / 身份 | 字段与来源、空值和单位 | 价值与关系 |
| --- | --- | --- |
| `songs` / `song_id` 正整数主键 | 可空 TEXT：name、artist、dancer、genre、group_name、composed_title、composed_title_spell；可空 INTEGER：player_count、rpe；可空 REAL：volume、start、end（上游秒）；flip、double_width、skip_random、disable_public 为可空 0/1；aya_id 保存可空上游数值 JSON 文本；tags_json、original_urls_json、shader_motion_json 为可空合法 JSON 数组文本 | 清单资料，NULL=未知，0/false/空串/空数组是已知值；清单可重新获取，旧歌曲不因清单删除而丢失统计 |
| `media` / `md5` | 小写 32 位十六进制非空键；byte_size 可空非负字节数；source_path 可空来源路径 | 资源索引，不是文件存在/完整性的证明；与 MD5 文件相互独立 |
| `song_media` / `song_id` | 歌曲外键 → 非空 media.md5 外键；md5 索引 | 每首歌一条当前关联，多首歌可共享同一媒体；清单权威，显式 ID 下载仅补缺失关系 |
| `song_urls` / `(song_id,api,node)` | 歌曲正整数、API/节点/完整 URL 文本、MD5、正字节数；query_started_at、observed_at、failed_at 为 Unix **纳秒**，failed_at 默认 0=未失败 | API 证据缓存，不创建歌曲外键，不改变权威映射；node 为 API 选择（cf/nya），URL 可带签名参数；不属于代理密码。24 小时内未失败且不在未来的记录才可使用 |
| `song_usage` / `song_id` 外键 | demand_count 非负累计次数；demand_score 非负 REAL；last_demand_at 为 Unix 毫秒 | 按歌曲跨域 GET 需求、30 秒去重；60 天半衰期指数累计分数。明细清理后不能无损重算 |
| `song_playback` / `song_id` 外键 | transfer_count 累计次数、last_transfer_at Unix 毫秒 | HTTP GET、200/206、completed、正字节完成证据，按完成时刻 30 秒去重；不是游戏首帧或完整播放确认 |
| `playback_latency` / `cache_result` | HIT/MISS 等缓存分类文本；samples 次数；first_body_ns 为首字节耗时**纳秒累计和** | 与播放完成关联的测量事实，每次符合条件的传输累加；展示转换为平均毫秒，不是绝对时刻 |
| `catalog_state` / `catalog_key` | 当前键 songs；revision 原始民用水位；digest 为 sha256-v1；source 溯源文本；accepted_at Unix 毫秒 | 接受清单的事务水位，身份/摘要不可随展示更名修改 |
| `catalog_checks` / `catalog_key` | checked_at Unix 毫秒，默认 0=从未成功验证；message/error 默认空串=无诊断 | 成功验证时间和最近错误；失败不把旧成功时间改成失败时间 |
| `catalog_members` / `song_id` 外键 | 当前已接受完整清单的成员 ID | 与 song_media 一起推导覆盖，不复制另一份成员资料 |
| `media_access` / `md5` | last_requested_at Unix 毫秒，按 max 更新 | 最近 GET 事实；独立于歌曲归属、明细保留期，允许未被 media 收录的资源 |
| `request_events` / `event_id` INTEGER 主键 | resource_key 资源/需求 ID 文本，version_key 为 MD5，host/source/method/range_header/cache_result/outcome 为请求证据文本；requested_at Unix 毫秒；file_bytes/transferred_bytes 字节；elapsed_ms 持续毫秒；status HTTP 数值；counts_as_demand 0/1 | 有界原始请求日志，不存历史歌曲关联。按 resource_key+requested_at、requested_at 和 HTTP 部分索引（requested_at DESC,event_id DESC）查询；同毫秒按 event_id 排序，清理后不承诺 ID 全局永不复用 |
| `traffic_totals` / `id=1` | saved_bytes 非负累计命中字节；hits/misses 累计次数，初始化为零 | HTTP GET 成功完成 200/206 的 HIT/MISS 事实；requests、命中率由二者推导。删除试验期无读写用途的 local_samples/local_ns/upstream_samples/upstream_ns；首字节统计唯一来源为 playback_latency |

`request_events` 的 source 由业务写入（http 或 prefetch），method 是 HTTP 方法，cache_result 是 HIT/MISS 等响应分类，outcome 为 completed/failed/canceled/timeout 等结果；这些诊断文本不是可扩展行为开关，未知值不得当成成功。累计计数不因删除视频或清理明细而重置。评分固定 `oldScore * 2^(-elapsed/(60 days)) + 1`；回拨或 30 秒内重复需求不计分、不降低 last_demand_at。未来改评分必须保持旧含义或随数据库版本明确重置/重算策略，不假设请求历史完整。

### 时间与排序

- SQLite 真实时间点统一解释为 UTC Unix 毫秒，只有 song_urls 的三个字段明确使用纳秒，以区分同毫秒并发查询；纳秒范围必须可由 int64 无损表示。其新结果必须有严格更大的 query_started_at；同刻第一条获胜，较旧/回拨结果不覆盖新值。失败只能标记实际尝试的同一条查询。时钟回拨后未来观测不可复用，等待重新获取或时钟恢复。
- JSON 持久化真实时刻用 RFC 3339、UTC `Z`，允许 0–9 位可变小数秒，不按文本字典序比较。Go 零时间 `0001-01-01T00:00:00Z` 表示尚未发生；SQL 明确指定的 0 哨兵表示未知/未失败，不能展示为 1970 年。一般 SQL 时间不以 0 自动表示 NULL。
- `revision` 只接受 14 位 `YYYYMMDDhhmmss`，校验日历日期；不接受时区后缀/小数秒。用共同比较轴排序但不声称其真实 UTC 偏移；同水位同摘要等价，不同摘要冲突。该逻辑水位与机器时钟无关。
- 持续时间不编码为日期：吞吐 `durationNs` 是纳秒整数，播放首字节累计也是纳秒，请求 elapsed_ms 是毫秒。UI 用毫秒/秒/Bps 展示；song_urls 的纪元纳秒不作为 JSON Number 暴露给 JS，时间对象编码为字符串。不要把 SQL int64 原样新增到 JS 数值接口。
- 文件 ModTime 是 OS 文件属性，用于句柄/删除身份核对，非数据库事件时间、不保证跨文件系统精度。运行中计时优先 Go 单调时钟，持久化后不保留单调部分。快照读入偏移可解析，但再次保存规范成 UTC。吞吐冷却遇到未来 lastAttempt 时失效重采样，避免长期卡住。

### JSON、凭据与二进制身份

| 文件 | 内部格式、缺失/失效语义 |
| --- | --- |
| config.json / 自定义配置 | schemaVersion=3；字段见 Settings：目录文本，manualLogDir/autoStartCDN/autoStartQueue 布尔，requestRetentionDays（0=不清明细）、maxCacheBytes（0=不限）、queuePrefetchCount（缺失/0→3，1–100）、throughputIntervalMinutes（缺失/0→20，1–1440）、downloadUpstream（空→auto，auto/cf/hkg，hkg 对应 API nya）、upstreamMode（空→direct，direct/auto/socks5）、代理地址与用户名。缺失采用构造默认（保留期 30 天），显式 null 对标量沿用默认；false/合法 0 保留。未知字段以 RawMessage 保留数值精度；格式缺失/未来/旧版本、明文 socks5Password 均拒绝写入 |
| config.proxySecret | 缺字段=无保存密码。封套 `{version:1, protection:"dpapi-current-user", ciphertext:"<base64>"}`；与整个配置一起原子替换，无第二份凭据文件/明文快照。未知格式、null、损坏或当前用户解密失败=不可用并保留原值；省略设置 API 密码保留密文，明确输入新密码替换，用户名/密码一起清空移除。状态仅返回可用标志和固定诊断，不返回密文/明文 |
| terms.json | version 是法律正文版本，sha256 是正文摘要，acceptedAt 是 UTC 同意时刻；缺失/不符不授予同意。当前仍为现有明文法律记录；#15 的防伪 DPAPI 封套未在本次实现，不把代理封套当作法律授权。日后封套版本必须独立于法律版本 |
| channel.key | 无复杂封套，精确 32 个随机字节。HMAC-SHA256 输入为 JSON 数组 `["socks5-channel-v1",address,username,password]`，输出 stable-v1-hex；域分隔符冻结。缺失排他生成；长度错误时原文件保留并告警，使用临时通道身份，不复用持久样本；不可读时同样使用临时通道身份。不是加密密码的密钥 |
| state/throughput.json | schemaVersion=1、lastAttempt、samples；sample 包含 observedAt、songID、bytes、durationNs。只恢复合法域名通道和稳定代理身份、正字节/时长且非未来样本。无版本/其它版本不解释；坏 JSON 保留至下一次成功的原子保存，不生成隔离副本；历史节点键不迁移，重新采样。它不是网络当前健康真相 |
| state/inventory.json | schemaVersion=1、storageDir、result；result 为 Inventory：catalog（revision/checkedAt/message/error）、coverageKnown/coveredSongs/totalSongs/catalogRevision/videos/bytes/scanning/updated/error；恢复强制 scanning=false |
| state/batch.json、attempt.json | 同一独立快照版本 1 和库归属；result 为 Batch：catalog/catalogWarning/budgetReached/catalogHits/reused/scanOnly/missing/updated/finished/running/phase/total/checked/hits/downloaded/failed/current/failures（id/name/error）。恢复 running=false；attempt 需 finished 非零。不是可恢复执行队列 |
| Windows 会话发现 JSON | address（仅 127.0.0.1 回环地址）、Identity（SWD 应用/协议版本、EXE、配置、PID 和进程创建时间）；仅供临时发现，还核实进程、端口和后端身份；无需用户数据迁移版本，不作为授权依据 |

DPAPI CurrentUser 不使用路径作为额外熵，不请求 UAC。同用户覆盖/移动可用；另一用户或普通独立机器可能不可用，但域用户漫游可能保留能力，**不是绝对设备绑定**。非 Windows 无明文替代方案：保存密码明确失败，已有封套显示不可用。强制代理的不可用凭据使代理拨号失败；Auto 保留已明示的直连优先/代理回退策略，不篡改保存的偏好。未来节点令牌、私钥、带认证订阅 URL 必须使用同等保护，不落盘原始导入副本。

诊断日志也使用 RFC 3339 UTC 时刻，但不是恢复业务状态的格式；错误文本和事件属性可以随实现变化，不为日志增加用户数据迁移版本。

### 打开、失败与未来升级

已有数据库（包括零字节文件）只做身份、版本和冻结结构校验，不能同版补表/索引或降版本。发现 videos 目录而数据库缺失时拒绝重建，要求恢复完整备份。全新初始化使用一个 SQLite 事务写 DDL、初始累计行和版本；中途失败回滚，不在失败路径清理下载文件。目录锁覆盖初始化和整个可写连接寿命；只读入口不建库。未知对象或结构改变同样诊断，而不是当作可修复缓存。

本次没有伪造 v1→v2 或配置 v2→v3 迁移。将来正式发布的旧格式若需要升级，应在该唯一打开入口持锁检查身份后、启动业务前实现针对性事务步骤，保留旧样本并测试成功/失败回滚和降级拒绝；配置升级应原子替换，失败保留原文件。不能以删库重建、改版本号或清空陌生目录代替升级。

已覆盖固定样本反复读写打开不改字节、旧/未来/未知版本、缺表/索引/新增列、损坏/空/缺失数据库、初始化中途失败回滚、设置替换失败保留密文、DPAPI 篡改与重配、同用户移动、时间精度与并发同刻/回拨、跨库和 Portable 相对路径。真实断电/磁盘耗尽、另一 Windows 用户/普通跨机器和 VRChat/托盘/UAC 联合验收仍需专门环境，不用本地单用户测试冒充通过。
