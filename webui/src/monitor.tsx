import { Button, Link, Subtitle2, Text, Table, TableHeader, TableRow, TableHeaderCell, TableBody, TableCell, TableCellLayout } from '@fluentui/react-components';
import { useApp, useResource, useFocusRecovery } from './hooks';
import { Panel, Progress, Hint, RecordDetails, TableRegion } from './ui';
import { type Download, type RequestRecord, type HealthResult, type Health, date, mb, dated } from './types';
import { requestTitle } from './requests';

const reasons: Record<string,string> = {no_channel:'没有有效候选',no_sample:'等待有效歌曲及资源样本',expired:'观测已过期',timeout:'请求超时',origin_timeout:'源站响应超时（Cloudflare 524）',network_error:'网络请求失败',resolution_unavailable:'无可用资源地址',invalid:'响应校验失败',restricted:'访问受限',upstream_error:'上游错误',http_error:'HTTP 响应异常'};
const localReasons: Record<string,string> = {local_capacity:'本地代理连接额度已满',runtime_retired:'代理运行时已退出',channel_unavailable:'通道已失效',canceled:'本次检测已取消'};
const operationNames: Record<string,string> = {catalog:'歌曲列表',playback_url:'播放地址解析',resource:'视频资源加载'};
export function healthImpact(h: Health) {
  if (h.closed) return '检测已关闭，当前无法判断网络可用性。';
  const results = h.results || [];
  const summaries = [
    ['catalog', '歌曲清单', '有可访问来源', '更新可能失败'],
    ['playback_url', '播放地址查询', '有可用线路', '获取新播放地址可能失败'],
    ['resource', '视频下载', '有成功样本', '样本检测失败'],
  ].map(([operation, label, success, failure]) => {
    const group = results.filter(r => r.operation === operation);
    const usable = group.filter(r => r.state === 'available' &&
      (!r.validUntil || r.validUntil.startsWith('0001') || new Date(r.validUntil).getTime() > Date.now()));
    if (usable.length) return label + '：' + success + (usable.length < group.length ? '（其余线路异常或待确认）' : '');
    // Unmeasured or expired observations are not evidence of a failure.
    return label + '：' + (group.length && group.every(r => r.state === 'unavailable') ? failure : '尚待确认');
  });
  return summaries.join('；')+'。';
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
  return <TableRow>
    <TableCell><TableCellLayout truncate description={r.mode === 'direct' ? '直连 '+(r.ip || '') : r.mode === 'socks5' ? '代理 '+(r.channelID?.split('/').at(-1)?.slice(-8) || '') : '无候选'}>{context||r.route}</TableCellLayout></TableCell>
    <TableCell>{({available:'可用',unavailable:'不可用',unknown:'待测',stale:'已过期',closed:'已关闭'}[r.state] || r.state)}{r.localDiagnostic&&<span className="secondary"> · {localReasons[r.localDiagnostic]||'本次未完成'}</span>}</TableCell>
    <TableCell>{r.estimatedLatencyMS==null?'—':r.estimatedLatencyMS.toFixed(1)+' ms'}</TableCell>
    <TableCell>{speed==null?'—':(speed/1024).toFixed(1)+' KiB/s'}{expired&&<span className="secondary">（已过期）</span>}</TableCell>
    <TableCell><RecordDetails title="线路技术详情"><p className="wrap">{technical}{issuers.length?' · 地址来源：'+issuers.join('；'):''}</p><p>{r.reason&&(reasons[r.reason]||r.reason)}{observation}</p>
      <p>吞吐样本：{speed!=null?(speed/1024).toFixed(1)+' KiB/s'+(song?' · 歌曲 #'+song:'')+' · 测速时间 '+date(at)+(duration!=null?' · 下载 '+(duration/1000).toFixed(2)+' s / '+((bytes || 0)/1048576).toFixed(2)+' MiB':'')+(expired?' · 历史样本（已过期）':''):noData}</p>
      {r.localDiagnostic&&<p>{localReasons[r.localDiagnostic]||'本次检测未完成'} · {date(r.localObservedAt)}；保留此前观测，未计为节点失败。</p>}
    </RecordDetails></TableCell>
  </TableRow>;
}
function HealthTable({rows,minutes,wait,label}:{rows:HealthResult[];minutes:number;wait?:string;label:string}) {
  return <TableRegion label={label}><Table size="small" className="health-table" aria-label={label}><TableHeader><TableRow>{['线路','状态','延迟','速度','详情'].map(name=><TableHeaderCell key={name}>{name}</TableHeaderCell>)}</TableRow></TableHeader><TableBody>{rows.map((r,i)=><HealthRow key={`${r.entry}:${r.channelID}:${r.ip}:${i}`} row={r} minutes={minutes} separator={false} wait={wait}/>)}</TableBody></Table></TableRegion>;

}

