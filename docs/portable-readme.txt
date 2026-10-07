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
   视频位于 videos，歌曲资料和统计位于 storage.sqlite。全曲目下载可能占用大量空间。
4. 正常启动无需管理员权限。修改 hosts 时才会弹出 Windows 授权提示。
5. 退出请使用托盘菜单。退出不会恢复 hosts；停用 CDN 后需要直连上游时，
   请先在控制台点击「恢复 hosts」，移除 Still Wanna Dance 添加的映射；其他映射保留。
   删除程序前也请恢复 hosts，并核对接入状态。

配置、备份与移动
首次成功打开控制台时，程序即在 EXE 旁保存 still-wanna-dance-console.json，
无需点过“保存设置”。程序目录内的路径以相对路径保存，外部目录使用绝对路径。
自定义 -config 时，相对存储路径以该配置文件所在目录为基准。

完整配置组包括主 JSON 及其同名前缀附属文件：
  still-wanna-dance-console.json
  still-wanna-dance-console.json.terms.json（条款确认）
  still-wanna-dance-console.json.channel-key（代理通道身份）
  still-wanna-dance-console.json.throughput.json（测速状态）
  still-wanna-dance-console.json.inventory.json（库存结果）
  still-wanna-dance-console.json.batch.json、.attempt.json（任务摘要）
自定义配置名称时，附属文件使用该名称；建议整组备份，不要只复制主 JSON。
配置组可能包含代理密码，请妥善保管；日志按需备份。
.json.lock 与存储目录 .lock 是运行锁；文件存在不代表程序仍在运行。

备份前从托盘退出程序，再复制完整配置组和整个存储目录；不要在运行中
只复制 SQLite 主文件。如果存储目录位于程序目录外，也必须单独备份。
整体移动便携目录应先退出；内部相对路径保持有效，外部路径仍指向原位置。
修改“存储目录”只是选择另一套存储，不会自动搬运已有视频或统计。
需要搬迁时，先在设置中填写目标目录并保存，暂不重启，然后从托盘退出。
将旧存储目录中的全部内容移至目标目录后，再启动程序，避免自动启动功能
在搬迁完成前使用空目录。不要只搬 videos 而遗漏 storage.sqlite。

升级
1. 从托盘退出旧程序，按上面的范围完成备份。
2. 将新版 ZIP 解压到临时位置，将以下六个交付文件一起复制并覆盖到原程序目录：
   still-wanna-dance-console.exe、README.txt、TERMS.txt、LICENSE、
   THIRD-PARTY-NOTICES.txt、build-info.json。
3. 保留原目录中的完整配置组、日志和媒体存储，启动原目录中的 EXE。
   不要直接从新版本解压目录启动，否则默认路径会指向另一套配置和空存储。
4. 条款更新时重新确认；若提示配置或存储格式较新，使用更新程序，
   不要删除文件、修改版本号或换空目录来消除提示。

这是发布前定稿的首版文件格式。不读取、自动发现或迁移无版本的旧配置，
不转换旧 stepstash.sqlite。旧测试数据请保留备份，使用独立的新配置和存储目录。
若所选目录含 stepstash.sqlite，启动缓存引擎及离线删除会拒绝继续；即使同时
存在 storage.sqlite 也不会自动选择，不会建库、清理临时下载或淘汰视频。
这只是已知旧库标记保护，不保证识别所有历史目录；不要通过改名、移除标记
或修改版本号强行复用旧库。请保留原目录，另选独立新目录。

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

单实例发现与 hosts 备份仍保留 StepStash 协议名称；它们不表示旧文件格式兼容。
