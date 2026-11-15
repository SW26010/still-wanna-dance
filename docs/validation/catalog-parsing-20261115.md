# 后台曲库检测的清单解析验证

2026-11-15，基于 `53f006f0` 的工作区变更。现行规则见[清单同步](../song-catalog-design.md#清单同步与增改不删)。

## 复现与修改

原检测链路在网络探测中解析正文选样本，来源选择和 Console 同步又分别完整解析一次；完整解析还为每首歌建立字段 map、修改字段、重新序列化，再解码为歌曲结构。`TestCatalogResponseReusesValidation` 在未复用结果时失败，报 `catalog was parsed again`。

本次在既有 `CatalogResponse` 内保留单轮响应的完整解析结果及错误，来源选择和同步调用同一 `Parse` 方法。成功和无效资料的反馈均不再完整解析第二遍；轻量选样本仍保持原行为。共有歌曲资料直接解码，来源特有字段在现有解析器内转换。继续沿用网络与 Store adapter，Store 对直接传入的 `Catalog` 仍执行防御性归一化；不添加跨轮缓存、持久化字段或新的 adapter 层。

## 解析阶段基准

Windows amd64、Go 1.27.3、AMD Ryzen 7 9850X3D。`BenchmarkCatalogDetectionParsing` 使用 10,575 首合成歌曲，分别为 2,939,420 字节 Udon 清单与 2,981,721 字节备用清单。基准经过实际探测读取／选样本，再调用来源选择与同步所用的解析方法，排除真实网络等待、来源竞速和数据库写入；不代表整轮检测耗时或真实上游正文。基线仅统一了解析方法的调用入口，尚未复用结果或修改字段转换。

每组 3 次、每次 3 个迭代，下表取各列中位数；耗时不设自动测试阈值。

| 格式 | 解析阶段耗时 | 累计分配量 | 对象分配次数 |
| --- | --- | --- | --- |
| Udon 修改前 | 185.23 ms | 151.69 MiB | 1,946,650 |
| Udon 修改后 | 53.92 ms | 40.65 MiB | 486,783 |
| 备用修改前 | 210.74 ms | 162.66 MiB | 2,020,758 |
| 备用修改后 | 58.48 ms | 48.05 MiB | 539,687 |

Udon 耗时约减少 71%，累计分配量约减少 73%，对象分配约减少 75%；备用格式相应约减少 72%、70%、73%。分配量是本次解析累计申请的临时内存，不是常驻内存或进程峰值。此处使用独立合成夹具，不将用户先前 2.78 MB 清单的测量视为同一批数据。

复跑修改后的基准：

```powershell
./scripts/with-test-temp.ps1 node scripts/go-patches.mjs test -tags=no_easytier ./internal/upstreamcheck -run '^$' -bench '^BenchmarkCatalogDetectionParsing$' -benchmem -benchtime=3x -count=3
```

原始前后结果保留在忽略目录 `artifacts/catalog-parsing/{before,after}.log`。

## 回归与边界

- 完整解析成功及失败均复用；并行来源选择把已验证响应交给业务；全部候选资料无效时仍一次性反馈原有错误。
- 字段回归覆盖缺失／null 与零值／false 的区别、Udon 数字布尔值、备用布尔值、无关来源字段、分组继承、大小写 MD5、float32 音量、数组形状与大整数；ID／MD5 校验先于可选资料错误。
- `upstreamcheck` 与 `upstreamstate` 整包测试通过；`cacheproxy` 的曲库／Udon 回归和 Console 的来源选择、同步、初始化、失败／取消、存储故障、直接刷新与通知回归通过。四个包的 vet 通过。
- 使用已有 w64devkit、`CGO_ENABLED=1` 执行四个包的定向 Windows race，覆盖结果复用、字段适配、并行检测、来源选择与同步、无效资料及存储故障反馈，全部通过；Console 组耗时 211.096 秒。未运行全包 race 分组。

Go 检查均经过现有临时目录和补丁入口。Console 首次定向检查在初始化 `terms.json` 时遇到已知沙箱错误 `renameat: The handle is invalid`；`upstreamstate` 整包首次也因 `throughput.json` 的相同重命名限制失败。两项在普通用户权限下复跑通过，未修改产品文件操作。失败输出及通过日志保留在本次工具记录与 `artifacts/catalog-parsing/`，不把沙箱失败当作产品回归通过。

本次使用合成清单、受控网络和隔离存储，未访问真实上游、替换运行中的程序、启动 VRC 或修改系统 hosts；没有需要新增到 VRC 台账的剩余场景。
