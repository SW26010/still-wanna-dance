# Go 依赖补丁

当前仅修补 Mihomo `v1.19.32` 的 AnyTLS Stream 关闭错误同步。上游源文件为
[`transport/anytls/session/stream.go`](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/transport/anytls/session/stream.go)，
原文件 SHA256 为 `afdb9642846ca669c584b9b317bf4210074cce933db8ed74f02373933630c6e3`。
原模块与完整 GPLv3 许可随对应源码交付；仓库中的许可副本见
[LICENSE.mihomo](../internal/proxyengine/mihomo/LICENSE.mihomo)。本地修改按 GPLv3 分发。

`mihomo-anytls-stream.go.txt` 是该文件的完整修改版，保留原实现，只将 `dieErr`
改为原子发布、读取。远端关闭会在接收 goroutine 中写入此字段，调用者同时读写
Stream 时原实现存在数据竞态；在返回的连接外层加锁不能保护该内部路径。
回归见 `TestAnyTLSStreamConcurrentClose`，并保留真实协议集成测试与 race 检查。
不改变协议、连接复用、关闭原因或错误传播语义。

开发和 CI 的 Go 构建、测试及 vet 使用 `node scripts/go-patches.mjs <Go 参数>`。
入口先校验选定版本、原模块校验和及原文件哈希，再复制到本次调用独占的
进程临时目录下的 `swd-go-*` 目录，替换一个文件，并通过 Go 标准 `-modfile`
使用该副本。子进程继承同一选择；命令结束后清理副本。原 `go.mod`、`go.sum`
和共享模块缓存保持原样。Go 的 overlay 不允许替换模块缓存中的文件，因此不用它。
副本不再嵌套于源码目录，避免正式检出及源码解包增加路径层级后，Windows
无法在过长的依赖包目录启动 vet。Windows 临时目录包装器仍负责选择并清理本次工作目录。
包装器嵌套调用时沿用最外层临时根，每次创建独立的并列目录；临时检出中的
包装器副本不会继续增加路径层级，结束时恢复调用者环境。

二进制的 Go 元数据明确记录本地替换，目录名含修改文件的 SHA256。
发布校验只接受这一模块、版本和目录格式；源码清单的 `goPatches` 同时保存
原模块校验和、原文件哈希及修改文件哈希。修改后的模块不冒充原模块校验和，
正式包和独立重建必须匹配同一补丁。修改版、说明和构建入口均进入源码清单。

升级 Mihomo 时必须复核这项补丁：优先采用上游正式修复；确认无需本地修改后，
一起移除补丁、构建接入和专门的替换识别规则，保留行为回归。不得仅更新哈希
绕过版本或原文件变化的拒绝。
