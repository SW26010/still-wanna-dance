# 目录选择器前台与 DPI：开发检查

日期：2026-10-12；基线 HEAD `d21c21c`，被测为未提交工作区。本轮不标记任何实机验收场景通过；LA-029 / LA-030 继续待验收。

## 原因与修改

对照本地 `dance-trail/dance_trail/windows_picker.py`：其目录窗口在 STA 线程内设置 `DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2`，并把前台窗口作为原生对话框的 owner。SWD 原实现没有 DPI 设置，以零 owner 显示，启动占位窗口还可能先激活自身。

`internal/desktop/folder_dialog.ps1` 现于任何窗口/COM 对话框创建前捕获前台窗口并设置线程 PMv2，结束与异常均恢复原 DPI。透明占位窗口不激活自身，以捕获的浏览器为 owner；IFileDialog 以此本地窗口为 owner，形成归属链。沿用本地代理窗口是为了让模态禁用只作用于辅助进程，取消或终止辅助进程不会遗留被禁用的外部浏览器。没有设置全局置顶或周期性抢焦点。

依据：[Windows 窗口归属与层级](https://learn.microsoft.com/en-us/windows/win32/winmsg/window-features)、[DPI awareness context](https://learn.microsoft.com/en-us/windows/win32/hidpi/dpi-awareness-context)、[ShowWithoutActivation](https://learn.microsoft.com/en-us/dotnet/api/system.windows.forms.form.showwithoutactivation)。这些是实现依据，不是本机视觉通过的证据。

正式程序通过 Go embed 使用脚本；原生目录预览在每次请求时读取同一脚本，因此现有预览无须重启即使用新版。现有发布程序需要重新构建才包含修改。

## 自动检查

- `go test ./internal/desktop ./internal/console -run 'Test.*(Folder|Setup)' -count=1`：宿主环境通过；desktop 0.995 s，console 0.976 s。首次沙箱内 desktop 通过，console 因 Windows 临时目录/保护数据访问返回 `Access is denied`，宿主重跑成功。
- `TestFolderDialogDefinitionLoads` 使用真实系统 PowerShell STA 编译共用 COM/C# 定义。
- `TestFolderDialogDPIContext` 调用真实 user32 API，确认进入后线程为 PMv2、模拟异常后的 Dispose 恢复原上下文；没有弹窗，也未改系统缩放。
- 目录服务回归覆盖鉴权/条款门禁、只读选择、取消、边界拒绝、单窗口及退出取消；未改接口和草稿规则。
- `git diff --check` 通过。本轮未改前端代码，未重复前端测试；此前 208 项结果保留在原开发记录。

## 预览请求与取消

使用现有本地原生目录预览（50421），页面明确标记「目录选择为真实 Windows 窗口，其余操作仅修改内存」。仅在内存夹具中确认条款并进入存储页，不写真实 DPAPI 记录。

点击缓存路径框后，输入框进入忙碌禁用，底部无新增提示；服务记录一次 `/api/setup/folder`，创建真实 Windows PowerShell 辅助进程。Windows 自动化窗口清单没有返回该原生对话框，无法确认它在屏幕上的实际位置、前后台或清晰度，未把请求启动记作视觉通过。

点击「上一步」取消请求后返回开始页；再次进入存储页，原默认路径保留且路径框恢复可用。进程检查确认本次 PowerShell 子进程已退出；没有启动导入、保存正式设置、改 hosts、启动缓存服务或游戏。浏览器未观察到脚本错误。预览仍保持运行供用户复查。

## 待验收与清理

LA-029 的 29.4 / 29.10 已补充两个路径入口、普通/最大化浏览器、100%/150%/200% 系统缩放、跨 DPI 屏幕移动、实际选择/取消、超时/离页/退出清理和浏览器恢复可用等步骤。Windows 缩放与视觉场景均保持未测；尚未完成真实选择/取消按钮检查，不用浏览器尺寸模拟替代。LA-030 未因本轮原生窗口修正新增结论。

本轮创建的原生选择请求已取消，辅助进程清理完毕；保留用户预览服务和原有草稿。未 commit/push，证据仅为脱敏文本与哈希。

## 源文件 SHA-256

按本轮 Windows 工作区文件字节计算。

| 文件 | SHA-256 |
| --- | --- |
| `internal/desktop/folder_dialog.ps1` | `074F79A49DEAFC297810D8FE97BDAC4B03E65BEC0319A399E5B3EA2062F333D3` |
| `internal/desktop/folder_windows.go` | `1D73F8D880967364517D878982DB0054B468D052F961406345C153B03B6C3CF9` |
| `internal/desktop/folder_windows_test.go` | `3350D3338369699468FE1967F846EBA20303E7EA91B4F4F6D84B724EEB4880B3` |
| `scripts/setup-folder-dialog.mjs` | `18B014C0A8C545309D33CADBC591AA0839A92E3A878DA5BEBF16422AD9FA0887` |
