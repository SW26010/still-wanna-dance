import { useEffect, useRef, useState } from 'react';
import { Button, Field, Select, Spinner, Tooltip, Text, Caption1, tokens } from '@fluentui/react-components';
import { GaugeChart } from '@fluentui/react-charts';
import { Play20Filled, Stop20Filled } from '@fluentui/react-icons';
import { request } from './api';
import { Feedback, Panel } from './ui';

interface EventState { revision: number; run: number; upstream: string; running: boolean; closed?: boolean; phase: string }
interface ConnectionResult {operation:string;state:string;latencyMS?:number | null}
interface Connectivity extends EventState { results?: ConnectionResult[]; lastValid?: Record<string,ConnectionResult[]> }
interface Measurement {singleSpeedBPS?:number | null;fullSpeedBPS?:number | null;fullConcurrency?:number;fullCPUPercent?:number | null;cpuP99Percent?:number | null;referenceBitrateBPS?:number}
interface Calibration extends EventState, Measurement {
  lastValid?:Record<string,Measurement>; measurementStage?:string; reason?:string; preparationProgress?:number;
  liveSingleSpeedBPS?:number | null; liveFullSpeedBPS?:number | null; liveSpeedBPS?:number | null; open?:number;
}
type Kind = 'connectivity' | 'calibration';
function useDiagnostic<T extends EventState>(kind:Kind, enabled:boolean, onUpstream:(v:string)=>void) {
  const [state,setState] = useState<T>(), [online,setOnline] = useState(false), [message,setMessage] = useState(''), [pending,setPending] = useState(false), [intent,setIntent] = useState<'start'|'cancel'>();
  const latest = useRef(state), currentIntent = useRef<{action:'start'|'cancel';run:number} | undefined>(undefined);
  const upstreamCallback = useRef(onUpstream); upstreamCallback.current = onUpstream;
  useEffect(() => {
    if (!enabled) return;
    let stream: EventSource | undefined;
    const disconnect = () => { stream?.close(); stream=undefined; setOnline(false); };
    const connect = () => {
      if (document.hidden) { disconnect(); return; }
      if (stream) return;
      let revision = -1;
      const source = stream = new EventSource(`/api/setup/network/${kind}/events`);
      setMessage('正在连接检测状态…');
      source.addEventListener(kind,event => {
        if (stream !== source) return;
        try {
          const next = JSON.parse((event as MessageEvent).data) as T;
          if (typeof next.revision !== 'number') throw new Error('invalid revision');
          if (next.revision < revision) return;
          revision = next.revision; latest.current = next; setState(next); setOnline(true); setMessage('');
          if (next.running && next.upstream) upstreamCallback.current(next.upstream);
          const wanted = currentIntent.current;
          if (wanted && (wanted.run !== next.run || wanted.action==='cancel' && (!next.running || next.phase==='canceling'))) {currentIntent.current=undefined;setIntent(undefined);}
          if (next.closed) { disconnect();setMessage('应用正在关闭。'); }
        } catch {setOnline(false);setMessage('检测状态无法读取，请重新进入网络页。');}
      });
      source.onerror = () => { if (stream===source) {setOnline(false);setMessage('检测状态连接已断开，正在重新连接…');} };
    };
    connect();document.addEventListener('visibilitychange',connect);addEventListener('pagehide',disconnect);addEventListener('pageshow',connect);
    return () => {disconnect();document.removeEventListener('visibilitychange',connect);removeEventListener('pagehide',disconnect);removeEventListener('pageshow',connect);};
  }, [enabled,kind]);
  async function toggle(upstream:string, proxy:unknown) {
    if (!online || pending || currentIntent.current || state?.closed) return;
    const action = latest.current?.running ? 'cancel' : 'start';
    currentIntent.current={action,run:latest.current?.run || 0};setIntent(action);setPending(true);setMessage('');
    try {await request(`setup/network/${kind}/${action}`, action==='cancel' ? {} : {upstream,...(upstream==='proxy'?{proxy}:{})});}
    catch(e) {currentIntent.current=undefined;setIntent(undefined);setMessage('操作未确认：'+String(e)+'。请根据推送状态核对。');}
    finally {setPending(false);}
  }
  return {state,online,message,pending,intent,toggle};
}
const valid = (value:unknown):value is number => typeof value==='number' && Number.isFinite(value) && value>=0;
const metric = (value?:number|null, factor=1) => valid(value) ? (value*factor).toFixed(2) : '—';
function Speed({state:s,upstream,discard}: {state?:Calibration;upstream:string;discard:boolean}) {
  const previous=s?.lastValid?.[upstream], matches=!s?.run || s.upstream===upstream;
  const canceled=discard || ['canceling','canceled','failed'].includes(s?.phase || '');
  const complete=s?.phase==='completed' && valid(s.singleSpeedBPS) && valid(s.fullSpeedBPS);
  const result:Measurement = canceled || !matches || (!s?.running && !complete) ? previous || (matches && !canceled ? s || {} : {}) : s || {};
  const live=result===s && s.running && !canceled, singleStage=['single_warmup','single'].includes(s?.measurementStage || '');
  const single=live ? s.liveSingleSpeedBPS ?? s.singleSpeedBPS ?? (singleStage?s.liveSpeedBPS:null) : result.singleSpeedBPS;
  const full=live ? s.liveFullSpeedBPS ?? s.fullSpeedBPS ?? (!singleStage?s.liveSpeedBPS:null) : result.fullSpeedBPS;
  const speed=live ? s.liveSpeedBPS ?? (singleStage?single:full) : full;
  const ratio=valid(speed) && (result.referenceBitrateBPS || 0)>0 ? speed*8/result.referenceBitrateBPS! : undefined;
  const usable=valid(ratio) && (result!==s || ['running','completed'].includes(s?.phase || ''));
  const advice=usable?(ratio>2?'可用':ratio>1?'不稳定':'不可用'):'', maximum=Math.max(3,Math.ceil(ratio || 0));
  const stages:Record<string,string>={single_warmup:'单路预热…',single:'单路测速…',ramping:'逐渐增加并发…',full_warmup:'满路预热…',full:'满路测速…',complete:'正在结束…'};
  const label = !matches ? previous?'上次有效结果':'尚未测速' : canceled ? s?.running?'正在停止…':s?.phase==='failed'?previous?'未完成，保留上次结果':'测速未完成':previous?'已恢复上次结果':'已放弃检测' : s?.phase==='preparing'?'准备视频…'+(s.preparationProgress?` ${s.preparationProgress}%`:''): s?.running?stages[s.measurementStage || ''] || '测速中…':complete?'测速完成':s?.phase==='completed'?'样本不足':previous?'上次有效结果':'尚未测速';
  return <><p role="status">{label}{s?.reason==='preparation_insufficient' && ' · 视频准备不足，请重试'}</p>
    <div className="metrics"><div>单路速度 <output><Text block size={600} font="numeric">{metric(single,8/1e6)} Mbps</Text></output></div><div>满路速度（{(live?s.open:result.fullConcurrency) || '—'} 并发）<output><Text block size={600} font="numeric">{metric(full,8/1e6)} Mbps</Text></output></div></div>
    <p>CPU 使用率：<span aria-label="满路稳定采样阶段的平均 CPU 使用率">平均</span> <output>{metric(result.fullCPUPercent)}%</output> · <span aria-label="正式下载全程的 CPU P99，不含视频准备">p99</span> <output>{metric(result.cpuP99Percent)}%</output></p>
    {usable ? <div role="group" aria-label={`${live?'当前':'满路'} ${metric(ratio)} 倍，${advice}${live?'（测量中）':''}`}>
      <GaugeChart chartValue={ratio} maxValue={maximum} chartValueFormat={()=>metric(ratio)+'×'} sublabel={advice}
        segments={[
          {legend:'不可用',size:1,color:tokens.colorPaletteRedBackground3,accessibilityData:{ariaLabel:'≤1 倍不可用'}},
          {legend:'不稳定',size:1,color:tokens.colorPaletteYellowBackground3,accessibilityData:{ariaLabel:'1 至 2 倍不稳定'}},
          {legend:'可用',size:maximum-2,color:tokens.colorPaletteGreenBackground3,accessibilityData:{ariaLabel:'>2 倍可用'}},
        ]} />
    </div> : <p>尚无速度样本</p>}
    <Caption1 as="p">以 2 Mbps 为 1×；≤1 倍不可用，1 至 2 倍不稳定，&gt;2 倍可用</Caption1></>;
}
export function Diagnostics({visible,proxy}: {visible:boolean;proxy:unknown}) {
  const [upstream,setUpstream]=useState('direct');
  const connectivity=useDiagnostic<Connectivity>('connectivity',visible,setUpstream), calibration=useDiagnostic<Calibration>('calibration',visible,setUpstream);
  const panels=[connectivity,calibration];
  const busy=panels.some(p=>!p.online || p.pending || p.intent || p.state?.running || p.state?.closed);
  const button=(kind:Kind,label:string) => {
    const p=kind==='connectivity'?connectivity:calibration, other=kind==='connectivity'?calibration:connectivity;
    const running=p.state?.running || p.intent==='start';
    const disabled=!p.online || p.pending || !!p.intent || p.state?.closed || p.state?.phase==='canceling' || other.pending || !!other.intent || other.state?.running;
    return <Tooltip relationship="description" showDelay={0} content={running?'放弃检测':'请先准备好日常游戏的网络环境，保持常用加速器、代理和 TUN 设置，并暂停其他下载，再手动开始'}><Button icon={p.pending?<Spinner size="tiny"/>:running?<Stop20Filled />:<Play20Filled />} aria-label={(running?'停止并放弃':'开始')+label} disabled={disabled} onClick={()=>void p.toggle(upstream,proxy)}/></Tooltip>;
  };
  const c=connectivity.state, discard=connectivity.intent==='cancel' || ['canceling','canceled'].includes(c?.phase || '');
  const matching=!c?.run || c.upstream===upstream;
  const rows=matching&&!discard ? c?.results || [] : c?.lastValid?.[upstream] || [];
  return <><Field label="检测上游"><Select value={upstream} disabled={busy} onChange={(_,v)=>setUpstream(v.value)}><option value="direct">直连</option><option value="proxy">代理（SOCKS5）</option></Select></Field>
    <Panel title="连通性检测" action={button('connectivity','连通性检测')}><div><span role="status">{connectivity.intent==='start'?'正在启动…':discard?c?.running?'正在停止…':'已恢复上次结果':''}</span></div>
      <div className="diagnostic-rows">{[['catalog','歌曲列表'],['playback_url','播放地址'],['resource','视频下载']].map(([op,label])=>{const row=rows.find(r=>r.operation===op);return <p key={op}>{row?.state==='checking'&&<Spinner size="tiny"/>}{label}：{({available:'可连接',failed:'连接失败',skipped:'未检测（前项未通过）',checking:'检测中'}[row?.state || ''] || (c?.running&&matching&&!discard?'等待检测':'尚未检测'))}{valid(row?.latencyMS)&&` · 响应 ${Math.round(row.latencyMS)} ms`}</p>;})}</div><Feedback>{connectivity.message}</Feedback>
    </Panel><Panel title="CF 并发测速" action={button('calibration','CF 并发测速')}><Speed state={calibration.state} upstream={upstream} discard={calibration.intent==='cancel'} /><Feedback>{calibration.message}</Feedback></Panel></>;
}
