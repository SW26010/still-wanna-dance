import {useEffect,useLayoutEffect,useRef,useState} from 'react';
import {Badge,Button,Checkbox,Field,Input,Link,Spinner,Text,Table,TableHeader,TableRow,TableHeaderCell,TableBody,TableCell,TableCellLayout} from '@fluentui/react-components';
import {useApp,useFocusRecovery,useResource} from './hooks';
import {Hint,Notification,RecordDetails,TableRegion} from './ui';
import {date,mb,type RequestRecord} from './types';
import {requestOutcome} from './requests';
import {songHref,resourceHref} from './library';

type Group={samples:number;p50MS:number|null;p95MS:number|null};
type Latency={from:number;to:number;availableFrom:number|null;partial:boolean;blocking:Group;ready:Group};
type History={storageID:string;requests:RequestRecord[];hasMore:boolean;nextCursor:string};
type Range={from:string;to:string};
const localDate=(value:number)=>{const d=new Date(value);return new Date(value-d.getTimezoneOffset()*60000).toISOString().slice(0,16);};
const milliseconds=(value:string)=>value?new Date(value).getTime():undefined;
const deliveredBytes=(value:number)=>value<1e6?`${value.toLocaleString()} B`:mb(value);
const ms=(value:number|null)=>value==null?'—':`${value.toLocaleString(undefined,{maximumFractionDigits:1})} ms`;

export function RequestTimeline({visible}:{visible:boolean}) {
  const app=useApp(),storage=app.status.data!.activeSettings.databasePath;
  const [range,setRange]=useState<Range>(()=>({from:localDate(Date.now()-24*3600000),to:''}));
  const [applied,setApplied]=useState(range),[cursors,setCursors]=useState(['']),[error,setError]=useState(''),[paused,setPaused]=useState(false),[queryRevision,setQueryRevision]=useState(0);
  const previousStorage=useRef(storage);
  useEffect(()=>{if(previousStorage.current!==storage){previousStorage.current=storage;setCursors(['']);}},[storage]);
  const latency=useResource<Latency>('latency',visible&&!app.stopped,app.revision,30000);
  const query=new URLSearchParams({limit:'50',cursor:cursors.at(-1)!});
  for(const key of ['from','to'] as const){const value=milliseconds(applied[key]);if(value!==undefined)query.set(key,String(value));}
  return <div className="page-stack">
    <section className="latency-summary" aria-label="首正文等待时间">
      <div className="toolbar spread"><h2>首正文等待时间 <Hint label="首正文统计说明">收到请求至写出首个视频字节。P50 为中位数，P95 覆盖 95% 的样本；不等于游戏起播时间。</Hint></h2><div className="inline"><span className="secondary">最近 72 小时{latency.data?.partial?' · 数据不足 72 小时':''}</span><Button size="small" disabled={(latency.loading&&!latency.data)||app.unavailable} onClick={latency.refresh}>刷新首正文统计</Button></div></div>
      <Notification message={visible&&latency.error?'首正文统计读取失败，请重试。':undefined} error/>
      <div className="metric-grid">{([['等待下载',latency.data?.blocking,'首字节前等待过缺失的视频块。'],['缓存／本地',latency.data?.ready,'首字节前直接读取可用的视频块或完整视频。']] as const).map(([name,g,hint])=><div key={name} className="latency-metric"><div>{name}<Hint label={name+'说明'}>{hint}</Hint></div><Text size={500} weight="semibold">{!g?latency.error?'读取失败':'正在读取…':g.samples?`P50 ${ms(g.p50MS)} · P95 ${ms(g.p95MS)}`:'暂无数据'}</Text><span className="secondary">有效样本 {g?.samples??'—'} 个</span></div>)}</div>
    </section><section id="recent" className="history-section" tabIndex={-1}>
    <form className="form-grid history-filter" onSubmit={e=>{e.preventDefault();const from=milliseconds(range.from),to=milliseconds(range.to);if((from!==undefined&&!Number.isFinite(from))||(to!==undefined&&!Number.isFinite(to))||(from!==undefined&&to!==undefined&&to<=from)){setError('结束时间必须晚于开始时间。');return;}setError('');setApplied({...range});setCursors(['']);setQueryRevision(v=>v+1);}}>
      <div className="history-range">
      <Field label="开始时间（含）"><Input type="datetime-local" value={range.from} onChange={(_,v)=>{setRange({...range,from:v.value});setError('');}}/></Field>
      <Field label="结束时间（不含）" validationState={error?'error':'none'} validationMessage={error||undefined}><Input type="datetime-local" value={range.to} onChange={(_,v)=>{setRange({...range,to:v.value});setError('');}}/></Field>
      </div><div className="inline"><Button type="submit" disabled={app.unavailable||paused}>查询历史</Button><Button disabled={app.unavailable||paused} onClick={()=>{const all={from:'',to:''};setRange(all);setApplied(all);setCursors(['']);setQueryRevision(v=>v+1);setError('');}}>全部保留期</Button></div>
    </form>
    <Checkbox label="暂停请求刷新" checked={paused} onChange={(_,v)=>setPaused(!!v.checked)}/><HistoryPage paused={paused} focusOnMount={queryRevision>0} key={storage+':'+query+':'+queryRevision} query={query.toString()} visible={visible} pageNumber={cursors.length} live={cursors.length===1&&!applied.to} previous={()=>{setCursors(v=>v.slice(0,-1));setQueryRevision(v=>v+1);}} next={cursor=>{setCursors(v=>[...v,cursor]);setQueryRevision(v=>v+1);}}/>
  </section></div>;
}

