"""Generate reproducible numeric tables for the repository CPU report."""
import json
from pathlib import Path
import statistics as st
import sys

def make(root,destination):
    data=json.loads((root/"summary.json").read_text());checks=json.loads((root/"checks.json").read_text());cross=json.loads((root/"crosschecks.json").read_text())
    levels=[1,4,8,12,16,24,32]
    def rows(kind,n):return [r for r in data if r["kind"]==kind and (kind=="stepped" or r["target"]==n)]
    def v(kind,n,key,phase="steady"):
        if kind=="stepped" and phase=="steady":phase=f"steady-{n}"
        return [r["windows"][phase][key] for r in rows(kind,n)]
    def median(kind,n,key,phase="steady"):return st.median(v(kind,n,key,phase))
    def spread(values,digits=2):return f"{min(values):.{digits}f}–{max(values):.{digits}f}"
    def table(headers,records):return "| "+" | ".join(headers)+" |\n| "+" | ".join(["---"]*len(headers))+" |\n"+"\n".join("| "+" | ".join(map(str,r))+" |" for r in records)+"\n"
    b32=median("burst",32,"machinePercent");b4=median("burst",4,"machinePercent")
    text=f'''# Issue #20：CF 并发的 CPU 成本（2026-10-14）

**本机持续下载时，32 路同时启动后的下载进程稳态 CPU 中位数为整机 {b32:.2f}%，4 路为 {b4:.2f}%；32 路相当于持续占用 {median("burst",32,"coreEquivalent"):.3f} 个逻辑处理器。** 但平均占用会掩盖短突发：本轮分别测量了 10 ms、100 ms、1 s 窗口，以及微秒精度调度轨迹中的真正同时运行线程数。进程外还有系统驱动中断开销，下文单列；不能把上述百分比当成整机总新增成本。

本次完成三轮 **45 组**主实验（包括三个空闲基线），正文 **{checks['totalBytes']/2**20:,.0f} MiB**、**{sum(r['requests'] for r in checks['trials']):,} 个请求**，全部通过范围、长度、强 ETag、固定远端和 SHA256 校验。固定 104.26.4.28、最高 32 路，不再向上增加；预实验及受干扰尝试单独保留，见下文。

这是开发实验，不改变产品默认三路后台和全局 6/4 请求预算。**LA-037 继续待实机验收**，没有启动 VRChat、修改 hosts、优先级、电源计划或 CPU 亲和性。

## 先说明单位

- 本机 AMD Ryzen 7 9850X3D：8 个物理核心、16 个逻辑处理器，Windows 11 build 26300，平衡电源计划。SMT 关系由 GetLogicalProcessorInformationEx 实际查询，掩码为 3、12、48、192、768、3072、12288、49152；不是按编号猜测。
- CPU 时间把每个实际运行线程占用的时间相加。1 秒窗口累计 100 ms CPU 时间，就是 0.1 个逻辑处理器当量，在本机换算为整机 0.625%。这里采用时间归一化口径。
- HTTP 在途数、Go goroutine、Windows 线程数、正在执行线程数是四件事。网络等待仍占用 HTTP 槽位，但不会一直占住 CPU。线程在不同逻辑处理器之间迁移，“先后触及全部 16 个”不意味着持续占满 16 个。
- 10/100 ms 峰值是完整固定窗口的平均，末尾不足一个窗口的片段不参加峰值。1 s 峰值以 100 ms 步长滑动。瞬时并行数来自调度区间重叠（时间分辨率约 1 µs），另列持续占比；这些不是游戏帧时间。

## 设计与测量范围

工作负载源码 `{checks['sourceCommit']}`，Go 1.25.0 / windows amd64，CGO_ENABLED=0、no_easytier、trimpath。被测 EXE SHA256 为 `{checks['exeSHA256']}`。分析与报告工具的实际文件哈希包含在本报告清单中。

资源与上一轮相同：song 10552、play.udon.dance、资源大小 103,118,762 字节。预检经产品独立 DNS 取得新地址，核对 DNS 三个 IPv4 后固定 104.26.4.28。每个进程使用新的产品 HTTP candidate Transport、保留 Host/SNI/TLS 校验；HTTP/1.1，没有实验层重试，标准库透明重试未另行计数。

为使 7.75 秒的 32 路逐个启动也能测到稳态，这次循环读取前 96 个 1 MiB 块，**有意重复下载**；吞吐代表持续传输量，不是唯一文件完成速度，不能直接与上一轮 96 MiB 一次下载相除。每路保留一个可复用的 1 MiB 缓冲区，与产品 Range 工作者的保留方式一致，避免旧测速脚本逐请求分配大缓冲区所引入的额外 GC。

三轮固定种子 20261014，顺序在正式测量前冻结，完整顺序见 [plan.json](evidence/cf-cpu-20261014/plan.json)。每轮含：

1. 空闲基线 6 秒，仍启用相同的 100 ms 轻量采样和 ETW。
2. 同时启动 1/4/8/12/16/24/32 路，各保持 6 秒。
3. 从 1 路开始每 250 ms 增加 1 路，分别达到 4/8/12/16/24/32 后再保持 6 秒。
4. 1→4→8→12→16→24→32，每档保持 6 秒，共 42 秒，原有工作者和连接继续运行。

稳态取每个目标档最后约 4 秒（排除到达目标后的前 2 秒）。突发窗口包含建连、TLS、初次缓冲区分配、读取、SHA256、调度与轻量记录。到时不再领取新块，允许已有请求自然排空，排空另列。进程初始化、最终 JSON 写入和连接池清理不在主计时窗口中；不包含完整缓存引擎的磁盘写入、最终 MD5、Seek、多视频额度竞争和游戏解码。

ETW 记录上下文切换与 ReadyThread，按逻辑处理器重建运行区间，再扣除去重的 DPC/ISR 区间。主批次三份轨迹均无事件或缓冲区丢失。GetProcessTimes 的用户态/内核态时间每 100 ms 另存，其数值有明显计时量化，短窗口不以它替代 ETW。解析结果与 xperf 原生 `cswitch -process -exc_dpcisr` 在相同进程/时间边界核对：**{len(cross['checks'])} 组通过，最大绝对差 {max(c['absoluteDifferenceUS'] for c in cross['checks']):,} µs**。

指标含义参考微软 [CPU Analysis](https://learn.microsoft.com/en-us/windows-hardware/test/wpt/cpu-analysis)、[GetProcessTimes](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-getprocesstimes) 和 [Context Switch Action](https://learn.microsoft.com/en-us/previous-versions/windows/desktop/xperf/the-context-switch-action)。这些文档说明计时与调度语义；本报告所有性能数值来自此次本机采集。

当前 Windows/WPT 组合的导出日志对另一类内核事件（GUID b3e675d7-2554-4f18-830b-2762732560de、Type 0x25）有 Invalid Event 解码警告。这里“零丢失”指 ETW 的 Events Lost/Buffers Lost 计数，不意味着所有事件类型都能解码；用于 CPU 计算的调度区间另经上述原生工具总量校核。

## 同时启动：并发增加的 CPU 成本

下表是三轮稳态中位数；范围为三次实际最小–最大值，不是置信区间。“CPU 秒/GiB”使用该窗口已完成分块的字节数，窗口边界会有少量分块量化。完整组（含排空）的成本另保存在 summary.json。

'''
    text+=table(["并发","MiB/s","整机 CPU %（范围）","逻辑处理器当量","CPU 秒/GiB"],[[n,f"{median('burst',n,'mibPerSec'):.2f}",f"{median('burst',n,'machinePercent'):.3f}（{spread(v('burst',n,'machinePercent'),3)}）",f"{median('burst',n,'coreEquivalent'):.3f}",f"{median('burst',n,'cpuSecondsPerGiB'):.2f}"] for n in levels])
    speed=median("burst",32,"mibPerSec")/median("burst",4,"mibPerSec")
    text+=f'''\n从 4 到 32 路，稳态吞吐中位数约 {speed:.2f} 倍，CPU 增加约 {b32-b4:.3f} 个整机百分点。这既包含并发管理，也包含每秒处理更多数据的成本；没有固定总带宽的随机对照，不能把差值全部归因于“并发调度”。每 GiB 成本用于辅助区分这两件事，不能替代等速实验。

![CPU 与吞吐对照](evidence/cf-cpu-20261014/cpu-summary.png)

## 峰值、核心与线程

以下取从启动到停止领取新块的整个活动窗口，包含启动阶段。10/100 ms 数值是三轮观测到的最大值，瞬时并行、线程数列出三轮范围；平均值不能代替它们。

'''
    text+=table(["并发","10 ms 峰值：整机 %","100 ms 峰值：整机 %","创建的 OS 线程","窗口内有 CPU 时间的线程","瞬时同时运行逻辑线程","≥2 线程同时运行的时间 %（中位数）"],[[n,f"{max(v('burst',n,'peak10msCores','active'))*100/16:.2f}",f"{max(v('burst',n,'peak100msCores','active'))*100/16:.2f}",spread([r['threadsCreated'] for r in rows('burst',n)],0),spread(v('burst',n,'activeThreads','active'),0),spread(v('burst',n,'maxSimultaneousLogical','active'),0),f"{median('burst',n,'twoOrMorePercent','active'):.3f}"] for n in levels])
    phys=spread(v('burst',32,'maxSimultaneousPhysical','active'),0)
    text+=f'''\n32 路三次先后触及的逻辑处理器数为 {spread(v('burst',32,'logicalTouched','active'),0)}，实际同时涉及的物理核心峰值为 {phys}；这说明调度分布很广，不代表这些核心长时间被占用。32 路活动窗口内，≥4 个逻辑线程同时执行的时间占比中位数仅 **{median('burst',32,'fourOrMorePercent','active'):.4f}%**。三次中单个逻辑处理器最忙的完整 10 ms 窗口占用达到 **{max(v('burst',32,'peakSingleLogical10msPercent','active')):.1f}%**，因此整机平均很低也不能推导出游戏主线程完全不受影响。

OS 线程统计包括 Go 运行时、网络、采样及进程生命周期线程；有 CPU 时间的线程包括短暂唤醒。Go goroutine 峰值（32 路）为 {spread([r['goroutinesPeak'] for r in rows('burst',32)],0)}，也不等于正在运行的 OS 线程。没有采集调用栈，不能把所有短峰确定归因为 TLS 或 GC；GC 次数、暂停累计时间、累计分配和线程 CPU 分布均在原始脱敏记录中。

## 同时启动、每 250 ms 加一路、逐档增加

同一目标 32 路，比较三个场景的最后约 4 秒：

'''
    text+=table(["方式","达到 32 路的计划时间","稳态 MiB/s","稳态进程 CPU（占整机 %）","稳态 CPU 秒/GiB","活动期最大 10 ms 进程 CPU（占整机 %）"],[[{"burst":"同时启动","stagger":"每 250 ms 加一路","stepped":"每 6 秒升一档"}[kind],{"burst":"0 s","stagger":"7.75 s","stepped":"36 s"}[kind],f"{median(kind,32,'mibPerSec'):.2f}",f"{median(kind,32,'machinePercent'):.3f}",f"{median(kind,32,'cpuSecondsPerGiB'):.2f}",f"{max(v(kind,32,'peak10msCores','active'))*100/16:.2f}"] for kind in ('burst','stagger','stepped')])
    text+='''\n本轮渐增两种方式的最大 10 ms 窗口均明显低于直接启动 32 路，而最终稳态 CPU 接近；其主要观测收益是把突发分散，而非大幅减少达到相同吞吐后的持续 CPU。逐路启动延后达到最大吞吐的时间，也不会消除传输期间的短峰。逐档增加保留低档建立的连接，因此与全新连接池的同时启动存在历史差异，不能只按最高并发把两者看成完全相同条件。三次观测不足以保证所有线路和游戏负载都按同一比例受益。

逐档过程的各档稳态结果如下（每档排除最初 2 秒）：

'''
    text+=table(["档位","计划活动时间","MiB/s 中位数","整机 CPU % 中位数","CPU 秒/GiB"],[[n,f"{i*6}–{(i+1)*6} s",f"{median('stepped',n,'mibPerSec'):.2f}",f"{median('stepped',n,'machinePercent'):.3f}",f"{median('stepped',n,'cpuSecondsPerGiB'):.2f}"] for i,n in enumerate(levels)])
    text+='''\n![三种过程的 CPU 时序](evidence/cf-cpu-20261014/cpu-timeline.png)

时序图事先固定展示第 2 次有效测量，不挑选最平滑或最差的一次。左轴为 100 ms 平均逻辑处理器当量，右轴为在途 HTTP 请求数。所有三轮的 100 ms 序列、线程/逻辑处理器占用、排空、Ready 延迟和精细调度区间均可从证据复算。

## 固定开销、系统中断与未测事项

'''
    idle=[r['windows']['steady']['machinePercent'] for r in data if r['kind']=='idle']
    text+=f'''空闲工具自身的稳态整机 CPU 为 **{spread(idle,4)}%**。这量化了进程内轻量计时的固定开销，但不是 ETW 全系统开销的估计：没有做开启/关闭 ETW 的专门配对，调度记录本身也有成本。

系统整体 DPC/ISR 的活动期平均占用：空闲基线 {spread([r['windows']['active']['systemInterruptMachinePercent'] for r in data if r['kind']=='idle'],3)}%，32 路同时启动 {spread(v('burst',32,'systemInterruptMachinePercent','active'),3)}%。这是机器上全部设备与应用的总和，不能都归因于这次下载；它没有被偷偷算入实验进程 CPU。没有隔离其他日常系统进程、记录 CPU 温度/实际频率或缓存失效率。

对 VRChat 的含义：这台机器上，下载的平均 CPU 时间没有随 HTTP 并发数按“一路一个满载核心”增长；32 路的传输收益仍然存在，而短时线程并行和单核局部占用确实高于平均数能呈现的程度。本轮不能给出 FPS、1% low 或帧时间变化，也不能证明游戏主线程、SMT 同胞和 X3D 缓存没有受到竞争。调高产品默认值前，仍应在 LA-037 的真实冷播/Seek/多视频场景中同时记录游戏帧时间与请求轨迹；本次不据此修改默认值。

## 完整保留的试采、失败与干扰

1. 普通权限 ETW 尝试返回系统 Access denied；随后按用户授权请求 UAC。阶段提供程序第一次参数错误，内核轨迹已停止；第二次 UAC 返回用户取消，用户明确要求重弹后继续。另修复 Windows PowerShell 把 xperf 成功合并的 stderr 信息当错误的问题。试采均不计入主结果，原始私密轨迹本地留存。
2. `459bcfe` 的初版完整区组尝试，在 42 秒阶梯的第 36 秒碰到本地 1,024 个块/1 GiB 上限；995 个请求完成，29 个本地取消。没有收到 429/503。整个不完整区组（11 组，含基线）列为预实验，修正至 2 GiB 单组上限后按原计划重跑，不按快慢选择保留。脱敏原始数据见 preliminary.json.gz。
3. 第一轮轨迹导出后的分析任务退出晚于预期，第二轮首次尝试的开头与其重叠；发现后停止该尝试，保留 8 个已完成组及未完成组的本地 ETW。整个尝试不进入主结果，之后按完全相同顺序重测第二轮。未完成进程未生成完整 JSON，不能补造数据；流量预算为它保守预留 2 GiB。脱敏已完成数据见 interference.json.gz。
4. 主结果由第 1 轮、重测的第 2 轮和第 3 轮组成，共 45 组，**没有再删除任何慢样本**。正式 CPU 采集结束后才开始后续批量分析，没有并发执行本任务的编译或另一路测速。主批次上限 20 GiB、包括历史尝试与保守预留的合计上限 24 GiB，完整校验见 checks.json。

## 验证、证据与验收状态

- 开发检查：实验包 Windows race、vet 通过，流量边界修正后又在采集结束时复跑通过；Python 分析回归包括迁移不冒充并行、真实重叠、嵌套中断去重、末尾不足窗口不冒充峰值及完整计划。
- [环境与拓扑](evidence/cf-cpu-20261014/environment.json)、[计划](evidence/cf-cpu-20261014/plan.json)、[完整性校验](evidence/cf-cpu-20261014/checks.json)、[独立 xperf 校核](evidence/cf-cpu-20261014/crosschecks.json)、[统计结果](evidence/cf-cpu-20261014/summary.json)、[原始请求及进程计时](evidence/cf-cpu-20261014/trials.json.gz)、[哈希清单](evidence/cf-cpu-20261014/manifest.json)。每组 `*-scheduling.json.gz` 包含脱敏线程序号、逻辑 CPU 编号、相对微秒区间与系统中断时间；不包含其他进程身份。使用 `delta-us-v1` 无损整数差分压缩，调用 `analyze_cpu.unpack_trace` 可完整还原；45 组均验证压缩前后整数逐项一致，不降采样。
- 原始 ETL/CSV、私密资源 URL、PID/系统进程信息只留在忽略目录。公开证据移除 PID、完整远端端口和个人路径；阶段相对时间零点为各组 begin 标记，traceBeginUS 为本轮 ETW 起点后的相对时间，不是绝对时钟。
- 本次只有实验工具、测试、文档和开发证据变化，因此不新增实机编号。产品行为未改，LA-037 保持待验收。本地提交，不推送。
- [清理回执](evidence/cf-cpu-20261014/cleanup.json)：管理员助手 exited，匹配的内核/标记采集会话为 0，测试下载进程为 0。原始证据保留在本地忽略目录，没有修改系统设置。
'''
    destination.write_text(text,encoding='utf-8')

if __name__=='__main__':make(Path(sys.argv[1]),Path(sys.argv[2]))
