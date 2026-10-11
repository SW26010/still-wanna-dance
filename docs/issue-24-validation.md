# Issue #24：SWD 运行身份与 Windows 本地验证

2026-10-11，Windows amd64、Go 1.27.1。代码在 `918af33`（活动命名）和 `a5f6e5b`（实例归属）依次提交；本记录与便携包身份断言另行提交。只保留本地提交与产物，未 push 或更新 GitHub issue。

## 实现范围

- 普通桌面仍是同一 Windows 会话唯一：`Local\SWD.Desktop.v1`。发现记录只写当前用户 `%LOCALAPPDATA%\SWD\console-session-<id>.json`，正常退出删除本会话记录，取得互斥锁后清理本会话崩溃遗留记录。旧 StepStash 选举、协议和发现目录不迁移、不读取、不双写。
- 发现记录包含回环地址、`swd-console` 协议版本 1、实际 EXE、配置路径、PID 和进程创建时间。激活前核对端口所属进程、Windows 报告的 EXE/创建时间与后端完整身份，HTTP 探测后再查一次归属；禁用 HTTP 代理和重定向。
- 同 EXE、同配置才激活。不同 EXE 或配置报告现有 PID、程序和配置位置，不创建第二个普通桌面实例，不打开另一安装，不干预正在运行的任务。`-no-open` 也校验归属。
- 使用 Windows 文件句柄规范化实际路径，覆盖 junction、符号链接、短名称和大小写；相对路径先绝对化，不存在的配置解析已有祖先，存在的文件辅以文件身份比较。配置锁也使用规范路径，避免目录别名绕过锁。`-no-tray` 不参与桌面选举，仍保留配置锁、存储锁与端口冲突检查。
- 活动请求头改为 `X-SWD-Token`、`X-SWD-Cache`，页面 meta 为 `swd-token`，测试变量统一 `SWD_*`，服务密码变量仅接受 `SWD_SOCKS5_PASSWORD`。前后端、测试和验收脚本同步修改，授权及同源限制不变，不接受旧别名。
- 新增 hosts 条目为 `# SWD managed`，首次系统备份为 `hosts.swd-backup`。精确识别旧受控标记仅用于安全恢复；已有 `hosts.stepstash-backup` 不重命名、不覆盖、不删除。旧配置和 `stepstash.sqlite` 等阻断标记、研究数据与历史证据原样保留；数据库 application_id、版本化摘要及代理身份未修改。

## 已执行验证

| 检查 | 结果 |
| --- | --- |
| `go test ./cmd/... ./internal/...` | 全部通过，由打包脚本执行；部分未变包复用 Go 测试缓存 |
| `go vet ./cmd/... ./internal/...` | 通过 |
| UI / 导航 / 条款 Node 测试 | 120 项通过 |
| 验收协议 / 请求 Node 测试 | 8 项通过 |
| Windows 桌面回归 | 通过：等价路径、不同 EXE/配置、`-no-open`、伪造身份、过期 PID/创建时间、外部端口、进程互斥与崩溃恢复、发现记录清理、junction 配置锁 |
| hosts 隔离文件测试 | 新旧三种标记均精确恢复；未知内容保留；新备份不覆盖，旧备份不变 |
| `package-portable.ps1 -Version issue24-local` | 成功生成 `SWD/SWD.exe`、许可、README 与 build-info，以及 ZIP / SHA-256 |
| `test-portable.ps1` | 通过：固定布局、条款门禁、实际身份、配置锁、重复无托盘启动拒绝、强制终止后重启、同格式覆盖用户数据保持、整体移动、自定义配置并发隔离、旧布局拒绝 |

ZIP：`artifacts/still-wanna-dance-issue24-local-windows-amd64-portable.zip`，SHA-256：`17fddbed6a741e4e2e0038d098ddc85cad09f9fd4f4d8db32bbea9e0ddd038d2`。打包时 HEAD 为 `a5f6e5b`，存在待提交文档与冒烟脚本，因此 build-info 如实标记 dirty；没有将该包称为正式 Release。

首次受限沙箱测试出现临时文件原子重命名 Access denied 与端口归属查询失败；随后在普通 Windows 用户权限下完整复验通过。符号链接专用测试因本机缺少创建符号链接权限跳过；另有无需管理员权限的 Windows junction 回归实测通过，且该测试发现并推动修复了未创建配置的目录别名问题。

## 暂缓的实机验收

未运行真实 VRChat 音画、实际 hosts/UAC、交互托盘或真实注销/关机联合测试。托盘交互测试仍为显式 opt-in；自动测试覆盖的是状态分派和 OS 互斥行为，不冒充托盘实机验收。不同桌面安装/配置的冲突逻辑已自动验证，完整 GUI 提示与托盘体验仍由 #5 / #26 后续验收。测试未接入游戏、下载歌曲或修改系统 hosts。

已知旧数据保护和系统恢复不等于迁移支持。便携包覆盖测试只验证同一新格式文件保留，不承诺旧配置、旧数据库、旧桌面进程与新版共存。
