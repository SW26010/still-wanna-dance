# Fluent UI React v9

全部 Web UI 使用 React、TypeScript 和 Fluent UI React v9；Go 通过 embed 提供编译后的资源，运行时不依赖 Node、CDN 或外部网络。

## 实现边界

以 `207e431fefd150c59a4b6dc117b1aab535056007` 为文案和业务能力基准。保留文字内容及业务独有功能，通用交互优先采用标准组件，不为复刻旧控件的布局或细节增加自定义实现。组件替换导致说明失实时只调整必要说明，其余原文不顺带改写。

| 范围 | 当前实现 |
| --- | --- |
| 页面与主题 | FluentProvider、Card/CardHeader、Text、Image、Divider；系统深浅主题、窄屏换行和 reduced-motion |
| 导航 | NavDrawer/NavItem/Hamburger；桌面左侧 inline 可展开／收起，≤640px 左侧 overlay；页面保留真实链接，引导步骤校验后切换 |
| 字段与操作 | Field/Input/Select/Textarea/Checkbox/RadioGroup、Button、Dialog、Accordion、MessageBar、ProgressBar |
| 记录列表 | List/ListItem 的官方 composite 键盘导航；业务负责受保护项、请求分组身份及移除行后的焦点回退 |
| 测速 | `@fluentui/react-charts` v9 GaugeChart；实际倍率与 1×/2× 阈值，线性范围至少 0–3，随更高样本扩展；没有自绘指针或分段刻度 |
| 块需求时间轴与需求视图 | Canvas 绘制连续时间与方块、可见需求行的块；D3 scale/zoom/selection 负责刻度及缩放拖动；Fluent Button、Slider、Switch、Popover、Badge 承担外围控件和共用详情，按需 SSE 提供数据 |
| 文件选择 | 浏览器原生 file input，通过 Field 关联标签；Fluent Input 不支持 file 类型。目录选择调用 Windows IFileDialog |
| 必要业务逻辑 | 条款门禁、独立草稿、保存后状态同步、原 ID 回执核对、请求交集合并、检测事件与取消恢复；不重复实现标准组件的键盘或弹窗行为 |

五页及四步引导的内容分工见[内容分类](webui-content.md)，平台辅助技术边界见[可访问性约定](webui-accessibility.md)。组件契约以锁定依赖的类型和实现为准；参考 [Fluent 官方接入](https://fluent2.microsoft.design/get-started/develop)与 [GaugeChart](https://microsoft.github.io/fluentui-charting-contrib/docs/Charting-Concepts/GaugeChart)。

## 页面排版

通用界面优先使用 Fluent 默认组件、官方字阶和主题 tokens。页面背景使用 `colorNeutralBackground2`，桌面 inline 导航与窄屏 overlay 均保留标准抽屉外观；Card 保留标准边框、阴影和圆角。页面标题与卡片标题分别使用 Title2、Subtitle2，说明使用次级文字色，语义表单分组保留 fieldset/legend，以标题和留白分隔。

控制台页面和引导步骤共用侧栏展开／收起按钮。桌面默认展开，收起后保留按钮并释放正文宽度；按钮保持挂载，键盘操作不会丢失焦点。应用内切换页面时保留展开状态，刷新后默认展开，不写入配置；窄屏抽屉仍由 Fluent 管理焦点、Escape 和遮罩关闭，独立于桌面展开状态。

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

浏览器、类型、资源一致性及隔离真实控制台的结果按轮次保存在[Web UI 开发验证记录](validation/webui-validation.md)。用例位于 `webui/e2e/`，内存服务器及 Go 联调驱动位于 `webui/test-support/`；测试输出保存在忽略目录 `artifacts/`。

验证分工遵循[现行机制](live-acceptance-process.md)，UAC、目录窗口、托盘、系统 DPI 和辅助技术的环境缺口见[非 VRC 跟进](validation-followups.md)。这里只引用记录，不再维护另一份逐项实机清单。
