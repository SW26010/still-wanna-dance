import { useEffect, useRef, useState } from 'react';
import { Button, Spinner, Tooltip, Text, Caption1, tokens } from '@fluentui/react-components';
import { GaugeChart } from '@fluentui/react-charts';
import { Play20Filled, Stop20Filled } from '@fluentui/react-icons';
import { request } from './api';
import { RecordDetails, Feedback, Panel } from './ui';
import {useApp} from './hooks';
import {date,dated} from './types';

interface EventState { revision: number; run: number; upstream: string; running: boolean; closed?: boolean; phase: string; finished?:string }
interface ConnectionResult {operation:string;entry?:string;state?:string;reason?:string;scheme?:string;latencyMS?:number | null;observedAt?:string}
interface Connectivity extends EventState { initializing?:boolean;nextCheck?:string;results?: ConnectionResult[]; lastResult?: {finished:string;results:ConnectionResult[]} }
interface Measurement {finished?:string;singleSpeedBPS?:number | null;fullSpeedBPS?:number | null;fullConcurrency?:number;fullCPUPercent?:number | null;cpuP95Percent?:number | null;referenceBitrateBPS?:number}
interface Calibration extends EventState, Measurement {
  lastValid?:Measurement; measurementStage?:string; reason?:string; preparationProgress?:number;
  liveSingleSpeedBPS?:number | null; liveFullSpeedBPS?:number | null; liveSpeedBPS?:number | null; open?:number;
}
type Kind = 'connectivity' | 'calibration';
function useDiagnostic<T extends EventState>(kind:Kind, enabled:boolean) {
  const [state,setState] = useState<T>(), [online,setOnline] = useState(false), [message,setMessage] = useState(''), [pending,setPending] = useState(false), [intent,setIntent] = useState<'start'|'cancel'>();
  const latest = useRef(state), currentIntent = useRef<{action:'start'|'cancel';run:number} | undefined>(undefined);
  useEffect(() => {
    if (!enabled) return;
    let stream: EventSource | undefined;
    const disconnect = () => { stream?.close(); stream=undefined; setOnline(false); };
    const connect = () => {
      if (document.hidden) { disconnect(); return; }
      if (stream) return;
      let revision = -1;
      const source = stream = new EventSource(`/api/network/${kind}/events`);
      setMessage('正在连接检测状态…');
      source.addEventListener(kind,event => {
        if (stream !== source) return;
        try {
          const next = JSON.parse((event as MessageEvent).data) as T;
          if (typeof next.revision !== 'number') throw new Error('invalid revision');
          if (next.revision < revision) return;
          revision = next.revision; latest.current = next; setState(next); setOnline(true); setMessage('');
          const wanted = currentIntent.current;
          if (wanted && (wanted.run !== next.run || wanted.action==='start' && next.running || wanted.action==='cancel' && (!next.running || next.phase==='canceling'))) {currentIntent.current=undefined;setIntent(undefined);}
          if (next.closed) { disconnect();setMessage('应用正在关闭。'); }
        } catch {setOnline(false);setMessage('检测状态无法读取，请重新进入网络页。');}
      });
      source.onerror = () => { if (stream===source) {setOnline(false);setMessage('检测状态连接已断开，正在重新连接…');} };
    };
    connect();document.addEventListener('visibilitychange',connect);addEventListener('pagehide',disconnect);addEventListener('pageshow',connect);
    return () => {disconnect();document.removeEventListener('visibilitychange',connect);removeEventListener('pagehide',disconnect);removeEventListener('pageshow',connect);};
  }, [enabled,kind]);
  async function toggle() {
    if (!online || pending || currentIntent.current || state?.closed) return;
    const action = latest.current?.running ? 'cancel' : 'start';
    currentIntent.current={action,run:latest.current?.run || 0};setIntent(action);setPending(true);setMessage('');
    try {await request(`network/${kind}/${action}`, {});}
    catch(e) {currentIntent.current=undefined;setIntent(undefined);setMessage('操作未确认：'+String(e)+'。请根据推送状态核对。');}
    finally {setPending(false);}
  }
  return {state,online,message,pending,intent,toggle};
}
const valid = (value:unknown):value is number => typeof value==='number' && Number.isFinite(value) && value>=0;
const metric = (value?:number|null, factor=1) => valid(value) ? (value*factor).toFixed(2) : '—';
function Speed({state:s,discard}: {state?:Calibration;discard:boolean}) {
  const previous=s?.lastValid;
  const canceled=discard || ['canceling','canceled','failed'].includes(s?.phase || '');
  const complete=s?.phase==='completed' && valid(s.singleSpeedBPS) && valid(s.fullSpeedBPS);
  const result:Measurement = canceled || (!s?.running && !complete) ? previous || (!canceled ? s || {} : {}) : s || {};
  const live=result===s && s.running && !canceled, singleStage=['single_warmup','single'].includes(s?.measurementStage || '');
  const single=live ? s.liveSingleSpeedBPS ?? s.singleSpeedBPS ?? (singleStage?s.liveSpeedBPS:null) : result.singleSpeedBPS;
  const full=live ? s.liveFullSpeedBPS ?? s.fullSpeedBPS ?? (!singleStage?s.liveSpeedBPS:null) : result.fullSpeedBPS;
  const speed=live ? s.liveSpeedBPS ?? (singleStage?single:full) : full;
  const ratio=valid(speed) && (result.referenceBitrateBPS || 0)>0 ? speed*8/result.referenceBitrateBPS! : undefined;
  const usable=valid(ratio) && (result!==s || ['running','completed'].includes(s?.phase || ''));
  const advice=usable?(ratio>2?'可用':ratio>1?'不稳定':'不可用'):'', maximum=Math.max(3,Math.ceil(ratio || 0));
  const stages:Record<string,string>={single_warmup:'单连接预热…',single:'单连接测速…',ramping:'逐渐增加连接…',full_warmup:'多连接预热…',full:'多连接测速…',complete:'正在结束…'};
  const label = canceled ? s?.running?'正在停止…':s?.phase==='failed'?previous?'未完成，保留上次结果':'测速未完成':previous?'已恢复上次结果':'已放弃检测' : s?.phase==='preparing'?'准备在线测速样本…'+(s.preparationProgress?` ${s.preparationProgress}%`:''): s?.running?stages[s.measurementStage || ''] || '测速中…':complete?'测速完成':s?.phase==='completed'?'样本不足':previous?'上次有效结果':'尚未测速';
  return <><p role="status">{label}</p>
    {s?.phase==='failed' && s.reason==='preparation_insufficient' && <Feedback error>在线测速样本不足，请检查连通性后重试。</Feedback>}
    <div className="metrics"><div>单连接速度 <output><Text block size={600} font="numeric">{metric(single,8/1e6)} Mbps</Text></output></div><div>多连接速度<output><Text block size={600} font="numeric">{metric(full,8/1e6)} Mbps</Text></output></div></div>
    <RecordDetails title="测速技术详情"><p>使用 CF 播放地址接口解析视频，分别测量单连接与多连接下载速度。并发连接数：{(live?s.open:result.fullConcurrency) || '—'}。</p><p>CPU 使用率：<span aria-label="多连接稳定采样阶段的平均 CPU 使用率">平均</span> <output>{metric(result.fullCPUPercent)}%</output> · <span aria-label="正式下载全程的 CPU P95，不含视频准备">p95</span> <output>{metric(result.cpuP95Percent)}%</output></p></RecordDetails>
    {usable ? <div role="group" aria-label={`${live?'当前':'多连接'} ${metric(ratio)} 倍，${advice}${live?'（测量中）':''}`}>
      <GaugeChart chartValue={ratio} maxValue={maximum} chartValueFormat={()=>metric(ratio)+'×'} sublabel={advice}
        segments={[
          {legend:'不可用',size:1,color:tokens.colorPaletteRedBackground3,accessibilityData:{ariaLabel:'≤1 倍不可用'}},
          {legend:'不稳定',size:1,color:tokens.colorPaletteYellowBackground3,accessibilityData:{ariaLabel:'1 至 2 倍不稳定'}},
          {legend:'可用',size:maximum-2,color:tokens.colorPaletteGreenBackground3,accessibilityData:{ariaLabel:'>2 倍可用'}},
        ]} />
    </div> : null}
    {usable&&<Caption1 as="p">以 2 Mbps 为 1×</Caption1>}</>;
}
export function Diagnostics({visible}: {visible:boolean}) {
  const app=useApp(),enabled=visible&&!app.stopped;
  const connectivity=useDiagnostic<Connectivity>('connectivity',enabled), calibration=useDiagnostic<Calibration>('calibration',enabled);
  const button=(kind:Kind,label:string) => {
    const p=kind==='connectivity'?connectivity:calibration, other=kind==='connectivity'?calibration:connectivity;
    const running=p.state?.running || p.intent==='start';
    const disabled=!p.online || p.pending || !!p.intent || p.state?.closed || p.state?.phase==='canceling' || other.pending || !!other.intent || other.state?.running;
    const hint=kind==='connectivity'?(running?'停止本次检测，自动检测仍会继续':'立即检测当前上游'):(running?'放弃检测':'使用日常游戏网络环境，暂停其他下载后开始');
    return <Tooltip relationship="description" showDelay={0} content={hint}><Button icon={p.pending?<Spinner size="tiny"/>:running?<Stop20Filled />:<Play20Filled />} aria-label={(running?'停止并放弃':'开始')+label} disabled={disabled} onClick={()=>void p.toggle()}/></Tooltip>;
  };
  const c=connectivity.state, discard=connectivity.intent==='cancel' || ['canceling','canceled'].includes(c?.phase || '');
  const rows=discard?c?.lastResult?.results||[]:c?.results||[];
  const finished=discard?c?.lastResult?.finished:c?.finished;
  const active=app.status.data!.activeSettings;
  const upstream=active.upstreamMode==='direct'?'直连':active.proxyBackend==='mihomo'?'Mihomo':'SOCKS5';
  const names:Record<string,string>={catalog:'歌曲列表',playback_url:'播放地址',resource:'视频下载'};
  const entries:Record<string,string>={api:'Udon',kiva:'Kiva',wanna:'WannaInfo',cf:'CF'};
  const reasons:Record<string,string>={timeout:'请求超时',network_error:'网络请求失败',invalid:'响应校验失败',restricted:'访问受限',upstream_error:'上游错误',origin_timeout:'源站响应超时',http_error:'HTTP 响应异常',local_capacity:'代理连接额度已满',runtime_retired:'代理已退出',channel_unavailable:'连接不可用'};
  const shown=rows.length?rows:[{operation:'catalog'},{operation:'playback_url'},{operation:'resource'}] as ConnectionResult[];
  const speedState=calibration.state, speedRestored=calibration.intent==='cancel'||['canceling','canceled','failed'].includes(speedState?.phase||'');
  const speedFinished=speedState?.finished, savedSpeedFinished=speedState?.lastValid?.finished;
  return <><p className="secondary">检测上游：{upstream}</p>
    <div className="settings-columns"><Panel title="连通性检测" action={button('connectivity','连通性检测')}>
      <p className="secondary">每 5 分钟自动检测，也可手动开始。</p>
      <div><span role="status">{connectivity.intent==='start'?'正在启动…':discard?c?.running?'正在停止…':'已放弃本次检测':''}</span></div>
      <div className="diagnostic-rows">{shown.map(row=><p key={row.operation+'/'+(row.entry||'')}>
        {row.state==='checking'&&<Spinner size="tiny"/>}{names[row.operation]}{row.entry?' · '+(entries[row.entry]||row.entry):''}：
        {({available:'可连接',failed:'连接失败',skipped:row.operation==='catalog'?'未使用（已有可用清单）':'未检测（缺少有效样本）',checking:'检测中'}[row.state||'']||(c?.running&&!discard?'等待检测':'尚未检测'))}
        {row.reason&&reasons[row.reason]?' · '+reasons[row.reason]:''}{valid(row.latencyMS)&&(' · 响应时间 '+Math.round(row.latencyMS)+' ms')}{row.scheme?' · '+row.scheme.toUpperCase():''}
        {dated(row.observedAt)&&<Caption1 block className="secondary">检测时间：<time dateTime={row.observedAt}>{date(row.observedAt)}</time></Caption1>}
      </p>)}</div>
      {dated(finished)&&<p className="secondary">最后测试：{date(finished)}</p>}
      {c?.initializing&&<p className="secondary">正在建立歌曲清单，暂时自动重试清单检测。</p>}
      <Feedback>{connectivity.message}</Feedback>
    </Panel><Panel title="下载速度测试" action={button('calibration','下载速度测试')}>
      <Speed state={speedState} discard={calibration.intent==='cancel'} />
      {dated(speedFinished)&&<p className="secondary">最后测试：{date(speedFinished)}</p>}
      {speedRestored&&dated(savedSpeedFinished)&&savedSpeedFinished!==speedFinished&&<p className="secondary">保留的测速结果：{date(savedSpeedFinished)}</p>}
      <Feedback>{calibration.message}</Feedback>
    </Panel></div></>;

}
