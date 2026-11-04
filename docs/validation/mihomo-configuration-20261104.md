# Mihomo 完整配置导入验证（2026-11-04）

本轮将节点提取替换为原生完整配置接入，现行规则见 [Mihomo 与 zashboard](../mihomo-dashboard.md)。文件／配置内容、完整配置 Base64、一次性 HTTP/HTTPS URL 共用同一个处理入口。原始配置内容使用现有 DPAPI 封套保存，来源 URL 不保存、不自动更新；原生缓存不额外加密或改写，接受明文节点和凭据可能存在于缓存的结果。旧节点快照按普通非法配置处理，失败保留密文，不迁移、不额外引导。

## 实现与验证范围

- `configuration.go` 集中维护唯一的运行覆盖规则；校验与实际启动均从原始内容生成独立配置。保留原节点／组名称、DNS、hosts、规则及引用；添加 SWD 入口和默认自动故障切换，直接引用原组。没有原组时使用原生 include-all。组名冲突采用新生成名称，不改写原对象。
- 原生完整解析放在独立校验进程中，stdin 传递配置，原生 stdout/stderr 丢弃；不调用 ApplyConfig。必要资源仍由 Mihomo 原生处理。运行核心及已有连接不受草稿检查影响。
- 接入设置关闭原配置监听、TUN、控制器、NTP 等，禁用系统 hosts 和 Geo 定期更新；不裁剪规则、不增加通用循环诊断、不自行加载 provider 内容。原生 provider 更新不受 Geo 更新开关控制。
- 仅原始配置加密；原生日志不进入 SWD 日志。原有 select 组可在面板操作，保留已有 selector 同步保护；fallback/url-test 自动组不开放原生未同步的手动固定状态。关闭新增的 rule-provider，并在原生退出持久化完成后关闭缓存。

## 已通过的检查

统一使用 `scripts/with-test-temp.ps1` 和 `scripts/go-patches.mjs`；涉及 Windows 原子替换和 Edge 的用例在沙箱外、普通用户权限执行。测试使用回环服务、隔离配置和数据库，不修改系统 hosts、不启动游戏。

| 范围 | 结果与证据 |
| --- | --- |
| 两个受影响 Go 包 | `test -tags=no_easytier ./internal/proxyengine/mihomo ./internal/console -count=1` 通过；Mihomo 25.564 秒、Console 167.339 秒。本机原始输出 `artifacts/mihomo-configuration/affected-packages.log`。包括原生协议、本地出口、选择恢复、退出预算、配置加密／篡改、保存失败不覆盖、草稿过期／配额和条款门禁 |
| 原组与专用节点 DNS | 回环 DNS 为节点域名提供独立解析，普通 nameserver 指向不可用回环端口；真实请求分别经原组出口 A/B 返回，证明使用专用节点解析且原组可切换。原组名为 SWD 时，新入口为 SWD (1)；检查草稿后既有核心继续通过 B 访问 |
| 三种来源 | 真实 Console API 验证文件内容、Base64、URL 得到完全相同的原文草稿；URL 只下载一次。错误 HTTP 状态、超大响应、非法来源、Base64 URI 列表被拒绝且不改保存内容；错误不包含来源凭据。见 `native-sources.log` 的初次失败及 `browser-real.log` 的通过复验 |
| 真实 Edge／Console | `TestMihomoConfigurationBrowser` 通过文件、Base64、URL 的核心校验、原文 DPAPI 保存、待重启、刷新后清空来源和重新加载。390/1280 像素无横向溢出、无页面错误。最后复验见 `final-targeted.log`、`browser-final/result.json` 及本机截图；现有 `TestSettingsValidationBrowser` 同时通过 |
| 浏览器夹具回归 | 30 项通过，覆盖草稿失败重试／释放、文件大小／重选、字段校验、窄屏及相关恢复流程。记录 `browser-fixtures-after.log`。夹具结果只证明前端交互；持久化结论使用上一行真实服务结果 |
| Windows race | 复用固定 w64devkit 2.10.0，归档 SHA256 与 CI 一致（18D0A4C…C6325E），GCC 16.2.0。`TestOriginalGroupSelectionUsesDedicatedNodeDNS`、`TestConfigurationNativeValidationIsolatedAndPrivate`、`TestStrategyGroupSwitchFallbackAndPersistence` 通过；包括并发选择、序列化、健康检查、实际拨号和进程重启。记录 `native-race.log` |
| 私人原配置 | 使用先前移除无效空 hosts 项的本机副本，经新原生检查通过：45 个内置节点、29 个原有代理组。原名称和重复连接配置均保留，因此数量不同于旧提取器的 42 接受／3 去重。没有拨号私人节点，不外推为机场连通性或视频播放通过。只保留数量结果 `private-native-check.log`；配置、路径、URL 和凭据不入库 |
| 静态与构建 | 受影响包 `go vet`、桌面入口开发构建、`pnpm --dir webui build/check`、`git diff --check` 通过；内嵌产物和构建清单更新并按仓库要求登记。开发 EXE 位于忽略目录 `artifacts/mihomo-configuration/SWD.exe`，未替换或启动现有程序 |

## 保留的失败与修正

1. 沙箱内 Console 在 `renameat .terms.json.swd-tmp terms.json` 返回 `The handle is invalid`，与 [开发说明](../development.md) 已记录的环境限制一致。没有修改产品原子写入逻辑；沙箱外相同受影响包整包通过。沙箱内浏览器运行未完成，后续使用沙箱外 Edge 证据。
2. 旧选择缓存用例依赖导入顺序。完整配置的原生 include-all 按核心顺序给出名称后，断言失败；改为按真实原名称定位，并提前登记 Close 清理，保留选择写入／恢复及不泄漏连接参数的检查。
3. 新来源用例最初将未同意条款时的拒绝断言为 400，实际由已有统一门禁返回 428。修正为现行状态码，仍检查没有任何下载请求。初次失败记录在 `native-sources.log`，后续 API 与浏览器联调通过。
4. 草稿浏览器旧用例断言未检查内容时保存按钮禁用，但现行表单在点击保存时给出字段校验。改为点击后确认字段无效且没有保存请求，随后继续检查草稿和释放；没有修改或放宽产品字段校验。初次 29/30 与最后 30/30 分别保留于 `browser-fixtures.log`、`browser-fixtures-after.log`。

上述本机原始输出均在忽略目录 `artifacts/mihomo-configuration/`，私密内容不作为仓库夹具。重启子用例的单独入口和可选 60 秒 soak 默认跳过，重启行为已由父用例显式启动子进程验证；本轮没有执行可选 soak、全包 race、真实机场连接或 VRC 游戏验收。不新增仅为本次配置接入而设的 VRC 台账项。验证阶段未执行 Git commit/push。
