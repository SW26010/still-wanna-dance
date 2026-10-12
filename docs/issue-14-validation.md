# Issue #14 开发验证

基线 `c26b920`，2026-10-13 开始产品接入。开发与实机验收分开，本轮不启动 VRChat、不修改 hosts、不 push。实际游戏/桌面环境验证继续积攒。

## 节点导入与快照

`internal/proxyengine/mihomo/import.go` 复用固定版本上游转换器，调用前限制 URI 协议/参数并隔离已知会输出原始 URI 的错误路径。仅提取 YAML proxies；支持 URI/Base64，不加载完整运行配置。端口、地址、默认参数归一化，展示名和 TCP 无关 UDP 标志不影响身份。重复节点跳过；不支持/无效项逐条固定诊断，不返回输入、名称、地址或凭据。VLESS 的 TLS client-fingerprint 属于明确传入上游的连接参数。

`internal/console/proxy_nodes.go` 使用当前用户 DPAPI 保护整份版本化节点文档，嵌入 config 的同一次原子写入；预览是有限内存草稿，设置及向导最后保存才提交。快照参与 Settings 比较及向导 HMAC 版本校验。失败写入不发布，解密失败保留密文；API 导入预算与普通设置接口分开。

自动验证覆盖三种输入的一致性、重复/部分失败、字段拒绝、输入预算、私密结果、鉴权、草稿过期和冲突、关键参数身份变化、向导版本、保存/重启/同用户搬迁、损坏密文和失败原子替换。真实跨用户/机器不以这些测试替代。

2026-10-13：`go test -tags=no_easytier ./internal/console ./internal/proxyengine/mihomo` 通过。本机沙箱内控制台测试因读取 Windows 进程身份返回 `Access is denied`；正常用户上下文通过，后续控制台回归使用正常用户上下文。

## 待实机验收

- LA-034：节点导入、受保护快照、保存与重启；在完整 UI 接入后执行。
- 运行时多节点及 UI 后续交付将在本记录补充自动检查与对应台账。
