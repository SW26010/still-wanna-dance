# Windows Portable 打包

构建需要 Go 1.25 或更新版本、Node.js（CI 使用 22）、Git 和 PowerShell；用户只需要 Windows x64。网页和 SQLite 编译进 EXE，无需外部运行库。

```powershell
./scripts/package-portable.ps1 -Version dev-local
./scripts/test-portable.ps1 -Zip artifacts/still-wanna-dance-dev-local-windows-amd64-portable.zip

# 正式产物：已有 v1.2.3 tag 必须指向当前干净 HEAD；不自动创建 tag 或发布
./scripts/package-portable.ps1 -ReleaseTag v1.2.3
```

开发包先运行依赖无关的前端源码/资源清单校验（`scripts/check-webui.mjs`）、`go test -tags=no_easytier ./cmd/... ./internal/...`、`go vet -tags=no_easytier ./cmd/... ./internal/...`，失败即停止。正式包在独立干净检出中运行全部 Node 测试和上述 Go 检查，排除原目录中被 Git 忽略的额外代码/嵌入资源。构建使用 `GOOS=windows GOARCH=amd64 GOAMD64=v1 CGO_ENABLED=0`、`-tags=no_easytier -trimpath -mod=readonly -ldflags '-s -w -H=windowsgui'`；不继承 GOFLAGS、工作区覆盖或 Go 实验配置，不自动下载工具链。

ZIP 名允许带版本；ZIP 内固定为：

```text
SWD/
  SWD.exe
  README.txt
  build-info.json
  licenses/
    LICENSE
    LICENSE-MIT
    LICENSING.md
    TERMS.txt
    THIRD-PARTY-NOTICES.txt
    SOURCE.txt
    SOURCE.json
```

不交付 data、空媒体目录、本机配置、hosts、日志或缓存。README 暂时保留，按 #23/#26 的分工，等 #16 内置帮助可用后由 #26 取消；构建信息的交付简化也留给 #26。应用数据位置已经固定，完整树、生命周期及备份范围以[统一存储](storage.md)为准。

产物暂存于 `artifacts/still-wanna-dance-<版本>-windows-amd64-portable/SWD/`，ZIP 和 SHA-256 文件位于 artifacts 根。不指定版本时采用 `dev-日期-时间`；`-OutputDir` 可指定输出目录。遇到同名产物报错，不覆盖已有包。

`build-info.json` 记录包类型、版本、提交 SHA、dirty 状态、UTC 构建时间、Go 版本及目标平台。`-Version dev-* / ci-*` 允许本地未提交代码，类型明确为 development，不声称存在正式源码下载。形如 `1.2.3`/`v1.2.3` 的版本必须改用 `-ReleaseTag`，禁止把 dirty 开发包冒充正式版本。

正式包同时生成独立的 `*-corresponding-source.tar.gz` 及 SHA256、`*-release.json` 和可用作 Release 正文的 `*-release-notes.md`。完整源码不放进 Portable。`licenses/SOURCE.txt` 给出该 tag 的准确源码附件 URL、提交、校验及构建入口；`SOURCE.json` 将 EXE 哈希与源码哈希绑定，发行清单再绑定 ZIP/源码资产哈希和实际 EXE 模块。详见[完整对应源码](corresponding-source.md)。

正式流程用最终源码附件进行全新缓存、仅包内 file GOPROXY 的独立重建，比较实际二进制模块；再运行真实 ZIP 的 Portable 隔离验证，全部成功才复制到输出目录。缺少 notices、源码、构建脚本、错误 tag/commit、dirty 或版本/哈希不符均失败。`go-lzo` 固定版本的[已接受风险](issue-30-license-audit.md)保留在产物记录中，不作为失败条件。Mihomo 已接入桌面；发行链自动验证不替代 LA-034/035/036 的真实桌面、代理和游戏验收，实际覆盖版本见[开发验证记录](issue-14-validation.md)。

已有产物可再次独立验证：

```powershell
node scripts/release.mjs verify --manifest artifacts/still-wanna-dance-v1.2.3-release.json
```

官方默认在同一 GitHub Release 同时上传 Portable、对应源码、两个校验文件与发行清单，并将生成的 Release notes 中源码入口置于二进制下载旁。发布后、对外宣布前，从无凭据公共 URL 检查实际附件：

```powershell
node scripts/release.mjs verify-published --manifest artifacts/still-wanna-dance-v1.2.3-release.json
```

此命令确实下载两个公开附件、核对哈希和对应关系并独立重建；任一附件 404/403/内容不符都会失败。构建前附件尚未发布，不能把本地校验当作公开可用性通过。若公开检查失败，先停止提供受影响的二进制下载，修正源码交付再重新核验。镜像需同等、免费、持续地提供匹配源码并保留声明；离线再分发默认同时附完整对应源码，不能只依赖在线链接。GitHub 自动源码快照不替代本附件。

`licenses/LICENSE` 为 GPLv3 全文，`licenses/LICENSE-MIT` 保留历史 MIT 授权与版权；自有新贡献采用 GPL-3.0-only，第三方许可不改写。`licenses/THIRD-PARTY-NOTICES.txt` 包含直接/间接依赖及内嵌组件许可全文；打包追加实际 Go 工具链声明。脚本从 EXE 模块版本校验声明覆盖，未登记版本或 replacement 会阻止打包。更新依赖需核对实际许可，不能只改声明中的版本号。单独分发 EXE 也应附带必要许可。

