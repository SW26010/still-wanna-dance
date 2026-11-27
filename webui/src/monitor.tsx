import { Button, Subtitle2, Table, TableHeader, TableRow, TableHeaderCell, TableBody, TableCell, TableCellLayout } from '@fluentui/react-components';
import { useState } from 'react';
import { APIError, request, unconfirmedAction } from './api';
import { useApp } from './hooks';
import { Panel, RecordDetails, TableRegion } from './ui';
import { type DownloadRoute, type HealthResult, date, dated } from './types';

const reasons: Record<string,string> = {no_channel:'没有有效候选',no_sample:'等待有效歌曲及资源样本',expired:'观测已过期',timeout:'请求超时',origin_timeout:'源站响应超时（Cloudflare 524）',network_error:'网络请求失败',resolution_unavailable:'无可用资源地址',invalid:'响应校验失败',restricted:'访问受限',upstream_error:'上游错误',http_error:'HTTP 响应异常'};
const localReasons: Record<string,string> = {local_capacity:'本地代理连接额度已满',runtime_retired:'代理运行时已退出',channel_unavailable:'通道已失效',canceled:'本次检测已取消'};
export function healthReason(r:HealthResult) { return reasons[r.reason||'']||localReasons[r.localDiagnostic||'']||'网络请求失败'; }
const operationNames: Record<string,string> = {catalog:'歌曲列表',playback_url:'播放地址解析',resource:'视频资源加载'};
function healthImpact(results: HealthResult[], routes: DownloadRoute[] = []) {
  const summaries = [
    ['catalog', '歌曲清单', '有可访问来源', '更新可能失败'],
    ['playback_url', '播放地址查询', '有可用线路', '获取新播放地址可能失败'],
    ['resource', '视频下载', '有成功样本', '样本检测失败'],
  ].map(([operation, label, success, failure]) => {
    if (operation==='resource') {
      if (!routes.length) return label+'：尚待观测';
      return label+'：'+(routes.some(row=>!row.failed)?success:failure);
    }
    const group = results.filter(r => r.operation === operation);
    const usable = group.filter(r => r.state === 'available' &&
      (!r.validUntil || r.validUntil.startsWith('0001') || new Date(r.validUntil).getTime() > Date.now()));
    if (usable.length) return label + '：' + success + (usable.length < group.length ? '（其余线路异常或待确认）' : '');
    // Unmeasured or expired observations are not evidence of a failure.
    return label + '：' + (group.length && group.every(r => r.state === 'unavailable') ? failure : '尚待确认');
  });
  return summaries.join('；')+'。';
}
function observationDurationMS(row:HealthResult) {
  return row.operation==='catalog' ? row.probeDurationMS : row.estimatedLatencyMS;
}
function compareObservations(a:HealthResult,b:HealthResult) {
  const rank=(r:HealthResult)=>r.state==='available'?0:r.state==='unavailable'?2:1;
  return rank(a)-rank(b) || (observationDurationMS(a)??Infinity)-(observationDurationMS(b)??Infinity) || 0;
}
function HealthRow({row:r,minutes,wait='等待手动检测',context}: {row:HealthResult;minutes:number;wait?:string;context?:string}) {
  const durationMS=observationDurationMS(r);
  const speed = r.estimatedSpeedBPS, at = r.throughputObservedAt;
  const song=r.sampleSongID, duration=r.transferDurationMS, bytes=r.transferredBytes;
  const expired = dated(at) && Date.now() >= new Date(at!).getTime()+minutes*60000;
  const issuers=[...new Set((r.sources || []).map(x=>x.api+' · node='+(x.node || '(默认)')+(x.apiChannelID?' · API 通道 '+x.apiChannelID:'')))];
  const noData=r.state==='unavailable'?'尚无成功样本 · 当前资源检查失败：'+(reasons[r.reason || ''] || r.reason || '未知原因'):['no_sample','resolution_unavailable'].includes(r.reason || '')?'尚无成功样本 · 未取得该域名的有效资源地址':'尚无成功样本 · '+wait;
  const technical=r.entry+(r.operation==='catalog'?' · 清单版本：'+(r.catalogTime || '未获取'):'')+(r.channelID?' · 通道 '+r.channelID:'');
  const observation=(r.sampleSongID?' · 歌曲 #'+r.sampleSongID:'')+(r.http?' · HTTP '+r.http:'')+' · 观测 '+date(r.observedAt)+' · 有效至 '+date(r.validUntil);
  return <TableRow>
    <TableCell><TableCellLayout truncate description={r.mode === 'direct' ? '直连 '+(r.ip || '') : r.mode === 'socks5' ? '代理 '+(r.channelID?.split('/').at(-1)?.slice(-8) || '') : '无候选'}>{context||r.route}</TableCellLayout></TableCell>
    <TableCell>{({available:'可用',unavailable:'不可用',unknown:'待测',stale:'已过期',closed:'已关闭'}[r.state] || r.state)}{r.localDiagnostic&&<span className="secondary"> · {localReasons[r.localDiagnostic]||'本次未完成'}</span>}</TableCell>
    <TableCell>{durationMS==null?'—':durationMS.toFixed(1)+' ms'}</TableCell>
    <TableCell>{speed==null?'—':(speed/1024).toFixed(1)+' KiB/s'}{expired&&<span className="secondary">（已过期）</span>}</TableCell>
    <TableCell><RecordDetails title="线路技术详情" openOnHover><p className="wrap">{technical}{issuers.length?' · 地址来源：'+issuers.join('；'):''}</p><p>{r.reason&&(reasons[r.reason]||r.reason)}{observation}</p>
      {r.operation==='catalog' && r.estimatedLatencyMS!=null && <p>平均首字节延迟：{r.estimatedLatencyMS.toFixed(1)} ms</p>}
      <p>吞吐样本：{speed!=null?(speed/1024).toFixed(1)+' KiB/s'+(song?' · 歌曲 #'+song:'')+' · 测速时间 '+date(at)+(duration!=null?' · 下载 '+(duration/1000).toFixed(2)+' s / '+((bytes || 0)/1048576).toFixed(2)+' MiB':'')+(expired?' · 历史样本（已过期）':''):noData}</p>
      {r.localDiagnostic&&<p>{localReasons[r.localDiagnostic]||'本次检测未完成'} · {date(r.localObservedAt)}</p>}
    </RecordDetails></TableCell>
  </TableRow>;
}
function HealthTable({rows,minutes,operation}:{rows:HealthResult[];minutes:number;operation:string}) {
  const label='线路检测：'+operation;
  const columns=['线路','状态',operation==='catalog'?'完整清单耗时':'首字节延迟','速度','详情'];
  return <TableRegion label={label}><Table size="small" className="health-table" aria-label={label}><TableHeader><TableRow>{columns.map(name=><TableHeaderCell key={name}>{name}</TableHeaderCell>)}</TableRow></TableHeader><TableBody>{rows.map(r=><HealthRow key={`${r.route}:${r.entry}:${r.channelID}:${r.ip}`} row={r} minutes={minutes}/>)}</TableBody></Table></TableRegion>;

}

