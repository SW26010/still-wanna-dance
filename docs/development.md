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

可选实网验收脚本 `scripts/acceptance.mjs` 使用 Node.js 标准库，不增加 Go 服务依赖；它接收 StepStash 可执行文件路径（默认 `bin/stepstash.exe`），在隔离的统一存储中下载 1343/1344 样本，检查跨 Host 命中与重启复用，并将报告保存到已忽略的 `test-runs`。不读取原版旧库，也不运行原版程序。脚本当前要求 API 返回 HTTP 302 和 HTTP 视频地址；上游协议变化可能使它失败，不能据此直接判定缓存服务故障。游戏 hosts 辅助脚本保留早期双视频域名接入及固定样本检查，仅用于对应的人工联合验收，不覆盖当前播放 API 接入，详见[验收记录](acceptance.md)。
