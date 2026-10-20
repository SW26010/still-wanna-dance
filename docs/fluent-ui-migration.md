# Fluent UI React v9

全部 Web UI 使用 React、TypeScript 和 Fluent UI React v9；Go 通过 embed 提供编译后的资源，运行时不依赖 Node、CDN 或外部网络。

## 实现边界

以 `207e431fefd150c59a4b6dc117b1aab535056007` 为文案和业务能力基准。保留文字内容及业务独有功能，通用交互优先采用标准组件，不为复刻旧控件的布局或细节增加自定义实现。组件替换导致说明失实时只调整必要说明，其余原文不顺带改写。

| 范围 | 当前实现 |
| --- | --- |
| 页面与主题 | FluentProvider、Card/CardHeader、Text、Image、Divider；系统深浅主题、窄屏换行和 reduced-motion |
| 导航 | NavDrawer/NavItem；桌面左侧 inline，≤640px 左侧 overlay；页面保留真实链接，引导步骤校验后切换 |
| 字段与操作 | Field/Input/Select/Textarea/Checkbox/RadioGroup、Button、Dialog、Accordion、MessageBar、ProgressBar |
| 记录列表 | List/ListItem 的官方 composite 键盘导航；业务负责受保护项、请求分组身份及移除行后的焦点回退 |
| 测速 | `@fluentui/react-charts` v9 GaugeChart；实际倍率与 1×/2× 阈值，线性范围至少 0–3，随更高样本扩展；没有自绘指针或分段刻度 |
| 块需求时间轴与需求视图 | Canvas 绘制连续时间与方块、可见需求行的块；D3 scale/zoom/selection 负责刻度及缩放拖动；Fluent Button、Slider、Switch、Popover、Badge 承担外围控件和共用详情，按需 SSE 提供数据 |
| 文件选择 | 浏览器原生 file input，通过 Field 关联标签；Fluent Input 不支持 file 类型。目录选择调用 Windows IFileDialog |
| 必要业务逻辑 | 条款门禁、独立草稿、保存后状态同步、原 ID 回执核对、请求交集合并、检测事件与取消恢复；不重复实现标准组件的键盘或弹窗行为 |

