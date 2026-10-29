# 预置资源快照

`media-seed.json` 随 Go 程序嵌入，不需要联网。当前快照版本 `20261029000000` 包含 10,446 个资源、10,471 条歌曲映射，其中 10,417 个资源有已知字节数，29 个大小未知。文件只含 MD5、可选大小、歌曲 ID 与来源摘要；没有 URL、文件路径、播放统计或用户偏好。

应用启动时在一个事务中补充 `media`、占位 `songs` 和缺失的 `song_media`；已有映射及已有大小不被预置覆盖。`catalog_state.media_seed` 保存独立版本和摘要，同版重复启动不写入，较旧程序不回退版本，同版异文报错。预置不会建立 `catalog_members`、歌曲清单水位或 URL，也不表示本地已有视频。新 API 观察仍可更新映射。SQLite 建库、只读检查等底层操作不隐式加载预置。

本次输入为 2026-10-29 保留的 Udon/Kiva `20261028232411`、WannaInfo `20260215004959` 清单，以及用户指定本地库的脱敏投影。清单按版本降序处理；同版 MD5 冲突拒绝生成。参考库提供 10,433 个资源及 10,417 个大小；参考映射以各歌曲最后一次 API 观察优先于库内映射，再优先于备选清单。同版 Udon 不提供 MD5，只记录输入来源。各输入 SHA256 在文件的 `sources` 中。

维护时仅在需要更新发行快照时重新生成，不启动持续同步任务。所有原始输入和本地脱敏投影放在忽略的 `artifacts/`，不提交用户数据库。导出使用正常只读 SQLite 事务，包含已提交 WAL，既不改日志模式也不复制单独主库：

```powershell
python scripts/export-media-seed.py <参考库.sqlite> artifacts/local-media-projection.json --version <采集版本>
go run -mod=readonly scripts/build-media-seed.go -version <发行快照版本> -reference artifacts/local-media-projection.json <udon.json> <kiva.json> <wanna.json>
```

没有参考库时可省略 `-reference`，此时大小未知。生成器只读取显式输入、不联网，固定输入可得到字节相同的输出。修改快照必须提高版本，运行预置加载/重开/补缺测试，并更新资源策略中的精确字节数和 SHA256。旧库投影仅是开发时生成素材，不是产品迁移路径。
