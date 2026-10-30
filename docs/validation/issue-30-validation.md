# Issue #30 开发验证

2026-10-30 许可结论更正：下文 go-lzo「未决／已接受风险」是历史审计记录，已由[上游 GPL-2.0-or-later 澄清](../issue-30-license-audit.md#go-lzo版本选项已由上游澄清)替代。原验证结果与产物记录按当时范围保留。

本轮发行工具回归 **49/49** 通过，覆盖新发行空风险列表、旧发行准确风险记录的兼容读取，以及未登记或被修改记录的拒绝；前端资源一致性与 `git diff --check` 通过。发布说明改为中文下载、运行和文档入口，日志保留于忽略目录 `artifacts/release-notes-20261030/tool-tests.log`。本轮仅更正文案和发行元数据规则，未重新编译或替换已发布附件。

## 许可声明与条款（2026-10-12）

- `go test ./internal/legal ./internal/console -run 'TestExplicitVersionRequired|TestTerms' -count=1 -timeout=120s`：通过。首次受限沙箱因 Windows 用户/路径访问返回 Access denied；在正常用户权限、临时配置下重跑通过，没有忽略失败或运行游戏。
- `node --test scripts/terms.test.mjs scripts/setup.test.mjs`：80/80 通过。
- `git diff --check`：通过。GPLv3 全文来自已固定的 Mihomo LICENSE 副本；原根 MIT 文件完整保存在 `LICENSE-MIT`，没有删除第三方声明。
- 条款版本改为 `2026-10-12.1`，旧 CLI 版本明确拒绝；已有版本/摘要门禁与确认持久化回归通过。真实升级、关于页及重新确认继续待实机验收 **LA-033**，不以自动测试结项。

本次只做代码、文件及本地自动检查。既有授权和隔离构建证据见[许可审计](../issue-30-license-audit.md)；go-lzo 是维护者接受的非阻断风险，不是已确认兼容。

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
- 深入源码检查发现模块 ZIP 不含所有生成输入：补充 SQLite 固定提交完整树和 SQLite 3.50.4 C 原稿；将源码包收窄为当前生产编译需要的完整 ZIP 与全图元数据，避免分发无关预编译 WASM。已知未来 EasyTier 内嵌 WASM 的原始源码缺口单独 fail closed，详见[原始源码记录](../licenses/README.md)。这没有更改 go-lzo 的风险接受决定。
- 源码/声明/发行专项测试共 11/11 通过，新增 SQLite 版本变更必须复核、EasyTier 缺源码拒绝、go-lzo 未决许可仍允许的负例。
- 补充源码下载实测中，Node 对 SQLite ZIP 的 GET 长时间无响应（HEAD 可达）；停止本轮该下载子进程，Windows 下载改用已验证可用的 `Invoke-WebRequest`，固定 SHA256 校验仍强制执行。新下载路径实测通过，不复用未校验响应，也未将失败构建输出为正式包。

## 最终本地发行链（2026-10-12）

被测实现提交：`11d8aa5ac49362cab18e708763050e9d8a0ebbea`。在独立临时本地克隆创建 `v0.0.0-issue30-validation`，主仓库没有该 tag，没有 push、上传或创建 GitHub Release。产物仅保留在忽略目录 `artifacts/issue30/release-check/`，不是已发布版本。

正式流程完整通过：独立干净检出的全部 Node 回归、`go test ./cmd/... ./internal/...`、`go vet ./cmd/... ./internal/...`、源归档/哈希核验、生产参数构建、源码独立重建、真实 ZIP 的 Portable smoke。公开下载测试仍仅为受控响应负例，未伪称公开资产已存在。

| 产物 | 字节数 | SHA256 |
| --- | ---: | --- |
| Portable ZIP | 6,874,687 | `405ae33cb32b3e9b5a58f134a76150f14d42caf87fb184e1a33f26ef7c7de105` |
| 完整对应源码 tar.gz | 115,091,522 | `af0613fd4195c8ef5e01ad3ea9f0699b0b142c0415f3c0654367f2114cab9a41` |
| 原始生产参数 EXE | — | `53871339cc263e5ad6b745143ac9b6b15a96e54c76a8a681ce5d7455e4586614` |
| 独立重建 EXE（无 VCS 标记） | — | `2fab37730c1f671c558e950f1ae947e1049af913d0916d9618841c415c99d131` |

源码清单包含 194 个模块图记录，其中当前生产编译所需 11 个模块交付完整 ZIP，1158 个文件逐项校验；另外包含固定 SQLite 上游完整树与 3.50.4 原稿，原稿 VERSION 文件也已核对。生产构建和独立重建均使用全新模块/编译缓存与包内 file GOPROXY；源码包没有 `.git`。实际 EXE 的 11 个依赖版本/h1 均相同，当前仍无 Mihomo/go-lzo，不将模块元数据当作未来所有函数保留的证明。EXE 哈希不同是预期范围，没有宣称比特可复现。

随后直接对**最终落盘的两个附件**运行：

```powershell
node scripts/release.mjs verify --manifest artifacts/issue30/release-check/still-wanna-dance-v0.0.0-issue30-validation-release.json
```

结果 `verified: true`：重新检查 ZIP/源码 SHA256、解压布局、实际 EXE/包内声明与 SOURCE 的对应关系，并再独立重建成功。另补的身份负例明确拒绝“EXE 引用的模块仅交付 .mod/.info、没有完整 ZIP”。

Portable smoke 实测包含同意前 API 428、条款文本一致、固定交付路径、配置锁/实例身份、强制结束后重启、覆盖升级数据不变、整目录移动、并发自定义配置隔离及旧布局拒绝；只启动自己创建的 `-no-tray -no-open` 隔离进程，结束时终止这些进程，未接受条款、启动游戏、修改 hosts 或运行真实托盘。

开发实现已完成，以下发布/实机条件仍开放：

- **LA-033**：真实旧确认记录升级、关于/条款页面人工核对及用户数据保持，与 LA-003/009/029 联动。
- **#14 接入后**：复验实际桌面 EXE 与编译/链接证据。2026-10-13 已确认并采用官方 `no_easytier` 开关，不携带 EasyTier WASM，因此无需等待其源码。若未来启用它，再核对完整对应源码。go-lzo 未决许可本身继续非阻断；未 Fork 或替换内核，未向上游发消息。
- **正式发布时**：使用实际版本 tag 重新走门禁，同一 Release 提供两个附件、校验与源码入口；运行 `verify-published` 核验公开无凭据下载。#30 保持 release blocker，不以本地验证关闭。

最终记录提交只补验证文字/负例，不改变产品运行行为；未新增其他实机项，已有台账全部保留。

## 2026-10-13 官方 EasyTier 排除开关

- 补查官方 Mihomo `v1.19.32` 的 `no_easytier` 构建标签，统一应用于开发/正式桌面、源码依赖收集、离线重建和 CI；EXE 身份检查拒绝漏带标签或仍包含 EasyTier 模块的构建。此前默认 adapter 探针确实包含整份 6,850,909 字节 WASM；仅限制运行时协议不足以排除它。
- 同一个 `New/Proxy/Close` 探针以生产参数及该标签重建：EXE 34,187,264 字节，SHA256 `16c4261f702e1460c34c4d8e0ed1016563db17b2c0742464070f925f9615224b`。Go build info 无 EasyTier，完整 WASM 字节搜索未找到；go-lzo 仍存在且继续按已接受风险处理。探针只编译未运行。
- 发行/源码/声明专项 11 项通过（包括无 Git、空缓存重建）；`go test -tags=no_easytier ./internal/proxyengine/mihomo` 通过，覆盖受控本地 SS/VLESS/AnyTLS/Hysteria2。
- 当前桌面未接入 Mihomo，已有 adapter 协议允许列表也未支持 EasyTier；本次改动构建与交付工具，不改变已支持的产品行为，因此不新增实机项。既有 LA-033 等台账不变，#14 接入后仍复验实际桌面 EXE。

## 2026-10-13 源码交付精简

- 保留 11 个实际依赖的原始模块 ZIP。SQLite 的额外 Git 档案只导出同一固定提交的 `LICENSE` 和 `vendor_libsqlite3/`，从 247,040,000 字节缩为 20,480 字节；SQLite 3.50.4 原始 C 源码 ZIP 保留。逐文件比对确认原 Git 快照与模块 ZIP 的 1,321 个同名文件统一换行后全部相同，另一个新增嵌套目录 `issue198/` 是历史测试样例。
- `.gitattributes` 仅从源码导出排除 `experiments/`、`docs/evidence/`，仓库文件和历史证据不删。保留 `docs/research/` 与绘图脚本，因为程序内置的 `storage-benefit.svg` 与研究图表相同，需要保留可修改输入；数据种子、测试夹具、构建脚本和原许可也保留。
- 全图仅查询 `.mod/.info` 元数据，源码 ZIP 仅下载实际编译模块。未使用模块没有已知 ZIP h1 时清单记录 `null`，不冒充已取得源码；实际编译模块仍强制 Go h1 和完整 ZIP。新增测试提供一个仅有元数据、根本没有 ZIP 的模块，完整打包和全新缓存离线重建通过。
- 源码专项 7 项通过，包括历史文件排除但仓库保留、图表输入保留、缺 ZIP 的未使用模块可构建、漏带编译标签和 EasyTier 模块残留拒绝。下面再记录真实最终附件的验证，不把夹具测试当作完整发行验证。
- 本次仅修改构建/交付工具与文档，不改变产品运行行为；不新增实机项，已有台账全部保留。

### 精简后的真实附件验证

被测实现提交 `96dec315cf64a4a94f124fde1ae6bbe3c4e8feef`，Windows amd64、Go 1.27.1、CGO=0。在独立临时克隆建立 `v0.0.0-source-slim-validation`，执行完整正式发行流程；主仓库未创建该标签，没有 push、上传或发布。产物保留在忽略目录 `artifacts/issue30/source-slim-check/`，是本地验证包。

| 产物 | 字节数 | SHA256 |
| --- | ---: | --- |
| 精简对应源码 tar.gz | 76,393,234 | `809a0758c1e42eb195c91a0ba8f9b18781a1009af42180b2358c753e81f66f4d` |
| Portable ZIP | 6,874,723 | `cad36fa7b758bebe6d7f0ca7eaba94ed34c6c3554acca06f010f48497b59eaf1` |
| 生产 EXE | — | `eb3b69ac8566e08366aa8ef2b37d4e9daa69e3d624dccdc4505116f3c8b6c166` |
| 无 Git、空缓存独立重建 EXE | — | `2c7dfcdc3e6844956cdf54ba51d91235a105f0df185902809bcc022ba977e195` |

- 源码包较旧包 115,091,522 字节减少 38,698,288 字节（33.62%）。清单仍为 194 个模块图记录、11 个完整模块 ZIP；交付文件 1,067 项（不含清单自身）。补充的 SQLite 生成器档案实测仅四个文件：LICENSE 与 `vendor_libsqlite3` 的 main.go/go.mod/go.sum，共 20,480 字节的 tar。
- 干净检出的全部 Node 测试、Go `./cmd/... ./internal/...` 测试及 vet 通过。生产构建与独立重建都使用同一 `no_easytier` 标签、新建 Go 模块/编译缓存和仅包内 file GOPROXY；11 个 EXE 模块身份相同。Go 的 downloading 日志此时读取包内文件，不是联网下载。不声称 EXE 比特相同。
- 真实 Portable ZIP 隔离检查通过，覆盖同意前门禁、身份、覆盖升级、移动、锁和旧布局拒绝；未接受条款、启动游戏、操作托盘或修改 hosts。
- 对最终落盘附件另核对文件大小/SHA256、解压后的所有源码文件校验、EXE 与 SOURCE/声明的对应关系。EXE 记录 `-tags=no_easytier`，无 EasyTier 模块，未包含已知 WASM 完整字节；逐一检查源码包内部 ZIP，也无 WASM 文件或 WASM 文件头。
- 实际归档确认无 `source/experiments/`、`source/docs/evidence/`、旧整份 SQLite tar；内置图表的研究数据和绘图脚本仍在，原模块 ZIP 和 SQLite C 原稿保留。当前桌面仍未接入 Mihomo；未来集成的排除证据来自前述实际 adapter 探针，#14 接入后仍复验桌面产物。
- 本段记录提交仅增加验证证据，不改变被测实现；既有 LA-033 等实机项及正式公开下载检查继续待办，#30 不据此关闭。
