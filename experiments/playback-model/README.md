# Issue #51 可选播放模型实验

这是临时验证机制，不是正式产品设置。判定及已验证范围以[方案记录](../../docs/research/issue-51-request-admission-proposal.md)为依据；模型默认关闭，当前正式服务不引用这个包。默认启用后仅对确认的非 owner 考虑入口 504，每次已识别播放最多一次。

## 模块边界

追加的 [owner A/B](../../docs/acceptance/live-acceptance-20261117-02.md)使用 `Options.AllowOwner=true`：该实验开关取消角色门槛，包括缺少多人角色记录的情况，不影响默认值。`owner-fixture.patch` 是该批当前 main 上的隔离夹具；B 使用 `LAB51_MODEL_ALLOW_OWNER=1`、`LAB51_MODEL_ENABLED=1` 和 `LAB51_MODEL_MARGIN_SECONDS=0`，A 只关闭模型。开头配对观察到 B 额外回零，后半段两组均由正式等待超时触发回零；不将实验开关自动推广到产品。

- `internal/vrclog` 的可选解析复用既有日志读取器，只产出正式播放器事件。普通队列解析不变。
- `Model.Observe` 接收事件及实际收到时间；失效时入口放行。同步目标用于时间线，不能用卡住的本地播放位置替代。
- `Model.Admit` 接收匹配版本的明确媒体信息、前次实际视频交付快照及当前块就绪状态。调用方必须先确认是同一正式播放器，检查过程不能创建下载需求。前次请求可以仍在活动；错误正文不计入交付。
- `Admission` 是受控 HTTP 测试用入口。拒绝时返回不缓存的 504，不调用后续处理器；其他请求照常进入原流程。
- `Model.Handoffs` 只为调用方提供的、原本可交接的剩余块范围选择 2 秒或 10 秒，不自行建立需求。模型失效或关闭时全部为 2 秒；最新 main 也是统一 2 秒交接，尚未启用模型覆盖。

时长要求是对应歌曲及媒体版本的目录／探测元数据。`Explicit=false`、无效大小或时长不能用于拒绝；禁止把默认码率换算结果标成明确元数据。此实验尚未提供生产元数据适配器、通用块就绪读取器或多个播放器的关联机制。

## 自动检查与离线回放

从仓库根目录执行：

```powershell
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test ./internal/vrclog ./experiments/playback-model/... -count=1
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs run ./experiments/playback-model/cmd/replay -input <private-input.json>
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs run ./experiments/playback-model/cmd/replay -input <private-input.json> -enabled
```

输入 JSON 含 `log`（原始日志本机路径）、`utcOffsetSeconds` 及 `cases`。每个 case 含 `id`、`media`（`songID/key/bytes/seconds/explicit`）、首个播放器请求的 `start`、受控来源 `releaseSeconds`、缺口字节位置 `missing` 和 `requests`。每个请求记录 `id/at/endAt/start/end/bytes/player/video`；时间为 RFC3339，范围终点不包含在请求内，`video` 只标记真实 200／206 视频响应。缺少旧请求的中间字节快照时只使用已经结束的响应，不根据未来结束值补造进度。

程序扫描 0／2／5／10 秒模型余量和 0／2／3 秒附加轮询延迟。日志事件在该秒结束后才视为可用，避免把该秒稍后由响应引发的错误用在该响应之前。输出只有样本编号、相对时间、条件判定，不复制原始日志。输入保留在忽略目录；公开结果见[证据](../../docs/evidence/issue-51-playback-model-20261116/log-model-replay.json)。回放不能模拟改变响应后的播放器后续行为。

## 文件位置与媒体时间的离线分析

[研究报告](../../docs/research/issue-51-media-time-map-20261117.md)记录 10,197 个文件的扫描、正负余量比较及独立解封装校验。这部分脚本不加载游戏日志，不改变运行中的模型。负余量只参与离线统计，当前 `Model` 仍不接受负值。

准备私有清单 JSON：顶层 `files` 数组，每项有 `key`（媒体身份）、`file`（绝对路径）、`bytes`、`mtime`（Node `statSync().mtimeMs`），以及 `songs` 数组；每个歌曲含数字 `id/start/end/byte_size`。媒体身份由本地文件名与目录媒体关联匹配，大小必须一致。清单、路径和逐文件输出不得提交到 Git。

