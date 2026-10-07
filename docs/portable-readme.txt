Still Wanna Dance — Windows x64 portable

首次使用请阅读随包 TERMS.txt，在控制台主动确认内容权利声明并同意完整条款。
MIT 仅授权软件，不授予视频、音乐等第三方内容的下载、传播或商业使用权。
未同意或条款更新后未重新确认时，不会自动启动缓存或下载；仍可恢复 hosts。
本机确认记录保存在 still-wanna-dance-console.json.terms.json，不证明用户身份或内容授权。

1. 将 ZIP 完整解压到可写目录（例如 D:\Apps\Still Wanna Dance），不要直接从压缩包内运行。
2. 双击 still-wanna-dance-console.exe。程序显示托盘图标，并打开本地浏览器控制台。
   无需 CMD / PS1 启动脚本，也不需要安装 Go、Node.js 或其他运行库。
   同意条款后关闭网页，程序仍在托盘运行；再次双击 EXE 会打开已有控制台。
   未同意时关闭条款页会尝试在 60 秒后退出，重新打开可取消。
3. 在控制台设置存储目录；VRChat 日志目录默认随当前用户自动读取，仅自定义路径时勾选「手动指定」。保存后按需启用功能。
   默认存储目录 still-wanna-dance-data 位于程序目录下。
   视频位于 videos，歌曲资料和统计位于 stepstash.sqlite。全曲目下载可能占用大量空间。
4. 正常启动无需管理员权限。修改 hosts 时才会弹出 Windows 授权提示。
5. 退出请使用托盘菜单。退出不会恢复 hosts；停用 CDN 后需要直连上游时，
   请先在控制台点击「恢复 hosts」，移除 Still Wanna Dance 添加的映射；其他映射保留。
   删除程序前也请恢复 hosts，并核对接入状态。

配置与移动
设置保存为程序旁的 still-wanna-dance-console.json。保存设置后，程序目录内的路径
以相对路径保存，退出程序再整体移动文件夹即可；外部存储和日志目录仍
使用原来的绝对路径，换电脑或移动外部目录后需重新选择。
still-wanna-dance-console.json.lock 是配置锁文件，退出后保留是正常现象。
升级时先退出程序并备份配置和整个存储目录，再替换 EXE 和说明文件。
当前统一存储不兼容原版歌曲库及旧版 StepStash 存储，也不提供迁移。
从旧格式升级时请使用新的空存储目录，保留旧数据备份。

排错
控制台端口由系统自动分配，启动时自动打开；也可从托盘打开控制台。
重复启动会打开已有控制台。CDN 的 80 或 443 端口冲突时请根据提示处理占用程序。
启动 CDN 会同时开启游戏 HTTP 缓存和网页 HTTPS 转发；两个端口须同时可用。
网页播放保持原站加密连接并复用独立 DNS 和多 IP 建连，不使用本地缓存。
如目录不可写，请移动到用户可写目录，避免 Program Files 等受保护目录。
build-info.json 记录版本、源码提交、工作区是否有未提交修改和编译器版本。
ZIP 旁的 .sha256 文件用于校验下载是否完整。

许可
Still Wanna Dance 采用 MIT 许可证，全文见 LICENSE。
第三方依赖、内嵌组件和 Go 运行库的版权及许可全文见 THIRD-PARTY-NOTICES.txt。
重新分发或升级程序时，请同时保留和更新这两份许可文件。

程序自身日志
程序日志在 logs\still-wanna-dance-console.json.log，与读取 VRChat 日志的设置无关。
日志记录启动退出、功能操作、下载与校验异常及任务汇总；每行是 JSON 文本。
单文件上限 5 MiB，保留 .1、.2、.3 三份旧日志，总计最多约 20 MiB。
重启会继续追加。反馈问题时可提供当前日志和旧日志；其中可能含本机路径和
歌曲请求信息，分享前请检查。正常退出有退出记录，强制结束或断电则没有。

重命名升级（StepStash → Still Wanna Dance）
本次更名不改变当前统一存储格式。退出旧程序后，将新 EXE 放在原程序目录。
默认先使用 still-wanna-dance-console.json；不存在时，原地使用 stepstash-console.json，
保留其相对路径、配置锁、日志与条款记录位置。两份配置同时存在时使用新名称，
也可通过 -config 明确选择。旧配置未写 storageDir 时继续使用 stepstash-data。
不要为重命名移动或重建数据库；stepstash.sqlite 文件名保持不变。
条款产品名和版本已更新，升级后需要重新确认；确认前不会自动启动 CDN。
单实例协议和 hosts 旧标记保持兼容。上文“旧版存储不兼容”仅指更早的存储格式。