五页及四步引导的内容分工见[内容分类](webui-content.md)，平台辅助技术边界见[可访问性约定](webui-accessibility.md)。组件契约以锁定依赖的类型和实现为准；参考 [Fluent 官方接入](https://fluent2.microsoft.design/get-started/develop)与 [GaugeChart](https://microsoft.github.io/fluentui-charting-contrib/docs/Charting-Concepts/GaugeChart)。

## 页面排版

通用界面优先使用 Fluent 默认组件、官方字阶和主题 tokens。页面背景使用 `colorNeutralBackground2`，桌面 inline 导航与窄屏 overlay 均保留标准抽屉外观；Card 保留标准边框、阴影和圆角。页面标题与卡片标题分别使用 Title2、Subtitle2，说明使用次级文字色，语义表单分组保留 fieldset/legend，以标题和留白分隔。

卡片和展开详情共用正文纵向间距，直接子段落、列表与操作区不再叠加外边距；空状态播报节点保留在 DOM 中，但不占布局行。单独的按钮按内容宽度排列，搜索表单内操作与字段底部对齐。设置页先展示偏好设置，再展示外部导入和使用引导；保存状态、按钮及重启说明保持在同一组。上述规则只统一排版，不删改正文或业务行为。

时间轴与需求视图的 Canvas 绘制、视频配色、尺寸和交互保持独立业务规则，本轮排版调整不修改其实现或专用样式。

## 开发与交付

- 源码和锁文件：`webui/src`、`webui/pnpm-lock.yaml`；安装使用 `pnpm --dir webui install --frozen-lockfile`。
- `pnpm --dir webui build`：生成 `internal/console/assets/webui`、第三方声明和构建清单；产物入 Git，支持离线 Go 构建。
- `pnpm --dir webui check`：类型检查并验证重建产物逐字节一致。`scripts/check-webui.mjs` 提供无需前端依赖的源码/产物哈希检查。
- `pnpm --dir webui test`：本机 Edge、CI Chromium；只使用内存 API 夹具。最新截图在忽略目录 `artifacts/webui-tests`，不是实机证据。
- 构建实际引用的依赖声明写入 `docs/licenses/WEBUI-NOTICES.txt`。图标包 2.0.344 的 tarball 缺 LICENSE，使用仓库保存的上游许可证；升级时重新核对。
- 正式对应源码包含 pnpm 完整包存储，清除机器相关 junction 后离线重建前端并核对产物，再构建 Go。流程见[打包](packaging.md)与[对应源码](corresponding-source.md)。

## 验证与待验收

2026-10-16，当前实现的 **39 项 Edge 内存场景通过**；类型、资源一致性、Node 工具测试及 Go 页面入口/条款/帮助筛选通过。覆盖基准正文、左侧导航、表单校验/保存同步、缓存毫秒时间戳/零值显示与保护/确认、暂停与请求分组、引导与回执恢复、切步保留检测上游及结果、检测与取消、测速阈值、320/390px、深浅主题及强制颜色。截图已检查；大倍率按 GaugeChart 默认截断与提示显示，完整值保留在可访问名称和 Mbps 指标中。

不将中间版本的测试次数、旧包哈希、已移除脚本或废弃控件布局作为当前证据。缓存时间显示、保存/重启、实际出口/测速由 agent 用浏览器及隔离真实服务验证；UAC/hosts、目录窗口、托盘、系统 DPI 和 Narrator/NVDA 按[非 VRC 分流](validation-followups.md)记录具体环境覆盖与缺口。原 LA-023、LA-030/045 不再单列游戏验收；只有真实播放器和游戏事件的剩余风险进入[台账](live-acceptance-backlog.md)。已有自动/浏览器结果按其覆盖范围复用，未覆盖部分不冒称通过。

### 通用排版收敛（2026-10-20）

本轮 Edge 内存夹具整套 83 项通过，覆盖正文保留、设置草稿与保存、导航和焦点、深浅主题、320/390px、forced-colors，以及时间轴现有专项。随后将暂停展示的说明改为 Fluent Field hint，相关暂停、固定详情、窄屏、正文与抽屉 8 项定向复验通过。另查看了五个控制台页面的桌面深浅主题截图，均无横向溢出或页面错误；导航保留 Fluent 默认背景。类型、前端构建和内嵌产物一致性检查通过。Canvas 源码及其专用 CSS 与本轮修改前一致；不新增 VRC 验收项，系统 DPI 和屏幕阅读器的既有验证边界继续适用。

证据在忽略目录 `artifacts/fluent-layout/` 与 `artifacts/fluent-layout-browser*.txt`。首轮沙箱内浏览器启动即退出，未执行页面断言；沙箱外首轮为 82 项通过，另一个旧条款超时测试因没有配置控制台状态夹具而收到 `{ok:true}`，读取 `storageDir` 时导致页面退出。已在该测试补用共用 `mockAPI`，保留原成功、等待和超时断言；失败输出及独立错误记录保留，最终整套结果为 `fluent-layout-browser-final.txt`，Field 定向结果为 `fluent-layout-browser-field.txt`，最终桌面截图及尺寸检查见 `fluent-layout/gallery.json`。

### 页面导航验证（2026-10-20）

帮助、关于、条款接入已有 Hash 路由，复用现有内容组件和后端启动数据；已同意条款的直接文档入口进入同一个应用，未同意时保留公开文档与条款门禁。八项导航、文档正文链接和页脚往返不重新请求 HTML/JS/CSS，前五页保持挂载。文档内容不依赖状态接口成功，路径仍区分已保存与当前生效配置。

37 项相关 Edge 内存接口回归通过，覆盖 390px/1280px 导航无整页重载、草稿保留、前进后退、旧地址直达与刷新、状态接口不可用时文档仍可读、条款操作后刷新控制台，以及既有引导、连接恢复、正文、键盘和存储故障场景。已检查桌面、窄屏及深色文档截图。Go 定向检查通过，覆盖真实 HTTP handler 的四个入口及同意前后启动数据、路径转义、资源响应、门禁、同意持久化和 Windows DPAPI；类型检查及嵌入资源一致性检查通过。这些检查不涉及用户安装环境或游戏行为。

结果保存在忽略目录 `artifacts/document-navigation/`：`browser-verified.txt`、`browser-verified/` 和 `backend-verified.txt`。首轮浏览器 36/37，失败暴露内存夹具未向引导新开的帮助窗口传递未同意状态；现以测试会话 cookie 同步该状态，产品门禁没有为夹具改变。`browser-first.txt` 保留该次失败；`backend-first.txt` 保留 Windows 沙箱内文件替换的 `The handle is invalid` 错误，同批检查在沙箱外经独立测试临时目录通过。

### 块需求时间轴验证（2026-10-20）

- 初版 Edge 内存接口整套 57 项通过；补入启动前等待场景后，时间轴专项 9 项通过。覆盖增量/移除/重连完整状态、重叠选择、来源更新、无期限行、键盘、缩放/拖动/跟随恢复、暂停与页面/标签/视野隐藏时断流，以及 320/390px、深浅主题、forced-colors、reduced-motion。后续三层与需求视图的当前证据见下方，初版全套结果不外推为新布局的全站回归。
- `TestBlockTimelineBrowser` 使用真实控制台路由、调度器、临时文件和回环 Range 上游，Edge 不拦截 API。已验证实际 GET 的下载/读取急需及来源详情、结束后 4 块延续保护（初版灰色，现为视频色半透明）、实际 HTTP 正文一致、收起后活动 SSE 为零。它使用测试专用配置和 `testConsole` 的独立条款夹具，不是用户安装环境或真实公网/游戏验收，也不会操作 hosts。
- 初版 4096 个密集覆盖块只使用一个 Canvas；当时本机绘制回调约 1.6–2.4 ms，收起后不再绘制。后续需求视图另外使用可见行 Canvas，当前数字如下。所有帧样本只包含 JS 绘制指令提交，不包含完整 GPU 合成成本，不外推游戏帧时间或其他设备。后端活动快照和关闭成本见[按需块观测](block-scheduler-design.md#按需块观测2026-10-20)。
- 检查了窄屏三种主题、属性浮层及真实下载/保护截图。最终原始输出、截图与帧耗时保留在忽略目录 `artifacts/block-timeline/`。初次浏览器启动被 Windows 沙箱的临时配置文件替换限制阻止；改用沙箱外的独立测试目录后正常。首次断言误将 Fluent Popover 的 `group` 当成 `dialog`，已修正定位器并保留该轮失败输出；没有修改控件语义迎合测试。
- 排版检查后暴露过来源加载改变浮层位置、鼠标仍在原块时延迟关闭导致按钮消失的问题。已统一清理关闭计时器，并在关闭前核对鼠标是否仍位于所选块或浮层；重叠块移除时也同步清理选择集合。失败保留在 `browser-final-polish.txt`、`browser-complete.txt`，修正后专项结果在 `browser-hover-fixed.txt`。
- 新增 D3 模块采用 ISC，随实际链接依赖生成 `WEBUI-NOTICES.txt`，保留上游声明；继续遵循项目 GPL-3.0-only 分发和对应源码要求。不引入额外字体或图形资源许可。

同日三层与需求视图扩展：

- 当前时间轴专项 10 项通过；最后补充的无期限三层命中场景单独复验通过。包含 4–36 px 方形绘制、相同视频跨状态配色、三层点击命中、GET／队列位置／手动排序、同歌矩形边框、来源撤销／换位、行内横向滚动到视频第 64 块、悬浮与键盘共用详情、最多一个主订阅加一个详情订阅。320/390px、暗色与 forced-colors 截图包含需求视图。类型、构建与嵌入资源一致性检查通过。
- 隔离真实控制台联调通过，增加了实际 GET 需求矩形出现、结束后矩形撤销而保护块保留的断言。仍未启动游戏或修改 hosts。
- 4096 块样本使用一个时间轴 Canvas 加一个可见需求行 Canvas；时间轴 33 个绘制回调样本中位 3.1 ms、P95 4.1 ms，收起后停止绘制。需求行仅在事件、选择和滚动时绘制，不随时间轴逐帧更新；可见范围以外的行和块不逐个创建元素。
- 原始输出、最终截图与样本保留在 `artifacts/block-demand-layout/`（`browser-third.txt`、`browser-final.txt`、`real-browser/result.json`）。前两轮失败暴露长面板视口变化时同一 IntersectionObserver 批次包含多次过渡，旧回调只取首项会错误暂停；已改为取最后一项。Canvas 记录断言曾误把斜纹描边当作块边框，并在绘制 effect 前读取旧尺寸，已修正测试记录与等待；失败输出保留为 `browser-first.txt`、`browser-second.txt`。

同日保护期透明度与视频内位置调整：当前专项 11 项通过，包含实际画布像素的原视频色 35% 不透明度检查、稀疏需求的前缀／中间空白、不同需求行的相同块对齐、移除前序需求后位置不变、空档点击不打开详情及键盘跳过空档。已查看稀疏需求与真实保护期截图。后端定向检查验证完整视频总块数独立于剩余需求、包含不足一块的尾部；真实控制台联调确认 GET 行总块数为 4，结束后仍正常释放需求与订阅。原始结果和截图在 `artifacts/block-demand-position/`。这些变化只读取已有大小并调整观测数据与绘制，不添加核心后台工作。

同日补充块边界、交接信息与编号：12 项场景均已通过（11 项首轮通过，新增场景修正像素采样后单独通过）。新增检查覆盖同色重叠块的深色细边、需求变动与重连时保留配色、交接仅外框白色且内部块原色、`13/45` 视频内编号及交接详情。已检查最终交接总览与详情截图；类型、嵌入资源一致性、后端定向与 race 检查通过。后端分别验证从队列转为交接时保留标题、交接后才打开视图时读取本地目录、已知歌曲身份优先、部分尾块计数和缓存清理；真实控制台联调确认下载及保护期详情均为 `1/4`。本机 4096 块画布样本中位约 3.8 ms、P95 约 5.1 ms，关闭后停止绘制；观测快照约 6.26 ms、8.50 MB，未订阅通知检查仍为 0 B／0 次分配。样本不含完整 GPU 合成成本，不外推游戏性能。

本轮证据在 `artifacts/block-demand-identity/`，保留初次队列夹具字段编译错误及浏览器边缘像素断言失败：时间平移使抗锯齿边缘有 1–2 色阶差异，稳定配色改为比较实心中心像素，同时仍独立断言边缘比主色深。最终新场景与截图为 `browser-final.txt` / `browser-final/`，其余 11 项结果为 `browser-first.txt`；后端为 `backend-second.txt`、`backend-race.txt`，真实联调为 `real-browser/result.json`。

同日滚轮交互调整后，时间轴专项 13 项通过：纵向滚轮、按钮与键盘缩放保持跟随／固定及视图中心；横向滚轮和 Shift + 滚轮只平移，跟随模式下保留时间偏移并继续走时；按住拖动后固定，“回到现在”清除偏移并恢复居中。暂停后移除滚轮监听，原有隐藏／恢复、来源详情和 4096 块收起停止绘制检查仍通过。已检查三层标签覆盖窗口色带的透明背景截图，类型与嵌入产物检查通过。结果与截图保留在 `artifacts/block-timeline-navigation/`。本轮仅改前端导航和绘制，不改变核心观测或下载行为。

同日需求行宽度与层叠顺序调整后，时间轴专项 14 项通过，类型与嵌入产物一致性检查通过。交接行位于 GET 和队列之间；行内块随矩形宽度分布，保留完整视频中的空档，允许重叠且不设横向滚动，替代上文历史版本的固定间距与行内滚动规则。像素及交互检查覆盖时间轴较早时间、需求行较早视频位置在上层，选中不提层，悬浮命中与浮层切换一致；另检查 390px 下 36px 方块的首尾完整可见、键盘选择和行内无溢出。已查看宽屏、窄屏及暗色截图，原有颜色延续、稀疏位置、4096 块收起停止绘制与隐藏断流检查仍通过。本轮只改前端显示与交互，不增加核心后台工作。

证据保留在 `artifacts/block-demand-fit/`：`browser-final.txt` / `browser-final/` 为最终 14 项结果及截图。首次沙箱内 Edge 在启动阶段管道关闭，尚未执行页面断言，记录在 `browser-first/`；改用隔离目录在沙箱外执行。`browser-second.txt`、`browser-focused.txt` 保留像素检查的两次失败：反转层叠后边框采样改到较早块的可见边缘，按跨像素的描边区域取样；需求行切换检查等待实际重绘完成后再比较颜色。这些检查使用内存接口，不涉及游戏验收。

同日修复悬浮集合更新：此前 14 项未覆盖从单块移入重叠区的连续悬浮，两个视图均会保留旧命中集合，缺少重叠切换按钮；从较晚块移入时也会保留旧选中块。复现证据保留在 `artifacts/block-hover-review/`。现在两个入口共用悬浮处理，未固定浮层在命中集合或最上层块变化时立即同步，首次悬浮仍延迟 100 ms，集合不变时保持稳定。新增两个 Edge 场景覆盖进入／退出重叠区、较晚块移入后选中上层块、切换重叠块与点击单块后的固定行为，以及关闭详情释放订阅；当前专项 16 项、类型及内嵌产物一致性检查通过，结果与截图在 `artifacts/block-hover-fix/`。更新只由已有鼠标事件触发，不新增轮询或核心后台工作。

同日观测性能优化：主画布单独离屏时停止 RAF 和自动重绘，下方可见需求行继续接收同一 SSE；回到视野恢复最新状态。仅额度／统计变化的增量复用块与需求集合，不重建颜色映射、不触发块排序或需求行重绘；只有需求变化时保留块集合，只有块变化时保留需求集合。颜色延续、移除和重连仍保持原语义，旧快照不会被修改。主画布滚回后，若鼠标事件早于可见性通知，先同步画布再命中，避免读取离屏期间的旧布局。

当前 Edge 专项 18 项、类型和内嵌产物检查通过；新增检查直接记录 Canvas 绘制次数，验证主画布离屏停止绘制、可见需求行继续更新、滚回恢复，以及无变化集合的复用与旧快照隔离。真实控制台／调度器／回环 Range 上游联调通过，下载来源详情、4 块延续保护、正文与收起后零 SSE 均保持正常。后端锁占用测量和并发验证见[按需块观测](block-scheduler-design.md#按需块观测2026-10-20)，没有新增关闭 UI 期间的后台工作。

本轮证据保留在 `artifacts/block-observation-performance/`：`browser-verified.txt` / `browser-verified/` 为最终 18 项结果与截图，`real-browser/` 为真实联调结果与截图。`browser-first.txt`、`browser-repro.txt` 保留离屏后切换无期限布局、滚回首次点击未命中的失败，已修正上述恢复顺序；`browser-final.txt`、`browser-targeted.txt` 保留悬浮脚本在异步来源详情改变浮层位置期间直接移动鼠标造成的失败，动作改用 Playwright 标准 `hover` 可操作性等待，原有重叠与固定断言保持不变。

同日排查固定详情偶发消失：真实联调复现了截图期间整个面板短暂离屏，原逻辑在 `active=false` 时清空固定选择；该事件发生在释放下载之前，并非保护期转换移除了块。现在暂时不可见只隐藏固定详情，恢复时等待新订阅的完整快照，再验证所选块；同时避免沿用旧连接状态，为已移除的块短暂重开详情请求。仅保留前端选择，离屏、暂停和收起仍释放订阅，不新增核心状态、后台观测或轮询。

新增两个确定性 Edge 场景覆盖时间轴／需求行固定选择、离屏期间转为保护、需求行消失后的详情恢复及关闭焦点、所选块移除后清空选择且不发起详情请求。修复前均失败，最终专项 20 项及类型／内嵌产物检查通过；真实控制台、调度器、回环 Range 下载连续三次联调通过，包含离屏零 SSE、返回后固定详情恢复、4 块保护及收起后零 SSE。

证据在 `artifacts/block-detail-protection/`：`diagnostic-7/diagnostics.json` 为原偶发问题的事件序列，临时诊断补丁另存且已从产品中移除；`before.txt`、`browser.txt`、`removed-request.txt` 保留固定选择丢失与无效详情请求的失败。`real-browser-1.txt` 保留测试在全部订阅释放期间完成下载，却仍要求保留历史 need-by 的错误假设；最终脚本在恢复观测后释放下载，保留连续观测时的历史定位断言，不要求后台保留历史。最终结果为 `browser-final.txt`、`removed-request-fixed.txt` 和 `real-final-1/` 至 `real-final-3/`。

同日修复需求列表更新时的闪烁：浏览器逐帧复现新需求行先显示空画布或默认宽度，再完成绘制；减少动态效果的全局极短过渡还会让尺寸短暂停留在旧值。需求行现于布局阶段测量并绘制，仅尺寸实际变化时重设画布缓冲区；列表缩短在渲染前限制可见范围，列表和画布尺寸明确不做 CSS 过渡。仍使用原事件订阅，不增加显示延迟、轮询或核心后台工作。

新增普通／减少动态效果两个场景，连续替换 GET、保留队列、滚到末尾后将 31 行缩至 3 行，并检查每个采样帧的行存在性、画布像素和宽度；两种模式各重复三次通过。最终时间轴专项 22 项、类型／产物一致性检查和真实后端联调通过。证据在 `artifacts/demand-view-stability/`：`before.txt` 保留原空白与宽度异常，`targeted.txt`、`width-diagnostic-*/` 保留定位极短尺寸过渡的过程，`targeted-final.txt`、`browser.txt` 与 `browser/` 为最终逐帧结果和截图，`real-browser/` 为真实下载、保护期及零订阅结果。逐帧记录仅存在于测试，不进入产品运行路径。

同日审查补充实际点击暂停和需求行移除：暂停控件原先被 Popover 当作外部关闭，导致固定选择在进入暂停前丢失；现在固定详情忽略外部自动关闭，Escape 和关闭按钮仍有效。需求视图复用已有焦点恢复机制，当前行撤销后回到具名区域，保留剩余行的 Tab 入口，也不抢走外部控件焦点。两个新增浏览器场景修复前均失败，修复后时间轴专项 24 项、类型与内嵌产物检查通过；真实控制台／调度器／回环 Range 联调确认下载、保护期和零订阅生命周期。证据保留在 `artifacts/block-review-followup/`：`browser-before.txt` / `browser-before/` 为原失败，`browser.txt` / `browser/` 与 `real-browser/result.json` 为最终结果。没有增加轮询或后台观测。

隔离联调入口（需要已安装前端依赖和 Edge；CI 可用 Playwright Chromium）：

```powershell
$env:SWD_TIMELINE_BROWSER_EVIDENCE = Join-Path (Get-Location) 'artifacts/block-timeline/real-browser'
./scripts/with-test-temp.ps1 go test -tags=no_easytier ./internal/console -run '^TestBlockTimelineBrowser$' -count=1 -v
Remove-Item Env:SWD_TIMELINE_BROWSER_EVIDENCE
```

本次不新增 VRC 待验项；既有调度器游戏体验边界仍在原场景中。系统 DPI、浏览器大倍率缩放及 Narrator/NVDA 的既有平台验证缺口仍适用，不能从 Canvas 键盘与截图检查推断已完成辅助技术验收。
