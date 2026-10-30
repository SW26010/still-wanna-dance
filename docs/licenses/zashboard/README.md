# zashboard 补充许可来源

zashboard `v3.30.0` 的 `dist-no-fonts` 仍包含 Noto 与 Twemoji 旗帜字体；它只排除界面文本字体。此目录保存已核对的上游许可原文与固定身份，供发行声明引用；上游静态包原有 `THIRD_PARTY_NOTICES.md` 仍须保留。

| 材料 | 上游固定来源 | 本地 SHA256 |
| --- | --- | --- |
| [Noto OFL 原文](noto-emoji-OFL-1.1.txt) | [googlefonts/noto-emoji `6c211821…` / fonts/LICENSE](https://github.com/googlefonts/noto-emoji/blob/6c211821b8442ab3683a502f9a79b2034293fced/fonts/LICENSE) | `6a73f9541c2de74158c0e7cf6b0a58ef774f5a780bf191f2d7ec9cc53efe2bf2` |
| [Twemoji Mozilla 原文](twemoji-mozilla-LICENSE.md) | [mozilla/twemoji-colr `v0.7.0`, `c0460fdb…` / LICENSE.md](https://github.com/mozilla/twemoji-colr/blob/c0460fdb2654dee584e75c4913bfa9fd2f873338/LICENSE.md) | `64419edc28e9163204c3be73f835a8dfc34cd6c9b8f7d067bb685f119f839a99` |

两份文件的本地 Git blob hash 与 connector 返回的上游 blob 相同；没有改写许可正文，包括上游 Markdown 的空白。

Noto 文件内嵌 `Copyright 2022 Google Inc.`，字体按 OFL-1.1 保留；Twemoji Mozilla 的代码为 Apache-2.0，视觉设计来自 Twemoji、按 CC BY 4.0 保留，二者不是二选一。Twemoji 原文引用 Apache-2.0 全文，发行声明还需包含该通用许可全文；不能仅把其许可标题作为全文替代。视觉署名应说明此处使用 zashboard 提供的旗帜子集/WOFF2 版本，不声称 SWD 制作了该转换。

[font-sources.json](font-sources.json) 保存实际字体大小、SHA256、版本元数据、许可文件身份与尚未查明的转换来源。已检查 zashboard 源码中的字体与其发行 dist 字节一致；没有将这些字体与完整原厂字体误称为逐字节一致。尤其 Noto 内嵌的 Google 提交只证明其版本元数据，那个提交的原厂 flags-only 文件与 zashboard 文件大小和 blob 不同。

这些未修改的字体是独立许可资源。OFL、Apache-2.0 和 CC BY 4.0 的上述分发条件不自行要求交付字体生成工具链；源码交付可保留固定 zashboard 源码中已有的精确字体输入、此目录原文/署名、文件身份和来源说明。该范围可重建 SWD 的静态面板，不代表能从上游原始图形重建旗帜字体。若将来修改/重新生成字体，再固定 Google 或 Mozilla 的原始图形、字体工具及明确的子集/压缩脚本；现有 zashboard 归档没有这套转换配方，不应虚构“已完整重建字体”的证据。

## vuedraggable 中预打包的运行时代码

`vuedraggable@4.1.0` 的 UMD 及其 source map 已嵌入另外四个项目的代码，单靠 zashboard 当前锁文件不会完整列出。只补收实际运行片段的许可，不收未进入包的历史 CLI 工具依赖。

| 实际嵌入部分 | 补充许可 | 身份边界 |
| --- | --- | --- |
| core-js | [3.6.5 MIT 原文](core-js-3.6.5-MIT.txt) | 嵌入的 `internals/shared.js` 自报 3.6.5，旧 package-lock 也固定为该版本 |
| @soda/get-current-script | [1.0.2 MIT 原文](get-current-script-1.0.2-MIT.txt) | 两份旧锁均为 1.0.2；保留 Haoqun Jiang 和 Adam Miller 的版权 |
| @babel/runtime helpers | [MIT 原文](babel-runtime-MIT.txt) | 固定 package-lock 候选 7.12.0 的原文；另一份 yarn.lock 不同，未把候选版本声称为唯一构建版本 |
| @vue/cli-service 的 `setPublicPath.js` / `entry-lib.js` | [运行片段 MIT 原文](vue-cli-runtime-MIT.txt) | 固定 package-lock 候选 4.5.7 的原文；只覆盖交付的运行片段，版本边界同上 |

[vuedraggable-bundled-sources.json](vuedraggable-bundled-sources.json) 保存 npm 发行 gitHead、两份锁文件、原文来源、blob 与 SHA256；证据是 npm 包已有 `dist/vuedraggable.umd.min.js.map` 的 `sources`/`sourcesContent`。上游两份锁不一致，不能为方便声明而伪造确切的 Babel/Vue CLI 生成版本；全部许可原文的本地 blob 均已与对应上游文本核对一致。
