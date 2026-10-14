# 证据与大文件存储约定

Git 保存源码、报告、精简统计、纯矢量 SVG 和清单。完整实验轨迹、逐请求记录、图片、压缩文件及构建产物使用版本化附件。必要的二进制测试样本单独登记；当前唯一例外为固定的 `internal/cacheproxy/testdata/storage-v2.sqlite`。

## 文件归属

| 内容 | 位置 |
| --- | --- |
| 报告、成功与失败结论、实验边界、关键统计 | `docs/` 与 `docs/evidence/<batch>/` |
| 绘图输入和真实矢量 SVG | 同一证据目录；禁止将图片用 Base64 包装成文本或 SVG |
| 完整脱敏请求、调度轨迹、原图及原始证据清单 | 同仓库 `evidence-*` Release 的版本化 ZIP 附件 |
| 被测源码身份和对应源码快照 | 附件中的 `_provenance/`；保留实际测量时的提交和文件哈希 |
| 私人日志、原始 ETL/CSV、带凭据输入、运行库及程序 | 本机忽略目录及本地备份；不加入公开证据附件 |

普通构建及单元测试无需下载历史实验附件。复核者可按批次获取完整材料。失败、干扰和中断记录与成功数据一起保留，存储调整不改变实验或实机结论。

## 获取、校验与复算

索引为 [archives.json](evidence/archives.json)。使用 Python 3.10+ 标准库下载并校验一个批次：

```powershell
python scripts/evidence_archive.py cf-cpu-20261014
python scripts/evidence_archive.py all --verify-only
# 离线使用已下载的同名 ZIP
python scripts/evidence_archive.py all --local-dir D:/evidence-downloads
```

脚本先验证整个包的大小和 SHA256，再验证包内清单的完整覆盖和每个成员的大小、SHA256；拒绝路径越界、重复成员及覆盖不同的本地内容。解压位置为 `artifacts/evidence/<batch>/`，包内保留原仓库相对路径。`--verify-only` 不解压。发布前只能使用 `--local-dir` 验证，不能将此结果表述为公开附件已可访问。

例如原始 CPU 统计与调度轨迹位于 `artifacts/evidence/cf-cpu-20261014/docs/evidence/cf-cpu-20261014/`。原清单位于该目录的 `manifest.json`；历史分析源码在附件的 `experiments/` 下。`_provenance/provenance.json` 记录原提交、树标识、源码快照 ZIP 及逐文件哈希。源码快照仅排除 `docs/evidence/`，完整保留其余跟踪文件；证据本身另由批次归档保存。

绘图需要 Matplotlib。从已校验的完整材料生成当前 CPU 精简绘图输入：

```powershell
python experiments/cf-concurrency/plot_cpu.py artifacts/evidence/cf-cpu-20261014/docs/evidence/cf-cpu-20261014 --prepare
# 与 Git 中 plot-data.json 做 JSON 内容比对后，可直接绘制 Git 中的输入
python experiments/cf-concurrency/plot_cpu.py docs/evidence/cf-cpu-20261014
python experiments/cf-concurrency/report_followup.py --chart-only
```

多 IP 与高并发图可由 `analyze_sweep.chart(summary, destination)` 使用 Git 中的 `summary.json` 重绘。SVG 使用固定 hashsalt 且不写当前日期，同一绘图库版本与输入下可检查重复生成结果；不声称不同 Matplotlib 版本字节完全相同。

## 清单与发布

Git 内文件按 LF Git blob 字节校验；附件成员按实际存档字节校验。每个批次的 schema v2 清单分别列出 `files` 和 `archivedFiles`，并引用固定附件名、URL、大小和 SHA256。原 schema 清单不覆盖丢弃，原样存入附件。分析脚本的历史版本与当前维护版本可能不同；原清单的工具哈希对应附件内恢复的历史源码。

Release 使用 `evidence-YYYYMMDD-<topic>-vN` 标签，明确标注开发证据、设为 pre-release 且不设为 Latest。标签必须指向整理后的干净提交。先在 draft 中上传所有附件，发布后从公开 URL 回下载并核对哈希，再将索引状态记为 `published-verified`。若仓库启用了 immutable releases，应在发布前备齐附件。更正使用新版本，不覆盖原附件。Actions artifacts、聊天附件和临时目录不能作为唯一持久存档。

## Git 历史和防回归

只整理尚未推送的提交。已发布历史保持不变；外部 Git bundle 备份保留原始提交。被测 EXE 的提交 SHA 和文件哈希是历史事实，不能机械替换成整理后的提交；[提交映射](evidence/history-rewrite-20261014.json)记录对应关系及源代码子树相等的校验。存档中的源码快照保证原身份仍可追溯。

运行检查：

```powershell
node scripts/check-repository-artifacts.mjs
node scripts/check-repository-artifacts.mjs --base origin/main --head HEAD
```

检查最终树以及指定基线后的每个提交，包括中途加入又删除的文件。默认基线固定为首次整理前的远端提交；push/PR 的独立轻量工作流持续运行，无需触发产品打包流程。

新增二进制默认拒绝；固定样本必须匹配登记路径与 SHA256。超过 256 KiB 提醒，超过 1 MiB 必须登记精确例外。例外文件为 `scripts/repository-artifacts-policy.json`，需说明用途；已发布研究输入和完整许可声明已登记。新增或修改例外需审查，不用修改后缀、Base64 或压缩规避检查。`.gitignore` 只减少误加文件，不能代替历史检查。

本次整理只改变文档、实验资料和维护工具，不改变产品行为；不新增实机编号，LA-037 等既有待验项保持原状态。
