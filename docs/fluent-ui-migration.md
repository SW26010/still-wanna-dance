# Fluent UI React v9

SWD 自有 WebUI 使用 React、TypeScript 和 Fluent UI React v9；Go 通过 embed 提供编译后的资源，运行时不依赖 Node、CDN 或外部网络。

网络页的原版 zashboard 是独立的第三方面板：SWD 外壳、导航、加载／错误反馈和容器布局继续使用 Fluent v9，面板通过标准同源 iframe 承载，内部保留上游组件、主题和布局。它不属于 Fluent 控件，不注入样式或为统一外观维护面板分支。加载及职责边界以 [Mihomo 与 zashboard](mihomo-dashboard.md#监测与界面) 为准。

## 实现边界

组件迁移以 `207e431fefd150c59a4b6dc117b1aab535056007` 为历史文案和业务能力基准。2026-10-21 的使用体验修订按用户反馈调整页面归属、信息分层和文案，后续按 WebUI 目标设计提供开始／停止加速和退出，保留后台生命周期能力；现行行为以[内容约定](webui-content.md)与[配置生命周期](settings-lifecycle.md)为准，不再以历史逐字匹配限制已授权的改写。业务独有能力继续保留，通用交互优先采用标准组件，不为复刻旧控件的布局或细节增加自定义实现。

| 范围 | 当前实现 |
| --- | --- |
| 页面与主题 | FluentProvider、Card/CardHeader、Text、Image、Divider；程序主题选择及系统跟随、窄屏换行和 reduced-motion |
| 导航 | NavDrawer/NavItem/Hamburger；桌面左侧 inline 可展开／收起，≤640px 左侧 overlay；页面保留真实链接 |
| 字段与操作 | Field/Input/Select/Textarea/Checkbox/RadioGroup、Button/ToggleButton、Dialog、MessageBar、ProgressBar |
| 记录列表 | 曲库、请求历史和程序日志共用 RecordTable（Table/TableCell/TableCellLayout）及 TablePagination（Button/Text），需要排序的列表使用 SortHeader（TableHeaderCell），保留 Fluent 默认尺寸、排序箭头、aria-sort 与键盘交互，Table 显式采用原生 table-layout: auto 分配列宽，每例一行；日志仅显示时间、级别、事件和内容，前三列收紧到完整内容所需宽度，超宽文本通过原生横向滚动完整阅读，不另设详情列，事件关键词使用 Field/Input（type="search"）并由原生表单提交。线路检测同样使用 Table，首页需求使用随内容增高的 Canvas 需求列表和 Fluent Text／Caption1 展示标题与百分比。业务负责受保护项、请求行身份及移除行后的焦点回退 |
| 测速 | `@fluentui/react-charts` v9 GaugeChart；实际倍率与 1×/2× 阈值，线性范围至少 0–3，随更高样本扩展；没有自绘指针或分段刻度 |
| 块需求时间轴与需求视图 | Canvas 绘制连续时间与方块、可见需求行的块；D3 scale/zoom/selection 负责刻度及缩放拖动；Fluent Button、Switch、Popover、Badge 承担外围控件和共用详情，按需 SSE 提供数据 |
| 文件与目录选择 | 本地路径共用只读 Field/Input，点击或按 Enter／空格打开 Windows IFileDialog，旁边只保留 Fluent“恢复默认”按钮；默认路径用主题次级色，自选路径用正文色，原生窗口复用 SWD ICO。数据库在同一窗口选择已有文件或新文件名；节点文件内容导入沿用浏览器原生 file input |
| 必要业务逻辑 | 条款门禁、设置草稿、保存后状态同步、全局排序、曲库偏移量与历史游标分页、检测事件与取消恢复；不重复实现标准组件的键盘或弹窗行为 |

任务页面及条款弹窗的内容分工见[内容分类](webui-content.md)，平台辅助技术边界见[可访问性约定](webui-accessibility.md)。组件契约以锁定依赖的类型和实现为准；参考 [Fluent 官方接入](https://fluent2.microsoft.design/get-started/develop)与 [GaugeChart](https://microsoft.github.io/fluentui-charting-contrib/docs/Charting-Concepts/GaugeChart)。

## 页面排版

通用界面优先使用 Fluent 默认组件、官方字阶和主题 tokens。页面背景使用 `colorNeutralBackground2`，桌面 inline 导航与窄屏 overlay 均保留标准抽屉外观；Card 保留标准边框、阴影和圆角。页面标题与卡片标题分别使用 Title2、Subtitle2，说明使用次级文字色，语义表单分组保留 fieldset/legend，以标题和留白分隔；设置的常规与下载缓存分组之间使用 Fluent Divider。

图形和配色以 `internal/branding/logo.svg` 为唯一来源。页面 Logo 通过 SVG 的 `prefers-color-scheme` 跟随承载页面的 color-scheme；标签页图标复用同源生成的两套 ICO，按程序主题切换 `/favicon-light.ico` 和 `/favicon.ico`，避免浏览器选择固定深色后备图标或缓存 SVG 的旧配色。浅色使用已确认的第四版（背景 `#EAFFDF`、圆环 `#80DE50`、人物／闪电 `#20552A`，无描边），深色保持原图。Windows 托盘使用相同两套 ICO，并读取同一程序主题偏好；自动保存、系统跟随与同步规则见[配置生命周期](settings-lifecycle.md)。资源生成和离线构建要求见[产品图标约定](evidence-storage.md#git-历史和防回归)。

常规页面提供侧栏展开／收起按钮。桌面默认展开，收起后保留按钮并释放正文宽度；按钮保持挂载，键盘操作不会丢失焦点。应用内切换页面时保留展开状态，刷新后默认展开，不写入配置；窄屏抽屉仍由 Fluent 管理焦点、Escape 和遮罩关闭，独立于桌面展开状态。

桌面侧栏宽 144px，从页头下方延伸至视口底部内边距；按页头实际高度分配可用空间，适应页头换行，不使用固定页头高度或额外撑出短页面滚动条。长页面保留浏览器滚动和侧栏 sticky 定位。加速、历史、曲库、网络位于顶部，设置、关于通过可伸缩留白推至底部，不使用分隔线或固定 footer；所有入口保留在同一个 NavDrawerBody 中。空间不足时先收缩留白，留白耗尽后全部入口一起滚动，保留组件默认行高、内边距与键盘顺序。窄屏保留 260px 标准 overlay 抽屉，并沿用同一分组与滚动规则。

六个主页面共用页头与标题容器；应用内的关于页面同样保留后台状态。根滚动容器使用标准 [`scrollbar-gutter: stable`](https://developer.mozilla.org/en-US/docs/Web/CSS/Reference/Properties/scrollbar-gutter)，使长短页面切换时可用宽度一致；短页面不强制显示滚动条或产生额外滚动范围。

网络页检测卡片和嵌入面板按正常文档流排列，iframe 高度随视口变化并保留最低可操作高度，面板内部自行滚动；不按猜测的页头高度写固定补偿值。小视口保留最低可操作高度，空间不足时允许外层正常滚动，确保导航和面板操作可达。自有控件使用 Fluent 间距和颜色 token，面板不另套 Card、Dialog 或第二层标签。

后台状态与退出按钮按内容宽度排列，间距使用 Fluent spacing token，空间不足时自然换行；不为状态预留固定宽度。

后台程序、游戏连接、视频服务、房间队列统一使用 `Badge appearance="tint" size="large"`，由组件提供主题浅色底、描边与语义色文字，通过同一个轻量展示组件保持颜色、Fluent 图标及“名称：状态”格式一致。颜色直接使用 `success / danger / warning / brand / informative`，不定义自有色板；`informative` 在这里使用其中性色表达未启用／待确认。以 `role="status" aria-atomic="true"` 播报完整状态，装饰图标隐藏于辅助技术。MessageBar 保留给需要处理的消息；组件依据见[官方资料核对](research/fluent-v9-ui-patterns.md#四项运行状态2026-10-25)，业务语义见[内容约定](webui-content.md#程序与界面术语)。

容器只负责布局，控件使用 Fluent 的尺寸、主题、焦点和交互。曲库、设置及关于分类使用 TabList/Tab；网络页直接展示全部适用模块，无页内入口栏或第二层标签。设置字段直接显示，卡片不重复显示分类标题；当前页的状态、保存、放弃更改和重启操作与顶部页签同行右对齐，空间不足时自然换行，整行随页面滚动保持可达。短说明使用 Tooltip，记录补充信息及包含“修复”操作的游戏连接说明使用非模态 Popover；修复浮层支持悬停、点击和键盘，使用蓝色 Link 按钮，授权处理中禁用但保留焦点。不把常用功能藏进折叠。2026-10-21 的高级字段折叠规则已由本次简洁重构替代。

日志表格的展开窗口复用 Dialog/DialogTrigger/DialogSurface/DialogContent，按用户选择铺满网页视口；普通卡片与展开窗口共用查询结果，展开时滚动显示全部已读取记录（最多 500 条），退出后恢复原有页码。按用户要求，展开窗口仅显示日志行、原生双向滚动条及右上角浮动退出 Button；DialogSurface 通过 aria-label 命名，不设置可见标题／条数／提示，表头仅视觉隐藏并保留列头语义。展开布局移除边框及容器内外边距，滚动区贴满视口；焦点轮廓放入视口内部。展开期间取消根滚动条预留，退出后恢复普通页面规则，保留标准模态焦点、Esc 关闭及触发器焦点恢复。退出按钮不另套会先消费 Esc 的 Tooltip；普通展开入口仍提供 Tooltip 和无障碍名称。

设置与统计列通过 CSS Grid `auto-fit` 按实际容器宽度换行，容纳侧栏及嵌套区域；操作栏不另加边框或固定提示高度。表单默认使用 Field 的上方标签布局，避免窄容器挤压标签。设置中的请求保留天数与视频预算紧跟对应单选项，天数使用 Input 的 contentAfter 显示单位，预算使用 SpinButton 并在旁标明 GiB；独立字段标签仅视觉隐藏，保留无障碍名称与 ValidationField 校验。曲库搜索／筛选和历史时间筛选使用原生 `orientation="horizontal"`，将标签放到对应输入框左侧，仅在这些区域按标签内容分配列宽，避免默认比例占宽挤压输入框；宽屏同行，窄屏按字段自然换行，其他字段不覆盖组件内部网格。时间筛选使用 Field 的原生 `validationMessage`，错误随内容自然展开，不用绝对定位或空白占位保持结果坐标。轮询中的已有结果仍保持稳定。CardHeader 的 action 保留简短操作，批量控件放入内容区，避免挤压窄屏标题。组件依据为官方 [Field](https://fluent2.microsoft.design/components/web/react/core/field/usage/)、[Tooltip](https://fluent2.microsoft.design/components/web/react/core/tooltip/usage/) 与 [Toast](https://fluent2.microsoft.design/components/web/react/core/toast/usage/) 指南。

首页视频需求使用标准 medium Card、CardHeader、CardPreview、Badge 与 small 图标 Button。Canvas 区域作为末尾 CardPreview，贴合卡片左右与底边，不绘制单独外框或区域分隔线，保留内部内容间距；标题保持 Card 默认内边距，区域圆角由 Card 统一裁切。图例、工具栏与需求列表共用内容区左右内边距；工具栏左侧放标题与时间窗口，右侧将分层、缩放与跟随操作归为可自然换行的一组，使用原生 small Switch（含 small Label）及 small Button，缩放与跟随图标采用 16px 规格。需求行使用 borderRadiusMedium 主题圆角，来源类型使用次级 Caption1 文字。需求矩形四边使用等宽细边框，左右留白对称；列表随需求数量自然增高，不设置内部滚动条或预留滚动条空间，由页面统一滚动。Canvas 区域、需求行及实际绘制的底色共用卡片的 colorNeutralBackground1 主题 token，普通文字、次级文字及网格线分别共用 colorNeutralForeground1、colorNeutralForeground2 和 colorNeutralStroke2；高对比度模式使用系统 Canvas 等颜色。状态展示徽章复用运行状态的 StatusBadge，保持尺寸、图标、主题色和完整状态播报一致，采用[程序与界面术语](webui-content.md#程序与界面术语)约定的“状态展示：状态”格式。下载速度采用 small transparent Button，以原有 Text 字阶显示数值与单位；按钮支持点击及键盘切换单位，Tooltip 说明动作，具体单位与换算遵循[内容约定](webui-content.md#内容与交互)。标题信息和图例各自使用可换行布局，间距使用主题 spacing tokens，不缩小组件内部点击区域。CardHeader 的 action 只放时间轴展开按钮，Tooltip 在悬浮和键盘聚焦时说明动作。依据为官方 [Card 布局](https://fluent2.microsoft.design/components/web/react/core/card/usage)与 [Tooltip 行为](https://fluent2.microsoft.design/components/web/react/core/tooltip/usage)。

Canvas 绘制文字读取 Fluent 的字体族、字号和字重，不另写固定像素字号；时间刻度（含「现在」标签）、图例及分层说明使用比正文小的 Caption1 字号（fontSizeBase200），「现在」标签使用同字号的主题半粗字重，其余 Canvas 说明沿用正文字号。卡片内部的「时间轴视图」「需求视图」小标题使用正文 Text（size=300）的半粗字重，低于卡片标题的 Subtitle2 字阶。需求歌曲名使用正文 Text（size=300）的常规字重，需求行标题按正文行高自然排布，仅保留上方小间距；块上方的 Canvas 空间保留读取圆点所需的 12 个 CSS 像素，避免裁切。时间轴与需求视图的 Canvas 绘制、视频配色和尺寸保持独立业务规则。需求浮层使用 Fluent 的 fixed 定位、自动限高与避让，与视口坐标锚点一致，避免源详情加载改变高度时使长页面跳动，并让小视口中的操作始终可达。

### 首页加速状态与按钮

加速总状态保留 Title2 字阶；仅“加速未运行”使用 colorNeutralForeground4 弱化灰色，运行、未就绪和处理中的状态保留默认正文色。

启停沿用原生 `Button appearance="secondary" size="large"`，与网络检测使用相同原生外观；未运行与运行时分别使用 Play20Filled、Stop20Filled 图标。仅在此按钮局部将 `--colorNeutralBackground1Hover` 映射到 `var(--colorBrandBackground2Hover)`，让悬停使用不透明的浅主题色并保持原生图标的对比度；常态、按下、禁用、高对比度和键盘焦点样式由 Fluent 提供。动作名称与提示内容见[内容约定](webui-content.md#内容与交互)。

## 开发与交付

- 源码和锁文件：`webui/src`、`webui/pnpm-lock.yaml`；安装使用 `pnpm --dir webui install --frozen-lockfile`。
- `pnpm --dir webui build`：生成 `internal/console/assets/webui`、第三方声明和构建清单；产物入 Git，支持离线 Go 构建。
- `pnpm --dir webui check`：类型检查并验证重建产物逐字节一致。`scripts/check-webui.mjs` 提供无需前端依赖的源码/产物哈希检查。
- `pnpm --dir webui test`：本机 Edge、CI Chromium；只使用内存 API 夹具。最新截图在忽略目录 `artifacts/webui-tests`，不是实机证据。
- 构建实际引用的依赖声明写入 `docs/licenses/WEBUI-NOTICES.txt`。图标包 2.0.344 的 tarball 缺 LICENSE，使用仓库保存的上游许可证；升级时重新核对。
- 正式对应源码包含 pnpm 完整包存储，清除机器相关 junction 后离线重建前端并核对产物，再构建 Go。流程见[打包](packaging.md)与[对应源码](corresponding-source.md)。

## 验证与待验收

浏览器、类型、资源一致性及隔离真实桌面程序的结果按轮次保存在[WebUI 开发验证记录](validation/webui-validation.md)。用例位于 `webui/e2e/`，内存服务器及 Go 联调驱动位于 `webui/test-support/`；测试输出保存在忽略目录 `artifacts/`。

验证分工遵循[现行机制](live-acceptance-process.md)，UAC、目录窗口、托盘、系统 DPI 和辅助技术的环境缺口见[非 VRC 跟进](validation-followups.md)。这里只引用记录，不再维护另一份逐项实机清单。

曲库歌曲／资源切换使用 TabList/Tab，分页结果使用紧凑 Table 与正常 Tab 顺序；偏好使用互斥且可取消的 ToggleButton，图标操作配合 Tooltip 与无障碍名称，本地状态及保护使用并列 Badge，删除复用共享 Fluent Dialog。单项下载只调用既有任务接口，不实现自定义任务控件。

设置四类和关于五类内容采用标准 TabList/Tab/tabpanel，网络页常驻连通性检测、下载速度测试及当前连接模式对应的内容，配置字段仅在设置中编辑。设置表单持续挂载，沿用独立部分草稿与最新保存配置合并；程序主题在网页中采用同一 FluentProvider，不另造主题样式系统。

2026-10-25 曲库与请求历史已按[简洁重构](webui-design.md#当前界面规则)移除嵌套统计折叠，使用 Tooltip、Popover 与 Toast；早期的统一纵向 Card/Accordion 排布不再作为现行界面要求。

## 设置字段的校验反馈（2026-11-04）

按本项目交互约定，设置页使用共用 `ValidationField`：保留 Field 的 `validationState=error`、`aria-invalid`、描述关联及错误播报，`validationMessage` 仅作视觉隐藏的无障碍说明；控件旁固定 24px 图标槽位，使用 Fluent `ErrorCircleRegular` 与 Tooltip。错误原因只在 hover／键盘聚焦图标时可见，不增加文字行；这是项目要求的组合方式，不宣称为 Fluent 官方默认校验模式。原生 file input 通过 Field 的 render props 接收标签与错误关联；只读路径框同样可高亮。设置表单使用 `noValidate` 关闭原生气泡，仍依据输入的 `required/min/max/step` 与 ValidityState 校验，不改变数值范围。预算的 SpinButton 使用文本输入以独立设置两位小数精度与 1 GiB 步进，范围及小数位数由共用 inputError 校验；其余数值框继续使用原生 ValidityState。预算按钮在捕获 mousedown 时先同步提交输入框 blur，避免 Fluent 9.74.9 在同一鼠标事件内提交文本并步进时使用旧值；方向键仍用组件默认逻辑。历史与日志的时间筛选保留原有字段下方错误，未纳入本次设置页修改。
