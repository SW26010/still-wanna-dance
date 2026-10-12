# Issue #30 开发验证

## 许可声明与条款（2026-10-12）

- `go test ./internal/legal ./internal/console -run 'TestExplicitVersionRequired|TestTerms' -count=1 -timeout=120s`：通过。首次受限沙箱因 Windows 用户/路径访问返回 Access denied；在正常用户权限、临时配置下重跑通过，没有忽略失败或运行游戏。
- `node --test scripts/terms.test.mjs scripts/setup.test.mjs`：80/80 通过。
- `git diff --check`：通过。GPLv3 全文来自已固定的 Mihomo LICENSE 副本；原根 MIT 文件完整保存在 `LICENSE-MIT`，没有删除第三方声明。
- 条款版本改为 `2026-10-12.1`，旧 CLI 版本明确拒绝；已有版本/摘要门禁与确认持久化回归通过。真实升级、关于页及重新确认继续待实机验收 **LA-033**，不以自动测试结项。

本次只做代码、文件及本地自动检查。既有授权和隔离构建证据见[许可审计](issue-30-license-audit.md)；go-lzo 是维护者接受的非阻断风险，不是已确认兼容。

## 完整对应源码工具（2026-10-12）

- `node --test scripts/release-source.test.mjs`：5/5 通过，包括临时 Git 仓库导出、无 Git 元数据/全新 Go 缓存重建 Windows EXE，以及 dirty、错误 tag、损坏/缺失/额外源码、错误二进制身份和版本的拒绝。接受固定 go-lzo 模块不会触发拒绝。
- 首次受限沙箱不能创建临时 `.git/config`，改为正常用户权限运行通过。测试只操作独立临时仓库，不推送，也未在本仓库创建发行 tag。
- 此阶段测试为小型构建夹具；实际 SWD 完整源码包与正式交付链验证在后续记录，不能以夹具测试代替。
- 仅新增交付工具、文档和自动测试，不改变应用运行行为；不新增实机项，LA-033 保持待验收。

## 真实源码重建与依赖声明（2026-10-12）

- 在独立临时本地克隆为 `632dd8c2c2743c3f8e7bfe14ba4383aa4aaf177a` 创建测试 tag `v0.0.0-issue30-source-check`，未在主仓库创建 tag 或发布。源码包包含 194 个模块、1328 个清单文件，SHA256 `18e63eb6136b1e3e39b75d2725393d37764c158c04170ab4ce1f194fd9351f63`。
- 解压无 `.git` 的源码包，Go 1.27.1、Windows amd64、CGO=0、全新模块及编译缓存、仅包内 file GOPROXY 重建成功；EXE SHA256 `2fab37730c1f671c558e950f1ae947e1049af913d0916d9618841c415c99d131`。Go 的 downloading 日志在此指向本地 proxy，不是网络下载。11 个实际二进制模块均匹配清单；当前桌面仍未链接 Mihomo/go-lzo。
- `node --test scripts/release-notices.test.mjs`：2/2 通过，覆盖声明被删、未登记模块、版本/校验变更拒绝及已接受 go-lzo 允许；真实重建 EXE 的声明覆盖校验通过。
- 保留原有声明，增加固定桌面/adapter 并集 97 模块及原文件 SHA256；展示文本仅统一 LF、去除行尾空白，原模块 ZIP 字节不改。原文件哈希指向上游字节，汇总声明哈希指向展示文本，两者用途不同。

## 正式发行门禁实现（2026-10-12）

- `node --test scripts/*.test.mjs`：227/227 通过。新增发行负例覆盖缺源码、资产损坏、非法 URL/名称、错误 tag/commit、缺少独立重建记录，以及公开源码返回 404。网络用例使用受控响应，不冒充已发布 GitHub 资产验证。
- `go test ./cmd/... ./internal/...`、`go vet ./cmd/... ./internal/...`：全部通过（正常 Windows 用户权限）。没有对包含忽略实验源码的整个工作目录宣称 `go test ./...` 通过。
- 正式流程固定 tag/commit，从干净独立检出构建并核对实际 EXE；生成源码附件、SOURCE 入口、哈希发行清单和 Release notes，独立重建后执行真实 Portable ZIP 隔离 smoke。开发包明确标记 development。tag CI 接入同一正式流程，代码不执行 push 或 GitHub Release 发布。
- 本段记录实现及回归通过；真实完整正式链验证结果另记。公开附件当前未发布，公开可用性、Mihomo 接入后的最终桌面 EXE、LA-033 仍未验收，#30 不据此关闭。
- 交付工具和 CI 变化不改变应用运行行为，因此不新增实机项；已有 LA 项保持不变。

## 完整链首次失败及修正（2026-10-12）

- `4c98d03` 的正式构建和独立重建通过，真实 ZIP smoke 在旧 `id="consent"` 断言失败，发行输出未被交付。检查当前 `serveConsolePage` 确认门禁已改为引导页；测试现改查 `setupAcceptTerms` 与服务器渲染的 `data-view="setup"`，继续断言 API 428、条款正文一致和未生成媒体/凭据。复用该实际测试 EXE、新建纯交付 ZIP 重跑：完整 Portable smoke 通过；没有靠接受条款绕过失败。
- 深入源码检查发现模块 ZIP 不含所有生成输入：补充 SQLite 固定提交完整树和 SQLite 3.50.4 C 原稿；将源码包收窄为当前生产编译需要的完整 ZIP 与全图元数据，避免分发无关预编译 WASM。已知未来 EasyTier 内嵌 WASM 的原始源码缺口单独 fail closed，详见[原始源码记录](licenses/README.md)。这没有更改 go-lzo 的风险接受决定。
- 源码/声明/发行专项测试共 11/11 通过，新增 SQLite 版本变更必须复核、EasyTier 缺源码拒绝、go-lzo 未决许可仍允许的负例。
- 补充源码下载实测中，Node 对 SQLite ZIP 的 GET 长时间无响应（HEAD 可达）；停止本轮该下载子进程，Windows 下载改用已验证可用的 `Invoke-WebRequest`，固定 SHA256 校验仍强制执行。新下载路径实测通过，不复用未校验响应，也未将失败构建输出为正式包。
