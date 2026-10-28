# 开发环境

Still Wanna Dance 使用 Go，管理 UI 采用 React + TypeScript + Fluent UI React v9，资源编译进 EXE 后在本地浏览器运行。

下载架构以[下载需求与资源管理设计](download-resource-design.md)为 canonical 文档，维护现行规则、实现接口及验证边界。阅读当前实现时从[全局块调度设计](block-scheduler-design.md)开始：需求管理层解释 GET/队列与交接，`internal/blocksched` 统一处理块期限、租约、启动窗口和并发；资源层负责执行与文件一致性。[运行规则](download-concurrency.md)是当前参数依据，[开发记录](validation/block-scheduler-validation.md)按历史顺序保留实现和自动检查，不应将目标设计或早期策略当作现状。

## 功能完成与实机验收

开发者（含 agent）先完成能由自动测试、隔离服务和浏览器判断的验证；只有必须依赖 VRC 播放器、真实游戏事件或游戏内体验的剩余风险才进入[待实机验收台账](live-acceptance-backlog.md)，说明不可替代的原因，并优先合并到已有操作。无需为每项行为改动登记，也不把已通过的代码断言重复交给游戏用户。托盘、跨用户等 Windows 条件单独记录环境缺口，见[非 VRC 分流](validation-followups.md)。按[实机验收机制](live-acceptance-process.md)积攒必要游戏场景；等待期间 agent 继续验证和准备，实际开测后承担采集、脱敏、报告及清理。

前端源码在 `webui/src`；Go 的 `internal/console/webui.html` 只负责外壳和经过 JSON 转义的 bootstrap。`webui/build.mjs` 用 esbuild 生成并提交 `internal/console/assets/webui`、第三方声明和构建清单。普通 Go 构建直接嵌入这些产物；桌面构建脚本用 Node 校验源码和产物哈希，界面改动后必须先重建。前端开发：

```powershell
pnpm --dir webui install --frozen-lockfile
pnpm --dir webui build
pnpm --dir webui check
./scripts/with-test-temp.ps1 pnpm --dir webui test
```

使用 `webui/package.json` 固定的 pnpm 版本。浏览器测试默认使用本机 Edge；CI 安装 Playwright Chromium，测试只调用内存夹具，不修改 hosts 或访问真实上游。截图保存在忽略目录 `artifacts/webui-tests`。Windows 工具测试用 `./scripts/with-test-temp.ps1 node --test scripts/*.test.mjs`。Linux/CI 可直接运行包装器后的原命令。架构、浏览器覆盖和真实系统验收边界见[Fluent 迁移记录](fluent-ui-migration.md)。

## Console 路由与共享引擎

普通管理请求在 [`management.go`](../internal/console/management.go) 选择处理函数后统一校验会话 Token 和 Origin；新增普通管理读取入口应登记在该路由组，POST 操作继续由 `serveAction` 分发。业务处理函数不再重复鉴权。条款操作复用同一校验函数，保留同意前可访问的规则；公开读取、媒体签名、SSE 和 WebSocket 仍由各自入口按现有规则处理。回归从 `ServeHTTP` 验证准入及方法错误，不能只测试校验函数。

共享引擎由 Console 持有，入口集中在 [`engine.go`](../internal/console/engine.go)。任务和网关在已有 `lifecycleMu` 保护的启动流程中调用 `ensureEngineLocked`，调用时不持有状态锁 `mu`，直接使用返回的引擎；任务登记与退出等待仍由各业务生命周期负责。预览通过 `retainEngineRead` 一次取得引擎及释放函数，涵盖首次淘汰前的保护。创建、发布通知与关闭不借用调用方的状态锁；状态读取继续只取得短期快照，不等待引擎 I/O。运行语义以[配置生命周期](settings-lifecycle.md)为准。

## Windows 环境

桌面开发需要 Windows amd64、Go 1.27.3+、Node.js 22+、Git 和 PowerShell。前端依赖使用 `webui/package.json` 固定的 pnpm。Go 最低版本和依赖选择分别以 `go.mod` 与锁文件为准。

