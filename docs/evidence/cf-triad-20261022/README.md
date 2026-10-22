# #21 三项统一对照证据

[研究报告](../../research/issue-21-cf-triad-20261022.md)按[公开实验协议](../../../experiments/cf-block-paths/TRIAD.md)回答 HTTP/HTTPS、固定 104 地址名单及 Speedtest 预测力三个问题。两个窗口均已完成，仅覆盖单线路，未覆盖独立网络或并发产品表现。

公开材料仅保留采样日期、粗粒度时段、持续时间和相对先后间隔；绝对采样时间、内部任务来源、网络拓扑及备份操作明细不公开。附件 ZIP 条目的时间也使用固定占位值。

| 文件 | 用途 |
| --- | --- |
| [window-01-summary.json](window-01-summary.json)、[window-02-summary.json](window-02-summary.json) | 逐池成功/失败/未知、协议差异、相关性、Top-k、成本与 W2 冻结预测 |
| [candidates.json](candidates.json)、[predictions-w1.json](predictions-w1.json) | 预先冻结的三个 104 池、随机种子及 W1 选择 |
| [window-01-checks.json](window-01-checks.json)、[window-02-checks.json](window-02-checks.json) | 计划、响应、进度、筛选审计、实际顺序；保留 W1 pilot 偏差 |
| [window-01-binding.json](window-01-binding.json)、[window-02-binding.json](window-02-binding.json) | 实际连接来源和目标核对，仅保留计数与私有文件指纹 |
| [freeze-validation.json](freeze-validation.json)、[final-verification.json](final-verification.json) | 预测冻结时序及最终内容/顺序/版本核对；前者采集于 W2 运行期间，后者覆盖最终记录 |
| [independent-content.json](independent-content.json) | 历史内容与独立视频的切片哈希 |
| [comparison.svg](comparison.svg) | 可重绘的纯矢量对照图 |
| [provenance.json](provenance.json)、[validation.json](validation.json)、[retention.json](retention.json)、[manifest.json](manifest.json) | 构建来源、验证状态、本地保留及文件/附件指纹 |

获得完整私有原件后，可替换以下目录占位符重新分析；绘图只需公开摘要：

```powershell
$evidenceRoot = '<私有原件目录>'
python experiments/cf-block-paths/analyze_triad.py "$evidenceRoot/window-01/triad-private.json" --candidates "$evidenceRoot/candidates.json" --out "$evidenceRoot/recomputed-w1.json"
python experiments/cf-block-paths/analyze_triad.py "$evidenceRoot/window-02/triad-private.json" --candidates "$evidenceRoot/candidates.json" --out "$evidenceRoot/recomputed-w2.json" --holdout "$evidenceRoot/predictions-w1.json"
python experiments/cf-block-paths/verify_triad.py "$evidenceRoot/window-02" --candidates "$evidenceRoot/candidates.json" --version 2 --out "$evidenceRoot/rechecked-w2.json"
python experiments/cf-block-paths/plot_triad.py docs/evidence/cf-triad-20261022
```

分析/校验仅需 Python 标准库；绘图另需 Matplotlib，本轮使用 3.11.2。完整测试从 Windows 临时目录入口运行：

```powershell
./scripts/with-test-temp.ps1 python -m unittest discover -s experiments/cf-block-paths -p '*triad.py' -v
```

当前脱敏附件为 `cf-triad-20261022-v2.zip`，发布状态及哈希见清单。v2 仅收紧公开信息范围，统计结果与原始测量指纹保持不变。它包含相对时间轨迹、两版实验源文件/公开协议副本、分析工具及 `_provenance/base-source.zip`；公开协议副本与私有冻结原版分别记录指纹。完整复算（包括超时细分）及身份/内容复验仍需私有原件。绘图与补充检查由本目录的小文件单独保留；备份仅公开校验状态和必要指纹。
