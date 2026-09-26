# 日志排障与验收证据

桌面程序日志位置和轮转规则见[控制台日志](console.md#程序日志)。仍为 JSON Lines、每份 5 MiB、保留三份历史文件；状态轮询不写日志。命令行 `stepstash` 的缓存日志输出到 stderr，需自行重定向保存。

## 如何关联事件

- `session_id`：一次桌面程序启动；`application_build` 记录 Go 构建信息中的版本、提交和工作区修改标记（构建器未嵌入时不提供提交）。不同启动不能只靠 PID 区分。
- `service_id`：一个缓存引擎实例，重新创建会变。`request_id` 和 `flight_id` 都只在这个实例内有效。
- `trace_id`：一次播放请求或歌曲预缓存操作。队列启动、上游解析、Auto 回退、预缓存及结束事件使用同一个值；共享下载继续使用最初创建它的操作标识。
- `resource_key`：视频内容版本的指纹。用 `cache_task_attached` 将每个操作连接到 `(service_id, flight_id)`，再查看这一共享任务的下载日志。同一版本重试会产生新的 flight。
- `song_id`：曲库列表中的歌曲 ID。多个歌曲可能共用资源，不能把它与资源指纹混为一谈。历史淘汰事件的 `key` 与 `resource_key` 是同一指纹。

例如两个播放请求共享一个下载时，会看到两个不同 `trace_id` 的 `cache_task_attached` 指向同一 flight；下载和发布只记录一次。客户端中断、移出队列不一定中止共享下载，必须分别检查请求结果和 flight 结果。

## 过程和结果

| 事件 | 用途 |
| --- | --- |
| `cache_engine_ready` | 引擎配置摘要、启动清理的残留临时文件数；残留不能直接证明上次为何退出 |
| `cache_capacity_wait` / `cache_capacity_rejected` | 后台等待额度或播放请求被限额拒绝；附着成功后的 `wait_ms` 是准入耗时 |
| `request_started` / `request_finished` | 请求方法、Range、版本，以及最终 HTTP 状态、实际写入字节、Content-Range/Length、耗时、结果和写入错误 |
| `upstream_response` / `download_first_byte` | 上游状态、声明长度；从缓存任务开始到读到第一批正文的毫秒数（包含缓存检查） |
| `download_stage` | 阶段变更以及上一阶段耗时：缓存校验、等待上游响应头、下载并计算哈希、发布文件、数据库登记 |
| `download_progress` | 每个仍活动的 flight 每 15 秒一条；阶段、已读取正文大小、预期大小、最近一个周期平均字节/秒、距最后数据的 `idle_ms` 和阶段耗时。短任务无此事件 |
| `download_verified` / `download_integrity_failed` | 正文大小和 MD5 是否满足预期；MD5 在读取中计算，因此与下载耗时合并记录 |
| `download_published` | 完整校验、正式文件发布和数据库登记均已成功；不能保证该资源随后不会因容量策略被淘汰 |
| `cache_task_finished` | 共享任务最终阶段、耗时和 `completed` / `failed` / `canceled` / `timeout` 结果；成功命中也会出现，不能将它单独当作新下载成功 |
| `prefetch_resolved` / `prefetch_finished` | 解析线路与耗时、资源关联，以及预缓存结果；`success=false` 的解析事件仍可能随后通过 Auto 回退成功 |
| `queue_updated` | 有序 `song_ids`（保留 -1 占位）、房间重置代数；只取前 3 个位置预缓存，不记录歌名、玩家或房间原文 |
| `queue_song_canceled` / `queue_song_discarded` | 队列变化取消等待或旧结果被丢弃；不是下载失败 |
| `batch_progress` / `batch_finished` | 每处理 100 首及最后一首的汇总；结束区分仅扫描、缺失数量、取消和是否处理完列表。扫描完成仍可能存在缺失文件 |

`bytes` 在下载进度中表示已从上游读取的正文，在请求结果中表示成功写入响应的数据；二者口径不同。`idle_ms` 在缓存检查或文件发布阶段也会增长，只有结合 `stage` 才能判断是否在等待网络。进度采样只观测，不改变超时、回退或下载优先级。

## 可判定的验收范围

1. 冷缓存：沿 trace 找到 flight，确认 `download_verified`、`download_published`；同时请求结果应符合预期状态和字节数。
2. 热缓存和 Range：核对 `request_finished.cache=HIT`、状态 200/206、范围及实际字节；HEAD 正文为 0 是正常情况。
3. 异常和中断：200 只表示响应头已发出。最终 `outcome=aborted/failed/canceled` 或写入错误不能算成功。一个小 Range 成功也不能证明整段视频已成功发布。
4. 队列与回退：以有序队列快照、代数、操作 trace 和实际线路为依据，核对取消、重试和最终结果；`queue_song_ready` 表示当次预缓存成功，不保证以后始终留存。
5. 正常关闭：桌面主程序应有 `application_stopped`。缺少结束事件只能视为证据不完整或非正常结束的线索，不能推断崩溃原因。

日志只能支持服务端传输、校验、入库和任务调度的判断。实际 VRChat 播放、音画同步、切歌体验以及 UAC/托盘交互仍需游戏侧日志或现场验证。当前版本仍需联合实机验收，不能用旧构建的记录替代。

不记录请求查询参数、鉴权头或原始 VRChat 正文；预缓存解析/下载的标准 URL 错误会去掉 URL，保留可用于判断取消和超时的底层错误。日志仍含本机路径、视频路径和网络错误，分享前应检查。轮转可能丢失早期记录，复现后应及时复制当前日志和 `.1`～`.3`，保留从启动到结束的一组证据。强杀、断电和其他 goroutine 的未恢复 panic 仍不保证留下原因。

非法重定向 `Location` 的解析错误可能由 `net/http` 将完整地址嵌入普通错误文本。此类错误统一显示为 `failed to parse Location header (URL details redacted)`，不保留地址或原始解析详情；错误关联仍保留，便于程序判断。地址解析、下载、版本确认、曲库接口与 DNS HTTP 请求都在返回此类错误前脱敏。