export function ResourceRoutes({rows,onSample,samplingDisabled}:{rows:DownloadRoute[];onSample?:(row:DownloadRoute)=>void;samplingDisabled?:boolean}) {
  if (!rows.length) return null;
  const columns=['资源主机','线路','最近结果','最近观测','完整 1 MiB 耗时'];
  return <TableRegion label="资源下载线路"><Table size="small" aria-label="资源下载线路">
    <TableHeader><TableRow>{columns.map(label=><TableHeaderCell key={label}>{label}</TableHeaderCell>)}</TableRow></TableHeader>
    <TableBody>{rows.map(row=><TableRow key={row.host+'/'+(row.ip||'proxy')}>
      <TableCell>{row.host}</TableCell><TableCell className="wrap">{row.ip||'当前代理'}
        {onSample&&row.ip&&<Button size="small" appearance="subtle" aria-label={`采样 ${row.host} ${row.ip}`} disabled={samplingDisabled} onClick={()=>onSample(row)}>采样</Button>}</TableCell>
      <TableCell>{row.source==='sample'?'采样':'下载'}{row.failed?'失败':'成功'}</TableCell>
      <TableCell>{date(row.observedAt)}</TableCell>
      <TableCell>{row.blockDurationNs ? <TableCellLayout description={date(row.blockAt)}>{(row.blockDurationNs/1e9).toFixed(2)} 秒</TableCellLayout> : '—'}</TableCell>
    </TableRow>)}</TableBody>
  </Table></TableRegion>;
}

