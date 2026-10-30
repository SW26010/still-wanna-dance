# zashboard 固定依赖的 trust-policy 例外依据

核对日期：2026-10-30。范围为 zashboard `v3.30.0` / `bf4f00bb8d905f8a78f627af7cf84457c4db53c4` 固定锁的两项依赖。本记录保留源码收集阶段的真实告警及有限核对结果，不把它解释为“已证明安全”；此研究步骤未修改包、锁文件或 pnpm 策略。

## 告警及来源核对

pnpm `11.25.0` 在 `trustPolicy: no-downgrade` 下报告：

```text
High-risk trust downgrade for "semver@6.3.1" (possible package takeover)
High-risk trust downgrade for "@trickfilm400/rollup-plugin-off-main-thread@3.0.0-pre1" (possible package takeover)
```

两项确实缺少发布时间更早的其他版本已具有的 provenance，符合 pnpm 规则；不是“旧版本一律不合格”，也不能仅凭年代久远认定误报。

| 固定依赖 | npm 发布时间（UTC）及告警依据 | 来源与完整性核对 |
| --- | --- | --- |
| `semver@6.3.1` | `2023-07-10T22:38:41.428Z`；早于它的 `7.5.1–7.5.4` 有 provenance，6.3.1 没有 | [npm 元数据](https://registry.npmjs.org/semver) 的 gitHead 为 [b717044e…](https://github.com/npm/node-semver/commit/b717044e57bd132c7e5aa50e9af9a03f10d4655a)，发布提交为 6.x 维护版；已安装全部 6 文件与该提交对应 blob 逐字节一致 |
| `@trickfilm400/rollup-plugin-off-main-thread@3.0.0-pre1` | `2025-12-02T16:21:14.510Z`；`2.4.3` 与当日早约 32 分钟的 `2.5.0` 有 provenance，此预发布版没有 | [npm 元数据](https://registry.npmjs.org/@trickfilm400%2Frollup-plugin-off-main-thread) 的 gitHead 为 [96840a1a…](https://github.com/Trickfilm400/rollup-plugin-off-main-thread/commit/96840a1acad0756e000abc70bcd2234f65dc7fb1)，GitHub 报告提交签名 valid；159 个包文件中，与 Git 提交同路径的 86 文件仅作 CRLF→LF 归一化后全部相同，其余 73 个均位于 `tests/fixtures/*/build/` |

插件的 `index.js`、`loader.ejs`、`package.json` 均在上述匹配范围；6 个 pnpm 安装生成的 `node_modules/.bin` shim 不算上游包文件。两项 package.json 都没有 `preinstall/install/postinstall`。这不代表插件构建代码永远不执行，或测试构建产物已做全面安全审计。

对两条元数据中的官方 registry tarball 进行了只读 HTTP 获取，在内存计算 SHA512/SHA1：分别为 19,093 和 23,319 字节，结果均同时匹配 npm 元数据和 [固定 zashboard 锁文件](https://github.com/Zephyruso/zashboard/blob/bf4f00bb8d905f8a78f627af7cf84457c4db53c4/pnpm-lock.yaml)；没有保存下载副本。[dependency-trust.json](dependency-trust.json) 记录完整 URL、integrity、gitHead 和核对边界。本地元数据依据位于 `artifacts/mihomo-dashboard/source-validation/collect/dashboard-registry-cache/v11/metadata-full/registry.npmjs.org/` 的两份对应 `.jsonl`。

两包元数据都含 npm registry signatures，但本次没有独立验签；registry signature、Git 提交签名、tarball 摘要匹配分别提供不同证据，均不能替代缺失的构建 provenance。

## 官方规则与精确例外

[官方配置说明](https://pnpm.io/settings/dependency-resolution#trustpolicyexclude) 支持按精确版本列出 `trustPolicyExclude`。本次还核对固定 pnpm `v11.25.0` 的 [trustChecks.ts](https://github.com/pnpm/pnpm/blob/6d90c71efdffbc909b499490b64c66badc720327/pnpm11/resolving/npm-resolver/src/trustChecks.ts)：比较范围按发布时间而非 semver major；安装稳定版时忽略此前预发布版，安装预发布版本身没有该排除；普通 registry signatures 不属于该函数的 trust evidence。其 [version-policy 解析器](https://github.com/pnpm/pnpm/blob/6d90c71efdffbc909b499490b64c66badc720327/pnpm11/config/version-policy/src/index.ts) 支持 scoped 包和精确版本，版本部分不是任意 semver 范围。

经上述来源和内容核对，对这两个固定版本作局部例外有可解释依据：维持原固定依赖集、接受这两项已知 provenance 缺口，不扩大为包名全部版本、通配符或按年龄整体忽略。配置语法为：

```yaml
trustPolicy: no-downgrade
trustPolicyExclude:
  - semver@6.3.1
  - '@trickfilm400/rollup-plugin-off-main-thread@3.0.0-pre1'
```

源码收集采用 CLI 也可以，无须为了此例外修改上游 `pnpm-workspace.yaml`：

```text
pnpm fetch --frozen-lockfile --ignore-scripts --trust-policy=no-downgrade --trust-policy-exclude=semver@6.3.1 --trust-policy-exclude=@trickfilm400/rollup-plugin-off-main-thread@3.0.0-pre1
```

固定版本的 [install.ts](https://github.com/pnpm/pnpm/blob/6d90c71efdffbc909b499490b64c66badc720327/pnpm11/installing/commands/src/install.ts) 注册该 CLI 参数，[fetch.ts](https://github.com/pnpm/pnpm/blob/6d90c71efdffbc909b499490b64c66badc720327/pnpm11/installing/commands/src/fetch.ts) 复用它，[types.ts](https://github.com/pnpm/pnpm/blob/6d90c71efdffbc909b499490b64c66badc720327/pnpm11/config/reader/src/types.ts) 将它声明为 `[String, Array]`。本机 pnpm 11.25.0 带两个重复参数执行 `fetch --help` 成功解析；另用只读 `config get trustPolicyExclude --json` 配合两个 `--config.trust-policy-exclude=...` 返回双元素数组。注意 `config` 子命令直接传普通 `--trust-policy-exclude` 报 Unknown option，其查询需使用 `--config.` 前缀；`fetch/install` 没有这个限制。

后续收集保留 `no-downgrade`、冻结锁与禁用安装脚本；只对此记录的两个完整 selector 作例外。该研究未运行安装或构建，也未宣称例外后的源码收集已经通过。