2026-10-28 将最低 Go 版本提高到包含当前安全补丁的 1.27.3，并更新 `golang.org/x/net` 至 v0.60.0 及其必要依赖；依据为 [Go 发布记录](https://go.dev/doc/devel/release)、[HTTP/2 公告](https://pkg.go.dev/vuln/GO-2026-6610)和 [X.509 公告](https://pkg.go.dev/vuln/GO-2026-6809)。桌面构建与打包固定使用 `GOTOOLCHAIN=local`，因此 PATH 中的 Go 安装也必须满足最低版本；普通 `go` 命令的自动版本切换不代表构建脚本已使用补丁版。本轮按任务边界未更新 GitHub Actions，其中的 Go 1.25.x 配置仍需单独对齐，不能用于当前发行构建。验证范围见[发布前修复记录](validation/release-readiness-20261028.md)。

在 PowerShell 中检查：

```powershell
go version
go env GOOS GOARCH GOPATH GOROOT GOPROXY GOSUMDB
```

保留默认模块代理 `https://proxy.golang.org,direct` 和校验数据库 `sum.golang.org`。无需手动设置 GOROOT，也不必把项目放进 GOPATH。

Go 自带构建、测试、格式化和静态检查工具。当前纯 Go 方案无需 C/C++ 编译器；运行网页 UI 无需 Node.js；执行前端回归和实网验收脚本需要 Node.js，CI 使用 22。

## 开发命令

SQLite 使用纯 Go 驱动 `modernc.org/sqlite`。开发命令：

```powershell
go fmt ./cmd/... ./internal/...
./scripts/with-test-temp.ps1 go test -tags=no_easytier ./cmd/... ./internal/...
./scripts/with-test-temp.ps1 go vet -tags=no_easytier ./cmd/... ./internal/...
go build -o bin/still-wanna-dance.exe ./cmd/still-wanna-dance
```

Windows 测试统一通过 `scripts/with-test-temp.ps1` 运行，定向检查也只需在原命令前加这个入口。它保持调用目录和参数，每次在忽略目录 `artifacts/tmp/<随机标识>` 创建独立临时目录，仅为本次命令及子进程设置 `TEMP`、`TMP`、`GOTMPDIR`；正常返回或命令报错后恢复环境并清理本次目录，原样返回命令退出码。无法清理时明确警告并保留路径，不影响测试失败的退出码。测试日志和需要保留的报告放在该临时目录之外。打包脚本的开发检查与正式发布准备也使用同一入口。

此入口避开 Windows MXC 默认 `sandbox.{GUID}/AC/Temp` 的重命名权限缺陷（[Microsoft 已确认的问题](https://github.com/microsoft/mxc/issues/1204)），不修改产品文件操作、用户/系统环境变量、ACL 或沙箱设置。换到工作区后 `os.Root.Rename`、硬链接、目录联接、浏览器及子进程仍可能受其他沙箱限制；保留失败证据，按实际权限要求验证，不能把所有失败都归为临时目录问题。强制结束 PowerShell 进程时可能来不及清理，残留目录不复用，也不自动清扫其他调用的目录。

2026-10-19 验证：同一 `TestPublicationAdmissionRanksFullCacheBeforeMerge` 在 MXC 默认临时目录报重命名拒绝，经入口运行通过。`TestAtomicWriteRetainsLastGoodFileAndBoundsWork` 在工作区内仍报 `renameat: The handle is invalid`，沙箱外经同一入口通过。工具回归在沙箱内为 28/29（已有 junction 夹具被拒绝），沙箱外 29/29 通过；新增入口回归覆盖原子替换、参数/工作目录/退出码传递、环境恢复、异常清理及嵌套隔离。原始成功与失败输出保留在忽略目录 `artifacts/windows-test-temp/`，未修改产品代码绕过失败。

Linux CI 额外运行 `go test -race ./...`；Windows race 检测需要额外的 C 工具链；本机已使用独立 w64devkit/GCC 完成下载与本地资源整包检查，执行时显式设置 CGO_ENABLED=1 和 CC，测试命令同样通过上述入口运行。工具及原始结果保存在忽略目录 artifacts/，不依赖全局 PATH。

真实磁盘耗尽是独立的 Windows 环境检查，不在普通测试中自动执行。`scripts/test-storage-full.ps1` 使用 Hyper-V 的虚拟磁盘命令创建新的固定 128 MiB NTFS 镜像；只接受本仓库 `artifacts/` 下的测试二进制和证据目录，校验新磁盘身份及容量后才初始化，并为宿主卷保留至少 512 MiB 空间。测试再次校验卷标记与容量，分别填满块、发布和数据库所在的可丢弃卷，检查错误、关闭、清理及已提交数据；结束后卸载并删除本次镜像，保留日志与 `result.json`。不接受现有磁盘号或镜像路径，不填满日常使用的卷。构建不需管理员权限，挂载阶段需要 Windows 管理员令牌；UAC 未确认时此项仍为未验证。

```powershell
New-Item -ItemType Directory -Force artifacts/storage-full | Out-Null
./scripts/with-test-temp.ps1 go test -tags=no_easytier -c -o artifacts/storage-full/cacheproxy.test.exe ./internal/cacheproxy
# 在本仓库的管理员 PowerShell 中执行已构建的定向测试：
./scripts/test-storage-full.ps1 -TestBinary artifacts/storage-full/cacheproxy.test.exe -EvidenceDirectory artifacts/storage-full
```

临时 Go 探测源码即使放在 Git 忽略的 `artifacts/` 中，仍会被 `go test ./...` 和 `go vet ./...` 发现。独立探测文件应在首行添加 `//go:build ignore` 并空一行；需要执行时用 `go run` 明确列出入口及其辅助源文件，避免多个 `main` 混入同一个包。也可放入独立的工具模块。

CF 专用实验的新构建要求显式指定本地网卡配置，统一绑定 API、媒体、Monitor 与 DoH 出站；配置错误或绑定失效不回退其他网卡。实现仅在实验测试文件中，正式 EXE 不读取此配置。配置、原始地址和个人运行偏好放入已忽略的 `local/`、`artifacts/`，仓库只保留通用实现和规则，详见[实验网卡绑定](../experiments/cf-block-paths/network.md)。

入口位于 `cmd/still-wanna-dance`，协议、本地资源和下载管理位于 `internal/cacheproxy`。运行方法见 [命令行服务手册](service.md)。默认本地存储、构建产物及测试覆盖率文件已忽略；测试使用 Go 的独立临时目录，不写入原版本地视频或证据目录。安装环境不会启动播放网关或修改 hosts。

桌面构建使用 `scripts/build-desktop.ps1 -Version dev-名称`，版本与 UTC 构建时间写入 EXE，源码提交和 dirty 状态取 Go VCS 标记。`& ./bin/SWD.exe -build-info | Out-String` 输出机器可读 JSON 并立即退出，不读取配置、创建数据目录或争用桌面实例；启动日志复用同一身份。正式标签只经发布流程注入，缺失/dirty 的 VCS 不显示为正式发行；无 Git 的对应源码重建独立标为 rebuild。直接 `go build` 仍是开发构建，未注入的时间明确留空。

Windows amd64 的桌面 EXE 图标使用 `internal/branding/logo.ico`。同目录已登记的 `logo_windows_amd64.syso` 由 Go 自动链接，普通 `go build` 与桌面／便携打包均生效。更新 ICO 后运行 `go generate ./internal/branding`，使用固定版本的 [rsrc](https://github.com/akavel/rsrc/tree/v0.10.2) 重新生成资源；正常构建无需安装或下载此工具。图标源、生成步骤与哈希登记见[资源约定](evidence-storage.md#git-历史和防回归)。

2026-10-21 图标验证：普通 `go build -tags=no_easytier` 与桌面构建均通过；用 Win32 资源 API 从两个实际 EXE 读取的九档图标逐字节匹配 `logo.ico`，`PrivateExtractIconsW` 在 16–256 像素九档尺寸均可提取，256 像素提取图已检查。原生图标解码、资源策略的三项回归及生成资源重复构建字节一致性通过。原始核对结果与提取图在忽略目录 `artifacts/exe-icon/`；未检查已有资源管理器窗口的图标缓存刷新行为。

可选实网验收脚本 `scripts/acceptance.mjs` 使用 Node.js 标准库，不增加 Go 服务依赖：

```powershell
go build -o bin/still-wanna-dance.exe ./cmd/still-wanna-dance
go build -o bin/SWD.exe ./cmd/still-wanna-dance-console
node scripts/acceptance.mjs bin/still-wanna-dance.exe bin/SWD.exe bin/data/state/terms.json
node --test scripts/acceptance-protocol.test.mjs scripts/acceptance-request.test.mjs
```

两个可执行文件参数分别默认上述路径。脚本在 `test-runs/acceptance-*` 中建立独立命令行服务库和桌面条款检查配置，记录两个程序的 SHA256、响应状态/头、字节数、哈希、耗时和进程日志。CLI 实网检查下载 1343/1344 样本，覆盖直接视频冷下载、跨 Host Range 内容命中、重启 HEAD 复用和播放 API 返回视频。桌面网关的冷／热播放、上游故障时本地 GET/HEAD/Range 复用和无本地视频时失败，由 Go 受控上游集成测试覆盖；脚本不再调用已移除的独立启动接口，也不宣称执行桌面网关实网验收。

验收前须由操作者在当前 Windows 用户下阅读并确认当前条款，第三个参数传入该 `terms.json`。脚本原样复制密文到独立配置，先启动关闭自动加速的桌面程序，通过门禁验证凭据，并核对内嵌条款与当前源码一致；不会解密、伪造或自动点击同意。失效凭据返回 environment/blocked；缺少桌面程序也会阻塞全部验收，因为不能验证同意。验证成功后才启动服务链路；缺少服务程序阻塞服务链路。

条款保护回归在 Windows 可运行 `./scripts/with-test-temp.ps1 go test ./internal/console -run Terms -count=1`。Windows 使用真实 DPAPI，包含独立子进程读取和同用户搬迁；Linux 测试进程使用临时认证加密替身，生产代码仍明确拒绝非 Windows 凭据保存。这不替代真实跨用户/跨机器测试，验证范围见 [#15 验收记录](validation/issue-15-validation.md)。

桌面条款检查以 `-no-tray -no-open` 启动，关闭自动加速并使用动态回环控制端口，不占用 80/443。脚本不改 hosts、不读取旧库、不运行原版程序；所有下载和配置均留在测试目录，结束时关闭本次进程。

上游预检使用 Node.js `Resolver.resolve4` 直接向配置的 DNS 服务器查询 IPv4，绕过系统 hosts；解析失败不回退系统解析，并过滤回环、内网及本机网卡地址。此查询仍可能受系统 DNS/代理影响，不等同于服务内置 DoH。HTTP/HTTPS 均保留原 Host 与正常 TLS 域名校验，报告记录实际 `remoteAddress`；显式端口的本地请求仍走回环。

上游预检支持 301/302/307/308、HTTP/HTTPS 视频地址及 API 的 HTTPS 升级跳转，保留原始 Location。未知协议/视频格式记为 `upstream-protocol-change`，网络不可达、限流及上游 5xx 记为 `upstream-unavailable`，缺失构建或端口占用记为 `environment`。依赖不满足的链路标为 blocked，其他独立检查继续。`results.json` 的总体 `outcome` 为 passed/failed/blocked，退出码分别为 0/1/2；存在程序检查失败时优先返回 1。blocked 不表示完整验收通过；程序检查失败仍应结合同期上游证据和日志判断，例如下载期间断网或歌曲版本变化，不能只凭退出码归因。

`scripts/game-hosts.ps1` 仅用于人工联合验收：先要求 1343 的 API 热缓存 Range 探测通过，再添加 `api.udon.dance` 映射；Disable 只清理当前验收会话标记的 API 映射。它不会添加两个历史视频域名；旧版操作结果见[验收记录](acceptance/early-service-20260925.md)。日常接入使用 WebUI 或托盘，开发检查不自动执行此脚本。

WebUI 的设计约定、浏览器验收与平台验证边界见 [可访问性验收](webui-accessibility.md)。
