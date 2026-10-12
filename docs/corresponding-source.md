# 完整对应源码

正式 Windows amd64 发行必须将 Portable ZIP 与独立的完整对应源码包放在**同一个 GitHub Release**。GitHub 自动生成的 Source code ZIP/TAR 不含依赖源码，不能替代本包。源码入口、工具链、构建参数和所有依赖固定到同一版本，不承诺逐字节可复现 EXE。

源码包由干净工作区的版本标签生成：

```powershell
node scripts/release-source.mjs create --tag v1.2.3 --output artifacts
```

脚本拒绝 dirty/untracked、标签不指向 HEAD、模块 replacement、尚未支持的符号链接或子模块。使用 `git archive` 导出提交，忽略本地数据、缓存和未跟踪文件；将所选模块图中每个模块的完整 `.zip`、`.mod`、`.info` 放入本地 Go proxy，包括当前桌面未链接但已固定的 Mihomo，以及测试/生成所需模块。没有用 `go mod vendor` 代替完整模块源码。原始许可、生成输入和仓库构建脚本一并保留；一般用途的 Go、Node.js、tar 和操作系统不随源码包复制。

产物 `still-wanna-dance-v1.2.3-corresponding-source.tar.gz` 解压为：

```text
SWD-source/
  BUILD.txt
  source-manifest.json
  source/                  固定提交全部受控文件（无 .git）
  proxy/                   完整模块 ZIP 和 Go proxy 元数据
```

清单记录提交、标签、工具链、目标、参数、模块版本及 Go h1 校验，每个文件的字节数与 SHA256。清单不记录自身哈希，整个归档另有 SHA256。原始模块 ZIP 不修改。源码和 EXE 对应检查另读取 EXE 的真实 Go build info，不接受只写在外部 JSON 的版本声明。

安装 BUILD.txt 中记录的 Go 版本、Node.js 22+ 和 tar 后，从解压目录执行：

```powershell
node source/scripts/release-source.mjs rebuild --bundle . --output ../SWD-rebuilt.exe
```

重建使用全新模块/编译缓存，`GOPROXY=file:...` 且没有联网 fallback，禁止自动下载工具链、工作区覆盖和隐式 Go 构建参数；以 Windows amd64、CGO=0 和生产参数构建。没有 `.git`，因此显式关闭 VCS 自动标记。重建后核对模块版本、校验和、工具链与目标，确认输入没有被修改。若缺少依赖、必要输入或校验失败则停止。与原 EXE 比较时允许 VCS 标记及构建时间等差异，不要求 EXE SHA256 相同。

许可证风险处理见[许可审计](issue-30-license-audit.md)。`go-lzo` 的固定版本即使进入 EXE，也只保留已接受风险及证据，不自动阻止发布；缺少源码、声明或对应关系仍会失败。

源码生成和独立重建属于交付工具验证，不改变应用运行行为，无需新增实机台账项；条款升级仍跟踪 LA-033。
