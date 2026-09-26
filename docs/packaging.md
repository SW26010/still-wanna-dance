# Windows portable 打包

构建环境需要 Go 1.25 或更新版本、Git 和 PowerShell；用户只需要 Windows x64。
网页内嵌在 EXE 内，无需 Node.js、Go 或外部运行库。

在仓库目录运行：

```powershell
./scripts/package-portable.ps1 -Version 0.1.0
./scripts/test-portable.ps1 -Zip artifacts/StepStash-0.1.0-windows-amd64-portable.zip
```

打包先运行 `go test ./...` 和 `go vet ./...`，失败即停止。随后以
`GOOS=windows GOARCH=amd64 CGO_ENABLED=0` 构建无命令行窗口的托盘程序，
输出文件夹、ZIP 和 ZIP 的 SHA-256 校验文件。编译后恢复原来的环境变量。
不指定版本时使用 `dev-日期-时间`；`-OutputDir` 可指定输出目录，默认 `artifacts`。
遇到同名产物会报错，不覆盖现有包。

交付文件夹只包含：

```text
StepStash-0.1.0-windows-amd64-portable/
  stepstash-console.exe
  README.txt
  build-info.json
```

不打包本机配置、hosts、歌曲库、日志或缓存。`build-info.json` 记录版本、
提交 SHA、是否存在未提交修改、UTC 构建时间、Go 版本和目标平台。
版本标签不等于 Git tag；本地未提交代码允许打包，并明确标记 dirty。

用户直接双击 `stepstash-console.exe`：程序显示托盘并打开网页控制台，重复启动
会打开已有实例。交付包不需要 CMD / PS1 启动脚本；CDN、hosts 接入及队列预缓存
在控制台中按需启用。仓库的 PowerShell 脚本仅用于开发构建和验收。

启动验证在临时目录解压 ZIP，从不同工作目录启动该 EXE 的无托盘模式，
检查内嵌网页、配置锁、默认数据路径和启动 JSON 日志，再停止该测试进程。
不会启动 CDN、下载歌曲或修改 hosts；解压目录保留以便检查。
此验证不替代真实桌面的托盘和 UAC 验收。
目录保存和整体移动后的配置解析由 Go 回归测试覆盖。

Windows CI 同样生成并验证 ZIP，通过后上传 ZIP 和校验文件为 workflow artifact。
当前检出未配置 Git 远程；实际 CI 是否通过须查看目标仓库的 workflow 运行记录。

仅编译 EXE 可用 `./scripts/build-desktop.ps1`，默认输出 `bin/stepstash-console.exe`。
Windows 默认配置位置为 EXE 旁；使用工作目录配置的旧版本用户可将配置移到 EXE
旁，或用 `-config` 明确指定旧文件。旧配置中的绝对路径继续有效，重新保存时
包内路径转换为相对路径。配置路径可继续使用不代表存储格式兼容。当前统一存储不支持旧版歌曲库或数据库迁移；存储要求见[统一存储](storage.md)。升级和 hosts 恢复操作见交付包的 README.txt。
