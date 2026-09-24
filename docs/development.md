# 开发环境

StepStash 使用 Go，后续管理 UI 计划采用本地 HTML/CSS/JavaScript 页面。

## Windows 环境

2026-09-25 本机通过现有 Scoop 安装 Go 1.27.1（windows/amd64）。Git 已安装。

在 PowerShell 中检查：

```powershell
go version
go env GOOS GOARCH GOPATH GOROOT GOPROXY GOSUMDB
```

本机安装位置为 `%USERPROFILE%\scoop\apps\go\current`，默认 GOPATH 为 `%USERPROFILE%\go`。Scoop 已配置 Go 命令和用户工具目录的 PATH；旧终端如未识别命令，请重新打开终端，必要时重启编辑器。

保留默认模块代理 `https://proxy.golang.org,direct` 和校验数据库 `sum.golang.org`。无需手动设置 GOROOT，也不必把项目放进 GOPATH。

Go 自带构建、测试、格式化和静态检查工具。当前纯 Go 方案无需 C/C++ 编译器；简单网页 UI 无需 Node.js。Python 仅在使用额外测试脚本时按需安装。

安装后已在独立临时模块中验证：编译并运行 Windows exe、`go test`、`go vet`、HTTP Range 的 206 状态/正文/Content-Range，以及通过默认代理下载并校验依赖。测试模块未加入项目依赖。

## 后续开发命令

仓库已有 `go.mod`，最低 Go 1.25。服务仅使用标准库；Windows 本机目前为 Go 1.27.1。开发命令：

```powershell
go fmt ./...
go test ./...
go vet ./...
go build -o bin/stepstash.exe ./cmd/stepstash
```

Linux CI 额外运行 `go test -race ./...`；Windows race 检测需要额外的 C 工具链，当前本机没有配置。

入口位于 `cmd/stepstash`，协议、缓存和下载管理位于 `internal/cacheproxy`。运行方法见 [MVP 手册](mvp.md)。默认缓存、构建产物及测试覆盖率文件已忽略；测试使用 Go 的独立临时目录，不写入原版缓存或证据目录。安装环境不会启动缓存服务或修改 hosts。

可选实网验收脚本 `scripts/acceptance.mjs` 使用 Node.js 标准库，不增加 Go 服务依赖；它需要明确指定旧库和原版程序，下载样本并保存到已忽略的 `test-runs`。游戏 hosts 辅助脚本仅用于人工联合验收，详见[验收记录](acceptance.md)。
