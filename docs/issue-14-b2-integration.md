# Issue 14 B2 产品集成

基线 `8ed4043`，Mihomo 固定 `v1.19.32`。开发分批提交，本轮不执行真实游戏验收或 push。

## 导入与身份边界

新增原生 YAML/URI/Base64 输入路径，沿用 2 MiB / 2048 条预算、逐条结果和稳定排序。暂不切换产品入口，避免旧 A 接受它无法执行的节点。

导入仅提取节点，原生转换和 `ParseProxy` 校验不调用 `ApplyConfig`。跨协议能力门禁拒绝链式出口、动态组、文件/接口/路由能力、overlay/IP stack，以及可在原生解析失败时遗留 base adapter 的可选 smux 包装。v1.19.32 的普通 stream adapter 构造不拨号；AnyTLS 可创建空闲维护 worker，校验后立即 `Close`。升级核心须重新审核这些副作用；不维护协议字段白名单。

Mihomo 使用的 logrus 输出在包初始化时永久丢弃，转换前即生效；不订阅其含原始参数的事件。应用自身继续使用 slog 固定诊断。显示名不进入节点数据；不改写凭据，不承诺所有跨格式/默认值语义等价节点都合并。连接参数有变化可以保守重测，不能错误继承另一出口。

自动检查与各批结果在后续提交中补充。该批原生路径尚未接入产品，不新增实机编号；LA-034/035/036 保持待验收。

第一批自动检查：go test -tags=no_easytier ./internal/proxyengine/mihomo 通过（包含新原生导入测试与旧执行层回归）。

## 完整运行时与所有权

新增 `NewRuntime`：受控 `ParseWithBytes → ApplyConfig`，仅传入活跃节点，捕获固定对象后调用 `proxydialer.New(proxy,true)`。每进程只允许一次完整初始化，失败或退役后不能重新启动。目录由调用方传入绝对应用路径；配置无代理入站、TUN、订阅规则或自动更新；核心使用不读 hosts 的证书校验 DoH，标准库默认 resolver/transport 和 SWD 直连解析器保持独立。

每节点 32 个准入槽，分别覆盖未完成拨号、HTTP 空闲/活跃连接及其实际关闭；它与 K=5 无关，也不是性能最优值。默认拨号预算 10 秒，调用者取消不归还仍被后台占用的槽。连接 Close 的单次后台清理也占槽，避免迟到连接关闭卡住时无限增长。关闭立即拒绝新拨号，取消在途工作，整个连接/adapter/provider 关闭与排空共享 3 秒等待预算；超预算返回固定错误，依赖进程退出，不声称完整卸载全局状态。

第二批自动检查：`go test -tags=no_easytier ./internal/proxyengine/mihomo` 通过；补充 `TestAdmission|TestRuntime` 定向检查通过（含阻塞 Close、未响应取消、成功/取消竞争、不同节点容量隔离、四协议固定出口、名称重绑和初始化失败不可重试）。完整核心测试使用独立测试进程，无重置生产单例后门。`scripts/build-desktop.ps1 -Output artifacts/issue14-b2/SWD-stage2.exe` 成功；新增完整核心依赖已通过 `go mod tidy` 固定。产品接线尚未切换，本批不新增实机项，既有台账继续积攒。
