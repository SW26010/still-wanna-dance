# 完整对应源码

正式 Windows amd64 发行必须将 Portable ZIP 与独立的完整对应源码包放在**同一个 GitHub Release**。GitHub 自动生成的 Source code ZIP/TAR 不含依赖源码，不能替代本包。源码入口、工具链、构建参数和所有依赖固定到同一版本，不承诺逐字节可复现 EXE。

正式交付使用 `scripts/package-portable.ps1 -ReleaseTag v1.2.3`，它会生成、重建并验证成对的二进制/源码；下面的 `create` 是只生成源码的低层命令，单独执行不代表已经完成发行校验。完整流程与公开附件检查见[打包说明](packaging.md)。

源码包由干净工作区的版本标签生成：

```powershell
node scripts/release-source.mjs create --tag v1.2.3 --output artifacts
```

脚本拒绝 dirty/untracked、标签不指向 HEAD、模块 replacement、尚未支持的符号链接或子模块。使用 `git archive` 导出提交，忽略本地数据、缓存和未跟踪文件；`.gitattributes` 的 `export-ignore` 排除历史 `experiments/` 和 `docs/evidence/`，原文件仍保留在相同提交的仓库中。保留产品源码、测试、许可、构建脚本，以及内置图表的研究聚合数据和绘图脚本。

按生产入口及相同构建标签重新列出实际编译依赖，仅下载这些模块的完整 `.zip`，放入本地 Go proxy。同时只查询整个固定模块图的 `.mod`、`.info` 元数据和版本清单，支持只读离线解析，不再下载未使用模块的 ZIP。清单中的 `sourceIncluded` 区分源码与仅图元数据；未取得的未使用模块 ZIP 校验和允许为 `null`，不伪造已交付源码，所有实际交付文件仍记录 SHA256。完整源码模块必须有 Go h1，EXE 引用仅元数据模块时拒绝。没有裁剪原模块 ZIP，保证 Go 校验和和原许可完整。一般用途的 Go、Node.js、tar、编译器/代码生成工具及操作系统不随源码包安装。

完整模块 ZIP 也不自动证明“首选修改形式”齐全：SQLite Go 模块省略嵌套的 `vendor_libsqlite3` 生成工具，实际 SQLite C 原稿也需另取。因此 `docs/licenses/source-inputs.json` 固定补充输入的版本、提交、选定路径和哈希：只从 modernc SQLite 固定 Git 提交导出 `vendor_libsqlite3/` 三个文件及 `LICENSE`，另保留对应的 SQLite 3.50.4 原始源码 ZIP。源码包 `upstream/` 保存这两个档案，不再重复交付整个 modernc SQLite 仓库。导出关闭 Git 换行转换，避免 Windows/Linux 生成不同归档。Go 模块 ZIP 内已有 Makefile、生成代码和条件参数；修改 SQLite 原稿时结合补充的生成辅助程序参考这些命令，需另装相应通用 C/Go 生成工具。普通桌面重建使用已交付的 Go 输入，不需要重新生成 SQLite。

Mihomo 已接入桌面节点导入及多出口运行时；[issue #14 验证记录](issue-14-validation.md#实际桌面发行链)记录了实际桌面 EXE 的依赖、对应源码与离线重建证据，以及后续修复的验证范围。桌面生产构建、依赖收集和离线重建统一使用官方 `-tags=no_easytier`，排除 SWD 不支持的 EasyTier 及其 WASM，无需 Fork。发行检查同时核对 EXE 的构建标签和模块身份，每次发布仍须按实际版本重新验证。若将来确需启用 EasyTier，仍须另行核实并交付对应源码；指定 Git SHA 不可读取不等于源码未公开。其他内嵌 `.wasm/.syso/.a/.dll` 的原始源码交付仍须复核，见[专项记录](licenses/README.md)。这不改变 go-lzo 已接受的非阻断风险。

产物 `still-wanna-dance-v1.2.3-corresponding-source.tar.gz` 解压为：

```text
SWD-source/
  BUILD.txt
  source-manifest.json
  source/                  固定提交受控文件，排除历史实验/验收证据（无 .git）
  proxy/                   编译所需完整模块 ZIP + 全图 Go proxy 元数据
  upstream/                生成/嵌入代码的额外原始源码和脚本
```

清单记录提交、标签、工具链、目标、参数、模块版本及 Go h1 校验、补充原始源码来源，每个文件的字节数与 SHA256。清单不记录自身哈希，整个归档另有 SHA256。原始模块 ZIP 不修改。源码和 EXE 对应检查另读取 EXE 的真实 Go build info，不接受只写在外部 JSON 的版本声明；EXE 中的模块必须有实际完整 ZIP，只有元数据不够。

安装 BUILD.txt 中记录的 Go 版本、Node.js 22+、固定 pnpm 和 tar 后，从解压目录执行：

```powershell
node source/scripts/release-source.mjs rebuild --bundle . --output ../SWD-rebuilt.exe
```

重建使用全新模块/编译缓存，`GOPROXY=file:...` 且没有联网 fallback，禁止自动下载工具链、工作区覆盖和隐式 Go 构建参数；以 Windows amd64、CGO=0 和生产参数构建。没有 `.git`，因此显式关闭 VCS 自动标记。重建后核对模块版本、校验和、工具链与目标，确认输入没有被修改。若缺少依赖、必要输入或校验失败则停止。与原 EXE 比较时允许 VCS 标记及构建时间等差异，不要求 EXE SHA256 相同。

许可证风险处理见[许可审计](issue-30-license-audit.md)。`go-lzo` 的固定版本即使进入 EXE，也只保留已接受风险及证据，不自动阻止发布；缺少源码、声明或对应关系仍会失败。

源码生成和独立重建属于交付工具验证，不改变应用运行行为，无需新增实机台账项；条款升级仍跟踪 LA-033。

## Fluent UI 前端源码

源码包增加 `frontend-store/`：由固定 pnpm 的 `fetch --ignore-scripts` 按锁文件收集完整 npm 包（含许可、JS/TS、source maps 和构建依赖），不把带符号链接的 node_modules 打包。支持 Windows/Linux/macOS 的 x64/arm64 构建依赖，所需包的原始内容由 pnpm integrity 和总文件清单共同校验。Go、Node、固定 pnpm 和 Windows PowerShell 属于另行安装的通用工具。

正式源码生成前检查已提交前端与源码一致。独立重建先在临时副本使用该存储执行 `pnpm install --offline --frozen-lockfile --ignore-scripts`，再执行类型检查和 esbuild 重建，比对 JS、CSS、第三方声明的实际字节，然后才构建 Go。原归档及其存储保持只读，缺包、缺资源、版本不符或字节不一致都停止。前端原稿、锁文件、构建脚本与存储随同一源码清单交付，运行 Portable 不需要 Node 或 pnpm。

pnpm v11 的 `projects/` 仅保存构建机项目链接，归档前清除；包内容与索引保留。离线安装在临时存储副本执行，避免向原源码包写入新的项目链接。此清理有 junction 目标保留测试，实际清理后的存储已完成离线安装和重建比对。
