# 第三方声明核对

`reviewed-modules.json` 固定 Windows 桌面入口与已固定的 Mihomo adapter 依赖并集：97 个模块。每项包含模块版本、Go h1、原许可文件路径与 SHA256；整个根 `THIRD-PARTY-NOTICES.txt` 的 LF 文本另有 SHA256。构建从实际 EXE 读取模块身份；新增、升级、replacement、声明内容变化而未复核时失败。Go runtime/标准库许可按实际构建工具链追加。

2026-10-12 核对保留原有 SQLite、musl、Go 等声明，补入依赖模块内根目录及子目录的 LICENSE/LICENCE/COPYING/NOTICE/COPYRIGHT 全文（含原文件后缀），并保守收集源码开头版权行。包含一些未链接的源码/测试组件，不据此声称它们全部进入 EXE。上游完整模块 ZIP 仍独立随对应源码交付。

当前清单包含 MIT、BSD、Apache-2.0、CC0、MPL-2.0、GPLv3 和 LGPLv3 等原文。不能把根 LICENSE 当作全部文件的统一授权。Mihomo、mieru、sing 系列等保留 GPL 原文，easytier-go 保留 LGPLv3，mipstack/yamux/zerotier-go 保留 MPL 及 NOTICE。完整源码及脚本使修改、重建和重新链接可行；上游版本更新必须重新检查授权和源文件中的特殊许可，不能只机械替换版本或哈希。

两个特别记录：

- `rasky/go-lzo` 的 LICENSE.gpl 是 GPLv2 文本，README 的版本选项仍不明确；维护者已接受固定版本的非阻断风险。原文和[审计证据](../issue-30-license-audit.md)保留，声明没有改写为“已确认 GPLv3 兼容”。
- `metacubex/chacha v0.1.5` 的 `chacha/*.go` 头部保留 Andreas Auernhammer 2016 版权但指向缺失的同目录 LICENSE；随模块的 poly1305/chachapoly1305 MIT 文件均保留，另补[原 aead/chacha20 MIT 文件](https://github.com/aead/chacha20/blob/master/LICENSE)，Git blob `b6a9210b9f6a5d5220dec9a44485091f7ca85f6c`，本地原文 `chacha20-MIT.txt`。来源由模块 README 指向 aead 项目及源文件版权头支持；没有将此补充称为修改上游许可。

更新时，先用生产 GOOS/GOARCH/CGO/构建参数重新列出入口依赖并读取最终 EXE，下载所选模块并校验 Go h1，逐项核对许可全文、嵌入代码和特殊例外。更新声明和复核清单后运行 `node --test scripts/release-notices.test.mjs`，再跑完整发行和源码重建验证。测试只能证明清单一致，不能自动作出法律兼容性结论。

## 生成输入与内嵌二进制的源码

`source-inputs.json` 保存另外需要交付的原始源码。仅补足许可文本不等于完成源码交付。

- 当前实际桌面 SQLite `v1.39.1` 的生成文件明确记录 SQLite `3.50.4`；模块 ZIP 缺少其 Makefile 使用的嵌套 `vendor_libsqlite3` 目录。已从上游 GitLab 固定提交 `17e06226647c18b11f8bce329dea8d9c24901f3f` 取得完整树，`git archive --format=tar --prefix=modernc-sqlite/` 的 SHA256 为 `9792e7bd5b354a8a8930b30f4e5107d7fc487e72d3fde2139e61277b987bea06`；[SQLite 官方 3.50.4 原始源码](https://www.sqlite.org/2025/sqlite-src-3500400.zip) SHA256 为 `b7b4dc060f36053902fb65b344bbbed592e64b2291a26ac06fe77eec097850e9`。生成器和原始 C 输入纳入源码包，不改动原模块或运行行为。
- `github.com/easytier/easytier/easytier-go v0.0.0-20260910071355-3d0c9c3ca5e2` 的 `internal/artifact/embed.go` 嵌入 `easytier_core.wasm`，LGPLv3。其[provenance.go](https://github.com/EasyTier/EasyTier/blob/3d0c9c3ca5e2/easytier-go/internal/artifact/provenance.go)记录 producer commit `63519db2b5f2a6a1b9b7f20905f036dab54eb829`、WASM SHA256 `8b82f37d62fba2ffe8e256386096fddf448d4261d51c495d5bd97cd11bbb24a0`，`proto/provenance.go` 使用相同来源。2026-10-12 通过 GitHub connector 查询该完整/短提交均返回 404；Go 模块自身 `3d0c9c3ca5e2` 可读取。该事实只表示本次未取得精确生产源码，不能擅自以 Go wrapper 的提交替代 WASM 生产提交。
- 当前桌面不编译 EasyTier，因此正式源码包不夹带该无关 WASM；将来 #14 接入后，必须先取得正确 Rust 原始源码、所需非系统依赖、protobuf 原稿与构建脚本并核对来源，然后更新补充清单。门禁对此失败，不能只凭 Go 重建成功放行。未修改、替换或 Fork Mihomo，也未向上游发送消息。

最初验证的“整个模块图 ZIP”包仅为本地实验，尚未发布；后续正式实现改为实际编译源码 + 全图元数据，避免把未使用的预编译构件当作普通源码附件顺带分发。新增依赖/嵌入构件仍需复核首选修改形式，而不是仅登记哈希。
