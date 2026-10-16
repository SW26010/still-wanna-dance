# 开发约定

本文件只保留基本原则与文档入口，具体要求见对应文档。

## 基本原则

- 开发者（含 agent）负责完成适当验证并跟进问题；优先使用自动测试、集成检查和浏览器，复用已有证据，减少用户操作。
- 仅将必须依赖 VRC 的剩余风险合并积攒到实机台账，不按改动机械登记；不因开发完成自动启动游戏或改 hosts。
- 如实区分已验证与未验证范围，保留历史失败和必要证据；私密材料不入库，Git commit/push 遵循当前授权。

## 文档入口

- 开发环境、构建与测试：[开发说明](docs/development.md)。
- 开发、行为修复或验收前阅读：[验证与实机验收机制](docs/live-acceptance-process.md)、[VRC 待验台账](docs/live-acceptance-backlog.md)。
- 跟进已分流的非游戏验证：[非 VRC 验证记录](docs/validation-followups.md)。
- 开展实机验收时使用：[报告模板](docs/live-acceptance-report-template.md)、[证据与脱敏约定](docs/evidence/README.md)。
