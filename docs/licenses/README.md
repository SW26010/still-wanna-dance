# 第三方声明核对

`reviewed-modules.json` 固定 Windows 桌面入口与已固定的 Mihomo adapter 依赖并集：97 个模块。每项包含模块版本、Go h1、原许可文件路径与 SHA256；整个根 `THIRD-PARTY-NOTICES.txt` 的 LF 文本另有 SHA256。构建从实际 EXE 读取模块身份；新增、升级、replacement、声明内容变化而未复核时失败。Go runtime/标准库许可按实际构建工具链追加。

2026-10-12 核对保留原有 SQLite、musl、Go 等声明，补入依赖模块内根目录及子目录的 LICENSE/LICENCE/COPYING/NOTICE/COPYRIGHT 全文（含原文件后缀），并保守收集源码开头版权行。包含一些未链接的源码/测试组件，不据此声称它们全部进入 EXE。上游完整模块 ZIP 仍独立随对应源码交付。

当前清单包含 MIT、BSD、Apache-2.0、CC0、MPL-2.0、GPLv3 和 LGPLv3 等原文。不能把根 LICENSE 当作全部文件的统一授权。Mihomo、mieru、sing 系列等保留 GPL 原文，easytier-go 保留 LGPLv3，mipstack/yamux/zerotier-go 保留 MPL 及 NOTICE。完整源码及脚本使修改、重建和重新链接可行；上游版本更新必须重新检查授权和源文件中的特殊许可，不能只机械替换版本或哈希。

两个特别记录：

- `rasky/go-lzo` 的 LICENSE.gpl 是 GPLv2 文本，README 的版本选项仍不明确；维护者已接受固定版本的非阻断风险。原文和[审计证据](../issue-30-license-audit.md)保留，声明没有改写为“已确认 GPLv3 兼容”。
- `metacubex/chacha v0.1.5` 的 `chacha/*.go` 头部保留 Andreas Auernhammer 2016 版权但指向缺失的同目录 LICENSE；随模块的 poly1305/chachapoly1305 MIT 文件均保留，另补[原 aead/chacha20 MIT 文件](https://github.com/aead/chacha20/blob/master/LICENSE)，Git blob `b6a9210b9f6a5d5220dec9a44485091f7ca85f6c`，本地原文 `chacha20-MIT.txt`。来源由模块 README 指向 aead 项目及源文件版权头支持；没有将此补充称为修改上游许可。

更新时，先用生产 GOOS/GOARCH/CGO/构建参数重新列出入口依赖并读取最终 EXE，下载所选模块并校验 Go h1，逐项核对许可全文、嵌入代码和特殊例外。更新声明和复核清单后运行 `node --test scripts/release-notices.test.mjs`，再跑完整发行和源码重建验证。测试只能证明清单一致，不能自动作出法律兼容性结论。
