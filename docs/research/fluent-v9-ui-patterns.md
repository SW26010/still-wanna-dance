# Fluent React v9：本轮简洁重构参考

2026-10-25 首次查阅 Microsoft 官方设计文档、`microsoft/fluentui` 的 React v9 Storybook 源码，以及本项目安装包的类型声明；同日对重构后的实现再次复查，见文末。本文记录参考依据，不代替 [WebUI 设计](../webui-design.md)，不要求照搬示例的数据模型或外观。

## 版本与入口

- 项目使用 [`@fluentui/react-components` 9.74.9](../../webui/package.json)。本地实际子包为 Table 9.19.22、Tooltip 9.10.7、Toast 9.8.4、Popover 9.14.9、Toolbar 9.8.5、Field 9.5.5、Radio 9.6.6；复查补充 Input 9.8.7、SpinButton 9.6.7、Search 9.4.7、Tabs 9.12.5，已核对各自 `dist/index.d.ts`。
- [Fluent React 组件目录](https://fluent2.microsoft.design/components/web/react/)提供使用原则；各页的 React Storybook 链接通向代码示例与属性。不要混用同页的 Web Components 链接或旧 `@fluentui/react` v8 API。
- 下文 GitHub 链接指上游 `master`，会继续变化；实现时以项目锁定版本的类型和实际浏览器行为为准。例如 Toast 设计页建议无操作结果显示 7 秒，本地类型声明默认值为 3000 ms，需要时显式设置，不能假设两者相同。

## 表格与行内操作

官方将 `DataGrid` 定位为内置常用特性的高层组件，将 `Table` 定位为可自由组合的基础组件；两者都适合按行表达二维数据。需要标准排序、行选择和网格键盘导航时，先看 DataGrid 示例；业务已有服务端排序和分页、无需网格导航时，直接组合 Table 可以更简洁。不应为了换组件重建一套客户端查询语义。[Table 说明](https://github.com/microsoft/fluentui/blob/master/packages/react-components/react-table/stories/src/Table/TableDescription.md)、[DataGrid 说明](https://github.com/microsoft/fluentui/blob/master/packages/react-components/react-table/stories/src/DataGrid/DataGridDescription.md)

优先使用现成的 `TableCellLayout`：`description` 放一行次要资料，`truncate` 处理截断，`appearance="primary"` 表达主字段；避免为每格再造一套文字布局。`Table size="small"` 已有紧凑尺寸。若选用 DataGrid，默认示例已有 `createTableColumn`、`getRowId`、`selectionMode` 和 `focusMode="composite"`，不自写方向键系统。[Table 示例](https://github.com/microsoft/fluentui/blob/master/packages/react-components/react-table/stories/src/Table/Default.stories.tsx)、[DataGrid 示例](https://github.com/microsoft/fluentui/blob/master/packages/react-components/react-table/stories/src/DataGrid/Default.stories.tsx)；上述 TableCellLayout 属性已在本地 9.19.22 类型中确认。

本项目的适用判断：保持“一行一首歌／资源／请求”；常用操作直接位于行内；详情只补充当前任务不常用的数据。窄屏允许表格区域横向滚动，避免整页横向溢出，也不要因此退回每例一张厚卡片。这个布局取舍来自本次目标，并非 Fluent 强制指定的断点或列宽。需要组合键盘导航时必须遵守 grid 语义；普通 Table 中的按钮和链接可以使用正常 Tab 顺序。[Table 键盘约束](https://github.com/microsoft/fluentui/blob/master/packages/react-components/react-table/stories/src/Table/TableDescription.md)

## Tooltip：短而非必要的说明

Tooltip 用于目标旁的补充纯文本，由 hover 或 focus 触发；不承载系统反馈、必需操作信息或链接。已命名控件的额外解释用 `relationship="description"`；纯图标按钮的名字可用 `relationship="label"`。`inaccessible` 只适用于内容已有其他无障碍入口的情况。保留可聚焦触发器，不能仅依赖鼠标 hover 来传达必需条件。[使用原则](https://fluent2.microsoft.design/components/web/react/core/tooltip/usage)、[v9 属性定义](https://github.com/microsoft/fluentui/blob/master/packages/react-components/react-tooltip/library/src/components/Tooltip/Tooltip.types.ts)

当前 `Hint` 的 Tooltip + Fluent 图标按钮适合短说明，但应尽量靠清楚的标签消除说明需求。官方 InfoLabel 指出，同一区域超过两三个信息按钮往往意味着设计还需要简化；不要把原来的说明段落机械改成大量问号。需要点击后稳定显示并包含链接的短内容，可直接用官方 `InfoLabel`，其内部是 Popover。[InfoLabel 原则](https://fluent2.microsoft.design/components/web/react/core/infolabel/usage)

## 反馈与位置稳定

| 场景 | 官方组件与本轮用法 |
| --- | --- |
| 保存、标记、下载提交等非关键结果 | Toast；一个共享 Toaster 放在固定角落，避免遮挡主要操作，最多同时显示 4 条。标题简短，hover 或窗口失焦时可暂停计时。[Toast 原则](https://fluent2.microsoft.design/components/web/react/core/toast/usage) |
| 断连、存储故障、必须处理才能继续的错误 | 对应页面或应用区域的 MessageBar，给出下一步。不能仅依靠即将消失的 Toast。[MessageBar 原则](https://fluent2.microsoft.design/components/web/react/core/messagebar/usage) |
| 某个字段无效 | Field 的 `validationState` / `validationMessage`，解释怎样改正。必要填写条件仍在标签或短 hint 中。[Field 原则](https://fluent2.microsoft.design/components/web/react/core/field/usage) |

`Notification` 共用一个 Toaster、为每次派发保留对应 `toastId` 并在 effect 清理时关闭，符合官方的命令式通知 API。当前实现已限制 4 条，普通结果显式显示 7 秒，错误或处理中提示不自动过期。对于允许手动关闭的通知，官方要求信息仍能从其他位置找回；对 SWD 而言，已保存偏好、任务状态或故障状态通常就是该持久位置，无需为此凭空建设通知中心。没有持久结果入口的重要错误，不应只显示 Toast；`timeout=-1` 不能代替内容归属判断。[Toast API](https://github.com/microsoft/fluentui/blob/master/packages/react-components/react-toast/stories/src/Toast/ToastDescription.md)、[关闭原则](https://fluent2.microsoft.design/components/web/react/core/toast/usage)、[当前实现](../../webui/src/ui.tsx#L28)

布局稳定是本次产品要求：Toast 不占正文流；持续故障使用预留的简短状态区域；表单校验在字段附近留出适当空间。Fluent 的 MessageBar 会按内容自然增高，组件本身不会替页面预留位置。避免任意固定高度截掉错误、用全局绝对定位盖住操作，或给每个潜在反馈都留一大块空白。[MessageBar reflow](https://fluent2.microsoft.design/components/web/react/core/messagebar/usage)

应用自行负责页面归属：切页清除局部操作通知，迟到请求不向新页面派发提示；持续全局故障按真实状态保留。该规则来自本项目交互契约，Toaster 不理解应用路由。验收应检查通知出现前后主要按钮／表格的坐标，而不仅检查通知文字。

## Dialog 与 Popover

Dialog 用于需要专注完成的任务或重要确认；普通补充上下文优先 Popover。官方分别要求不嵌套 Dialog、不嵌套 Popover，并非一概禁止不同类型的浮层组合。Dialog 应有简短标题和退出方式，打开后聚焦内部，关闭后恢复触发器焦点。首次查阅指出导入表单内调用确认不应叠出第二层 Dialog；当前导入已移入曲库工具区域，保留单独确认即可。[Dialog 原则](https://fluent2.microsoft.design/components/web/react/core/dialog/usage)、[Popover 原则](https://fluent2.microsoft.design/components/web/react/core/popover/usage)、[当前曲库工具](../../webui/src/library.tsx#L97)

首次查阅建议普通资料详情使用 Popover；当前 `RecordDetails` 已使用 `Popover + PopoverTrigger + PopoverSurface`，无需再换组件。信息过多时考虑明确的详情区域；必需条件与操作不能只藏在 Popover 内。本地 Popover 9.14.9 默认 `trapFocus=false`；默认会把焦点移向浮层的首个可聚焦元素，应保留官方行为并验证 Escape 与返回焦点。[Popover 原则](https://fluent2.microsoft.design/components/web/react/core/popover/usage)、[当前实现](../../webui/src/ui.tsx#L43)

## 表单与工具栏

- Field 原生 `orientation="horizontal"` 可将简短标签与一致宽度的控件并排；一般字段仍可使用默认顶部标签。必需说明不要只写在 placeholder。[Field 布局](https://fluent2.microsoft.design/components/web/react/core/field/usage)
- RadioGroup 原生 `layout="horizontal"` 适合“不保留／限制为／不限制”等少量短选项，窄屏按可用空间安排；不要截断选项文字。默认选中合理选项，使用标准方向键交互。[RadioGroup 布局](https://fluent2.microsoft.design/components/web/react/core/radiogroup/usage)
- Toolbar 适合一组常用命令，有自己的单行与键盘语义；官方用 Overflow 处理放不下的命令，不建议把 Toolbar 换成多行。包含搜索、筛选表单的响应式横排区域可以继续用普通布局，不必机械地将每个按钮容器都改成 Toolbar。图标按钮使用熟悉的图标、Tooltip 与可访问名称。[Toolbar 原则](https://fluent2.microsoft.design/components/web/react/core/toolbar/usage)

原则是先使用已有组件属性、slot 和标准布局。没有对应行为需求时，不引入通用表格引擎、统一浮层状态机或自定义焦点系统。

## 重构后复查快照：2026-10-25

本次重新阅读了 Field、Input、SpinButton、SearchBox、TabList、Toast、Button 官方页，并对照当前源码和安装包类型。以下“确定”指源码或官方 API 可直接确认；“建议”是针对本项目目标的判断，尚未实施，不是新增强制规范。行号以本次复查时工作区为准。

1. **先缩短批量结果，并保留失败意图。** 确定：[cache.tsx:29](../../webui/src/cache.tsx#L29) 把删除结果逐条拼接，异常也只保存字符串；[library.tsx:61](../../webui/src/library.tsx#L61) 将其作为默认 info 通知，列表一次可选最多 50 项。官方要求 Toast 标题简短，并用 `intent` 表达反馈语义。建议：显示“已删除 18 个，跳过 2 个”等摘要；需处理的失败在曲库当前短状态区保留具体原因和下一步，避免错误变成限时普通提示。共享 Toaster 本身无需重写。[Toast 内容与 intent](https://fluent2.microsoft.design/components/web/react/core/toast/usage)

2. **首页合并指向同一动作的两个按钮。** 确定：[home-library.tsx:28](../../webui/src/home-library.tsx#L28) 的“开始加速”和“重新检测视频请求”均调用 `activation/enable`；准备就绪后前者禁用并显示“加速运行中”，与标题重复。建议：用一个按钮按现有 `ready`、`needsExitPreparation`、`active` 显示“开始加速／重新检测视频请求／正在处理…”，保留退出恢复授权和 busy 禁用条件。这是减少选择负担的设计调整，并非当前接口错误；官方依据是按钮表达下一步动作、一个区域突出一个主要操作。[Button 用法](https://fluent2.microsoft.design/components/web/react/core/button/usage)

3. **删掉详情里的再次悬停，把故障下一步放在原处。** 确定：[cache.tsx:47](../../webui/src/cache.tsx#L47) 的统计 Popover 内还有两个 Hint；[home-library.tsx:53](../../webui/src/home-library.tsx#L53) 的日志读取失败只在表面显示“读取失败”，原因与“检查日志设置”在详情内。建议：前者改成两句就地短注释；后者在现有短状态区显示可理解的原因和针对性的入口，原始错误仍可留在详情。首页加速的“检查连接”已直接显示，不能把它误判为隐藏操作。依据是 Tooltip、InfoLabel、Popover 承载非必要补充，不应成为获知下一步的关卡；不需要到处新增 MessageBar 或重复播报。[Tooltip](https://fluent2.microsoft.design/components/web/react/core/tooltip/usage)、[InfoLabel](https://fluent2.microsoft.design/components/web/react/core/infolabel/usage)、[Popover](https://fluent2.microsoft.design/components/web/react/core/popover/usage)

4. **利用现有语义，补齐标签页与字段关联。** 确定：[library.tsx:66](../../webui/src/library.tsx#L66) 有 TabList，但结果区没有对应的 `tabpanel`；设置与关于已有面板的 `aria-labelledby`，仍缺 Tab 的 `aria-controls`。按 Fluent 链接的 WAI 模式补充面板 ID、`aria-controls` 和标签关系即可，无需改变页面层级或自写方向键。[TabList](https://fluent2.microsoft.design/components/web/react/core/tablist/usage)、[WAI Tabs 模式](https://www.w3.org/WAI/ARIA/apg/patterns/tabs/) 另一个确定点是 [request-timeline.tsx:33–37](../../webui/src/request-timeline.tsx#L33) 的时间范围错误仅在独立 status 区显示；建议交给结束时间 Field 的 `validationMessage` / `validationState` 关联输入。本地 Field 会设置 `aria-invalid`、描述关联和错误播报；保留原有短反馈空间，替代原 status 播报，避免重复与跳高。[Field 校验](https://fluent2.microsoft.design/components/web/react/core/field/usage)

5. **网络未检测时先露动作。** 确定：[monitor.tsx:59–62](../../webui/src/monitor.tsx#L59) 为三个操作区分别展示“暂无检测结果”，同时展示尚无值的完成／下次时间。建议：未检测时每项收成“名称 · 未检测 · 检测按钮”一行，取得结果后直接显示实际数据，不增加折叠操作。这是本项目的信息密度判断；官方 Button 的动作优先与 Popover 的补充信息原则只能提供方向，并未规定这类空状态必须采用行布局。[Button](https://fluent2.microsoft.design/components/web/react/core/button/usage)、[Popover](https://fluent2.microsoft.design/components/web/react/core/popover/usage)

暂不值得为组件替换增加工作：数字字段继续使用 `Input type="number"` 和现有字符串草稿、`min/max/step`，官方明确大范围数值更适合 Input，存储预算、毫秒与保留天数并不适合逐步点击；SpinButton 仅在确有小范围递增需求时考虑。[SpinButton](https://fluent2.microsoft.design/components/web/react/core/spin/usage)、[Input](https://fluent2.microsoft.design/components/web/react/core/input/usage) 曲库搜索可低优先使用 SearchBox 获得统一搜索图标与清空入口，但不必改变按“查找”提交的语义；本地 Search 9.4.7 的清空会通过 `onChange` 返回空字符串，默认清空名称还需本地化。[SearchBox](https://fluent2.microsoft.design/components/web/react/core/searchbox/usage)、[官方属性](https://github.com/microsoft/fluentui/blob/master/packages/react-components/react-search/library/src/components/SearchBox/SearchBox.types.ts)、[官方实现](https://github.com/microsoft/fluentui/blob/master/packages/react-components/react-search/library/src/components/SearchBox/useSearchBox.tsx) Table 加普通上一页／下一页继续匹配曲库偏移分页和历史游标分页；没有网格导航需求，无需迁移 DataGrid。搜索、筛选和按钮组成的响应式表单也继续使用普通布局，不机械替换成单行 Toolbar。[Table 说明](https://github.com/microsoft/fluentui/blob/master/packages/react-components/react-table/stories/src/Table/TableDescription.md)、[Toolbar](https://fluent2.microsoft.design/components/web/react/core/toolbar/usage)

本次验证仅为官方资料、安装包类型及源码对照；未修改前端，未运行新的浏览器或屏幕阅读器验收。没有下载整套文档，保留此可追溯笔记并以锁定安装包作为本地 API 参考即可。


## 后续落实：2026-10-25

用户确认继续后，已落实上述五项：合并首页主按钮、批量结果摘要与留在页面的未完成项、统计短注释和日志设置直达入口、网络紧凑空状态、标签面板与 Field 校验关联。上节保留实施前发现，不再作为未完成清单；现行行为以 [WebUI 设计](../webui-design.md#当前界面规则) 为准，验证见 [开发记录](../validation/webui-validation.md#2026-10-25-fluent-文档复查后的交互精简)。SearchBox 替换仍属于低优先建议，本次未增加相关组件或抽象。

## 四项运行状态：2026-10-25

本次对照 `@fluentui/react-components` **9.74.9** 锁定的 Badge **9.5.6**、MessageBar **9.7.7** 本地类型和实现，复查官方设计页。结论适用于后台程序、游戏连接、视频服务、房间队列的状态展示；不改变四者各自的业务判定。

- **常驻状态使用 Badge。** 官方将 Badge 定义为通过短文字、颜色与图标快速说明关联对象状态的组件，要求同一区域尺寸一致、文字简短。四项可统一为“对象名称 + 状态 Badge”，采用同一 `size` 和 `icon` slot；外观按信息层级选择，官方没有要求所有状态必须 filled。[Badge 使用原则](https://fluent2.microsoft.design/components/web/react/core/badge/usage/)、[Badge 属性](https://github.com/microsoft/fluentui/blob/master/packages/react-components/react-badge/library/src/components/Badge/Badge.types.ts)
- **直接用语义颜色及主题。** 本地 API 支持 `filled / tint / outline / ghost`，颜色含 `success / warning / danger / informative / subtle / brand`；filled 的 success、warning、danger 分别使用 Fluent 绿、黄、红色 tokens，informative 使用中性色，并非蓝色。正常可用、需检查、明确故障、关闭／未知的对应关系属于应用语义，应保持一致，不能把正常关闭标为故障。不自定义同义色板。[官方样式映射](https://github.com/microsoft/fluentui/blob/master/packages/react-components/react-badge/library/src/components/Badge/useBadgeStyles.styles.ts)
- **PresenceBadge 不适合这四项。** 其 `available / busy / away / offline / do-not-disturb / out-of-office` 等状态主要表达人员在线情况，不能把后台运行状态硬映射成人员状态；本地没有 PresenceBadge 的通用 `color` 属性。[PresenceBadge 属性](https://github.com/microsoft/fluentui/blob/master/packages/react-components/react-badge/library/src/components/PresenceBadge/PresenceBadge.types.ts)、[Avatar 的 presence 用途](https://fluent2.microsoft.design/components/web/react/core/avatar/usage)
- **MessageBar 留给需要理解或处理的消息。** 官方把它用于产品或当前区域的重要信息，并要求 warning/error 提供解决入口；页面级消息应放在正文中、命令区下方，不放到侧栏上方。无需把四项常驻状态各做成一条 MessageBar；实际故障说明和操作可继续用 MessageBar 或原有清晰入口。[MessageBar 使用原则](https://fluent2.microsoft.design/components/web/react/core/messagebar/usage)
- **状态文字始终可见。** 颜色不能成为唯一辨识方式；保留 Fluent 对比度和主题处理。[颜色无障碍](https://www.w3.org/WAI/WCAG22/Understanding/use-of-color.html)、[Fluent 可访问性](https://fluent2.microsoft.design/accessibility) 本地图标包 2.0.344 对没有 `title / aria-label / aria-labelledby` 的图标自动设置 `aria-hidden=true`，与文字同义的装饰图标无需重复命名。[图标默认语义](https://github.com/microsoft/fluentui-system-icons/blob/main/packages/react-icons/src/core/useBaseIconState.ts) Badge 默认不抢焦点，也没有 live region；需要变化播报时显式使用 `role="status" aria-atomic="true"`，将“名称：状态”放在同一容器内。status 已隐含 polite，无需再加一套重复播报区域。[Badge 默认结构](https://github.com/microsoft/fluentui/blob/master/packages/react-components/react-badge/library/src/components/Badge/useBadge.ts)、[W3C ARIA22](https://www.w3.org/WAI/WCAG22/Techniques/aria/ARIA22)

以上仅是官方资料与锁定安装包核对，不代表实现或浏览器验收已完成。

随后已按此依据落实四项 Badge；初版使用 filled，用户反馈实心过于醒目后统一改用原生 `tint`：组件通过主题 tokens 提供浅色背景、描边和语义色文字，无需自定义透明度或样式覆盖。`informative` 中性灰仍表示未启用／待确认，`brand` 表示连接或启动中。实现及浏览器结果见[开发验证](../validation/webui-validation.md#2026-10-25-四项运行状态统一)。
