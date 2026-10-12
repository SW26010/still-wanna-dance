# 完整对应源码

正式 Windows amd64 发行必须将 Portable ZIP 与独立的完整对应源码包放在**同一个 GitHub Release**。GitHub 自动生成的 Source code ZIP/TAR 不含依赖源码，不能替代本包。源码入口、工具链、构建参数和所有依赖固定到同一版本，不承诺逐字节可复现 EXE。

正式交付使用 `scripts/package-portable.ps1 -ReleaseTag v1.2.3`，它会生成、重建并验证成对的二进制/源码；下面的 `create` 是只生成源码的低层命令，单独执行不代表已经完成发行校验。完整流程与公开附件检查见[打包说明](packaging.md)。

源码包由干净工作区的版本标签生成：

```powershell
node scripts/release-source.mjs create --tag v1.2.3 --output artifacts
```

脚本拒绝 dirty/untracked、标签不指向 HEAD、模块 replacement、尚未支持的符号链接或子模块。使用 `git archive` 导出提交，忽略本地数据、缓存和未跟踪文件。对生产入口重新列出实际编译依赖，将所需模块的完整 `.zip` 放入本地 Go proxy，同时保留整个固定模块图的 `.mod`、`.info` 和版本清单，以支持只读离线解析。清单中的 `sourceIncluded` 区分源码与仅图元数据；未用于当前桌面的模块不作为额外源码负载分发。没有用 `go mod vendor` 代替完整模块源码。一般用途的 Go、Node.js、tar、编译器/代码生成工具及操作系统不随源码包安装。

完整模块 ZIP 也不自动证明“首选修改形式”齐全：SQLite Go 模块省略嵌套的 `vendor_libsqlite3` 生成工具，实际 SQLite C 原稿也需另取。因此 `docs/licenses/source-inputs.json` 固定补充输入的版本、提交和哈希：modernc SQLite 上游完整 Git 快照（含嵌套模块/生成脚本）与对应的 SQLite 3.50.4 原始源码 ZIP。源码包 `upstream/` 保存这些原始档案；Go 模块内生成命令及条件参数保留。修改 SQLite 原稿时参考原快照的 Makefile、`vendor_libsqlite3/main.go` 和生成 Go 文件头部命令，需另装相应通用 C/Go 生成工具。普通桌面重建使用已交付的 Go 输入，不需要重新生成 SQLite。

当前 Mihomo 尚未接入桌面。接入后源码集自动扩展，但发现的内嵌 `.wasm/.syso/.a/.dll` 若没有原始源码交付复核会失败；已知 EasyTier WASM 的上游来源缺口见[专项记录](licenses/README.md)。这属于完整对应源码要求，不是重新将 go-lzo 授权疑点设为阻断。

产物 `still-wanna-dance-v1.2.3-corresponding-source.tar.gz` 解压为：

```text
SWD-source/
  BUILD.txt
  source-manifest.json
  source/                  固定提交全部受控文件（无 .git）
  proxy/                   编译所需完整模块 ZIP + 全图 Go proxy 元数据
  upstream/                生成/嵌入代码的额外原始源码和脚本
```

清单记录提交、标签、工具链、目标、参数、模块版本及 Go h1 校验、补充原始源码来源，每个文件的字节数与 SHA256。清单不记录自身哈希，整个归档另有 SHA256。原始模块 ZIP 不修改。源码和 EXE 对应检查另读取 EXE 的真实 Go build info，不接受只写在外部 JSON 的版本声明；EXE 中的模块必须有实际完整 ZIP，只有元数据不够。

安装 BUILD.txt 中记录的 Go 版本、Node.js 22+ 和 tar 后，从解压目录执行：

```powershell
node source/scripts/release-source.mjs rebuild --bundle . --output ../SWD-rebuilt.exe
```

重建使用全新模块/编译缓存，`GOPROXY=file:...` 且没有联网 fallback，禁止自动下载工具链、工作区覆盖和隐式 Go 构建参数；以 Windows amd64、CGO=0 和生产参数构建。没有 `.git`，因此显式关闭 VCS 自动标记。重建后核对模块版本、校验和、工具链与目标，确认输入没有被修改。若缺少依赖、必要输入或校验失败则停止。与原 EXE 比较时允许 VCS 标记及构建时间等差异，不要求 EXE SHA256 相同。

许可证风险处理见[许可审计](issue-30-license-audit.md)。`go-lzo` 的固定版本即使进入 EXE，也只保留已接受风险及证据，不自动阻止发布；缺少源码、声明或对应关系仍会失败。

源码生成和独立重建属于交付工具验证，不改变应用运行行为，无需新增实机台账项；条款升级仍跟踪 LA-033。
