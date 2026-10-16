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
| 文件选择 | 浏览器原生 file input，通过 Field 关联标签；Fluent Input 不支持 file 类型。目录选择调用 Windows IFileDialog |
| 必要业务逻辑 | 条款门禁、独立草稿、保存后状态同步、原 ID 回执核对、请求交集合并、检测事件与取消恢复；不重复实现标准组件的键盘或弹窗行为 |

五页及四步引导的内容分工见[内容分类](webui-content.md)，平台辅助技术边界见[可访问性约定](webui-accessibility.md)。组件契约以锁定依赖的类型和实现为准；参考 [Fluent 官方接入](https://fluent2.microsoft.design/get-started/develop)与 [GaugeChart](https://microsoft.github.io/fluentui-charting-contrib/docs/Charting-Concepts/GaugeChart)。

## 开发与交付

- 源码和锁文件：`webui/src`、`webui/pnpm-lock.yaml`；安装使用 `pnpm --dir webui install --frozen-lockfile`。
- `pnpm --dir webui build`：生成 `internal/console/assets/webui`、第三方声明和构建清单；产物入 Git，支持离线 Go 构建。
- `pnpm --dir webui check`：类型检查并验证重建产物逐字节一致。`scripts/check-webui.mjs` 提供无需前端依赖的源码/产物哈希检查。
- `pnpm --dir webui test`：本机 Edge、CI Chromium；只使用内存 API 夹具。最新截图在忽略目录 `artifacts/webui-tests`，不是实机证据。
- 构建实际引用的依赖声明写入 `docs/licenses/WEBUI-NOTICES.txt`。图标包 2.0.344 的 tarball 缺 LICENSE，使用仓库保存的上游许可证；升级时重新核对。
- 正式对应源码包含 pnpm 完整包存储，清除机器相关 junction 后离线重建前端并核对产物，再构建 Go。流程见[打包](packaging.md)与[对应源码](corresponding-source.md)。

## 验证与待验收

2026-10-16，当前实现的 **39 项 Edge 内存场景通过**；类型、资源一致性、Node 工具测试及 Go 页面入口/条款/帮助筛选通过。覆盖基准正文、左侧导航、表单校验/保存同步、缓存毫秒时间戳/零值显示与保护/确认、暂停与请求分组、引导与回执恢复、切步保留检测上游及结果、检测与取消、测速阈值、320/390px、深浅主题及强制颜色。截图已检查；大倍率按 GaugeChart 默认截断与提示显示，完整值保留在可访问名称和 Mbps 指标中。

不将中间版本的测试次数、旧包哈希、已移除脚本或废弃控件布局作为当前证据。真实缓存时间显示、保存/重启、UAC/hosts、目录窗口、实际出口/测速、游戏、托盘、系统 DPI 和 Narrator/NVDA 继续按[待验收台账](live-acceptance-backlog.md)执行；缓存时间及检测选择修复分别沿用 LA-023、LA-030/045，自动检查不改变实机状态。
