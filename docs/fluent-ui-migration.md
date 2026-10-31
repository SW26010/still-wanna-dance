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
| 记录列表 | 曲库和请求历史共用 RecordTable（Table/TableCellLayout）、SortHeader（TableHeaderCell）及 TablePagination（Button/Text），保留 Fluent 默认尺寸、排序箭头、aria-sort 与键盘交互，Table 显式采用原生 table-layout: auto 分配列宽，每例一行；线路检测同样使用 Table，首页需求使用 Canvas 虚拟列表和 Fluent Text／Caption1 展示标题与百分比。业务负责受保护项、请求行身份及移除行后的焦点回退 |
| 测速 | `@fluentui/react-charts` v9 GaugeChart；实际倍率与 1×/2× 阈值，线性范围至少 0–3，随更高样本扩展；没有自绘指针或分段刻度 |
| 块需求时间轴与需求视图 | Canvas 绘制连续时间与方块、可见需求行的块；D3 scale/zoom/selection 负责刻度及缩放拖动；Fluent Button、Slider、Switch、Popover、Badge 承担外围控件和共用详情，按需 SSE 提供数据 |
| 文件与目录选择 | 本地路径共用只读 Field/Input，点击或按 Enter／空格打开 Windows IFileDialog，旁边只保留 Fluent“恢复默认”按钮；默认路径用主题次级色，自选路径用正文色，原生窗口复用 SWD ICO。数据库在同一窗口选择已有文件或新文件名；节点文件内容导入沿用浏览器原生 file input |
| 必要业务逻辑 | 条款门禁、设置草稿、保存后状态同步、全局排序、曲库偏移量与历史游标分页、检测事件与取消恢复；不重复实现标准组件的键盘或弹窗行为 |

