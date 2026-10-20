import { useEffect, useState } from 'react';
import { Button, Checkbox, Link, Subtitle2, List, ListItem, Divider, Field } from '@fluentui/react-components';
import { useApp, useResource, useFocusRecovery } from './hooks';
import { Details, Feedback, Panel, Progress } from './ui';
import { type Download, type RequestRecord, type HealthResult, type Health, date, mb, dated } from './types';
import { reconcileRequestGroups, type RequestGroup, requestOutcome, requestCache, requestTitle } from './requests';

type RequestPage = {storageID:string; requests:RequestRecord[]; hasMore:boolean};
function useRequestGroups(data?: RequestPage) {
  const [snapshot,setSnapshot] = useState<{data?:RequestPage; groups:RequestGroup[]}>({groups:[]});
  if (snapshot.data === data) return snapshot.groups;
  const groups = reconcileRequestGroups(data?.requests || [], snapshot.data?.storageID === data?.storageID ? snapshot.groups : []);
  setSnapshot({data,groups});
  return groups;
}

const reasons: Record<string,string> = {no_channel:'没有有效候选',no_sample:'等待有效歌曲及资源样本',expired:'观测已过期',timeout:'请求超时',origin_timeout:'源站响应超时（Cloudflare 524）',network_error:'网络请求失败',resolution_unavailable:'无可用资源地址',invalid:'响应校验失败',restricted:'访问受限',upstream_error:'上游错误',http_error:'HTTP 响应异常'};
const localReasons: Record<string,string> = {local_capacity:'本地代理连接额度已满',runtime_retired:'代理运行时已退出',channel_unavailable:'通道已失效',canceled:'本次检测已取消'};
const operationNames: Record<string,string> = {catalog:'歌曲列表',playback_url:'播放地址解析',resource:'视频资源加载'};
export function healthImpact(h: Health, route: string) {
  if (h.closed) return '检测已关闭，当前无法判断网络可用性。';
  const results = h.results || [];
  const summaries = [
    ['catalog', '歌曲清单', '有可访问来源', '更新可能失败'],
    ['playback_url', '播放地址查询', '有可用线路', '获取新播放地址可能失败'],
    ['resource', '视频下载', '有成功样本', '样本检测失败'],
  ].map(([operation, label, success, failure]) => {
    const group = results.filter(r => r.operation === operation &&
      (operation !== 'playback_url' || !['cf', 'hkg'].includes(route) || r.route === route));
    const usable = group.filter(r => r.state === 'available' &&
      (!r.validUntil || r.validUntil.startsWith('0001') || new Date(r.validUntil).getTime() > Date.now()));
    if (usable.length) return label + '：' + success + (usable.length < group.length ? '（其余线路异常或待确认）' : '');
    // Unmeasured or expired observations are not evidence of a failure.
    return label + '：' + (group.length && group.every(r => r.state === 'unavailable') ? failure : '尚待确认');
  });
  return summaries.join('；') + '。检测样本不保证每首歌曲可播放。';
}
function HealthRow({row:r,minutes,separator,wait='等待调度信息',context}: {row:HealthResult;minutes:number;separator:boolean;wait?:string;context?:string}) {
  const saved = r.lastThroughput, speed = saved ? saved.bytes / (saved.durationNs/1e9) : r.estimatedSpeedBPS;
  const at = saved?.observedAt || r.throughputObservedAt;
  const song=saved?.songID ?? r.throughputSongID, duration=saved?saved.durationNs/1e6:r.transferDurationMS, bytes=saved?.bytes ?? r.transferredBytes;
  const expired = dated(at) && Date.now() >= new Date(at!).getTime()+minutes*60000;
  const issuers=[...new Set((r.sources || []).map(x=>x.api+' · node='+(x.node || '(默认)')+(x.apiChannelID?' · API 通道 '+x.apiChannelID:'')))];
  const noData=r.state==='unavailable'?'尚无成功样本 · 当前资源检查失败：'+(reasons[r.reason || ''] || r.reason || '未知原因'):['no_sample','resolution_unavailable'].includes(r.reason || '')?'尚无成功样本 · 未取得该域名的有效资源地址':'尚无成功样本 · '+wait;
  const technical=r.entry+(r.operation==='catalog'?' · 清单版本：'+(r.catalogTime || '未获取'):'')+(r.channelID?' · 通道 '+r.channelID:'');
  const observation=(r.sampleSongID?' · 歌曲 #'+r.sampleSongID:'')+(r.http?' · HTTP '+r.http:'')+' · 观测 '+date(r.observedAt)+' · 有效至 '+date(r.validUntil);
  return <ListItem><div role="gridcell" className="record-content">{separator && <Divider className="record-divider" />}{context&&<p>{context}</p>}{r.localDiagnostic && <Feedback>{localReasons[r.localDiagnostic] || '本次检测未完成'} · {date(r.localObservedAt)}；保留此前观测，未计为节点失败。</Feedback>}
    <strong>{r.mode === 'direct' ? '直连 '+(r.ip || '') : r.mode === 'socks5' ? '代理 '+(r.channelID?.split('/').at(-1)?.slice(-8) || '') : '无候选'} · {({available:'可用',unavailable:'不可用',unknown:'待测',stale:'已过期',closed:'已关闭'}[r.state] || r.state)} {r.reason && `· ${reasons[r.reason] || r.reason}`}</strong>
    {r.operation === 'resource' && <><p>响应延迟：{r.estimatedLatencyMS == null ? '暂无有效数据' : r.estimatedLatencyMS.toFixed(1)+' ms'}</p><p>下载速度：{speed == null ? '尚无成功样本' : `${(speed/1024).toFixed(1)} KiB/s${expired ? '（历史样本，已过期）' : ''}`}</p></>}
    <Details title="线路技术详情">{r.operation==='resource'?<>
      <p>{technical}{issuers.length?' · 地址来源：'+issuers.join('；'):''}</p>
      <p>响应延迟：{r.estimatedLatencyMS!=null?'首字节 '+r.estimatedLatencyMS.toFixed(1)+' ms':'暂无有效数据'}{observation}</p>
      <p>吞吐样本：{speed!=null?(speed/1024).toFixed(1)+' KiB/s'+(song?' · 歌曲 #'+song:'')+' · 测速时间 '+date(at)+(duration!=null?' · 下载 '+(duration/1000).toFixed(2)+' s / '+((bytes || 0)/1048576).toFixed(2)+' MiB':'')+(expired?' · 历史样本（已过期）':''):noData}</p>
    </>:<p>{technical}{r.estimatedLatencyMS!=null?' · 首字节 '+r.estimatedLatencyMS.toFixed(1)+' ms':''}{r.estimatedSpeedBPS!=null?' · 样本吞吐 '+(r.estimatedSpeedBPS/1024).toFixed(1)+' KiB/s':''}{dated(r.throughputObservedAt)?' · 测速时间 '+date(r.throughputObservedAt):''}{r.transferDurationMS!=null?' · 下载 '+(r.transferDurationMS/1000).toFixed(2)+' s / '+((r.transferredBytes || 0)/1048576).toFixed(2)+' MiB':''}{observation}</p>}
    </Details></div></ListItem>;
}

