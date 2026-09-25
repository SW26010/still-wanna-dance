# 本地控制台

Windows 桌面版构建、启动（托盘驻留，无命令行黑窗）：

```powershell
.\scripts\build-desktop.ps1
.\bin\stepstash-console.exe
```

首次启动会打开 <http://127.0.0.1:18081>，`-no-open` 可禁止自动打开。关闭网页后仍在托盘运行。原有 `stepstash.exe` 命令行入口保持可用。

## 托盘与实例避让

托盘菜单包含打开控制台、启动 / 关闭 CDN、停止批量任务、退出 StepStash。鼠标或键盘激活图标均可打开控制台；菜单及悬浮提示跟随网页操作更新。Explorer 重建任务栏时重新注册托盘图标。

Windows 桌面入口使用会话级命名互斥锁 `Local\StepStash.Desktop.v1`，固定使用控制端口 18081。重复启动不创建第二个服务或托盘，而是最多等待 5 秒，确认 `/api/identity` 属于 StepStash 后打开已有控制台。若首次启动者在就绪前崩溃，后启动者可重新竞争所有权；进程死亡不会留下永久实例锁。不会仅因为端口有人监听就打开未知服务。

配置文件还通过 `<配置路径>.lock` 在整个进程生命周期独占，防止自定义端口实例同时修改同一份设置。锁文件保留是正常现象；OS 在进程退出或崩溃时释放锁，不要运行时删除。缓存引擎仍沿用原有缓存目录独占锁。

退出从统一清理入口停止 HTTP 接口、取消批量任务和下载、移除托盘图标，最后释放配置锁和实例锁。注销 / 关机回调最多等待 4 秒；如果系统取消注销，程序继续运行。退出不会恢复 hosts。hosts 提权助手在实例检查前单独执行，修改完成即退出，主程序不会因此提权。

80 或控制端口冲突时，Windows 下显示占用进程名和 PID；不结束其他进程，也不自动请求 UAC。旧的无托盘版控制台不会持有新实例锁，需要退出旧版后再启动新版。

自定义控制端口及无托盘运行使用：

```powershell
go build -o bin/stepstash-console-cli.exe ./cmd/stepstash-console
.\bin\stepstash-console-cli.exe -no-tray -listen 127.0.0.1:18082 -config .\other-settings.json
```

该模式不自动打开浏览器，不创建托盘，使用 Ctrl+C 退出。Linux 默认使用无托盘模式。配置独占及现有缓存独占规则仍然生效。

