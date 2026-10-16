# Web UI 可访问性与浏览器验收

当前实现为 `webui/src` 的 Fluent UI React v9 标准组件。参考 [Fluent accessibility](https://fluent2.microsoft.design/accessibility)、[WCAG 2.2](https://www.w3.org/TR/WCAG22/) 和 [WAI-ARIA APG](https://www.w3.org/WAI/ARIA/apg/)，目标为 WCAG 2.2 AA；已有测试不构成完整符合性认证。

## 当前约定

- 左侧 NavDrawer/NavItem 保留页面真实链接；引导步骤是受内容校验约束的按钮。标准组件负责方向键、抽屉焦点圈定、Esc 关闭及触发器焦点恢复。
- 提供跳到正文、语义标题、当前页面/步骤标记。Field 关联标签和字段，文件选择使用浏览器原生 input，表单保留必填和数值范围校验。
- Dialog、Accordion、List、Tooltip 和 GaugeChart 使用各自标准交互。状态有文字，不只依赖颜色；装饰图标不重复朗读。
- 轮询移除当前行且焦点掉到 body 时回到所属区域；仍存在的请求分组复用身份，保留展开和焦点，不抢走列表外焦点。
- 暂停仅冻结下载及请求展示，迟到响应不能覆盖冻结内容；保存设置后等待新状态同步才解锁，避免旧值覆盖前次保存。
- 主题跟随系统，支持 reduced-motion、可换行操作区和长路径。普通文档语义、fieldset/legend 与页面间距使用 HTML/CSS，不模拟通用控件。

## 验证范围

当前浏览器套件覆盖键盘导航、抽屉/弹窗焦点、列表内操作、行消失后的焦点回退、文件标签、数值校验、暂停/恢复及 320/390px 深浅主题和 forced-colors。完整结果与运行命令统一见 [Fluent UI 验证](fluent-ui-migration.md#验证与待验收)，不再列已删除旧脚本的历史测试次数或截图路径。

这些检查在真实 Edge 中使用内存接口，未操作实际 hosts、游戏或用户数据。下一轮 **LA-028** 仍须核对 Windows 浅/深/高对比度主题、125%/150%/200% 系统 DPI、浏览器 200%/400% 缩放，以及 Narrator/NVDA 对导航、字段说明、错误、进度和结果的朗读。键盘完成导航、展开、选择、编辑及保存，记录系统/辅助技术版本、可见焦点与朗读结果。
