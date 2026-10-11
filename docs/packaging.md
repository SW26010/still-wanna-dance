# Windows Portable 打包

构建需要 Go 1.25 或更新版本、Node.js（CI 使用 22）、Git 和 PowerShell；用户只需要 Windows x64。网页和 SQLite 编译进 EXE，无需外部运行库。

```powershell
./scripts/package-portable.ps1 -Version 0.1.0
./scripts/test-portable.ps1 -Zip artifacts/still-wanna-dance-0.1.0-windows-amd64-portable.zip
```

打包先运行三个前端测试文件（refresh、navigation、terms）、`go test ./cmd/... ./internal/...`、`go vet ./cmd/... ./internal/...`，失败即停止。随后以 `GOOS=windows GOARCH=amd64 CGO_ENABLED=0` 构建无命令行窗口的托盘程序，并恢复原环境变量。

ZIP 名允许带版本；ZIP 内固定为：

```text
SWD/
  SWD.exe
  README.txt
  build-info.json
  licenses/
    LICENSE
    TERMS.txt
    THIRD-PARTY-NOTICES.txt
```

不交付 data、空媒体目录、本机配置、hosts、日志或缓存。README 暂时保留，按 #23/#26 的分工，等 #16 内置帮助可用后由 #26 取消；构建信息的交付简化也留给 #26。应用数据位置已经固定，完整树、生命周期及备份范围以[统一存储](storage.md)为准。

产物暂存于 `artifacts/still-wanna-dance-<版本>-windows-amd64-portable/SWD/`，ZIP 和 SHA-256 文件位于 artifacts 根。不指定版本时采用 `dev-日期-时间`；`-OutputDir` 可指定输出目录。遇到同名产物报错，不覆盖已有包。

`build-info.json` 记录版本、提交 SHA、dirty 状态、UTC 构建时间、Go 版本及目标平台。版本标签不等于 Git tag；允许打包未提交代码，并明确标记 dirty。

`licenses/LICENSE` 为项目 MIT 许可证。`licenses/THIRD-PARTY-NOTICES.txt` 包含直接/间接依赖及内嵌组件许可全文；打包追加实际 Go 工具链声明。脚本从 EXE 模块版本校验声明覆盖，未登记版本或 replacement 会阻止打包。更新依赖需核对实际许可，不能只改声明中的版本号。单独分发 EXE 也应附带必要许可。

用户双击 SWD.exe 显示托盘并打开网页；重复启动打开已有实例。无需 CMD / PS1 启动脚本。仅编译可执行 `./scripts/build-desktop.ps1`，默认输出 `bin/SWD.exe`。

默认配置为 EXE 旁的 `data/config.json`，首次成功打开即保存 schemaVersion=2。程序日志为 `data/logs/app.log`，默认媒体为 `data/storage`，未使用时不预建。自定义 `-config`、多实例隔离、相对路径及外置库约定见[统一存储](storage.md)。无版本和版本 1 测试配置及旧库不迁移。

覆盖升级先退出并备份，然后只将 ZIP 的 SWD/ 内交付文件覆盖到原安装目录；不覆盖 data、外置库或未知文件。ZIP 内层路径不随版本变化，不能将新解压的另一目录当作原实例启动。详见[交付说明](portable-readme.txt)。

自动验证解压真实 ZIP，从不同工作目录启动无托盘 EXE，检查固定路径、许可及模块覆盖、未用目录不生成、条款门禁、配置锁、JSON 日志、重复实例拒绝及强制停止后的重启。随后覆盖解压，逐文件比较用户数据哈希；整目录移动后再启动，并验证同父目录的两个自定义配置并发启动且锁/日志/默认媒体隔离。另验证已知旧测试布局在创建 data 前被拒绝。Go 回归补充确认记录、通道密钥、快照的移动、外置绝对路径和旧存储标记保护。

验证不主动接受条款、不启动 CDN、不下载歌曲、不改 hosts，不替代真实 VRChat、托盘及 UAC 联合验收；测试退出只终止自己创建的进程，保留隔离目录供检查。

Windows CI 同样生成并验证 ZIP，再上传 ZIP 和校验文件为 workflow artifact。工作流只在手动选择 `check` → `Run workflow` 或推送 `v[0-9]*` tag 时运行，普通分支 push/PR 不自动运行。脚本不发布 GitHub Release；首次正式发布仍须完成 #23、#24、#25、#26 的适用验收。
