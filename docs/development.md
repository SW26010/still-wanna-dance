# 开发环境

StepStash 使用 Go，管理 UI 采用本地 HTML/CSS/JavaScript 页面。

控制台页面结构位于 `internal/console/index.html`，样式和脚本分别位于 `internal/console/assets/console.css`、`internal/console/assets/console.js`。三者通过 `go:embed` 编译进可执行文件，无需前端构建步骤或额外分发资源文件；修改后需重新编译。脚本按区域拆分渲染函数，操作请求与刷新调度集中在后半部分。页面注入当前会话 token，静态脚本从页面的 meta 标签读取。

前端刷新和操作超时回归测试使用 Node.js 标准库，运行 `node --test scripts/refresh.test.mjs`。

## Windows 环境

2026-09-25 本机通过现有 Scoop 安装 Go 1.27.1（windows/amd64）。Git 已安装。

在 PowerShell 中检查：

```powershell
go version
go env GOOS GOARCH GOPATH GOROOT GOPROXY GOSUMDB
```

本机安装位置为 `%USERPROFILE%\scoop\apps\go\current`，默认 GOPATH 为 `%USERPROFILE%\go`。Scoop 已配置 Go 命令和用户工具目录的 PATH；旧终端如未识别命令，请重新打开终端，必要时重启编辑器。

保留默认模块代理 `https://proxy.golang.org,direct` 和校验数据库 `sum.golang.org`。无需手动设置 GOROOT，也不必把项目放进 GOPATH。

Go 自带构建、测试、格式化和静态检查工具。当前纯 Go 方案无需 C/C++ 编译器；运行网页 UI 无需 Node.js；执行前端回归和实网验收脚本需要 Node.js，CI 使用 22。

安装后已在独立临时模块中验证：编译并运行 Windows exe、`go test`、`go vet`、HTTP Range 的 206 状态/正文/Content-Range，以及通过默认代理下载并校验依赖。测试模块未加入项目依赖。

## 开发命令

仓库已有 `go.mod`，最低 Go 1.25。SQLite 使用纯 Go 驱动 `modernc.org/sqlite`，依赖版本以 `go.mod` 为准；上面的 Go 1.27.1 是当时的本机环境记录。开发命令：

```powershell
go fmt ./...
go test ./...
go vet ./...
go build -o bin/stepstash.exe ./cmd/stepstash
```

Linux CI 额外运行 `go test -race ./...`；Windows race 检测需要额外的 C 工具链，当前本机没有配置。

临时 Go 探测源码即使放在 Git 忽略的 `artifacts/` 中，仍会被 `go test ./...` 和 `go vet ./...` 发现。独立探测文件应在首行添加 `//go:build ignore` 并空一行；需要执行时用 `go run` 明确列出入口及其辅助源文件，避免多个 `main` 混入同一个包。也可放入独立的工具模块。

入口位于 `cmd/stepstash`，协议、缓存和下载管理位于 `internal/cacheproxy`。运行方法见 [命令行服务手册](service.md)。默认缓存、构建产物及测试覆盖率文件已忽略；测试使用 Go 的独立临时目录，不写入原版缓存或证据目录。安装环境不会启动缓存服务或修改 hosts。

可选实网验收脚本 `scripts/acceptance.mjs` 使用 Node.js 标准库，不增加 Go 服务依赖：

```powershell
go build -o bin/stepstash.exe ./cmd/stepstash
go build -o bin/stepstash-console.exe ./cmd/stepstash-console
node scripts/acceptance.mjs bin/stepstash.exe bin/stepstash-console.exe
node --test scripts/acceptance-protocol.test.mjs scripts/acceptance-request.test.mjs
```

两个可执行文件参数分别默认上述路径。脚本在 `test-runs/acceptance-*` 中建立独立服务库和控制台库，记录两个程序的 SHA256、响应状态/头、字节数、哈希、耗时和进程日志。下载 1343/1344 及控制台独立的 1343 样本（当前合计约 106 MB）。覆盖直接视频冷下载、跨 Host Range 内容命中、重启 HEAD 复用、播放 API 接管后直接返回视频、控制台 Auto 冷/热播放，以及重启后上游不可用时的本地 GET/HEAD/Range 降级和无缓存歌曲的 502。Auto 使用真实选路，不能据此保证每种线路故障或测速组合均已覆盖；故障分支另由 Go 测试覆盖。

可执行文件按组件检查：缺少服务程序仅阻塞服务链路，控制台 Auto 与本地降级仍继续；缺少控制台程序时，服务链路仍继续。两者均缺失时分别记录环境阻塞，不请求上游。

控制台以 `-no-tray -no-open` 启动，需本机 `127.0.0.1:80/443` 空闲；端口被占用时记录环境阻塞，不关闭已有服务。降级通过仅供本次测试的本地失败 SOCKS5 出口触发，断言 `X-StepStash-Fallback: upstream-unavailable` 与缓存内容一致。脚本不改 hosts、不读取原版旧库、不运行原版程序；所有下载和配置均留在测试目录，结束时关闭本次进程与失败出口。

上游预检使用 Node.js `Resolver.resolve4` 直接向配置的 DNS 服务器查询 IPv4，绕过系统 hosts；解析失败不回退系统解析，并过滤回环、内网及本机网卡地址。此查询仍可能受系统 DNS/代理影响，不等同于服务内置 DoH。HTTP/HTTPS 均保留原 Host 与正常 TLS 域名校验，报告记录实际 `remoteAddress`；显式端口的本地请求仍走回环。

上游预检支持 301/302/307/308、HTTP/HTTPS 视频地址及 API 的 HTTPS 升级跳转，保留原始 Location。未知协议/视频格式记为 `upstream-protocol-change`，网络不可达、限流及上游 5xx 记为 `upstream-unavailable`，缺失构建或端口占用记为 `environment`。依赖不满足的链路标为 blocked，其他独立检查继续。`results.json` 的总体 `outcome` 为 passed/failed/blocked，退出码分别为 0/1/2；存在程序检查失败时优先返回 1。blocked 不表示完整验收通过；程序检查失败仍应结合同期上游证据和日志判断，例如下载期间断网或歌曲版本变化，不能只凭退出码归因。

游戏 hosts 辅助脚本保留早期双视频域名接入及固定样本检查，仅用于对应的人工联合验收，不覆盖当前播放 API 接入，详见[验收记录](acceptance.md)。

Web UI 的设计约定、浏览器验收与平台验证边界见 [可访问性验收](webui-accessibility.md)。