export function NetworkMonitor() {
  const app = useApp(), s = app.status.data!, h = s.upstreamMonitor || {};
  const results = h.results || [];
  const observed = results.filter(r=>dated(r.observedAt) || dated(r.localObservedAt));
  const [sampling,setSampling]=useState(false), [sampleStatus,setSampleStatus]=useState('');
  async function sample(row?:DownloadRoute) {
    if (sampling || app.unavailable) return;
    setSampling(true);
    setSampleStatus(row ? `正在采样 ${row.ip}…` : '正在采样…');
    try {
      await request('upstream/check/throughput',row ? {host:row.host,ip:row.ip} : {},AbortSignal.timeout(30_000));
      setSampleStatus(row ? `${row.ip} 采样已完成。` : '资源采样已完成。');
    } catch (error) {
      setSampleStatus(error instanceof APIError ? error.message : unconfirmedAction);
    } finally {
      setSampling(false);
      app.refresh();
    }
  }
  const minutes = (h.policy?.throughputInterval || 20*60*1e9)/(60*1e9);
  const schedule=[['最近完成',h.finished],['下次检测',h.nextCheck]].filter(([,at])=>dated(at)).map(([label,at])=>`${label}：${date(at)}`).join(' · ');
  return <Panel id="network" title="当前连接" action={<Button disabled={app.unavailable || h.checking} onClick={() => void app.action('upstream/check')}>{h.checking?'检测中…':'检查当前连接'}</Button>}>
    <p role="status">{h.closed?'网络检测已关闭':h.checking?'正在检测…':observed.length||s.downloadRoutes?.length?healthImpact(results,s.downloadRoutes):''}</p>
    {schedule&&<span className="secondary">{schedule}</span>}
    {(['catalog','playback_url'] as const).map(op=>{
      const rows=observed.filter(r=>r.operation===op).sort(compareObservations);
      return <section key={op} className="health-section">
      <div className="toolbar spread"><div className="inline"><Subtitle2 as="h3">{operationNames[op]}</Subtitle2>{!rows.length&&<span className="secondary">未检测</span>}</div>
        <Button size="small" disabled={app.unavailable||h.manual} onClick={()=>void app.action(op==='catalog'?'upstream/check/catalog':'upstream/check/playback')}>检测{operationNames[op]}</Button></div>
      {!!rows.length&&<HealthTable rows={rows} minutes={minutes} operation={op}/>}
    </section>;})}
    <section className="health-section">
      <div className="toolbar spread"><Subtitle2 as="h3">{operationNames.resource}</Subtitle2>
        <Button size="small" disabled={app.unavailable||sampling} onClick={()=>void sample()}>立即采样</Button></div>
      {sampleStatus&&<p role="status">{sampleStatus}</p>}
      {s.downloadRoutes?.length ? <ResourceRoutes rows={s.downloadRoutes} onSample={sample} samplingDisabled={app.unavailable||sampling}/> : <span className="secondary">暂无下载或采样记录</span>}
    </section>
  </Panel>;

}
