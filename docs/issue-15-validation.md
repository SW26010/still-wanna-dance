# Issue #15：条款确认凭据保护

## 实现范围

沿用 #23 的配置归属和 `terms.json` 路径；Windows 使用现有 DPAPI CurrentUser 底层封装，无机器作用域标志、路径绑定或额外 UAC。外层格式为 `format=1`、`protection=dpapi-current-user`、Base64 密文；密文内包含用途、条款版本、正文 SHA-256 和确认时间。

每次启动解密校验。缺失、旧明文、未知格式、损坏、解密失败和条款变化均保持未同意；失效文件不自动改写。普通 I/O 错误返回诊断。只有现有双勾选、Token/Origin 校验通过且保护和原子写成功后更新内存。原有自动启动抑制、清理通道、退出倒计时保持不变。

非 Windows 生产实现拒绝保存确认，不降级为明文。Linux 单元测试使用仅在测试二进制中的临时认证加密替身；它不验证 Windows 用户绑定。独立 CLI 的显式条款参数不变。

不引入严格机器绑定：域账户/漫游配置可能跨机器解密。不将本地时钟或 DPAPI 当作身份、亲自阅读或法律签名证明。确认时间要求非零且可表示，允许之后系统时钟回拨。

## 本地验证（2026-10-11）

- Windows `go test ./internal/console -count=1` 通过；条款专项测试使用真实 DPAPI。
- `TestTermsDPAPIFreshProcess`：同用户新进程读取凭据通过。
- `TestTermsProtectedMoveAndInvalidReceipts`：搬入不同路径的自定义配置后保留确认；密文不暴露版本/摘要/时间字段；篡改、截断、旧明文、未知格式、错误保护方式、错误密文、空/超大文件、条款版本/摘要变化、零时间和错误用途均不授权。自动 CDN/队列启动受阻，状态 API 返回 428，原始失效记录保留。
- 保护失败、写入失败、读取错误以及现有 API 验证、双勾选、页面离开/返回/超时回归通过。
- `go test ./cmd/... ./internal/...`、`go vet ./cmd/... ./internal/...`、Windows 产品包构建通过。
- Linux 产品包交叉构建及控制台测试二进制编译通过。本机未安装可用 WSL Linux 环境，未声称执行过 Linux 测试或 race 测试。
- 前端及验收协议/请求脚本共 129 项 Node 测试通过。
- 编译后的独立无托盘控制台验证旧明文凭据：`scripts/acceptance.mjs` 返回退出码 2、environment/blocked，在服务下载链路前停止，未提交同意或修改 hosts。

完整 `go test ./...` / `go vet ./...` / `go build ./...` 会扫描本工作区已有的非交付实验源码：`artifacts/migration-20260927` 等引用旧 `stepstash` 包，`artifacts` 下另有重复 `main`/辅助函数。此次未修改这些历史文件，改用明确产品包范围完成检查。

本地详细输出保留在忽略目录：`artifacts/issue15-go-test.log`、`artifacts/issue15-product-test.log`、`artifacts/issue15-node-test.log`。可执行检查报告位于 `test-runs/acceptance-1791733875501/results.json`。

## 待环境验收

- 同一机器第二个真实 Windows 用户复制凭据后必须重新确认。
- 另一台普通独立 Windows 机器复制完整 Portable 后必须重新确认；需要支持域/漫游环境时另行记录其边界。
- 最终发布包连续覆盖升级及完整 Portable 搬迁，与 #26 一并验收。
- 按任务安排暂缓真实 VRChat 联合测试；此次未运行实网视频下载、hosts 提权或跨用户/跨机器测试。

以上待验项不影响代码实现完成，但不应将 #15 的全部真实环境验收标记为完成。