任务页面及条款弹窗的内容分工见[内容分类](webui-content.md)，平台辅助技术边界见[可访问性约定](webui-accessibility.md)。组件契约以锁定依赖的类型和实现为准；参考 [Fluent 官方接入](https://fluent2.microsoft.design/get-started/develop)与 [GaugeChart](https://microsoft.github.io/fluentui-charting-contrib/docs/Charting-Concepts/GaugeChart)。

## 页面排版

通用界面优先使用 Fluent 默认组件、官方字阶和主题 tokens。页面背景使用 `colorNeutralBackground2`，桌面 inline 导航与窄屏 overlay 均保留标准抽屉外观；Card 保留标准边框、阴影和圆角。页面标题与卡片标题分别使用 Title2、Subtitle2，说明使用次级文字色，语义表单分组保留 fieldset/legend，以标题和留白分隔。

图形和配色以 `internal/branding/logo.svg` 为唯一来源。页面 Logo 通过 SVG 的 `prefers-color-scheme` 跟随承载页面的 color-scheme；标签页图标复用同源生成的两套 ICO，按程序主题切换 `/favicon-light.ico` 和 `/favicon.ico`，避免浏览器选择固定深色后备图标或缓存 SVG 的旧配色。浅色使用已确认的第四版（背景 `#EAFFDF`、圆环 `#80DE50`、人物／闪电 `#20552A`，无描边），深色保持原图。Windows 托盘使用相同两套 ICO，并读取同一程序主题偏好；自动保存、系统跟随与同步规则见[配置生命周期](settings-lifecycle.md)。资源生成和离线构建要求见[产品图标约定](evidence-storage.md#git-历史和防回归)。

常规页面提供侧栏展开／收起按钮。桌面默认展开，收起后保留按钮并释放正文宽度；按钮保持挂载，键盘操作不会丢失焦点。应用内切换页面时保留展开状态，刷新后默认展开，不写入配置；窄屏抽屉仍由 Fluent 管理焦点、Escape 和遮罩关闭，独立于桌面展开状态。

常规侧栏按导航内容自然占高，仅限制最大高度以适应视口；不以整屏高度撑开页头下方的区域。长页面保留浏览器滚动和侧栏 sticky 定位。

六个主页面共用页头与标题容器；应用内的关于页面同样保留后台状态。根滚动容器使用标准 [`scrollbar-gutter: stable`](https://developer.mozilla.org/en-US/docs/Web/CSS/Reference/Properties/scrollbar-gutter)，使长短页面切换时可用宽度一致；短页面不强制显示滚动条或产生额外滚动范围。

网络页检测卡片和嵌入面板按正常文档流排列，iframe 高度随视口变化并保留最低可操作高度，面板内部自行滚动；不按猜测的页头高度写固定补偿值。小视口保留最低可操作高度，空间不足时允许外层正常滚动，确保导航和面板操作可达。自有控件使用 Fluent 间距和颜色 token，面板不另套 Card、Dialog 或第二层标签。

后台状态与退出按钮按内容宽度排列，间距使用 Fluent spacing token，空间不足时自然换行；不为状态预留固定宽度。

后台程序、游戏连接、视频服务、房间队列统一使用 `Badge appearance="tint" size="large"`，由组件提供主题浅色底、描边与语义色文字，通过同一个轻量展示组件保持颜色、Fluent 图标及“名称：状态”格式一致。颜色直接使用 `success / danger / warning / brand / informative`，不定义自有色板；`informative` 在这里使用其中性色表达未启用／待确认。以 `role="status" aria-atomic="true"` 播报完整状态，装饰图标隐藏于辅助技术。MessageBar 保留给需要处理的消息；组件依据见[官方资料核对](research/fluent-v9-ui-patterns.md#四项运行状态2026-10-25)，业务语义见[内容约定](webui-content.md#程序与界面术语)。

容器只负责布局，控件使用 Fluent 的尺寸、主题、焦点和交互。曲库、设置及关于分类使用 TabList/Tab；网络页直接展示全部适用模块，无页内入口栏或第二层标签。设置字段直接显示，保存操作固定在表单顶部。短说明使用 Tooltip，记录补充信息及包含“修复”操作的游戏连接说明使用非模态 Popover；修复浮层支持悬停、点击和键盘，使用蓝色 Link 按钮，授权处理中禁用但保留焦点。不把常用功能藏进折叠。2026-10-21 的高级字段折叠规则已由本次简洁重构替代。

设置与统计列通过 CSS Grid `auto-fit` 按实际容器宽度换行，容纳侧栏及嵌套区域；操作栏不另加边框或固定提示高度。表单默认使用 Field 的上方标签布局，避免窄容器挤压标签。曲库搜索／筛选和历史时间筛选使用原生 `orientation="horizontal"`，将标签放到对应输入框左侧，仅在这些区域按标签内容分配列宽，避免默认比例占宽挤压输入框；宽屏同行，窄屏按字段自然换行，其他字段不覆盖组件内部网格。时间筛选使用 Field 的原生 `validationMessage`，错误随内容自然展开，不用绝对定位或空白占位保持结果坐标。轮询中的已有结果仍保持稳定。CardHeader 的 action 保留简短操作，批量控件放入内容区，避免挤压窄屏标题。组件依据为官方 [Field](https://fluent2.microsoft.design/components/web/react/core/field/usage/)、[Tooltip](https://fluent2.microsoft.design/components/web/react/core/tooltip/usage/) 与 [Toast](https://fluent2.microsoft.design/components/web/react/core/toast/usage/) 指南。

首页视频块需求使用标准 medium Card、CardHeader、Badge、Slider 与 small 图标 Button；事件连接／暂停状态复用运行状态的 StatusBadge，保持尺寸、图标、主题色和完整状态播报一致，已有完整状态短句无需重复加名称前缀。标题信息和图例／块显示大小各自使用可换行布局，间距使用主题 spacing tokens，不缩小组件内部点击区域。CardHeader 的 action 只放时间轴展开按钮，Tooltip 在悬浮和键盘聚焦时说明动作。依据为官方 [Card 布局](https://fluent2.microsoft.design/components/web/react/core/card/usage)与 [Tooltip 行为](https://fluent2.microsoft.design/components/web/react/core/tooltip/usage)。

时间轴与需求视图的 Canvas 绘制、视频配色和尺寸保持独立业务规则。需求浮层使用 Fluent 的 fixed 定位、自动限高与避让，与视口坐标锚点一致，避免源详情加载改变高度时使长页面跳动，并让小视口中的操作始终可达。

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

设置四类和关于四类内容采用标准 TabList/Tab/tabpanel，网络页常驻连通性检测、下载速度测试及当前连接模式对应的内容，配置字段仅在设置中编辑。设置表单持续挂载，沿用独立部分草稿与最新保存配置合并；程序主题在网页中采用同一 FluentProvider，不另造主题样式系统。

2026-10-25 曲库与请求历史已按[简洁重构](webui-design.md#当前界面规则)移除嵌套统计折叠，使用 Tooltip、Popover 与 Toast；早期的统一纵向 Card/Accordion 排布不再作为现行界面要求。
