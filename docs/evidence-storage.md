# 证据与大文件存储约定

Git 保存源码、报告、精简统计、纯矢量 SVG 和清单。完整实验轨迹、逐请求记录、图片、压缩文件及构建产物使用版本化附件。必要的二进制测试样本单独登记，当前为固定的 `internal/cacheproxy/testdata/storage-v2.sqlite` 、`storage-v3.sqlite`、`storage-v4.sqlite` 、`storage-v5.sqlite` 、`storage-v6.sqlite` 、`storage-v7.sqlite` 和 `storage-v8.sqlite`，v2～v7 覆盖旧格式拒绝，v8 覆盖当前格式及全部 14 张表。产品运行所需的 `internal/branding/logo.ico`、`logo-light.ico` 及 EXE 图标资源 `logo_windows_amd64.syso` 另按精确路径、字节数与 SHA256 登记，保证普通离线构建不依赖附件下载。

## 文件归属

| 内容 | 位置 |
| --- | --- |
| 开发验证、实机报告与关键统计 | `docs/validation/`、`docs/acceptance/` 与 `docs/evidence/<batch>/`；历史设计和调查归入 `docs/history/` |
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

仓库文件整理不要求改写 Git 历史。若另行获准整理尚未推送的提交，已发布历史仍保持不变，并在仓库外保留原始 Git bundle。被测 EXE 的提交 SHA 和文件哈希是历史事实，不能机械替换成整理后的提交。2026-10-14 曾进行的历史整理及源代码子树相等校验见[提交映射](evidence/history-rewrite-20261014.json)；存档中的源码快照保证原身份仍可追溯。

运行检查：

```powershell
node scripts/check-repository-artifacts.mjs
node scripts/check-repository-artifacts.mjs --base origin/main --head HEAD
```

检查最终树以及指定基线后的每个提交，包括中途加入又删除的文件。默认基线固定为首次整理前的远端提交；push/PR 的独立轻量工作流持续运行，无需触发产品打包流程。

新增二进制默认拒绝；固定样本必须匹配登记路径与 SHA256。超过 256 KiB 提醒，超过 1 MiB 必须登记精确例外。例外文件为 `scripts/repository-artifacts-policy.json`，需说明用途；已发布研究输入和完整许可声明已登记。新增或修改例外需审查，不用修改后缀、Base64 或压缩规避检查。`.gitignore` 只减少误加文件，不能代替历史检查。

Web UI 的 `app.js` 是离线构建所需的生成产物。重建后按构建清单核对字节数和 SHA256，为新版本追加精确例外，保留历史版本的登记。历史改写后重新检查整个范围，不能只更新最终产物的哈希。

历史扫描按 Git blob 的实际字节数分批读取，避免生成产物随提交积累而耗尽单次输出缓冲；单个大文件仍完整分类，历史中随后删除或替换的文件仍须检查。

`data/media-seed/media-seed.json` 是离线启动所需的产品资源快照，按精确字节数和 SHA256 登记。只包含 MD5、可选大小、歌曲映射及脱敏来源摘要；生成与版本规则见[快照说明](../data/media-seed/README.md)。用户数据库、URL、统计及偏好不得随快照入库。

产品图标例外仅允许 `internal/branding/logo.ico`、`internal/branding/logo-light.ico` 和 `internal/branding/logo_windows_amd64.syso`，仍拒绝其他图片、证据、压缩包和程序文件。同目录 `logo.svg` 是两种主题共用的矢量源，默认浅色、带独立的深色 media 样式；配色与主题选择规则见 [Fluent UI](fluent-ui-migration.md#页面排版)。安装开发依赖 `sharp@0.35.5` 后运行 `node scripts/generate-logo.mjs`，生成深／浅两套 16–256 像素的九档 ICO 并输出字节数及 SHA256；生成器明确选择 media 样式，不依赖构建机器主题。`--check` 只校验 ICO 重建结果，不写文件。随后运行 `go generate ./internal/branding`，由固定版本 `github.com/akavel/rsrc@v0.10.2` 将 `logo.ico` 封装为 Windows amd64 图标资源，不附加 manifest 或执行代码。更新例外记录后审查图案与哈希。正常 Go 构建直接嵌入已登记文件，无需运行生成器。重新生成可能受 SVG 渲染库版本影响，不能只改策略哈希而跳过图案核对。

验证范围按[现行机制](live-acceptance-process.md)判断；文件归类本身不产生新的实机验收要求。

## README 展示素材

`docs/images/` 保存中英文 README 直接引用的产品展示素材。`logo-dark.svg` 从 `internal/branding/logo.svg` 生成，只将已有暗色配色固定启用，品牌图形仍以原文件为准。

三个 PNG（`webui-acceleration.png`、`webui-library.png`、`webui-settings-dark.png`）是当前生产 WebUI 在暗色模式下的浏览器截图，使用虚构歌曲、示例路径和内存状态，不含个人数据，不作为性能或游戏验收证据。为使仓库首页可直接显示，按精确路径、字节数与 SHA256 登记为 `readme-screenshot` 例外；每张不得超过 256 KiB，不放宽其他图片或实验附件的归档规则。

安装 `webui` 依赖并确认前端构建产物为当前版本后，从仓库根目录执行（Node.js 22.18+，本机 Edge，18473 端口未被占用）：

```powershell
./scripts/with-test-temp.ps1 node webui/test-support/readme-screenshots.mts
```

脚本复用浏览器夹具，输出上述四份素材及忽略目录 `artifacts/readme-refresh/` 中的采集结果，不启动程序、游戏或修改 hosts。更新后人工检查图像及示例数据，追加新截图的精确例外，保留历史版本登记供提交历史检查。

内嵌代理面板的唯一归档例外为 internal/dashboard/assets/zashboard.zip：固定原版发布包供 EXE 离线服务，必须匹配精确路径、字节数、SHA256，并随发行附许可与固定源码/依赖。此产品资源不允许任意 ZIP、实验结果或私人配置借用例外。版本更新须同时审阅 release.json、声明和对应源码输入。
