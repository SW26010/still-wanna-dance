# Udon Kiva 和 WannaInfo 清单内容与使用核对

核对日期：2026-10-29。依据为本轮读取的工作区代码；当时 HEAD 为 `2819074c56e233f146d21354c2efee1b8e4b6ace`，工作区另有并行修改。这三项是三个远端歌曲清单入口，本地没有按它们分别保存三套业务表。当前实现把 Kiva 和 WannaInfo 的带 MD5 清单同步到同一个本地曲库；Udon 清单用于网络探测和测速样本准备，不参与本地歌曲资料或版本水位更新。

本文是固定批次研究，文中的“当前实现”仅指上述核对范围，不随之后的产品改造改变。网络可用性及后续成功复核见[连接观测汇总](../validation/kiva-connectivity-20261029.md)，较早的协议对照见[10 月 28 日 HTTP 批次](upstream-http-20261028.md)；这些记录不单独决定长期协议策略。曲库改造的已确认目标见[决策记录](udon-catalog-decision-draft-20261029.md)，尚未实施，与本文描述的采样时行为分开。

## 本轮样本内容比较

2026-10-29 通过 HTTP 成功读取 [Udon](http://api.udon.dance/Api/Songs/list)，返回 200，没有重定向；与本轮通过 HTTPS 读取的 [Kiva](https://x.kiva.moe/api/v2/wanna/songs) 和 [WannaInfo](https://wanna.kiva.moe/api/wannaInfo) 逐项比较。**Udon 与 Kiva 同版本、同歌曲集合，共同歌曲资料经过协议表示归一化后一致；WannaInfo 结构与 Kiva 相同，但内容明显较旧。** Kiva/WannaInfo 均为 `code` 加 `data` 外层，歌曲在 `data.groups[].entries[]`，版本在 `data.time`；每组含 `entries`、`major`、`title`。三份清单均有 51 组。

| 项目 | Udon | Kiva | WannaInfo |
| --- | ---: | ---: | ---: |
| 本次获取协议 | HTTP | HTTPS | HTTPS |
| 上游原始版本 | `20261028232411` | `20261028232411` | `20260215004959` |
| 版本表达的日期时间，无时区偏移 | 2026-10-28 23:24:11 | 2026-10-28 23:24:11 | 2026-02-15 00:49:59 |
| 歌曲条目数及唯一 ID 数 | 10,470 | 10,470 | 10,076 |
| 唯一 MD5 数 | 不提供 | 10,136 | 9,744 |
| 响应正文大小 | 2,885,371 字节 | 5,992,687 字节 | 5,768,543 字节 |
| 每条歌曲的字段数 | 16 | 22 | 22 |

Kiva/WannaInfo 两份响应的业务码均为 200，全部歌曲 ID 为正整数，全部 checksum 为 32 位十六进制 MD5。两者每条歌曲均包含相同的 22 个字段：`id`、`name`、`artist`、`dancer`、`playerCount`、`volume`、`start`、`end`、`flip`、`doubleWidth`、`skipRandom`、`disablePublic`、`rpe`、`genre`、`group`、`composedTitle`、`composedTitleSpell`、`ayaId`、`tag`、`originalUrl`、`shaderMotion`、`checksum`。本次 `ayaId` 全为 null；没有独立文件字节数、码率或最终播放视频 URL 字段。

Kiva 与 WannaInfo 按歌曲 ID 对齐之后：

- 共有 10,075 个 ID；Kiva 独有 395 个 ID，WannaInfo 独有 1 个 ID，即 5274。因此不能简单说 Kiva 是 WannaInfo 加了 394 首新歌。
- 共有 ID 中 272 条歌曲记录不同，其中 268 条 `checksum` 不同，说明同一歌曲 ID 对应的视频资源版本也发生过变化。
- 其他差异为 `volume` 14 条、`end` 5 条、`doubleWidth` 和 `tag` 各 4 条、`name`、`composedTitle`、`composedTitleSpell` 各 3 条、`disablePublic` 和 `skipRandom` 各 1 条。各字段计数可发生在同一歌曲上，不能相加当作不同歌曲数量。
- 例如 ID 10926 在 WannaInfo 中名为 `Kitipo`，Kiva 为 `Kitipo (Extreme)`；ID 7550 的 MD5 和 `end` 都不同，后者分别为 302 和 303。

这些数据表明 **WannaInfo 此次返回的是较旧版本的同构曲库，不是额外字段更丰富的第三套曲库**。这描述本次响应，不推断该服务今后是否更新。两份原始正文的 SHA-256 分别为 Kiva `78ee9d017746a361b1c580c4aabaaf9f478030e5a007dc128ec66fd4401a1eab`、WannaInfo `373642dc3353bfe7f699feab068ccba2332f220e2df506b5e2fb7ffb5e911e0b`；本地抓取与逐 ID 比较结果位于忽略目录 `artifacts/catalog-sources-20261029/`。

## WannaInfo 的历史版本记录

本轮检索仓库文档、Git 历史和本地验收快照，能追溯到的 WannaInfo 观测从 2026-09-26 开始；所有已找到的有效版本均为 `20260215004959`，未发现其他版本。

| 观测日期 | 留存证据 |
| --- | --- |
| 2026-09-26 | [完整响应摘要](../evidence/playback-domain-20260926/game-sequence.json#L344)记录 HTTP 200、5,768,543 字节、`complete: true`；正文 SHA-256 与 10 月 29 日完全相同，因此对应相同正文和版本 |
| 2026-10-05 | [协议核对记录](../song-catalog-design.md#本轮协议边界)明确记载 `data.time = 20260215004959` |
| 2026-10-14、10-15、10-20、10-28 | 本地实际验收状态快照的 WannaInfo `catalogTime` 均为 `20260215004959`，各批次分别提取到 9、7、3、3 条状态记录；重复保存的观测不视为独立请求 |
| 2026-10-29 | 本轮直接下载正文，版本仍为 `20260215004959`，正文 SHA-256 仍为 `373642dc3353bfe7f699feab068ccba2332f220e2df506b5e2fb7ffb5e911e0b` |

这表明我们留存的观测中，它一直返回同一版本；9 月 26 日与 10 月 29 日甚至是逐字节相同的正文。记录不是连续采样，也没有覆盖 2 月 15 日至最早记录之前的时段，不能据此确定服务实际停止更新的日期。本地提取摘要保存在忽略目录 `artifacts/catalog-sources-20261029/wanna-history.json`，私人验收快照不入库。

## Udon 的结构差异及本轮同版本对照

本轮 Udon 的 HTTPS 请求在本机遇到连接重置或超时；随后按用户指定使用 HTTP，成功取得当前正文。以下以本次 HTTP 响应为依据，不再以历史快照代替当前结果。Udon 与 Kiva 的版本均为 `20261028232411`，歌曲 ID 集合完全相同，各 10,470 首；51 个分组的名称、major、分组顺序及组内歌曲 ID 顺序也完全一致，Kiva 条目自身的 `group` 全部对应 Udon 的父级 `groupName`。

| 内容 | 本次 Udon | 本次 Kiva/WannaInfo |
| --- | --- | --- |
| 外层 | `time`、`groups`、`tags`、`new` | `code`、`data`，数据内有 `time` 和 `groups` |
| 歌曲位置 | `groups.contents[].songInfos[]` | `data.groups[].entries[]` |
| 分组信息 | `groupName`、`major` | `title`、`major`，歌曲自身另有 `group` |
| 每首字段 | 16 个 | 22 个 |
| 内容指纹 | 没有 `checksum` | 有 `checksum`，可直接建立歌曲与视频 MD5 映射 |
| 标题及来源资料 | 没有以下七项 | 额外含 `checksum`、`ayaId`、`composedTitle`、`composedTitleSpell`、`genre`、`group`、`originalUrl` |
| 额外 ID | `danceid`，本次全部与 `id` 相等 | 没有 `danceid` |
| 属性命名 | `double_width`、`skip_random`、`disable_public`、`shader_motion` | 对应 `doubleWidth`、`skipRandom`、`disablePublic`、`shaderMotion` |
| 空值与布尔表示 | 空 `tag` 用 null，空 `shader_motion` 用空字符串；`skip_random`、`disable_public` 用 0/1 | 空数组用 `[]`，上述开关用布尔值 |
| 响应级辅助内容 | `tags` 提供多语言标签文字；`new` 为新歌 ID 数组 | 本次结构没有这两项顶层内容 |

本次 Udon/Kiva 经过字段改名、0/1 转布尔以及空标签/动作归一化后，所有对应字段一致，只有 `volume` 的数值展开不同：8,539 首的 JSON 数值不直接相等，例如 Udon 的 `0.79` 对应 Kiva 的 `0.7900000214576721`；转为 float32 后全部相同，最大绝对差约 `2.86×10⁻⁸`。这支持“同一份歌曲资料的两种协议表示”的理解，不能仅凭原始 JSON 差异断言两者音量设置不同。Udon 的顶层 `tags` 有 5 个标签的多语言文字，`new` 有 7 个新歌 ID，即 11497–11503，均存在于当前曲库。

Udon 与 WannaInfo 的歌曲交集同样为 10,075 首，Udon 独有 395 首，WannaInfo 独有 ID 5274；归一化后的共同歌曲字段有 21 首存在真实差异，涉及名称、音量、end、标签及开关。Udon 不提供 MD5，因此前述 268 首 MD5 差异仅由 Kiva 与 WannaInfo 的比较得出，不能声称直接用 Udon 校验了 MD5。

Udon HTTP 原始正文 SHA-256 为 `f7253636e98bc181ae7e6e357c810bebd1ece0b251f152b379ad568e92399454`。本地原文、响应头、比较脚本和结果分别保存在忽略目录 `artifacts/catalog-sources-20261029/` 的 `udon-http.json`、`udon-http-headers.txt`、`compare-http.cjs`、`http-comparison.json`。此前使用的 2026-10-15 历史同版对照仍见[历史统计](video-metadata-20261015/catalog-fields.json)及本轮 `historical-comparison.json`；其各 10,463 首和 8,533 条音量表示差异仅代表历史样本。本次 HTTP 获取用于调查，没有修改产品的 HTTPS 请求规则。

所以 Udon 并不是只有 ID 和名字的表：它也有歌手、舞者、人数、音量、起止时间、翻转、标签和 Shader Motion 等游戏播放资料。对本项目最关键的缺项是 MD5；Kiva/WannaInfo 则同时提供资源身份及更多展示资料。这也是当前业务选用带 MD5 清单的直接收益。

## 独有字段与独有信息的区别及使用情况

以 2026-10-29 11:48（北京时间）重新获取、版本同为 `20261028232411` 的两份响应复算。该次 Udon/Kiva 正文 SHA-256 分别与前述样本相同。字段只出现在一份歌曲条目上，不代表另一份响应没有这项信息：须同时检查父级分组和可直接派生的展示文本。

| Udon 独有字段 | 相比 Kiva 真正多出的信息 | 当前业务使用 |
| --- | --- | --- |
| 顶层 `tags` | 5 个标签的英、中、日、韩文字；与两表逐歌都有的 `tag` 标签键数组不同 | 不解析、不保存、不用于显示；逐歌 `tag` 则由 Kiva 入库 |
| 顶层 `new` | 本次列出 7 个歌曲 ID（11497–11503）；两表逐歌 `tag` 中含 `new` 的歌曲均有 3,412 首，不能用后者替代该 7 首集合 | 不解析、不保存，不用于新歌展示或下载优先级 |
| `danceid` | 本次 10,470 条均等于 `id`，没有增加独立身份信息 | 不使用，身份统一取 `id` |

Udon 的常规探测只解析顶层 `time` 与歌曲 `id`，测速样本准备也只抽取 `id`；正文在交付业务回调前被排除。因此本次找到的 Udon 独有字段均没有进入本地曲库功能。见 [`probe.go:149`](../../internal/upstreamstate/probe.go#L149)、[`probe.go:202`](../../internal/upstreamstate/probe.go#L202)、[`calibration.go:379`](../../internal/upstreamstate/calibration.go#L379)。

| Kiva 独有的歌曲字段 | 信息性质及本次内容 | 当前业务使用 |
| --- | --- | --- |
| `checksum` | Udon 没有的视频 MD5；10,470 条均有值，对应 10,136 个资源 | 核心使用：歌曲—资源映射、文件复用、缺失扫描、覆盖率、导入匹配、下载补齐及资源关联 |
| `originalUrl` | Udon 没有的原始作品来源链接；9,737 条非空、733 条为空数组 | 仅持久保存及参与清单内容比较，不展示、不用来下载视频 |
| `composedTitleSpell` | 额外提供组合标题的拼写/分词表示 | 仅持久保存及参与清单内容比较；当前搜索没有读取它 |
| `ayaId` | 额外 ID 字段，但本次全部为 null，没有实际 ID 数据 | 按可空数值协议验证、保存及参与内容比较；无业务消费者 |
| `group` | 全量等于所在分组的名称，Udon 已在父级 `groupName` 提供同一信息 | 保存到 `group_name`，用于曲库显示与搜索 |
| `genre` | 全量等于所在分组的 `major`，Udon 和 Kiva 的父级都已提供同一信息 | 仅持久保存及参与清单内容比较 |
| `composedTitle` | 全部 10,470 条都等于 `name + " - " + artist + " | " + dancer`，由共有资料直接派生 | 部分标题读取在名称缺失时使用它兜底；本次所有 `name` 均非 null 且非空，不触发这些兜底 |

“仅保存”仍包括类型校验和清单内容摘要比较；这些字段改变时可能影响同版本冲突判断，但没有单独的用户功能读取。字段入库和摘要见 [`catalog.go:86`](../../internal/cacheproxy/catalog.go#L86)、[`catalog_digest.go:41`](../../internal/cacheproxy/catalog_digest.go#L41)；分组搜索和标题兜底见 [`library.go:134`](../../internal/cacheproxy/library.go#L134)、[`queue_timing.go:25`](../../internal/cacheproxy/queue_timing.go#L25)。接口外层 Kiva 还多一个 `code` 业务状态码，解析器要求其为 200；它是响应有效性判据，不是歌曲资料，见 [`catalogmeta/catalog.go:46`](../../internal/catalogmeta/catalog.go#L46)。

本次复算摘要保存在忽略目录 `artifacts/catalog-sources-20261029/unique-information.json`。当前实际承担业务职责的 Kiva 独有核心信息是 MD5；组合标题作为派生文本有兜底读取，其余真正额外的歌曲资料目前没有独立业务消费者。Udon 的独有标签翻译和顶层新歌集合均未使用。

## 当前代码如何使用

| 入口 | 源码中的标识 | 当前用途 |
| --- | --- | --- |
| [Udon](https://api.udon.dance/Api/Songs/list) | `api` | 检查清单连通性、显示顶层 `time`、抽取歌曲 ID；CF 单路及满路测速也可从它准备多首样本。正文不交给业务同步回调 |
| [Kiva](https://x.kiva.moe/api/v2/wanna/songs) | `kiva` | 网络探测，以及本地曲库的完整 MD5 清单来源；手动全量检查、补齐、覆盖率与导入刷新默认直接请求它 |
| [WannaInfo](https://wanna.kiva.moe/api/wannaInfo) | `wanna` | 网络探测，以及经 monitor 回调同步的另一份完整 MD5 清单候选；与 Kiva 共用解析器和版本水位 |

入口定义见 [`routes.go:164`](../../internal/upstreamstate/routes.go#L164)，Udon 正文过滤见 [`probe.go:149`](../../internal/upstreamstate/probe.go#L149)，测速样本准备见 [`calibration.go:264`](../../internal/upstreamstate/calibration.go#L264)。这里“Udon 清单不参与业务”不涉及同域名的 `/Api/Songs/play` 播放地址接口；后者仍是播放、预加载和下载使用的接口。

### 曲库同步和回退

手动全量检查、补齐、覆盖率和视频导入调用 `refreshLocalCatalog`，默认 URL 固定为 Kiva。它会尝试该入口的可用网络通道，但不会在 Kiva 失败后额外请求 WannaInfo。刷新尝试之后，业务读取本地数据库快照；远端失败而本地已有有效 MD5 清单时仍可继续，无有效本地清单才失败。单个歌曲或资源下载直接读本地目标，不执行这次全量刷新。参见 [`console.go:175`](../../internal/console/console.go#L175)、[`catalog_checksums.go:18`](../../internal/console/catalog_checksums.go#L18)、[`batch.go:145`](../../internal/console/batch.go#L145)、[`inventory.go:160`](../../internal/console/inventory.go#L160)、[`import.go:131`](../../internal/console/import.go#L131)。

WannaInfo 的业务入口是 monitor：复用本轮已获取的 Kiva 和 WannaInfo 正文，完整解析后按上游 `data.time` 从新到旧提交。后台 monitor 和引导连通性 monitor 注册同一套回调。没有“无条件优先 Kiva”或“两个来源逐字段合并”的存储规则。参见 [`monitor_catalog.go:17`](../../internal/console/monitor_catalog.go#L17)、[`upstream_monitor.go:13`](../../internal/console/upstream_monitor.go#L13)。

两个来源共用唯一的 `songs` 水位。较新清单事务性更新歌曲、资源、当前映射和水位；较旧候选保留本地数据；同时间同规范化内容视为重复，同时间不同内容报告冲突。上游时间按 `YYYYMMDDhhmmss` 校验日历日期，放在同一个时间轴上比较，不将机器当前时间或获取时间当成版本。歌曲和关联增改不删，清单中消失的旧 ID 仍在本地；`catalog_members` 单独记录最近接受清单的成员集合。参见 [`catalog.go:24`](../../internal/cacheproxy/catalog.go#L24)、[`catalog.go:110`](../../internal/cacheproxy/catalog.go#L110)、[`catalogmeta/catalog.go:30`](../../internal/catalogmeta/catalog.go#L30)。

因此“WannaInfo 是备用来源”只能表示它能通过 monitor 提供合法清单候选，不能解释为每次手动刷新都有即时的跨入口回退；若它比本地版本旧，也不会覆盖较新的本地清单。

### 哪些字段真正影响功能

| 字段或信息 | 本地保存和实际消费者 |
| --- | --- |
| `id` | 本地歌曲身份、队列 ID、歌曲偏好和需求统计关联 |
| `checksum` | 写入 `media.md5` 与 `song_media`；确定歌曲当前对应哪个视频，按 MD5 复用同一文件、扫描缺失、下载补齐 |
| `name`、`composedTitle` | 曲库和队列标题，以及任务进度中的名称；不同查询对空值回退的细节略有区别 |
| `artist`、`dancer`、`group` | 曲库列表和搜索 |
| `end` | 与同 MD5 的文件大小共同用于播放及队列时长估算；这是估算输入，不是解析 MP4 得到的容器时长 |
| `start`、`playerCount`、`volume`、`flip`、`doubleWidth`、`skipRandom`、`disablePublic`、`rpe`、`genre`、`composedTitleSpell`、`ayaId`、`tag`、`originalUrl`、`shaderMotion` | 按已定义结构持久保存并参与清单内容摘要；当前未找到驱动播放、调度或曲库展示的其他读取方 |
| 父级分组结构、标签翻译及未知响应字段 | 不完整保留，不能从本地表无损重建原始游戏清单 |

字段映射见 [`catalog_format.go:18`](../../internal/cacheproxy/catalog_format.go#L18) 和 [`catalog.go:13`](../../internal/cacheproxy/catalog.go#L13)；曲库展示与搜索见 [`library.go:134`](../../internal/cacheproxy/library.go#L134)；队列标题、时长见 [`queue_timing.go:16`](../../internal/cacheproxy/queue_timing.go#L16)；资源时长估算见 [`media_timing.go:60`](../../internal/cacheproxy/media_timing.go#L60)。存储字段与原始协议的边界见[曲库设计](../song-catalog-design.md#songs歌曲资料)。

全量扫描和覆盖率使用本地已保存的所有有 MD5 映射的歌曲，包含被远端新清单移出的保留记录；覆盖率按歌曲计数，多首歌曲可共同命中同一视频。补齐按唯一 MD5 合并资源，优先选最近接受清单中的歌曲 ID 解析下载。视频导入的允许集合则仅取最近清单成员所引用的 MD5。参见 [`catalog_snapshot.go:93`](../../internal/cacheproxy/catalog_snapshot.go#L93)、[`inventory.go:160`](../../internal/console/inventory.go#L160)、[`download_fill.go:137`](../../internal/cacheproxy/download_fill.go#L137)、[`import.go:140`](../../internal/console/import.go#L140)。

### 清单不直接提供下载地址或文件大小

清单同步只补充 MD5 资源记录，不写资源大小或 URL。实际播放地址通过 `https://api.udon.dance/Api/Songs/play?node=cf&id=…` 获得；当前允许的节点统一为 `cf`。从有效返回 URL 的 `e` 解析 MD5、`s` 解析字节数，并保存地址观测。清单里的 `originalUrl` 属于歌曲资料，不被用作下载地址。参见 [`catalog.go:90`](../../internal/cacheproxy/catalog.go#L90)、[`playbacksource/source.go:23`](../../internal/playbacksource/source.go#L23)、[`videometa/parse.go:26`](../../internal/videometa/parse.go#L26)。

播放 URL 观测能在尚无映射时补歌曲及关联；若已有映射与 URL 的 MD5 不同，只记录差异，不覆盖映射。已有映射由后续合法新版本清单更新；批量补齐还要求下载资源符合任务所引用的 MD5。参见 [`song_urls.go:48`](../../internal/cacheproxy/song_urls.go#L48)、[`download_fill.go:144`](../../internal/cacheproxy/download_fill.go#L144)。

### 探测选歌并非总是随机

普通清单后备样本从有效歌曲 ID 中随机抽取，入口顺序为 Udon、Kiva、WannaInfo；没有新样本时，旧 ID 最多保留默认 10 分钟。Udon 只要求 JSON 可读且能找到正整数 ID，记录原始顶层 `time`；Kiva 和 WannaInfo 还必须通过业务码 200、14 位合法日期、完整分组/entries、唯一正整数 ID 和合法 MD5 校验。两个 ID 可以合法共享同一个 MD5，同一 ID 重复则整份拒绝。参见 [`probe.go:160`](../../internal/upstreamstate/probe.go#L160)、[`catalogmeta/catalog.go:46`](../../internal/catalogmeta/catalog.go#L46)、[`routes.go:39`](../../internal/upstreamstate/routes.go#L39)。

Console 注册了本地缺项候选回调：清单同步后，优先按资源分数选择缺当前 MD5 的 CF URL 或正资源大小的歌曲，利用本轮播放探测补齐地址和大小；每轮仍只选一首，未补齐的失败歌曲在本遍跳过，下遍再重试。没有缺项或本地查询失败才使用上述随机后备样本。因此不能把“Udon 优先采样”理解成“当前所有测速都优先随机抽 Udon 歌曲”。参见 [`check.go:120`](../../internal/upstreamstate/check.go#L120)、[`playback_sample.go:9`](../../internal/upstreamstate/playback_sample.go#L9)、[`song_metadata_candidates.go:17`](../../internal/cacheproxy/song_metadata_candidates.go#L17)、[`monitor_playback_url.go:12`](../../internal/console/monitor_playback_url.go#L12)。

独立 CF 单路及满路测速另外从这三个清单依次准备多首资源，取得足够样本便停止；它用实际播放 API 确定 URL、大小与资源身份，不将 Udon 正文写入业务曲库。参见 [`calibration.go:264`](../../internal/upstreamstate/calibration.go#L264)。

## 与现有文档的关系

[曲库设计](../song-catalog-design.md#udon-清单边界2026-10-06)中的“只有 Kiva/WannaInfo 写业务曲库、Udon 不写”与代码一致。理解具体运行方式时还需保留三点：手动刷新仅请求默认 Kiva，WannaInfo 经 monitor 入库；优先 Udon 的随机样本已是本地缺项选择之后的后备；“Udon 仅供 monitor”概括了业务排除边界，实际还包括独立 CF 测速的样本准备。

本次只核对源代码和公开清单，不修改产品行为。历史清单统计仍仅代表相应采样日期，不能替代当前接口数据。
