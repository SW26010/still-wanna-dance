# 第三方声明核对

2026-10-28 为修补桌面入口可达的网络和标准库问题，将 Go 最低版本提高到 1.27.3，`golang.org/x/net` 更新至 v0.60.0；模块选择同时要求 crypto v0.57.0、sys v0.48.0、text v0.42.0、sync v0.23.0，以及仅模块图涉及的 term v0.46.0、mod v0.41.0、tools v0.49.0。逐项比较新旧完整模块，根 LICENSE 与 PATENTS 字节未变，未新增版权主体、特殊许可头、嵌入指令或生成入口；text 的规范化表与同模块生成器一起更新。实际交付清单中的五项版本、Go h1 和总声明哈希已同步，原 BSD 全文及保守版权清单保留，WebUI 内嵌声明重新生成。验证及未执行的正式打包、离线重建范围见[发布前修复记录](../validation/release-readiness-20261028.md)。

2026-10-14 Windows race 回归复现 SQLite 查询取消后文件句柄残留，驱动由 `v1.39.1` 更新到包含[上游修复 !81](https://gitlab.com/cznic/sqlite/-/merge_requests/81)的 `v1.40.1`（`d33e568298f6d0da40718843d3562d716f7c4f1b`）。完整模块差异仅为驱动修复、可选 pre-update hook 和测试/作者记录，SQLite 3.50.4 生成引擎、依赖图及原 LICENSE 哈希不变；新增 `convert.go` 来源于 Go database/sql，其版权和 BSD 许可补入第三方声明。生成器归档仍按同一路径从新提交导出，SHA256 更新为 `d6b2789d5b21d17c1878f2ad2056bfec1f56ede34f57791b53b5fb34daa95e25`；SQLite C 原始源码归档不变。以下历史版本和失败证据保留。

`reviewed-modules.json` 固定 Windows 桌面入口与已固定的 Mihomo adapter 依赖并集：116 个模块。每项包含模块版本、Go h1、原许可文件路径与 SHA256；整个根 `THIRD-PARTY-NOTICES.txt` 的 LF 文本另有 SHA256。构建从实际 EXE 读取模块身份；新增、升级、replacement、声明内容变化而未复核时失败。Go runtime/标准库许可按实际构建工具链追加。

2026-10-27 退出通知直接使用模块图中已锁定的 `github.com/coder/websocket v1.8.12`，不升级版本。已核对模块 ZIP 的根 ISC `LICENSE.txt`、Go／汇编源码及生成标记，补入原许可全文、模块 h1 和许可文件哈希。没有额外内嵌二进制；`stringer.go` 标明的生成命令和枚举源码位于同一模块中。当前 EXE 的依赖声明检查通过；本次未执行发行打包或离线源码重建。

2026-10-13 B2 完整核心新增 13 个实际链接模块：regexp2、age、maxminddb-golang、chi、mhurl、sing-tun、go-ole、bodgit/plumbing、bodgit/windows、sevenzip、golang-lru/v2、xz、ppmd。固定版本、Go h1、根及子目录许可文件 SHA256 已加入清单，完整原文和源码版权头追加总声明。age 的 bech32 内联 MIT、golang-lru 的 list BSD 分别保留；sing-tun 的 GPL-3.0-or-later、gVisor Apache-2.0、Wintun/winfw MIT 不能概括成一种授权。

maxminddb 的 mmap_windows.go 保留 Evan Shaw BSD 头但根 LICENSE 为 ISC，补入其明确来源 [edsrzf/mmap-go LICENSE](https://github.com/edsrzf/mmap-go/blob/main/LICENSE)（GitHub connector 读取，blob 8f05f338ac5a0543e5cef2898cf5c06208b761aa），本地 mmap-go-BSD.txt。sing-tun 的 Wintun/memmod 绑定补入 [wintun-go MIT 原文](https://git.zx2c4.com/wintun-go/tree/LICENSE)（blob f85e365de02a257e41b27305e74277428218ff96），本地 wintun-go-MIT.txt，版权行保留在总声明。

sing-tun ZIP 含四架构 Wintun DLL，Windows amd64 构建会嵌入对应文件，关闭 TUN 不等于删去该依赖。四个文件逐字节哈希均与 [官方 Wintun 0.14.1](https://www.wintun.net/) ZIP 一致；amd64 SHA256 为 e5da8447dc2c320edc0fc52fa01885c103de8c118481f683643cacc3220dafce。官方区分 GPLv2 驱动源码、MIT Go 绑定及已签名 DLL 的独立预编译许可；DLL 未修改，只由上游绑定使用其 API，保留完整二进制许可（wintun-binary-license.txt），不把它改称 GPLv3。source-inputs.json 同时固定交付官方 0.14.1 源码（含驱动/API/构建脚本，SHA256 636f00ec329113f37039a96964737f6b6ae3982b34620955ff7a746e5e504d28）和原始已签名 ZIP（SHA256 07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51）。Go 离线重建复用原始已签名 DLL，不声称重建微软签名。sing-tun 汇编的生成器与系统调用生成入口已在完整模块 ZIP 内，源码材料并未仅以二进制哈希替代。

2026-10-13 #14 将 Mihomo 接入真实桌面，并新增一次性导入所用 `go.yaml.in/yaml/v3 v3.0.5`。核对固定模块的 LICENSE、NOTICE 与源文件头：libyaml 移植文件保留 Kirill Simonov/Canonical MIT，其余文件保留 Canonical Apache-2.0；原文追加至总声明，原始文件 SHA256 和模块 h1 加入清单，完整模块 ZIP 随对应源码交付。该模块没有额外嵌入二进制或生成输入声明。本次保留全部旧记录，不因 `no_easytier` 删除历史声明。

2026-10-12 核对保留原有 SQLite、musl、Go 等声明，补入依赖模块内根目录及子目录的 LICENSE/LICENCE/COPYING/NOTICE/COPYRIGHT 全文（含原文件后缀），并保守收集源码开头版权行。包含一些未链接的源码/测试组件，不据此声称它们全部进入 EXE。上游完整模块 ZIP 仍独立随对应源码交付。

当前清单包含 MIT、BSD、Apache-2.0、CC0、MPL-2.0、GPLv3 和 LGPLv3 等原文。不能把根 LICENSE 当作全部文件的统一授权。Mihomo、mieru、sing 系列等保留 GPL 原文，easytier-go 保留 LGPLv3，mipstack/yamux/zerotier-go 保留 MPL 及 NOTICE。完整源码及脚本使修改、重建和重新链接可行；上游版本更新必须重新检查授权和源文件中的特殊许可，不能只机械替换版本或哈希。

两个特别记录：

- `rasky/go-lzo` 的固定模块保留原 LICENSE.gpl；作者已在 [issue #15](https://github.com/rasky/go-lzo/issues/15) 及紧接固定版本、仅修改 README 的提交中明确 `GPL-2.0-or-later`。版本选项问题已解决，补充依据与历史记录见[许可审计](../issue-30-license-audit.md)。
- `metacubex/chacha v0.1.5` 的 `chacha/*.go` 头部保留 Andreas Auernhammer 2016 版权但指向缺失的同目录 LICENSE；随模块的 poly1305/chachapoly1305 MIT 文件均保留，另补[原 aead/chacha20 MIT 文件](https://github.com/aead/chacha20/blob/master/LICENSE)，Git blob `b6a9210b9f6a5d5220dec9a44485091f7ca85f6c`，本地原文 `chacha20-MIT.txt`。来源由模块 README 指向 aead 项目及源文件版权头支持；没有将此补充称为修改上游许可。

更新时，先用生产 GOOS/GOARCH/CGO/构建参数重新列出入口依赖并读取最终 EXE，下载所选模块并校验 Go h1，逐项核对许可全文、嵌入代码和特殊例外。更新声明和复核清单后运行 `node --test scripts/release-notices.test.mjs`，再跑完整发行和源码重建验证。测试只能证明清单一致，不能自动作出法律兼容性结论。

## 生成输入与内嵌二进制的源码

`source-inputs.json` 保存另外需要交付的原始源码。仅补足许可文本不等于完成源码交付。

- 当前实际桌面 SQLite `v1.39.1` 的生成文件明确记录 SQLite `3.50.4`；模块 ZIP 缺少其 Makefile 使用的嵌套 `vendor_libsqlite3` 目录。2026-10-13 将此前的完整 Git 快照缩为同一上游提交 `17e06226647c18b11f8bce329dea8d9c24901f3f` 的 `LICENSE` 和 `vendor_libsqlite3/`：`git -c core.autocrlf=false archive --format=tar --prefix=modernc-sqlite/ <revision> -- LICENSE vendor_libsqlite3`。新档案 `modernc-sqlite-generator.tar` 为 20,480 字节，SHA256 `bead0df19c42d02a3adafa675dc6f0e7f1cb8d7c28daa7a1e7919b63618661dd`。旧快照与模块 ZIP 的 1,321 个同名文件仅有换行差异，故无需重复交付；另一个 `issue198` 嵌套模块仅为测试样例，不作生成输入。[SQLite 官方 3.50.4 原始源码](https://www.sqlite.org/2025/sqlite-src-3500400.zip) 仍完整保留，SHA256 `b7b4dc060f36053902fb65b344bbbed592e64b2291a26ac06fe77eec097850e9`。不裁剪原模块 ZIP，也不改变运行行为。旧包与验证记录保留在[历史记录](../validation/issue-30-validation.md)。
- `github.com/easytier/easytier/easytier-go v0.0.0-20260910071355-3d0c9c3ca5e2` 的 `internal/artifact/embed.go` 嵌入 `easytier_core.wasm`，LGPLv3。其[provenance.go](https://github.com/EasyTier/EasyTier/blob/3d0c9c3ca5e2/easytier-go/internal/artifact/provenance.go)记录 producer commit `63519db2b5f2a6a1b9b7f20905f036dab54eb829`、WASM SHA256 `8b82f37d62fba2ffe8e256386096fddf448d4261d51c495d5bd97cd11bbb24a0`，`proto/provenance.go` 使用相同来源。2026-10-12 通过 GitHub connector 查询该完整/短提交均返回 404；Go 模块自身 `3d0c9c3ca5e2` 可读取。该事实只表示本次未取得精确生产源码，不能擅自以 Go wrapper 的提交替代 WASM 生产提交。
- 2026-10-13 补查确认官方 Mihomo `v1.19.32` 已提供 `no_easytier` 构建标签（`adapter/outbound/easytier_stub.go`）。桌面及后续 #14 集成统一使用该标签，排除 SWD 当前不支持的 EasyTier；无需为此等待其 WASM 源码，也无需修改、替换或 Fork Mihomo。若未来改为分发 EasyTier，需另行核对相应 Rust 源码、非系统依赖、protobuf 原稿与构建脚本；不强求特定 Git SHA 或逐字节相同的重建产物。保留此前来源证据，未向上游发送消息。

最初验证的“整个模块图 ZIP”包仅为本地实验，尚未发布；后续正式实现改为实际编译源码 + 全图元数据，避免把未使用的预编译构件当作普通源码附件顺带分发。新增依赖/嵌入构件仍需复核首选修改形式，而不是仅登记哈希。

## 2026-10-30：原生控制器与 zashboard

控制器使已固定的 google/pprof 和 ajg/form 成为桌面入口依赖：逐项核对原文和源码版权，分别为 Apache-2.0（保留模块内补充声明）与 BSD-3-Clause，原文及模块身份加入总清单。没有升级 Mihomo 或其它现有模块。

zashboard v3.30.0 根许可 MIT；实际应用、worker/PWA 和 CSS 辅助包共 63 项保守登记在 zashboard-packages.json，总声明 ZASHBOARD-NOTICES.txt 同时进入 WebUI 和 Portable 许可合并。ECharts 附带 d3 BSD 全文与 NOTICE；DOMPurify 选择 Apache-2.0 选项；预打包 vuedraggable 内联源码和两项旗帜字体的额外声明见 [补充来源](zashboard/README.md)。原 public/THIRD_PARTY_NOTICES.md 的地球纹理 CC BY 4.0 署名完整保留，未下载或再分发可选 DB-IP 数据库。完整源码交付及重建边界见 [对应源码](../corresponding-source.md#zashboard-静态面板)。
