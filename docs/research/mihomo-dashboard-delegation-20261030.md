# Mihomo 面板与代理节点管理委托研究

2026-10-30。本文是可行性研究和设计建议，**不改变现行规范，也不代表已实现或完成运行验证**。项目固定核心为 `v1.19.32`；核心细节核对本机该版本模块源码，在线文档和面板 README 为查阅时版本。现行规则仍见[上游通道](../upstream-channels.md)。

## 结论与建议边界

可行。代理模式可让 SWD 只使用一个 Mihomo 逻辑出口，将节点选择、代理连通性检查与故障切换交给 Mihomo，用户通过现成面板操作。官方文档列出 Metacubexd 和 zashboard；面板通过 `external-controller` API 控制核心，`external-ui` 负责提供静态页面，本身不执行代理。可随产品携带固定版本静态资源并从本地打开，无须自己重做节点管理界面。[官方面板入口](https://wiki.metacubex.one/en/startup/web/)、[控制 API 与静态页面配置](https://wiki.metacubex.one/en/config/general/)

责任建议如下；这是对现有代码和上游能力的设计推论。

| 责任 | 建议归属 |
| --- | --- |
| 代理节点选择、通用连通性/延迟探测、自动切换、选择持久化 | Mihomo |
| 节点与策略组操作界面 | 现成 dashboard |
| 直连模式的 DNS、固定 IP 候选及业务路径选择 | 保留 SWD 现有边界，不受本研究自动影响 |
| 清单同步、播放地址解析、资源版本和 Range 校验、下载重试/续传 | SWD |
| 当前逻辑出口能否完成真实业务的诊断 | SWD；不再为代理节点另做一套竞争排序 |

Monitor 不能直接整体删除：它的 `OnCatalog`、`OnPlaybackURL` 还交付清单和播放元数据。应取消代理分支的逐节点选优职责，同时保留或明确迁移业务同步和当前出口诊断，避免功能丢失。[当前接口](../../internal/upstreamstate/README.md)

## 策略语义与故障边界

- `select` 交给用户选择，可包含单个节点或自动组。外层手动选中单个节点时，不会因同组另有 fallback 就自动切到它；选择自动组才使用该组策略。[官方 select 配置](https://wiki.metacubex.one/en/config/proxy-groups/select/)、[v1.19.32 selector 实现](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/adapter/outboundgroup/selector.go)
- `fallback` 按配置顺序选健康节点，适合“优先这条，坏了换下一条”；`url-test` 依测试 URL 的延迟与 tolerance 选节点，适合自动延迟优选。后者不保证视频吞吐最高。[fallback 文档](https://wiki.metacubex.one/en/config/proxy-groups/fallback/)、[url-test 文档](https://wiki.metacubex.one/en/config/proxy-groups/url-test/)、[v1.19.32 延迟选择实现](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/adapter/outboundgroup/urltest.go)
- 固定版本的 fallback 自身还支持 API 手动固定一个成员；该成员对测试 URL 不健康时会释放固定并回到自动选择。这与外层 select 固定普通节点不同，面板是否清楚呈现此交互应实际检查。所有成员不健康时，固定版本实现仍返回首个成员尝试，不保证每次请求必定成功。[v1.19.32 fallback 实现](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/adapter/outboundgroup/fallback.go)
- 核心 URLTest 使用 HTTP HEAD，记录指定 URL 的可达性、状态与延迟。它不能证明真实资源的正文、Range、版本或持续吞吐正常。fallback/url-test 拨号失败会按条件触发健康检查；组方法本身返回该次拨号结果，没有遍历全部节点并重放应用 HTTP 的逻辑。真实 HTTP 5xx、正文断流等仍需 SWD 的业务错误处理。[v1.19.32 URLTest](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/adapter/adapter.go)、[组失败处理](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/adapter/outboundgroup/groupbase.go)
- 修改选择影响后续建连，不会把已建 TCP/HTTP 连接迁移到新节点。切换 API 只改选择和缓存；关闭连接是独立 API。SWD 的连接池可能继续复用旧连接，因此不能承诺点击后所有请求立即换出口。强制关闭连接会打断在途下载，需要按业务重试规则处理。[v1.19.32 切换 API](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/hub/route/proxies.go)、[连接 API](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/hub/route/connections.go)
- `profile.store-selected: true` 保存 API 选择，核心应用配置时恢复。它以组名/成员名保存，故应保持稳定命名；目前 SWD 明确关闭此选项。[配置说明](https://wiki.metacubex.one/en/config/general/)、[v1.19.32 选择缓存](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/component/profile/cachefile/cache.go)、[恢复入口](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/hub/executor/executor.go)

## 对当前实现的影响

当前内嵌版不能只挂一个面板就完成委托：

1. [Console](../../internal/console/network_mihomo.go) 将节点身份排序后取最多五个活跃节点，属于稳定暂定集合，并非全库测速 Top K。[Runtime](../../internal/proxyengine/mihomo/runtime.go) 捕获每个固定节点对象，当前业务拨号不会经过动态策略组。委托后应让业务使用组出口，并移除 SWD 对代理节点的活跃子集/竞争排序，否则用户在面板的选择无法成为唯一依据。
2. 当前 Runtime 不配置代理组、控制 API 或代理入站，规则为 `MATCH,REJECT`。它调用 `executor.ApplyConfig`，上游明确该入口不启动 ExternalController。需要接上 controller/UI 生命周期；仅新增 YAML 字段不够。上游 `hub.ApplyConfig` 会配置 controller；现有 `proxydialer.New` 可直接对组对象执行 `DialContext`，所以面板本身不要求必须改为独立核心进程。现有调用传入 `statistic=true`，该 adapter 会向核心 DefaultManager 注册 TCP/UDP tracker，连接表与流量统计并不因直接组拨号而必然缺失；但其 metadata 为 `INNER`，规则参数为 nil，不能将其等同完整入站规则匹配/进程识别路径。[v1.19.32 hub](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/hub/hub.go)、[executor](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/hub/executor/executor.go)、[proxydialer](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/component/proxydialer/proxydialer.go)
3. 当前候选/历史记录代表固定节点路径；动态组会更换实际出口，不能把旧样本继续解释成当前节点质量。逻辑出口诊断应明确观测时刻和范围，避免保留第二套控制节点选择的状态。[上游通道现行契约](../upstream-channels.md)
4. 完整面板也可能提供改运行模式、重载配置、升级核心等操作；需要明确受产品管理的配置边界。若继续保留 SWD 的强制代理语义，实际出口组不能夹带 DIRECT。内嵌对象在配置重载后的有效性、API/UI 关闭和选择持久化应一起验证；不能沿用当前固定对象的生命周期假设。[现有运行时](../../internal/proxyengine/mihomo/runtime.go)、[Metacubexd README](https://github.com/MetaCubeX/metacubexd/blob/main/README.md)、[zashboard README](https://github.com/Zephyruso/zashboard/blob/main/README.md)

外部 SOCKS5 后端已存在，可连接由用户独立管理的完整 Mihomo，由其 dashboard 选择节点；这是现有架构允许的接法。本轮只做文档和源码核对，没有启动核心/浏览器联调，也没有证明某个面板版本与 SWD 的完整兼容性。若正式内置，先固定一个面板版本，验证选节点、自动组失效切换、现有连接行为、重启恢复及真实业务错误处理即可收敛关键风险。

## 本轮确认的配置归属与面板建议

用户随后明确：配置继续由 SWD 管理并交给 Mihomo 消费；改造重点是清理 SWD 的代理节点选优职责，不将完整配置所有权交给面板。此决定仍未实施。当前 Runtime 已通过 `ParseWithBytes` 交付内存配置，不应仅因讨论使用“配置文件”一词就新增明文 YAML 副本。面板负责节点/组的运行时选择，SWD 保持配置的单一来源；接口是否允许修改配置应由后端边界保证，隐藏按钮只改善操作界面。

建议先选 **zashboard 固定版本的 `dist-no-fonts` 静态包**，待与固定核心完成联调后再决定发行版本。其 README 明确提供使用系统字体的无字体包、同源后端自动连接以及 `disableUpgradeCore`、`disableTunMode` 参数，适合 SWD 托管配置的范围。这是基于文档的适配性判断，不是已完成的兼容性验证。Metacubexd 虽已增加 desktop/server/agent，也明确保留独立静态面板，不需要引入整套应用。[zashboard README](https://github.com/Zephyruso/zashboard/blob/main/README.md)、[Metacubexd README](https://github.com/MetaCubeX/metacubexd/blob/main/README.md)

## 许可证限量核对

2026-10-30 通过 GitHub connector 读取两仓库默认分支文件。下列版本号来自当时 package 文件，**不是已经选定或验收的发布标签**；SHA 为许可证文件的 Git blob SHA，不是提交 SHA。

| 面板 | 根许可证与佐证 | 本次文件身份 |
| --- | --- | --- |
| Metacubexd | [LICENSE](https://github.com/MetaCubeX/metacubexd/blob/main/LICENSE) 为 MIT，Copyright 2023 MetaCubeX；[UI package](https://github.com/MetaCubeX/metacubexd/blob/main/packages/ui/package.json) 的 `license` 为 `MIT`，README 也链接 MIT。根 monorepo package 未设 license 字段，不影响上述明确声明。 | UI/根 package 版本 `1.273.1`；LICENSE blob `0e5028837118d11d27e94dca7bb7a5eab7e8f14c` |
| zashboard | [LICENSE](https://github.com/Zephyruso/zashboard/blob/main/LICENSE) 为 MIT/Expat 条款，Copyright 2024 Zephyruso；[package.json](https://github.com/Zephyruso/zashboard/blob/main/package.json) 明确 `license: MIT`。 | package 版本 `3.30.0`；LICENSE blob `4e20e4192e2dc9a98858879c2feb2a662011a3d1` |

**两项目自身的 MIT 许可证均可与 SWD 的 GPL-3.0-only 分发结合**，需保留各自版权、许可和免责声明；不能删掉原许可或把所有第三方代码宣称改为 GPL。GNU 官方将 Expat/MIT 列为 GPL 兼容许可，且说明组合时仍要附带被包含代码的许可副本；SWD 自身 [LICENSING.md](../../LICENSING.md) 也明确第三方依赖保留原许可，并要求正式二进制发行提供匹配源码。[GNU 许可列表](https://www.gnu.org/licenses/license-list.html#Expat)、[GNU 兼容性说明](https://www.gnu.org/licenses/license-compatibility.en.html)

根许可证不能替代发布包依赖审计。本次已直接发现 Metacubexd 当前 UI package 列有 `highcharts`，且[固定提交的 useHighcharts.ts](https://github.com/MetaCubeX/metacubexd/blob/8bbc8f58fef71148a94fb5c0ff808f79b057337d/packages/ui/composables/useHighcharts.ts) 实际懒加载它，并非仅看到依赖名称。[Highcharts 官方商业许可](https://shop.highcharts.com/license)与[免费使用条款](https://shop.highcharts.com/license-eula)另有条件，因此不能据根 MIT 直接认定其整份当前静态包可按 MIT/GPL 自由再分发。这也是本次更倾向 zashboard 的理由之一。尚未核实拟选发布包、依赖版本及是否另有授权，故不在此断言整个项目或具体包违规。zashboard 无字体包可减少附带字体及远程字体加载，但仍须在正式集成时按固定源码、锁文件和最终静态资产保留依赖声明。本轮未进行全量依赖审计，也未下载或运行发布包。

## 已授权实施的固定输入

以下是实施准备，不覆盖前文的历史研究结论，也不证明产品改造或运行验收已完成。2026-10-30 通过 GitHub connector 查询 `releases/latest` 和 tag ref，最新正式版为 **v3.30.0**，发布于北京时间 2026-10-29 11:22:41，`draft=false`、`prerelease=false`。tag 与 release 的目标提交一致为 **`bf4f00bb8d905f8a78f627af7cf84457c4db53c4`**。发行记录标记 `immutable=false`，因此下载仍须校验下列哈希，不以浮动 main、gh-pages 或 latest 地址作为产品输入。[Release](https://github.com/Zephyruso/zashboard/releases/tag/v3.30.0)、[tag ref](https://api.github.com/repos/Zephyruso/zashboard/git/ref/tags/v3.30.0)

| 输入 | 固定值与来源 |
| --- | --- |
| 无字体静态包 | [dist-no-fonts.zip](https://github.com/Zephyruso/zashboard/releases/download/v3.30.0/dist-no-fonts.zip)，asset ID `687213613`，`2,894,440` bytes；GitHub asset digest 为 SHA256 `ec794e7c4daf4c839ecbc0a1292fdaf7bc7a6e3e4a4ce284db2b32379145c278` |
| 源码归档 | Release 提供 [tag zipball](https://api.github.com/repos/Zephyruso/zashboard/zipball/v3.30.0)；固定完整提交可使用 [commit zipball](https://api.github.com/repos/Zephyruso/zashboard/zipball/bf4f00bb8d905f8a78f627af7cf84457c4db53c4)，本轮未取得归档字节或 SHA256 |
| package / 依赖管理器 | [package.json](https://github.com/Zephyruso/zashboard/blob/bf4f00bb8d905f8a78f627af7cf84457c4db53c4/package.json)：版本 `3.30.0`，`pnpm@11.20.0`，包含包管理器 SHA512；未声明 Node engines |
| 锁文件 | [pnpm-lock.yaml](https://github.com/Zephyruso/zashboard/blob/bf4f00bb8d905f8a78f627af7cf84457c4db53c4/pnpm-lock.yaml)，lockfileVersion `9.0`，Git blob `d39148f03935f9548c28e0ff3494f692a192ad3d` |
| 根许可证 | [LICENSE](https://github.com/Zephyruso/zashboard/blob/bf4f00bb8d905f8a78f627af7cf84457c4db53c4/LICENSE)：MIT，Git blob `4e20e4192e2dc9a98858879c2feb2a662011a3d1` |
| 静态资产声明 | [public/THIRD_PARTY_NOTICES.md](https://github.com/Zephyruso/zashboard/blob/bf4f00bb8d905f8a78f627af7cf84457c4db53c4/public/THIRD_PARTY_NOTICES.md)，Git blob `15f5075c7e20aad0d5892d97a823ba670f07e684` |

上游 [release-dist 工作流](https://github.com/Zephyruso/zashboard/blob/bf4f00bb8d905f8a78f627af7cf84457c4db53c4/.github/workflows/deploy.yml) 用 `FONT=none` 环境运行 `pnpm i`、`pnpm run build`，再执行 `zip -r dist-no-fonts.zip dist`，归档内有 `dist/` 顶层目录。Node setup 未固定 node-version。自行重建应固定可用 Node 与 packageManager，使用冻结锁文件安装；PowerShell 设置 `FONT` 后运行 `pnpm run build`，不直接执行含 Unix 环境赋值语法的 `build:no-fonts`。不承诺与上游 zip 逐字节相同。[Vite 配置](https://github.com/Zephyruso/zashboard/blob/bf4f00bb8d905f8a78f627af7cf84457c4db53c4/vite.config.ts) 使用相对资源基址 `./` 并构建 PWA；[字体入口](https://github.com/Zephyruso/zashboard/blob/bf4f00bb8d905f8a78f627af7cf84457c4db53c4/src/assets/load-fonts.ts) 在 `none` 分支不加载字体。

### 保持原包的集成边界

固定版确实支持 `disableUpgradeCore=1`、`disableTunMode=1`，可与后端地址/secret 一起放在 URL 查询或 hash 查询中；同源自动连接也包含于该正式版。**没有关闭“切换节点自动断开连接”的 URL 参数**：其全部后端 URL 解析在 [utils.ts](https://github.com/Zephyruso/zashboard/blob/bf4f00bb8d905f8a78f627af7cf84457c4db53c4/src/helper/utils.ts)，未读取该偏好。

自动断连在 [settings.ts](https://github.com/Zephyruso/zashboard/blob/bf4f00bb8d905f8a78f627af7cf84457c4db53c4/src/store/settings.ts) 默认 `true`，存储键是 `config/automatic-disconnection`；[选择动作](https://github.com/Zephyruso/zashboard/blob/bf4f00bb8d905f8a78f627af7cf84457c4db53c4/src/assembly/proxies/actions.ts) 成功选组后，会对 chain 包含该组的活跃连接调用 DELETE。可保持上游静态包不变，由 **与 dashboard 同源**的 SWD 引导页在载入它之前写入 `localStorage.setItem('config/automatic-disconnection', 'false')`。这是根据源码提出的集成方式，尚未浏览器验证；跨源的 SWD 主页面不能直接写 dashboard 的存储，且用户随后仍可在面板改回。

`disableUpgradeCore` 不等同禁用 dashboard 更新或配置重载。固定版仍有升级 UI、重启核心、修改/重载配置等动作；后端应只开放 SWD 已授权的操作，前端隐藏开关不是权限边界。`config/auto-upgrade`（面板自动更新）默认 false；是否保持该偏好与本地 PWA 版本更新需在集成中核对。[版本与升级实现](https://github.com/Zephyruso/zashboard/blob/bf4f00bb8d905f8a78f627af7cf84457c4db53c4/src/assembly/version.ts)、[API 实现](https://github.com/Zephyruso/zashboard/blob/bf4f00bb8d905f8a78f627af7cf84457c4db53c4/src/api/clash.ts)

### 兼容性、依赖和工具边界

节点/组操作使用 `/version`、`/proxies`、`PUT /proxies/{name}`、节点 `/delay`、`/group/{name}/delay` 与 `/providers/proxies`，这些路由在固定 Mihomo v1.19.32 中存在；面板通过版本字符串识别 Mihomo，未看到这些基本操作的更高最低版本要求。因此具备开展联调的源码依据，**不宣称全部面板功能已兼容或运行通过**。[固定面板 API](https://github.com/Zephyruso/zashboard/blob/bf4f00bb8d905f8a78f627af7cf84457c4db53c4/src/api/clash.ts)、[固定核心 router](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/hub/route/server.go)

无字体不等于仅含 MIT 代码。上游声明中的地球纹理是 Solar System Scope 素材的修改版，要求保留 **CC BY 4.0** 署名和修改说明；Three.js 示例派生部分是 MIT。声明还解释 DB-IP City Lite 为用户显式下载的 CC BY 4.0 数据、未随包附带。应保留该完整声明、根 LICENSE，并从固定依赖图生成最终 JS/CSS 依赖许可清单；不能把不包含字体外推为已完成全部依赖审计。[上游资产声明](https://github.com/Zephyruso/zashboard/blob/bf4f00bb8d905f8a78f627af7cf84457c4db53c4/public/THIRD_PARTY_NOTICES.md)

本轮可用 GitHub connector 能取 release 元数据、完整提交、树、锁文件和文本源码；其 `fetch` 明确拒绝非 UTF-8/超大响应，**不能下载 ZIP 二进制**，没有可用的 release-asset 二进制下载专用工具。这里已给出精确资源入口、大小和上游 SHA256，静态包字节保存、校验以及源码归档保存仍由实施阶段的可用下载能力完成。本研究未下载或运行用户节点，未做浏览器联调。

## 实际发行包许可核对补充

后续实施已将源码与依赖安装在 `artifacts/mihomo-dashboard/source/Zephyruso-zashboard-bf4f00b/`，发行文件在 `artifacts/mihomo-dashboard/release/dist/`。本小节只读核对这些材料，不代表全部发布检查通过。

**修正先前仅据 README 的“无字体”理解：`dist-no-fonts` 仍包含旗帜字体。** `src/assets/styles/theme/fonts.css` 无条件引用两个字体，源码和实际 dist 的字节相同。原 `public/THIRD_PARTY_NOTICES.md` 只列地球纹理、Three.js 示例和可选下载 DB-IP，没有列出这两个字体；SWD 应补充声明，不能仅沿用根 MIT 与该原有 notices。

| 实际生产材料 | 核实许可及路径 |
| --- | --- |
| Noto Color Emoji Flags 2.047 | `src/assets/fonts/NotoColorEmoji-flagsonly.ttf`，dist 为 `assets/NotoColorEmoji-flagsonly-CWWDk9km.ttf`；OFL-1.1，内嵌 Copyright 2022 Google Inc. |
| Twemoji Mozilla 0.7.0 旗帜子集 | `src/assets/fonts/TwemojiMozilla-flags.woff2`，dist 为 `assets/TwemojiMozilla-flags-B12sb_Bp.woff2`；Mozilla 代码 Apache-2.0、Twemoji 视觉设计 CC BY 4.0，不能只选其中一种 |
| 地球纹理 / 背景 | `src/assets/images/earth/` 及实际 dist 对应四张图片；CC BY 4.0，保留 `public/THIRD_PARTY_NOTICES.md` 的 Solar System Scope 署名及 resize/combine 修改说明 |
| dompurify 3.4.16 | `node_modules/.pnpm/dompurify@3.4.16/node_modules/dompurify/`：`(MPL-2.0 OR Apache-2.0)`，可明确选 Apache-2.0；`LICENSE` 为 Apache 全文，另有 `LICENSE-MPL` |
| zrender 6.1.0 | `node_modules/.pnpm/zrender@6.1.0/node_modules/zrender/LICENSE`：BSD-3-Clause，Baidu Inc. 版权 |
| tslib 2.3.0 | `node_modules/.pnpm/tslib@2.3.0/node_modules/tslib/LICENSE.txt` 与 `CopyrightNotice.txt`：0BSD，Microsoft 版权 |
| ECharts 6.1.0 的嵌入 d3.js | `node_modules/.pnpm/echarts@6.1.0/node_modules/echarts/licenses/LICENSE-d3`：BSD-3-Clause；还需 ECharts 根 `LICENSE` 和 `NOTICE`，不能只读取 package.json 的 Apache-2.0 |
| vuedraggable 4.1.0 的预打包代码 | 包内 `dist/vuedraggable.umd.min.js.map` 明列 core-js、@soda/get-current-script、@babel/runtime helpers 和 Vue CLI 运行片段；其中 core-js 自报 3.6.5，当前锁文件只有 core-js-compat 3.49.0，不能代替其许可 |

两字体及预打包代码的补充许可已保存在 [docs/licenses/zashboard/](../licenses/zashboard/README.md)，附固定来源、文本 blob、SHA256 与版本不确定范围。Google 内嵌提交对应的原厂字体与 zashboard 文件并非同一 blob；源码中没有子集/字体压缩配方，因此只承诺保留实际输入，不能声称从原始图形完整重建字体。独立 OFL/CC/Apache 资源的上述许可不自行要求交付字体生成工具链；最小可维护交付是随固定 zashboard 源码保留精确字体、声明、身份及来源记录，未来修改字体时再补可复跑配方。

通知收集建议以**实际交付的 JS/CSS、worker、service worker、懒加载 chunk 和静态资产**为依据；保守收集全锁定 npm 包许可也可行，但应标明是扩大覆盖范围。该方式仍要包含包内 `licenses/`、`NOTICE`、许可旁车及已预打包的外部代码，避免依赖图遗漏。`mmdb-lib@3.0.3` 虽在 devDependencies，却被 `src/` 运行时导入，不能只按 dependencies 字段排除；反过来 lightningcss 的 MPL-2.0、构建期 caniuse-lite 的 CC BY 4.0 或未选用文本字体的许可，不能仅因已安装就断言在生产 JS 中使用。实际包原有 notices 应原样保留，再把补充材料统一进入 SWD 发行通知；本轮未声称完成所有包的逐模块产物追踪。