设计参考 [dancing-log desktop_instance.py](https://github.com/SW26010/dancing-log/blob/c3fa28fa3b68879cb43513bc2cb53f1cc03fa1cc/dancing_log/desktop_instance.py)、[tray_app.py](https://github.com/SW26010/dancing-log/blob/c3fa28fa3b68879cb43513bc2cb53f1cc03fa1cc/dancing_log/tray_app.py) 和 [_win_tray.py](https://github.com/SW26010/dancing-log/blob/c3fa28fa3b68879cb43513bc2cb53f1cc03fa1cc/dancing_log/_win_tray.py) 的所有权、身份探测和统一退出机制。StepStash 以 Go / Win32 实现，无新增第三方运行依赖。

## 服务与下载独立

- **启动 / 关闭 CDN**：控制 `127.0.0.1:80` 视频监听。启动检查端口和目录权限，页面检查 hosts 接入；端口占用时提示关闭原版 CDN 或其他程序，不自动结束其他进程。hosts 缺失不阻止监听，提供独立修复按钮。
- **检查并下载所有已知歌曲**：不要求启动 CDN，不占用 80 端口，不要求修改 hosts。从实时 `/Api/Songs/list` 的 `groups.contents[].songInfos` 获取 ID，去重后逐首通过 HKG 播放接口获取视频地址，复用缓存引擎校验大小、MD5 和发布完整文件。列表不可用时明确报错。
- **停止批量任务**：取消排队和当前等待；已开始的共享下载继续完成，以免打断游戏。关闭 CDN 只关闭播放入口，不中断独立批量下载。退出控制台进程会取消全部下载。
- 显示总数、已检查数量、缓存命中、补齐数量及逐首失败原因。再次检查会重新校验并重试；暂不提供跨进程任务队列持久化或断点回源。

## 队列预缓存与刷新频率

队列预缓存的操作和轮询规则见[日志队列预缓存](queue-prefetch.md)。网页新增独立的「开启队列预缓存 / 停止预缓存」按钮，目录设置新增只读使用的 VRChat 日志目录。开启后等待新队列同步，不会下载历史日志里的歌曲。队列预缓存与全曲库批量任务互斥，可与 CDN 播放同时运行。

网页状态刷新改为可见时每 5 秒、隐藏时每 15 秒；操作按钮仍立即刷新。日志监听在后台独立运行，不受网页是否打开影响。

## 独立 DNS

控制台的歌曲列表、播放地址解析、视频回源均使用独立解析：直接向 `223.5.5.5:53` 发送 DNS 请求，失败时依次尝试 AliDNS、Cloudflare、Google 的 HTTPS DNS。DoH 用固定 IP 引导，TLS 仍验证对应域名证书。结果按 A 记录与 CNAME 链的最小 TTL 缓存（最多 300 秒），视频请求保留原始 HTTP Host。HKG 播放接口使用协议记录确认的 `node=nya`。

不调用系统 hosts 解析，失败时也不回退到系统解析。直接 DNS 包兼容网络代理的 DNS 劫持 / fake-IP 模式；若网络阻断全部解析通道，会显示错误。当前使用 IPv4。

独立 DNS 应用于控制台的缓存引擎；原有命令行服务的默认网络配置不变。

## hosts

修改和恢复是独立操作。只有实际需要修改时才通过 Windows UAC 启动短命提权助手；正常启动控制台、CDN 和下载不请求 UAC。

- 添加两个视频域名到 `127.0.0.1`，使用 `# StepStash managed` 标记。
- 已有正确映射直接复用；已有冲突映射或 API 域名映射时拒绝覆盖并提示检查。
- 首次修改前保存 hosts 同目录的 `hosts.stepstash-backup`。恢复只移除本程序的精确标记条目，保留其他配置，不整文件回滚；条目被其他工具改写时提示人工检查。
- 退出或关闭 CDN 不恢复 hosts。CDN 停止后如需直连上游，应点击恢复；其他工具原有映射需由对应工具恢复。
- 操作后刷新 DNS 缓存。UAC 取消或修改失败时页面报错。

## 目录与配置

填写路径并保存；已有歌曲库无需导入或迁移。歌曲库与临时缓存目录必须互不包含。运行 CDN、队列预缓存或批量任务时不能修改目录，保存时检查缓存目录可写性。日志目录仅检查读取权限，不创建或修改日志。

设置保存在工作目录的 `stepstash-console.json`，可通过 `-config` 指定。新视频同时占用应用缓存和歌曲库空间，下载时还有临时副本；暂无容量配额和自动清理。

## 验证

`go test ./...` 覆盖独立批量下载、缓存复用、失败记录、CDN 开关与批量取消独立、配置保存、端口冲突、hosts 增删及冲突保护、控制接口 Host / Origin / 随机令牌验证、DNS 报文校验。

桌面测试还覆盖同进程 / 跨进程互斥、异常终止后的锁恢复、就绪等待、浏览器打开失败、拒绝未知服务身份、配置锁、端口进程识别、托盘 / 网页共享服务状态，以及取消注销。实际桌面会话可运行托盘生命周期检查：

```powershell
$env:STEPSTASH_TRAY_CHECK = '1'
go test ./internal/desktop -run TestLiveTrayLifecycle -v -count=1
Remove-Item Env:STEPSTASH_TRAY_CHECK
```

此检查短暂创建并清理真实托盘图标，需要可访问交互式 Windows 桌面；不修改 hosts。

可选实网检查只获取列表、解析歌曲 1343 并请求 16 字节片段，不修改 hosts 或下载全库：

```powershell
$env:STEPSTASH_LIVE_CHECK = '1'
go test ./internal/console -run TestLiveIndependentDNS -v -count=1
Remove-Item Env:STEPSTASH_LIVE_CHECK
```

2026-09-25 获取 10,398 个去重 ID；视频域名有本机 hosts 映射时，独立解析的 API 和视频片段请求成功。数量为当时样本。本轮未实际执行 UAC 修改系统 hosts，纯转换逻辑已测试。
