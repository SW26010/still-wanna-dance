# Issue #30 开发验证

## 许可声明与条款（2026-10-12）

- `go test ./internal/legal ./internal/console -run 'TestExplicitVersionRequired|TestTerms' -count=1 -timeout=120s`：通过。首次受限沙箱因 Windows 用户/路径访问返回 Access denied；在正常用户权限、临时配置下重跑通过，没有忽略失败或运行游戏。
- `node --test scripts/terms.test.mjs scripts/setup.test.mjs`：80/80 通过。
- `git diff --check`：通过。GPLv3 全文来自已固定的 Mihomo LICENSE 副本；原根 MIT 文件完整保存在 `LICENSE-MIT`，没有删除第三方声明。
- 条款版本改为 `2026-10-12.1`，旧 CLI 版本明确拒绝；已有版本/摘要门禁与确认持久化回归通过。真实升级、关于页及重新确认继续待实机验收 **LA-033**，不以自动测试结项。

本次只做代码、文件及本地自动检查。既有授权和隔离构建证据见[许可审计](issue-30-license-audit.md)；go-lzo 是维护者接受的非阻断风险，不是已确认兼容。

## 完整对应源码工具（2026-10-12）

- `node --test scripts/release-source.test.mjs`：5/5 通过，包括临时 Git 仓库导出、无 Git 元数据/全新 Go 缓存重建 Windows EXE，以及 dirty、错误 tag、损坏/缺失/额外源码、错误二进制身份和版本的拒绝。接受固定 go-lzo 模块不会触发拒绝。
- 首次受限沙箱不能创建临时 `.git/config`，改为正常用户权限运行通过。测试只操作独立临时仓库，不推送，也未在本仓库创建发行 tag。
- 此阶段测试为小型构建夹具；实际 SWD 完整源码包与正式交付链验证在后续记录，不能以夹具测试代替。
- 仅新增交付工具、文档和自动测试，不改变应用运行行为；不新增实机项，LA-033 保持待验收。
