# 托管 Mihomo 独立进程的开发验证工具

历史冻结：正式产品已采用 B2，旧 A 的 `Node` API 已删除。本比较工具仅用于重现历史结果，请在 `8ed4043` 或本文报告所记的旧源码版本运行；当前驱动以 `historical_a` 构建约束退出默认构建，不与新产品维持第二套执行实现。历史源码、报告和失败证据继续保留。

本工具回答“标准 SOCKS5 + 官方控制 API 是否足以保留 SWD 的固定节点通道，同时交还协议运行时和原生 URI 解析”。它不被产品入口引用，不是正式导入器、进程管理器或第二套生产后端。验证结论和未覆盖范围见 [2026-10-13 报告](../../docs/validation/managed-mihomo-validation-20261013.md)。生产替换完成后，将有用的黑盒场景吸收到对应回归工具，删除此过渡比较工具及旧后端。

Windows amd64、Python 3 标准库、Go **1.25.0**，使用现有模块缓存中的 Mihomo **v1.19.32** 原版源码；核心与驱动分别构建，均使用上游 `no_easytier` 标签。不自动安装、下载发行 EXE 或更新核心。一次运行：

```powershell
./experiments/managed-mihomo/run.ps1 -GoExe <Go1.25.0的go.exe> -PythonExe <可运行的python.exe>
```

可选 `-PrivateSubscription <已保存在本地的Base64_URI订阅文件>`：逐条原样交给 provider，再激活文件顺序的前两个节点，执行普通 HTTPS 检查和 SWD Monitor/播放 API/最多 4 MiB 视频取样。该参数显式开启真实网络检查；不会获取或刷新订阅。不可将私人文件放进版本控制。YAML、URI、Base64 的合成输入在不传此参数时仍会验证。

`run.py` 每次创建随机运行目录，先撤销继承 ACL，仅授予当前用户和 SYSTEM 访问，再写 provider。核心主配置和随机认证经 stdin 传递；环境中的代理、核心配置覆盖和 SAFE_PATHS 不继承。所有子进程隐藏启动，使用 `CREATE_SUSPENDED → AssignProcessToJobObject → ResumeThread`，退出时关闭带 `KILL_ON_JOB_CLOSE` 的 Job；另一个专属父进程被强杀，用于检验孤儿清理。测试只终止本轮持有的进程。

输出在忽略目录 `artifacts/issue14/managed-core-prototype/run-*/`。`results.json` 是主动筛选的脱敏计数、状态、时长和摘要，核心原始日志留在受限目录。正常/测试失败收尾清理本轮已知 provider 文件，原订阅不删除。**异常主机退出后的遗留文件清扫尚未实现**，不可将这个工具的清理机制作为生产承诺。Windows 网络表读取可能受沙箱限制；需要允许只读 `netstat`/`netsh` 查询，不需要改 hosts、系统代理或启动游戏。

证据语义：

- `-t`、控制 API 可用、provider 加载、固定 group 映射、入口认证、业务成功分别判断。`Core.valid()` 只检查单成员映射；**不是完整安全或发布门禁**。错误名称/端口冲突场景故意建立探针以观察失败，不会接入真实应用。
- 两个回环 SOCKS 出口只允许预定测试目标，分别返回 A/B 标记；探针复用正式 `NewAuthenticatedSOCKS5Dialer` 和 `NewMultiPool`。故障后的 `Pool.Close()` 由实验驱动显式调用，生产退出监控尚未实现。
- 实际 Monitor 使用私有 `Channel`，不发布到应用全局、不写吞吐历史。诊断仅输出预设字段，不输出来源名、目标 URL、地址、密码或原始错误。
- 性能比较采用相同 Go 驱动、相同 SS 夹具，3 轮交替顺序，每轮 1 次冷请求、40 次复用请求、8 × 16 MiB 确定性正文并校验 SHA256。计时使用 Windows QPC / Python `perf_counter`；CPU 为 OS 累计进程 CPU 秒；内存为驱动加核心的工作集/Private Bytes，夹具不计入。两种模式的驱动均仍链接旧适配层，这不是完成迁移后的桌面整体内存比较。
- 原生输入的能力检查、文件引用策略、不可变身份、DPAPI/草稿、K 限制选择、进程状态 UI、控制面滥用防护和发行组件交付不在此工具中实现。不得据此接收任意不可信配置到生产。