export function NetworkMonitor() {
  const app = useApp(), s = app.status.data!, h = s.upstreamMonitor || {}, results = h.results || [];
  const minutes = (h.policy?.throughputInterval || 20*60*1e9)/(60*1e9);
  const waits: Record<string,string> = {closed:'监测已关闭',business_busy:'业务正在加载视频，自动测速已暂停',running:'正在依次测量资源通道',preparing:'正在获取歌曲及资源地址',other_manual_check:'等待当前手动检测结束',not_scheduled:'自动调度尚未启动',cooldown:`等待 ${minutes} 分钟间隔到期`,due:'已到期，等待调度',waiting_sample:'等待有效歌曲及资源地址',persistence_error:'保存测速记录失败，自动测速已延期',local_capacity:'本地连接额度已满，本次未取得新样本',runtime_retired:'代理运行时已退出，本次未取得新样本',channel_unavailable:'通道已失效，本次未取得新样本',canceled:'本次测速已取消，保留此前样本'};
  return <Panel id="network" title="网络检测"><Button disabled={app.unavailable || h.checking} onClick={() => void app.action('upstream/check')}>{h.checking ? '检测中…' : '立即检测'}</Button>
    <p role="status">{h.closed?'网络检测已关闭':h.manual&&h.checking?'手动检测中：'+({catalog:'视频列表',playback:'播放地址解析',latency:'视频资源响应延迟',throughput:'视频资源吞吐速度'}[h.checkKind || ''] || '全部项目'):h.resourcesPaused?'业务正在加载视频，自动资源测速已暂停':h.checking?'正在检测…'+(results.some(r=>r.state==='available')?`已有 ${results.filter(r=>r.state==='available').length} 条线路可用。`:''):!results.length?'等待首次检测…':results.some(r=>r.state==='available')?`已有 ${results.filter(r=>r.state==='available').length} 条检测线路可用；各环节结果见下方。`:'尚无可用线路，请展开下方结果查看原因。'}</p>
    <p>{healthImpact(h,s.activeSettings.downloadUpstream)}</p><p>当前检测范围：{({direct:'全部直连 IP',proxy:'活跃代理出口',socks5:'活跃代理出口',auto:'全部直连 IP 与活跃代理出口'}[s.activeSettings.upstreamMode] || '等待网络配置')}</p>
    <p>最近完成：{date(h.finished)} · 下次检测：{date(h.nextCheck)}</p>
    <Details title="检测说明"><p>后台自动检测歌曲列表、播放地址和视频下载是否可用。视频加载期间暂停自动测速；手动测速会下载少量视频并占用带宽。样本结果不保证全部歌曲都能播放。</p></Details>
    {(['catalog','playback_url','resource'] as const).map(op => <Details key={op} title={operationNames[op]}>
      <div className="actions">{(op === 'catalog' ? [['catalog','检测视频列表']] : op === 'playback_url' ? [['playback','检测播放地址解析']] : [['latency','检测响应延迟'],['throughput','测量下载速度']]).map(([key,label]) => <Button key={key} disabled={app.unavailable || h.manual} onClick={() => void app.action('upstream/check/'+key)}>{label}</Button>)}</div>
      <p>{h.closed?'已关闭':!results.some(r=>r.operation===op)?'等待检测':results.some(r=>r.operation===op&&r.state==='available')?`${results.filter(r=>r.operation===op&&r.state==='available').length} / ${results.filter(r=>r.operation===op).length} 条线路可用`:results.some(r=>r.operation===op&&r.state==='unavailable')?'暂无可用线路':'等待有效检测结果'}</p>{op === 'resource' && <><p>下次自动吞吐测速：{dated(h.nextThroughput)?date(h.nextThroughput):'首次具备样本时'} · {waits[h.throughputStatus || ''] || '等待调度信息'} {h.throughputSaveFailed && ' · 测速记录保存失败，重启可能无法恢复'}</p><p>手动吞吐检测依次测量所有视频资源通道，不受自动检测间隔及业务下载暂停规则限制。</p></>}
      {[...new Set(results.filter(r => r.operation === op).map(r => r.route))].map(route => <section key={route}><Subtitle2 as="h3">{({api:'Udon 歌曲列表',kiva:'Kiva 歌曲与 MD5 清单',wanna:'WannaInfo 歌曲清单',hkg:'node=nya',cf:'node=cf'}[route] || route)}</Subtitle2><List navigationMode="composite" className="records" aria-label={'线路检测：'+route}>{results.filter(r => r.operation === op && r.route === route).map((r,i) => <HealthRow key={`${r.entry}:${r.channelID}:${r.ip}:${i}`} row={r} minutes={minutes} separator={i > 0} wait={waits[h.throughputStatus || '']} />)}</List></section>)}
      {!results.some(r => r.operation === op) && <p>暂无检测结果</p>}
    </Details>)}</Panel>;
}
export function CurrentDownloads({visible,paused=false}: {visible:boolean;paused?:boolean}) {
  const app=useApp(), downloadFocus=useFocusRecovery();
  const downloads=useResource<{tasks:Download[];bytesPerSecond:number}>('downloads',visible&&!paused&&!app.stopped,app.revision);
  return <>    <Panel id="downloads" {...downloadFocus} title="当前下载"><Feedback error>{downloads.error && '活动下载读取失败，将自动重试。'}</Feedback><p role="status">{downloads.data?.tasks.length ? `${downloads.data.tasks.length} 个活动任务` : '暂无活动下载任务'} · 总下载速度 {mb(downloads.data?.bytesPerSecond)}/s</p>
      <List navigationMode="composite" className="records" aria-label="下载任务">{downloads.data?.tasks.map((task,i) => <ListItem key={task.resource+':'+task.id}><div role="gridcell" className="record-content">{i > 0 && <Divider className="record-divider" />}<strong>{task.songs?.[0]?.title || '未知歌名'}</strong><p>{(task.songs?.length || 0)>1 || task.moreSongs ? `等 ${task.moreSongs?'至少 ':''}${task.songs?.length || 0} 首关联曲目 · 播放歌曲未确定` : '资源关联曲目'}</p>
        <p>{({cache_check:'检查本地视频',upstream_headers:'等待上游',download:'下载中',range_download:'分段下载中',hash:'校验完整文件',publish:'发布文件',index:'更新索引'}[task.stage] || '阶段未知')} · {task.host || '域名待定'}</p>
        <p>进度 {task.size>0?Math.min(100,100*task.bytes/task.size).toFixed(1)+'%':'进度未知'} · 上游速度 {mb(task.bytesPerSecond)}/s</p><Progress value={task.bytes} max={task.size} label="已读取字节进度" /><p>{mb(task.bytes)} / {task.size ? mb(task.size) : '未知'} · {mb(task.bytesPerSecond)}/s</p>
        <Details title="下载详情"><p>实际上游域名：{task.host || '尚未确定'}</p><p>{requestTitle(task)}</p><p>资源：{task.resource}</p><p>距上次读取 {(task.idleMS/1000).toFixed(0)} 秒</p></Details></div></ListItem>)}</List>
      <Details title="下载统计说明"><p>速度是最近 5 秒从网络下载视频的估算值。完整视频持久化前需完成合并和校验；视频块缓存可在下载过程中使用。</p></Details></Panel>
</>;
}
export function RecentRequests({visible}: {visible:boolean}) {
  const app=useApp(), [paused,setPaused]=useState(false), [limit,setLimit]=useState(50), requestFocus=useFocusRecovery();
  const requests=useResource<RequestPage>('requests?limit='+limit,visible&&!paused&&!app.stopped,app.revision);
  const groups=useRequestGroups(requests.data);
  useEffect(()=>setLimit(50),[app.status.data!.activeSettings.storageDir]);
  return <><Checkbox label="暂停请求刷新" checked={paused} onChange={(_,v)=>setPaused(!!v.checked)}/>    <Panel id="recent" {...requestFocus} title="最近请求"><p>请求次数不是播放次数</p><Details title="请求分组与传输口径"><p>仅显示已结束的视频请求。请求次数不等于播放次数；同一视频的相邻分段请求会合并展示。传输量是发给客户端的数据量，分组耗时和速度为估算值。</p></Details><Feedback error>{requests.error && '最近请求读取失败，将自动重试。'}</Feedback>
      <p role="status">已显示 {requests.data?.requests.length || 0} 条 HTTP 请求 · 最近 {limit} 条 HTTP 请求窗口 {requests.data?.hasMore && '· 窗口外还有 HTTP 请求，分组可能不完整'}</p>
      {!groups.length&&<p>暂无请求记录</p>}{groups.map(g => { const first = g.requests[0], span = g.end-g.first; return <Details key={`${requests.data?.storageID}:${g.id}`} title={`${first.songs?.map(s => s.title || '未知歌名').join(' / ') || '未知歌名'} · 请求 ×${g.requests.length}`}>
        <p>{requestTitle(first)}</p><p>来源 / 结果：{[...new Set(g.requests.map(requestCache))].map(label=>`${label} ×${g.requests.filter(r=>requestCache(r)===label).length}`).join(' · ')} · {[...new Set(g.requests.map(requestOutcome))].map(label=>`${label} ×${g.requests.filter(r=>requestOutcome(r)===label).length}`).join(' · ')}</p><p>资源标识：{first.resource} · {date(g.last)} · 实际传输 {mb(g.bytes)} · {g.requests.length>1?'估算跨度':'耗时'} {(span/1000).toFixed(3)} 秒 · {g.requests.length>1?'估算平均速度':'平均速度'} {span ? mb(g.bytes/span*1000)+'/s' : '—'}</p>
        <p>{(first.songs?.length || 0)>1 || first.moreSongs ? '共享资源 · 实际播放歌曲未确定' : '资源关联曲目'}</p>
        {((first.songs?.length || 0)>2||first.moreSongs)&&<p>等 {first.moreSongs?'至少 ':''}{first.songs?.length || 0} 首关联曲目 · 全部候选见明细</p>}<ul>{g.requests.map(r => <li key={r.id}>{date(r.at)} · {r.method} {r.range || '完整请求'} · HTTP {r.status || '未返回'} · {requestCache(r)} · {requestOutcome(r)} · {mb(r.bytes)} · {(r.elapsedMS/1000).toFixed(3)} 秒 · {r.elapsedMS?mb(r.bytes/r.elapsedMS*1000)+'/s':'—'} · 资源 {r.resource}</li>)}</ul>
      </Details>; })}
      {requests.data?.hasMore && limit < 500 && <Button disabled={paused || app.unavailable} onClick={() => setLimit(Math.min(500,limit+50))}>加载更早记录（增加 50 条）</Button>}
      {limit === 500 && requests.data?.hasMore && <p>已达 500 条上限。</p>}
    </Panel></>;
}
export function Monitor({visible}: {visible:boolean}) {const app=useApp();return <><RecentRequests visible={visible}/><NetworkMonitor /><Panel title="向导直连诊断"><p role="status">{app.status.data?.setupNetwork?.running?'正在检测…':app.status.data?.setupNetwork?.error}</p><Details title="直连诊断详情"><List navigationMode="composite" aria-label="直连诊断结果">{app.status.data?.setupNetwork?.monitor?.results?.map((r,i)=><HealthRow key={`${r.operation}/${r.route}/${r.channelID}`} row={r} minutes={(app.status.data?.setupNetwork?.monitor?.policy?.throughputInterval || 20*60*1e9)/(60*1e9)} separator={i>0} context={`${operationNames[r.operation] || r.operation} · ${r.route}`} wait={app.status.data?.setupNetwork?.running?'正在测量':'等待重试'} />)}</List></Details><Link href="#/setup">返回使用引导</Link></Panel></>;
}
