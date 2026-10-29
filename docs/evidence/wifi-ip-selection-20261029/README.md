# Wi-Fi 单路 IP 优选证据

[单路报告](../../validation/wifi-ip-selection-20261029.md)、[前序窗口](../../validation/upstream-wifi-windows-20261029.md)和[最初排查](../../validation/upstream-window-20261029.md)分别保留各批次条件与失败，不混算分母。

Git 保存 `summary.json`、`legacy-summary.json`、冻结 `plan.json` / `selection.json`、`verification.json`、`protocol-comparison.json` 与清单。完整脱敏逐请求记录和历史采样源码放在版本化附件 `wifi-ip-selection-20261029-v2.zip`，清单见 [manifest.json](manifest.json)。附件仅在本机和外部备份校验，**尚未发布**；索引中的 Release URL 是预定发布地址，当前不能据此下载。v2 统一初始窗口时间脱敏并去除本机编号、私有日志指纹和与 Git 重复的摘要；v1 仅作本地修订记录，不作为待发布材料。

本机附件位于仓库忽略目录 `artifacts/evidence-archives/`，外部调查目录保留相同副本。离线获取和核验：

```powershell
python scripts/evidence_archive.py wifi-ip-selection-20261029 --local-dir artifacts/evidence-archives
node experiments/upstream-wifi/analyze.mjs
```

解压根目录为 `artifacts/evidence/wifi-ip-selection-20261029/`，包内沿用仓库相对路径：

| 包内位置 | 内容 |
| --- | --- |
| `docs/evidence/wifi-ip-selection-20261029/single-lane-trials.jsonl` | 894 条完整脱敏记录，含七条中断；没有把超时视作零速成功 |
| 同目录 `events.jsonl`、`legacy-*.jsonl` | 生命周期及前序观察、多路、固定 16 路和两个预试验，逐批保留 |
| `docs/evidence/upstream-window-20261029/*.jsonl` | 最初默认路由、修正后的 Wi-Fi、旧组件和候选快照，分别保留 |
| `_provenance/runner-source/` | 最终 r2 实际采样源码及五个内部依赖；不是完整产品源码树 |
| `_provenance/historical-tools/`、`_provenance/initial-tools/` | 旧版本片段、旧观察脚本和分析入口；仅作历史快照 |
| `_provenance/inventory.json` | 附件完整成员 SHA256；原源码版本、最终文件/构建指纹见 Git 中的 [provenance.json](provenance.json) |

公开数据保留目标 IP、相对时间、正文哈希及失败分类；排除精确绝对采样时间、本机接口编号、源地址、DNS 服务器、签名 URL、原始错误详情、PID、私人路径和私有日志指纹。初始窗口采用同一批次共同零点，摘要和五份轨迹的 `tSeconds` / `startSeconds` / `endSeconds` 可相互对齐；其他批次分别使用各自零点，不能跨批次比较。日期仅用于版本追踪。历史源码中的固定截止常量属于程序快照，不是实际采样时间，保持原字节供构建指纹核对。

最初报告的源地址误选、403 误停、迁移暂停和范围调整均保留；不将后来成功覆盖旧失败。旧导出清单和私有完整资料仅保留在本地备份，不加入发布附件。摘要、报告和离线工具以 Git 为单一维护位置，附件保留完整脱敏轨迹与对应历史采样源码。