用户双击 SWD.exe 显示托盘并打开网页；同一实际 EXE 和配置重复启动才打开已有实例，不同安装或配置显示冲突及归属。无需 CMD / PS1 启动脚本。仅编译可执行 `./scripts/build-desktop.ps1`，默认输出 `bin/SWD.exe`。

默认配置为 EXE 旁的 `data/config.json`，首次成功打开即保存 schemaVersion=3。程序日志为 `data/logs/app.log`，默认媒体为 `data/storage`，未使用时不预建。自定义 `-config`、多实例隔离、相对路径及外置库约定见[统一存储](storage.md)。无版本和版本 1、2 测试配置及旧库不迁移。

覆盖升级先退出并备份，然后只将 ZIP 的 SWD/ 内交付文件覆盖到原安装目录；不覆盖 data、外置库或未知文件。ZIP 内层路径不随版本变化，不能将新解压的另一目录当作原实例启动。详见[交付说明](portable-readme.txt)。

自动验证解压真实 ZIP，从不同工作目录启动无托盘 EXE，检查固定路径、许可及模块覆盖、未用目录不生成、条款门禁、配置锁、JSON 日志、重复实例拒绝及强制停止后的重启。首次启动、重启、移动和自定义配置均断言身份 API 中的 SWD 协议、实际 EXE、配置、PID 及 OS 进程创建时间。随后覆盖解压，逐文件比较用户数据哈希；整目录移动后再启动，并验证同父目录的两个自定义配置并发启动且锁/日志/默认媒体隔离。另验证已知旧测试布局在创建 data 前被拒绝。Go 回归补充确认记录、通道密钥、快照的移动、外置绝对路径和旧存储标记保护。

验证不主动接受条款、不启动播放网关、不下载歌曲、不改 hosts，不替代真实 VRChat、托盘及 UAC 联合验收；测试退出只终止自己创建的进程，保留隔离目录供检查。

Windows CI 在 tag 上运行正式流程，并上传 ZIP、源码、校验、发行清单及 Release notes；手动选择分支时生成明确的 ci 开发包。工作流只在手动选择 `check` → `Run workflow` 或推送 `v[0-9]*` tag 时运行，普通分支 push/PR 不自动运行。脚本和 CI 不发布 GitHub Release；首次正式发布仍须完成 #23、#24、#25、#26、#30 的适用验收，包括 LA-033、Mihomo 实际接入后的产物复验及公开下载验证。

## 2026-10-11 #23 / #27 本地验收

后续 #24 运行身份收口及便携包验证见 [issue #24 本地验证](issue-24-validation.md)。以下保留当次 #23 / #27 记录。

在 Windows amd64、Go 1.27.1 上验证最终代码 `798df22`（打包时待提交项仅为文档）：

- `go test ./cmd/... ./internal/...` 全部通过，`go vet ./cmd/... ./internal/...` 通过。使用正式包路径排除本地 artifacts 中历史实验 Go 源码；没有将整个含实验产物的工作目录宣称为 `go test ./...` 通过。
- `node --test scripts/*.test.mjs`：128 项通过。打包脚本自身再次执行其中 120 项 UI 测试及完整 Go 包测试/静态检查。
- `package-portable.ps1 -Version issue23-27-final` 成功；本地 ZIP 的 SHA-256 为 `64a51a80fb9847d433250378e43f225f7d4274c75cc65d79e2b83fdf2e0a2c50`。仅本地产物，没有推送、发布或上传。
- `test-portable.ps1` 对该 ZIP 实测通过：固定 SWD/ 交付、按需生成目录、独立工作目录、条款门禁、配置锁、重复实例拒绝、进程终止后重启、同格式覆盖时用户数据哈希保持、整体移动、同父目录自定义配置并发隔离、旧布局拒绝。实际 app.log 的时间为 UTC Z。
- 自动化回归另覆盖固定 SQLite/配置/快照样本、缺表/索引/新增列拒绝、未来/旧版本拒绝、缺失数据库不重建、事务失败回滚、纳秒查询竞争及时间回拨、CurrentUser DPAPI 同用户移动、篡改/未知封套保留、强制代理失败而非直连、显式凭据重配及保存失败不丢原密文。

Windows 受限沙箱中的临时文件重命名曾返回 Access denied；最终结果来自正常用户权限的本地测试，并非忽略那些失败。跨用户/普通跨机器的真实 DPAPI 环境、真实断电/磁盘耗尽、VRChat 音画、托盘交互及 UAC 联合测试本轮未执行。旧试验配置/数据库无迁移，便携覆盖验证不代表跨格式升级支持；#15 法律凭据防伪及 #24–#26 各自剩余范围仍独立推进。

### Fluent UI 构建检查

界面源码修改后先按开发指南重建并运行浏览器测试。CI 校验完整前端构建一致性并执行浏览器场景，测试范围及最新结果统一见 [UI 验证记录](fluent-ui-migration.md#验证与待验收)；开发包可直接使用已提交资源，只需 Node 做哈希校验。正式对应源码包额外要求固定 pnpm，并收集 npm 源码存储、验证前端完全离线重建。Portable 第三方声明包括 `docs/licenses/WEBUI-NOTICES.txt`。`test-portable.ps1` 在未确认条款的隔离进程检查新 bootstrap、JS/CSS 和 API 门禁，仍不自动接受条款或启用游戏链路。
