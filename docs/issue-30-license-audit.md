# Issue #30 许可与授权证据

## 范围和决定

2026-10-12：维护者确认 SWD 自有新贡献使用 `GPL-3.0-only`；历史 MIT 权利与版权保留于 `LICENSE-MIT`。根 `LICENSE` 保存 GPLv3 全文，当前源码授权范围见 [LICENSING.md](../LICENSING.md)。审计基线为 `5f215b2a24e952e1b041a1270c9823c58cba3892`，Git 作者记录只有 Himalia；这只是贡献记录，不代替第三方代码/素材的原许可。未发现产品源码中另有需移除或更改的第三方授权头；依赖和内嵌组件的现有 notices 原样保留。后续外来贡献须核实许可来源。

选定官方 Mihomo `v1.19.32`：模块 `LICENSE` 与仓库保存的 `internal/proxyengine/mihomo/LICENSE.mihomo` 为 GPLv3 全文，README 声明 GPL-3.0。按 v3 使用；没有把 GPL 正文附录的示范文字认作该项目对后续版本的明确授权，也不将所有子依赖都标作 GPLv3-only。

## go-lzo：版本选项已由上游澄清

2026-10-30 核实：[上游 issue #15](https://github.com/rasky/go-lzo/issues/15) 已由作者于 2026-10-13 00:47（北京时间）回复「Done」并关闭。[澄清提交 `ad21c35a111113348567708a4db74f998be1a4c3`](https://github.com/rasky/go-lzo/commit/ad21c35a111113348567708a4db74f998be1a4c3) 的直接父提交正是 SWD 固定的 `96a758eda86e0ca29bc2638000fdb0ff0a6d96d8`；唯一改动是 README 将 `GPLv2` 明确为 `GPL-2.0-or-later`，没有代码变化。作者回复见[原评论](https://github.com/rasky/go-lzo/issues/15#issuecomment-6128639788)。

该澄清解决了当前固定实现的许可版本选项问题；GPL-2.0-or-later 允许选择 GPLv3，依据见 [GNU 兼容性说明](https://www.gnu.org/licenses/gpl-faq.en.html#v2v3Compatibility)。固定依赖版本、模块 ZIP 和原 LICENSE.gpl 保留，上游澄清作为补充授权依据随本文进入对应源码。新发行不再生成 go-lzo 的「已接受风险」记录。

此前审计漏纳了该回复，因而 Beta 1、Beta 2 的发布说明和产物元数据仍沿用过时结论。2026-10-30 更正现行说明与生成器；已发布附件保持原始字节和校验值，其历史记录不代表当前仍存在该疑点。新验证器兼容读取原有准确记录，以便继续核验旧发行。

### 历史判断与原始证据（已由上述澄清替代）

2026-10-12 的原审计将固定 `github.com/rasky/go-lzo v0.0.0-20200203143853-96a758eda86e` 记为授权选项尚待澄清，维护者当时接受其为非阻断风险。以下保留当时依据，不作为现行未决风险。

- [固定提交 README](https://github.com/rasky/go-lzo/blob/96a758eda86e0ca29bc2638000fdb0ff0a6d96d8/README.md)称直接移植原始 C 源并沿用相同许可，简写为 GPLv2。
- [f01714f](https://github.com/rasky/go-lzo/commit/f01714f222ab986062f9f90d046821604ecae3ba) 删除初始 MIT 文本，改放 GPLv2 全文；至固定提交此许可文件未变。初始 MIT 不能替代当前固定版本的许可判断。
- [作者在 issue #6 的解释](https://github.com/rasky/go-lzo/issues/6#issuecomment-581445627)确认原始 C 来源，拒绝改为 MIT/Apache 双许可，但没有直接说明 v2-only 或 v2-or-later。
- [LZO 官方](https://www.oberhumer.com/opensource/lzo/)声明 GPLv2+。已下载核对官方 2.08（2014）和 2.10（2017）的 `src/lzo1x_d.ch`，文件头均允许 v2 或后续版本；未确认 Go 移植使用的精确原始 C 版本。
- 2.08 源包 SHA256：`ac1b3e4dee46febe9fd28737eb7f5692d3232ef1a01da10444394c3d47536614`；2.10：`c0f892943208266f9b6543b3ae308fab6284c5c90e627931446fb49b4221a072`。

当时没有认定已经发生 GPLv2-only 冲突；版本选项问题现以上方作者澄清为准。

## 已做构建核查（不是实机验收）

同一源码基线、Go 1.27.1、Windows amd64、CGO=0、`-trimpath -ldflags '-s -w -H=windowsgui'`：

| 对象 | 编译依赖/二进制模块 | 最终 PE 运行时函数表 | EXE SHA256 |
| --- | --- | --- | --- |
| 现有桌面 `cmd/still-wanna-dance-console` | 无 Mihomo/go-lzo | 17,122 个函数中无上述包 | `1834c3c6d199e4b9ca3607542816354e8a95a4d846e3bb1a6552abffb38a58d3` |
| 调用现有 SWD Mihomo `New/Proxy/Close` 的隔离探针 | 包含两者 | 保留 5 个 go-lzo 函数及 OpenVPN 解压包装函数 | `00db62c783c82360dcc3c04888efb865decefe1e5c151e8d2aca326441e9c863` |

直接使用 Go `debug/pe` 与 `debug/gosym` 解析去符号 EXE 的 runtime pclntab，检查函数范围落入 `.text`。`Decompress1X` 位于 `0x140b1dc00`，范围 3,872 字节。仅去掉 `-s` 的辅助构建通过 `go tool nm -size` 交叉检查：5 个 LZO 函数和 OpenVPN 包装函数地址、大小、指令字节一致；整段 `.text` 有 40 字节不同，不声称整包可重现。临时探针从参数读取配置，有合法格式节点 ID 和返回错误的 DNS 回调；只构建、不执行。

这说明仅在 UI 不开放 OpenVPN 不能保证裁剪该代码。探针不是正式桌面接入产物，#14 接入后应保存实际构建依赖、EXE 模块和相关二进制记录；记录到该依赖不自动拒绝发布。原始本地证据在忽略目录 `artifacts/issue30/check-20261012/`；关键结论和哈希同步在 [#30](https://github.com/SW26010/still-wanna-dance/issues/30)。没有向上游发送消息。