function HistoryPage({query,visible,pageNumber,live,paused,focusOnMount,previous,next}:{focusOnMount:boolean;query:string;visible:boolean;paused:boolean;pageNumber:number;live:boolean;previous:()=>void;next:(cursor:string)=>void}) {
  const app=useApp(),focus=useFocusRecovery();
  const history=useResource<History>('history?'+query,visible&&!paused&&!app.stopped,app.revision,live?5000:0);
  useLayoutEffect(()=>{if(focusOnMount&&visible)focus.ref.current?.focus({preventScroll:true});},[]);
  const disabled=app.unavailable||paused||(history.loading&&!history.data);
  return <div {...focus} tabIndex={-1} aria-label="历史查询结果" className="content-stack">
    <Notification message={visible&&history.error?'历史读取失败，请重试。':undefined} error/>
    <div className="toolbar"><Button disabled={disabled} onClick={history.refresh}>刷新本页</Button><Button disabled={disabled||pageNumber===1} onClick={previous}>上一页历史</Button><Button disabled={disabled||!history.data?.hasMore||!history.data.nextCursor} onClick={()=>next(history.data!.nextCursor)}>下一页历史</Button><span role="status">第 {pageNumber} 页 · {history.data?.requests.length??0} 条{live&&!paused?' · 自动刷新':''}</span></div>
    {history.loading&&!history.data&&<Spinner size="small" label="正在读取历史"/>}
    {!history.error&&history.data?.requests.length===0&&<p>该时间范围内暂无请求记录。</p>}
    <TableRegion label="请求历史表格"><Table size="small" className="data-table history-table" aria-label="请求历史列表">
      <TableHeader><TableRow><TableHeaderCell style={{width:'22%'}}>收到 / 结束</TableHeaderCell><TableHeaderCell style={{width:'13%'}}>请求歌曲</TableHeaderCell><TableHeaderCell style={{width:'20%'}}>视频资源</TableHeaderCell><TableHeaderCell style={{width:'20%'}}>结果</TableHeaderCell><TableHeaderCell>交付<Hint label="交付比例说明">已写出字节占本次响应范围的比例。</Hint></TableHeaderCell><TableHeaderCell style={{width:60}}>详情</TableHeaderCell></TableRow></TableHeader>
      <TableBody>{history.data?.requests.map(r=><TableRow key={history.data!.storageID+':'+r.id}>
        <TableCell><TableCellLayout description={r.completedAt==null?'结束：未知':date(r.completedAt)}>{date(r.at)}</TableCellLayout></TableCell>
        <TableCell>{r.songID?<Link href={songHref(r.songID)}>ID {r.songID}</Link>:'未知'}</TableCell>
        <TableCell>{r.resource?<Link className="resource-link truncate" title={r.resource} href={resourceHref(r.resource)}>{r.resource}</Link>:'未知'}</TableCell>
        <TableCell><div className="cell-stack"><Badge appearance="tint" color={r.outcome==='completed'?'success':r.outcome==='canceled'?'informative':'warning'}>{requestOutcome(r)}</Badge><span className="secondary">HTTP {r.status||'未返回'}</span></div></TableCell>
        <TableCell><div className="cell-stack"><span>{r.responseBytes!=null&&r.responseBytes>0?`${(r.bytes/r.responseBytes*100).toLocaleString(undefined,{maximumFractionDigits:1})}%`:'—'}</span><span className="secondary">{deliveredBytes(r.bytes)}{r.responseBytes!=null?` / ${deliveredBytes(r.responseBytes)}`:''}</span></div></TableCell>
        <TableCell><RecordDetails title={`请求 ${r.id} 的明细`}><dl className="detail-grid"><dt>请求范围</dt><dd>{r.range||'完整请求'}</dd><dt>处理耗时</dt><dd>{(r.elapsedMS/1000).toFixed(3)} 秒</dd><dt>首正文</dt><dd>{r.firstBodyNS==null?'未知':ms(r.firstBodyNS/1e6)} · {r.firstBodyBlocking==null?'分组未知':r.firstBodyBlocking?'Blocking':'缓存／本地'}</dd><dt>资源</dt><dd>{r.resource||'未知'}</dd></dl></RecordDetails></TableCell>
      </TableRow>)}</TableBody>
    </Table></TableRegion>
  </div>;
}