隔离取得 MP4Box.js 2.5.0 npm 包，校验 SHA-512（Base64）为 `AK5FQsN5aD2m91HuCGPlRDomDhGxK0o4F8ZVyAbAfcMWBW2QlhZvE0S7v9lBq0Ib4eONq7mT1SBVdrnnIrn4pw==`，将 `package/dist/mp4box.all.mjs` 及其相邻模块保留在忽略目录。没有新增项目 npm 依赖。依次运行，扫描及汇总输出必须是新位置：

```text
node experiments/playback-model/media-map.mjs <private-inventory.json> <mp4box.all.mjs> <new-scan-directory> 4
node experiments/playback-model/summarize-media-map.mjs <scan-directory> <new-summary.json> docs/evidence/live-20261117-01/playback.json
```

独立校验通过仓库测试临时目录入口，使用 uv 管理的 Python 3.12.13 和隔离的 `av==19.0.2`：

```text
scripts/with-test-temp.ps1 uv run --no-project --managed-python --python 3.12.13 --with av==19.0.2 python experiments/playback-model/verify-media-map.py <private-inventory.json> <scan-directory> <check.json>
```

沙箱内可将 `UV_CACHE_DIR` 指向忽略目录。检查脚本遇到时间或同步帧差异返回非零，本批块时间全部符合 10 微秒容差，但 3081 的同步帧存在差异，所以本批完整检查退出码为 1；不能把它包装成全部通过。聚合的同步帧结果仅说明索引标记，不能替代实际解码和播放器跳转验证。

## 隔离真实引擎

本节旧夹具用于首轮非 owner 验证。Owner A/B 应从固定 main `d7be45246d572567935a288e5a9ec34d60ec660a` 的隔离副本应用 [owner-fixture.patch](owner-fixture.patch)，复制实验包及 opt-in `internal/vrclog` 文件；不再叠加下列旧夹具。该补丁包括受控来源、模型接入和有效范围内的缺块位置校验。三个实际构建、对应源码快照和校准结果见[构建证据](../../docs/evidence/live-20261117-02/provenance.json)，不可混用身份。

`engine-fixture.patch` 只应用于可丢弃源码副本，禁止应用到当前产品工作区。准备顺序：

1. 解出固定源码 `96d95141db7afbf7baaecb34601a33e339ca444d` 到新的忽略目录。
2. 在副本应用 [variable-fixture.patch](../../docs/evidence/live-20261116-02/variable-fixture.patch)，再应用本目录的 `engine-fixture.patch`，然后执行 `gofmt -w internal/cacheproxy/issue51_model_test.go`。
3. 将 opt-in `internal/vrclog` 和 `experiments/playback-model` 复制到副本对应位置，再按[首轮构建身份](../../docs/evidence/live-20261117-01/provenance.json)的 `sourceSnapshots` 映射，用该目录保存的历史模型、配套测试与解析器快照覆盖对应文件；逐项核对 `compiledSourceFiles`，不能用当前源码代替历史快照。在副本经测试临时目录入口执行 `node scripts/go-patches.mjs test -c -tags=no_easytier -o <绝对输出路径>/model-engine.test.exe ./internal/cacheproxy`。
4. 用 `check-engine.mjs <binary> <4598-media-file> <new-output-directory>` 执行受控开关对照。校准要求与历史 4598 对应的 35,169,662 字节、133 秒媒体，不公开媒体本体。

校准脚本只启动回环端口的测试子进程，创建合成日志，发送请求并停止子进程，不启动游戏或修改 hosts。输出目录必须是新目录；结果不含媒体路径或原始游戏日志。文件读写、下载与数据库均在独立夹具目录内。

夹具实际启动时的相关环境变量：

| 变量 | 用途 |
| --- | --- |
| `LAB51_MODEL_ENABLED` | 仅 `1` 开启模型，其他值关闭 |
| `LAB51_MEDIA_SECONDS` | 必须明确给出该版本媒体时间，写入隔离歌曲记录 |
| `LAB51_MODEL_MARGIN_SECONDS` | 明确给出 0–60 秒余量；本次校准 5 秒，不是产品默认值 |
| `LAB51_GAME_LOG_DIR` | 启用模型时必填；从现有日志末尾开始，先启动实验服务再开始新播放 |

其余缺块、文件、歌曲、缓存等夹具输入沿用上一批实验。该适配器只面向单歌曲受控来源；块就绪来自注入故障的放行状态，不是通用生产实现。校准证明了入口不新增需求、一次拒绝和实际 2／10 秒范围交接。[首轮实机](../../docs/acceptance/live-acceptance-20261117-01.md)另记录两首受阻恢复、两首正常预读放行，以及一笔临界放行后等待来源的限制；尚未证明普遍稳定性或交接成本改善。
