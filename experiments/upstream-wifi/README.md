# Wi-Fi 上游与单路 IP 优选复核

[报告](../../docs/validation/wifi-ip-selection-20261029.md)记录 2026-10-29 晚至 10-30 凌晨的固定批次。本目录只维护离线复核入口，执行时不会访问网络或重新测速。

先按[附件说明](../../docs/evidence/wifi-ip-selection-20261029/README.md)用现有 `scripts/evidence_archive.py` 验证并解压脱敏附件，再在仓库根目录运行：

```powershell
node experiments/upstream-wifi/analyze.mjs
```

也可传入已验证的完整脱敏数据目录，其中包含 `single-lane-trials.jsonl` 与 `events.jsonl`。工具重新核对 329 地址的双协议覆盖、所有失败和中断分母、正文长度与哈希、单 IP 无重叠、全局四路上限、串行确认排名、冻结先于六轮留出，以及同协议 IP 收益和同 IP 协议配对。输出到标准输出，不修改原始数据或已提交统计。

私有绑定地址、接口 socket 选项与实际远端在采集及收尾时独立核对，结果见 `verification.json`；公开复算不声称重新验证缺省的私人输入。HTTP/HTTPS 同 IP 配对仅限双方共同进入留出的四个地址，不能据此评估整个候选池的稳定协议收益。

实际采样源码和所需内部依赖完整保存在附件 `_provenance/runner-source/`，r0/r1 的历史片段及旧窗口脚本也保留。它们是采样时快照，不用当前产品源码替换后冒充原构建；签名 URL、Wi-Fi 地址及二进制仅留在私人备份。源码快照需要重新准备私有输入才能另开实验，本次整理不自动复跑，也不改变产品协议或选点规则。