export function NetworkMonitor() {
  const app = useApp(), s = app.status.data!, h = s.upstreamMonitor || {}, results = h.results || [];
  const minutes = (h.policy?.throughputInterval || 20*60*1e9)/(60*1e9);
  const schedule=[['最近完成',h.finished],['下次检测',h.nextCheck]].filter(([,at])=>dated(at)).map(([label,at])=>`${label}：${date(at)}`).join(' · ');
  const waits: Record<string,string> = {closed:'监测已关闭',business_busy:'业务正在加载视频，自动测速已暂停',running:'正在依次测量资源通道',preparing:'正在获取歌曲及资源地址',other_manual_check:'等待当前手动检测结束',not_scheduled:'自动调度尚未启动',cooldown:`等待 ${minutes} 分钟间隔到期`,due:'已到期，等待调度',waiting_sample:'等待有效歌曲及资源地址',persistence_error:'保存测速记录失败，自动测速已延期',local_capacity:'本地连接额度已满，本次未取得新样本',runtime_retired:'代理运行时已退出，本次未取得新样本',channel_unavailable:'通道已失效，本次未取得新样本',canceled:'本次测速已取消，保留此前样本'};
  return <Panel id="network" title="当前连接" action={<Button disabled={app.unavailable || h.checking} onClick={() => void app.action('upstream/check')}>{h.checking?'检测中…':'检查当前连接'}</Button>}>
    <p role="status">{h.closed?'网络检测已关闭':h.checking?'正在检测…':results.length?healthImpact(h):''}</p>
    {schedule&&<span className="secondary">{schedule}</span>}
    {(['catalog','playback_url','resource'] as const).map(op=>{
      const rows=results.filter(r=>r.operation===op);
      return <section key={op} className="health-section">
      <div className="toolbar spread"><div className="inline"><Subtitle2 as="h3">{operationNames[op]}</Subtitle2>{!rows.length&&<span className="secondary">未检测</span>}</div><div className="inline">{(op==='catalog'?[['catalog','检测视频列表']]:op==='playback_url'?[['playback','检测播放地址解析']]:[['latency','检测响应延迟'],['throughput','测量下载速度']]).map(([key,label])=><Button size="small" key={key} disabled={app.unavailable||h.manual} onClick={()=>void app.action('upstream/check/'+key)}>{label}</Button>)}</div></div>
      {!!rows.length&&<HealthTable rows={rows} minutes={minutes} wait={waits[h.throughputStatus||'']} label={'线路检测：'+op}/>}
      {op==='resource'&&(h.throughputStatus||dated(h.nextThroughput)||h.throughputSaveFailed)&&<span className="secondary">{[waits[h.throughputStatus||''],dated(h.nextThroughput)?'下次测速：'+date(h.nextThroughput):'',h.throughputSaveFailed?'记录保存失败':''].filter(Boolean).join(' · ')}</span>}
    </section>;})}
  </Panel>;

}
export function CurrentDownloads({visible,paused=false}: {visible:boolean;paused?:boolean}) {
  const app=useApp(), downloadFocus=useFocusRecovery(), requestFocus=useFocusRecovery();
  const downloads=useResource<{tasks:Download[];bytesPerSecond:number;gets?:{total:number;requests:(Pick<RequestRecord,'id'|'at'|'songID'|'resource'|'range'|'status'|'bytes'|'responseBytes'>&{origin?:'preview'})[]}}>('downloads',visible&&!paused&&!app.stopped,app.revision,5000,true);
  return <section className="video-activity" aria-label="视频活动">
    <div className="toolbar spread"><h2>视频活动</h2><div className="inline"><Text size={600} weight="semibold">{downloads.data?mb(downloads.data.bytesPerSecond)+'/s':'—'}</Text><Hint label="下载速度说明">最近 5 秒的网络下载速度。</Hint></div></div>
    <div id="current-gets" {...requestFocus} tabIndex={-1}><div className="toolbar spread"><h3>当前请求</h3><span className="secondary" role="status">{downloads.error?'读取失败，正在重试':downloads.data?.gets?.total?`${downloads.data.gets.total} 个请求`:'暂无当前 GET 请求'}</span></div>
      <TableRegion label="当前请求表格"><Table size="small" className="activity-table" aria-label="当前 GET 请求列表"><TableHeader><TableRow><TableHeaderCell>请求歌曲</TableHeaderCell><TableHeaderCell>状态</TableHeaderCell><TableHeaderCell>本次交付</TableHeaderCell><TableHeaderCell style={{width:60}}>详情</TableHeaderCell></TableRow></TableHeader><TableBody>{downloads.data?.gets?.requests.map(r=><TableRow key={r.id}>
        <TableCell>{r.origin==='preview'?'曲库预览':r.songID?<Link href={'#/library?song='+encodeURIComponent(r.songID)}>ID {r.songID}</Link>:'未知'}</TableCell><TableCell>{r.status?'HTTP '+r.status:'准备中'}</TableCell><TableCell>{r.responseBytes!=null&&r.responseBytes>0?(100*r.bytes/r.responseBytes).toLocaleString(undefined,{maximumFractionDigits:1})+'%':'—'}</TableCell>
        <TableCell><RecordDetails title={`当前请求 ${r.id} 的范围`}><dl className="detail-grid"><dt>收到</dt><dd>{date(r.at)}</dd><dt>资源</dt><dd>{r.resource?<Link href={'#/library?resource='+encodeURIComponent(r.resource)}>{r.resource}</Link>:'尚未确定'}</dd><dt>范围</dt><dd>{r.range||'完整请求'}</dd><dt>已交付</dt><dd>{r.bytes.toLocaleString()} B{r.responseBytes!=null?' / '+r.responseBytes.toLocaleString()+' B':''}</dd></dl></RecordDetails></TableCell>
      </TableRow>)}</TableBody></Table></TableRegion>{(downloads.data?.gets?.total||0)>50&&<span className="secondary">显示最早的 50 个请求</span>}
    </div>
    <div id="downloads" {...downloadFocus} tabIndex={-1}><div className="toolbar spread"><h3>当前下载</h3><span className="secondary">{downloads.data?.tasks.length?`${downloads.data.tasks.length} 个任务`:'暂无活动下载任务'}</span></div>
      <TableRegion label="下载任务表格"><Table size="small" className="activity-table" aria-label="下载任务"><TableHeader><TableRow><TableHeaderCell>关联歌曲</TableHeaderCell><TableHeaderCell>进度</TableHeaderCell><TableHeaderCell>速度</TableHeaderCell><TableHeaderCell style={{width:60}}>详情</TableHeaderCell></TableRow></TableHeader><TableBody>{downloads.data?.tasks.map(task=><TableRow key={task.resource+':'+task.id}>
        <TableCell><TableCellLayout truncate description={({cache_check:'检查本地',upstream_headers:'等待上游',download:'下载中',range_download:'下载中',hash:'校验中',publish:'保存中',index:'保存中'}[task.stage]||'准备中')}>{task.songs?.[0]?.title||'未知歌名'}{((task.songs?.length||0)>1||task.moreSongs)&&' 等'}</TableCellLayout></TableCell>
        <TableCell><span>{task.size>0?Math.min(100,100*task.bytes/task.size).toFixed(1)+'%':'未知'}</span><Progress value={task.bytes} max={task.size} label="已读取字节进度"/></TableCell><TableCell>{mb(task.bytesPerSecond)}/s</TableCell>
        <TableCell><RecordDetails title="下载详情"><p>{requestTitle(task)}</p><dl className="detail-grid"><dt>资源</dt><dd>{task.resource}</dd><dt>上游域名</dt><dd>{task.host||'尚未确定'}</dd><dt>已读取</dt><dd>{mb(task.bytes)} / {task.size?mb(task.size):'未知'}</dd><dt>距上次读取</dt><dd>{(task.idleMS/1000).toFixed(0)} 秒</dd></dl></RecordDetails></TableCell>
      </TableRow>)}</TableBody></Table></TableRegion>
    </div>
  </section>;
}
