# Web UI 可访问性与浏览器验收

当前实现为 `webui/src` 的 Fluent UI React v9 标准组件。参考 [Fluent accessibility](https://fluent2.microsoft.design/accessibility)、[WCAG 2.2](https://www.w3.org/TR/WCAG22/) 和 [WAI-ARIA APG](https://www.w3.org/WAI/ARIA/apg/)，目标为 WCAG 2.2 AA；已有测试不构成完整符合性认证。

## 当前约定

- 左侧 NavDrawer/NavItem 保留页面真实链接；未同意条款时使用自动弹出的模态确认，Escape 不绕过条款门禁。标准组件负责方向键、抽屉焦点圈定、Esc 关闭及触发器焦点恢复。
- 提供跳到正文、语义标题、当前页面/步骤标记；Tab 通过 aria-controls 关联内容面板，tabpanel 通过 aria-labelledby 关联当前标签。Field 关联标签、字段和校验错误，文件选择使用浏览器原生 input，表单保留必填和数值范围校验。
- Dialog、Table、Popover、Tooltip、Toast 和 GaugeChart 使用各自标准交互。状态有文字，不只依赖颜色；装饰图标不重复朗读。
- 列表更新（包括需求视图事件更新）移除当前行且焦点掉到 body 时回到所属区域；仍存在的请求分组复用身份，保留展开和焦点，不抢走列表外焦点。
- 首页需求面板随页面／面板可见性自动暂停，冻结需求视图、状态计数、下载速度及块需求时间轴，不提供手动暂停复选框；历史列表按[事件更新规则](webui-design.md#历史查询范围)随页面可见性订阅，翻页或重新查询后迟到响应不能覆盖新结果；保存设置后等待新状态同步才解锁，避免旧值覆盖前次保存。
- 主题跟随系统，支持 reduced-motion、可换行操作区和长路径。普通文档语义、fieldset/legend 与页面间距使用 HTML/CSS，不模拟通用控件。
- 块时间轴的 Canvas 提供可访问名称和键盘入口；左右键选块、Home/End 到首尾、加减缩放、Escape 关闭详情。选择结果通过文字状态播报，属性与来源是普通 DOM；重叠块有具名切换按钮。状态同时使用文字、填充、斜纹和边框。reduced-motion 下不持续平移动画，事件和用户操作仍更新画面；暗色及 forced-colors 使用对应画布颜色。
- 时间轴展开箭头使用标准 Fluent Button 和 Tooltip，悬浮与聚焦均提供提示，可通过 Enter／Space 切换，具备可访问名称、aria-expanded 和 aria-controls。「块显示大小」Slider 与「分层显示」Switch 均有明确名称；方块配色标识视频，不单独承载状态。需求视图使用列表语义及虚拟行的位置／总数，行内 Canvas 的名称区分需求块数与视频总块数（或总数未知）；左右键、Home/End 和 Enter 在实际有需求的块之间选择，跳过空档，关闭详情回到原选择入口。行标题后以文字百分比说明需求就绪进度；属性播报包含视频及从 1 开始的块号。保护期块半透明，选中轮廓和详情文字保持清晰。forced-colors 下让系统颜色优先，通过视频标题与状态形态辨识，不强行保留彩色区分。

## 验证范围

当前浏览器套件覆盖键盘导航、抽屉/弹窗焦点、列表内操作、行消失后的焦点回退、文件标签、数值校验、暂停/恢复及 320/390px 深浅主题和 forced-colors。完整结果与运行命令统一见 [Fluent UI 验证](fluent-ui-migration.md#验证与待验收)，不再列已删除旧脚本的历史测试次数或截图路径。

这些检查在真实 Edge 中使用内存接口，未操作实际 hosts、游戏或用户数据。下一轮 **LA-028** 仍须核对 Windows 浅/深/高对比度主题、125%/150%/200% 系统 DPI、浏览器 200%/400% 缩放，以及 Narrator/NVDA 对导航、字段说明、错误、进度和结果的朗读。键盘完成导航、展开、选择、编辑及保存，记录系统/辅助技术版本、可见焦点与朗读结果。
