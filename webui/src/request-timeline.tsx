import {useEffect,useLayoutEffect,useRef,useState} from 'react';
import {Badge,Button,Caption1,Checkbox,Divider,Field,Input,List,ListItem,Spinner,Subtitle2} from '@fluentui/react-components';
import {useApp,useFocusRecovery,useResource} from './hooks';
import {Details,Feedback,Panel} from './ui';
import {date,mb,type RequestRecord} from './types';
import {requestOutcome} from './requests';

type Group={samples:number;p50MS:number|null;p95MS:number|null};
type Latency={from:number;to:number;availableFrom:number|null;partial:boolean;blocking:Group;ready:Group};
type History={storageID:string;requests:RequestRecord[];hasMore:boolean;nextCursor:string};
type Range={from:string;to:string};
const localDate=(value:number)=>{const d=new Date(value);return new Date(value-d.getTimezoneOffset()*60000).toISOString().slice(0,16);};
const milliseconds=(value:string)=>value?new Date(value).getTime():undefined;
const deliveredBytes=(value:number)=>value<1e6?`${value.toLocaleString()} B`:mb(value);
const ms=(value:number|null)=>value==null?'—':`${value.toLocaleString(undefined,{maximumFractionDigits:1})} ms`;

export function RequestTimeline({visible}:{visible:boolean}) {
  const app=useApp(),storage=app.status.data!.activeSettings.storageDir;
  const [range,setRange]=useState<Range>(()=>({from:localDate(Date.now()-24*3600000),to:''}));
  const [applied,setApplied]=useState(range),[cursors,setCursors]=useState(['']),[error,setError]=useState(''),[paused,setPaused]=useState(false),[queryRevision,setQueryRevision]=useState(0);
  const previousStorage=useRef(storage);
  useEffect(()=>{if(previousStorage.current!==storage){previousStorage.current=storage;setCursors(['']);}},[storage]);
  const latency=useResource<Latency>('latency',visible&&!app.stopped,app.revision,30000);
  const query=new URLSearchParams({limit:'50',cursor:cursors.at(-1)!});
  for(const key of ['from','to'] as const){const value=milliseconds(applied[key]);if(value!==undefined)query.set(key,String(value));}
  return <><Panel title="首正文等待时间">
    <Caption1>服务端收到 GET 到首次成功写出视频正文的耗时；不代表客户端已播放。</Caption1>
    <Feedback error>{latency.error&&'首正文统计读取失败，请重试。'}</Feedback>
    {latency.loading&&!latency.data&&<Spinner size="small" label="正在读取统计"/>}
    {latency.data&&<><p>最近 72 小时 · {date(latency.data.from)} 至 {date(latency.data.to)}</p>
      {latency.data.partial&&<Feedback>数据不足 72 小时：{latency.data.availableFrom==null?'尚未开始采集。':`已知完整采集范围从 ${date(latency.data.availableFrom)} 起；更早数据可能尚未采集或已按保留期清理。`}</Feedback>}
      <div className="latency-groups">{([['Blocking',latency.data.blocking,'首字节前实际等待过缺失视频块。'],['缓存／本地',latency.data.ready,'首字节前直接读取可用视频块或完整本地视频。']] as const).map(([name,g,hint])=><div key={name} className="content-stack"><Subtitle2 as="h3">{name}</Subtitle2><p>{g.samples?`P50 ${ms(g.p50MS)} · P95 ${ms(g.p95MS)}`:'暂无数据'}</p><p>有效样本 {g.samples} 个</p><Caption1>{hint}</Caption1></div>)}</div>
    </>}
    <Details title="统计口径"><p>P50 表示通常等待水平；约 95% 的样本等待时间不超过 P95。没有视频正文的请求不计样本，也不计为 0 毫秒；首字节后才等待不改变分组。两组都使用最近 72 小时，历史筛选不影响统计。</p></Details>
    <Button disabled={latency.loading||app.unavailable} onClick={latency.refresh}>刷新首正文统计</Button>
  </Panel><Panel id="recent" title="请求历史">
    <form className="actions history-filter" onSubmit={e=>{e.preventDefault();const from=milliseconds(range.from),to=milliseconds(range.to);if((from!==undefined&&!Number.isFinite(from))||(to!==undefined&&!Number.isFinite(to))||(from!==undefined&&to!==undefined&&to<=from)){setError('结束时间必须晚于开始时间。');return;}setError('');setApplied({...range});setCursors(['']);setQueryRevision(v=>v+1);}}>
      <Field label="开始时间（含）"><Input type="datetime-local" value={range.from} onChange={(_,v)=>setRange({...range,from:v.value})}/></Field>
      <Field label="结束时间（不含）"><Input type="datetime-local" value={range.to} onChange={(_,v)=>setRange({...range,to:v.value})}/></Field>
      <Button type="submit" disabled={app.unavailable||paused}>查询历史</Button><Button disabled={app.unavailable||paused} onClick={()=>{const all={from:'',to:''};setRange(all);setApplied(all);setCursors(['']);setQueryRevision(v=>v+1);setError('');}}>全部保留期</Button>
    </form><Feedback error>{error}</Feedback>
    <Caption1>时间使用本机时区。仅查询实际保留的 GET 记录，每页最多 50 条；清理或未记录的数据无法补回。</Caption1>
    <Checkbox label="暂停请求刷新" checked={paused} onChange={(_,v)=>setPaused(!!v.checked)}/><HistoryPage paused={paused} focusOnMount={queryRevision>0} key={storage+':'+query+':'+queryRevision} query={query.toString()} visible={visible} pageNumber={cursors.length} live={cursors.length===1&&!applied.to} previous={()=>{setCursors(v=>v.slice(0,-1));setQueryRevision(v=>v+1);}} next={cursor=>{setCursors(v=>[...v,cursor]);setQueryRevision(v=>v+1);}}/>
  </Panel></>;
}

function HistoryPage({query,visible,pageNumber,live,paused,focusOnMount,previous,next}:{focusOnMount:boolean;query:string;visible:boolean;paused:boolean;pageNumber:number;live:boolean;previous:()=>void;next:(cursor:string)=>void}) {
  const app=useApp(),focus=useFocusRecovery();
  const history=useResource<History>('history?'+query,visible&&!paused&&!app.stopped,app.revision,live?5000:0);
  useLayoutEffect(()=>{if(focusOnMount&&visible)focus.ref.current?.focus({preventScroll:true});},[]);
  const disabled=app.unavailable||paused||history.loading;
  return <div {...focus} tabIndex={-1} aria-label="历史查询结果" className="content-stack">
    <Feedback error>{history.error&&'历史读取失败，请重试。'}</Feedback>
    <div className="actions"><Button disabled={disabled} onClick={history.refresh}>刷新本页</Button><Button disabled={disabled||pageNumber===1} onClick={previous}>上一页历史</Button><Button disabled={disabled||!history.data?.hasMore||!history.data.nextCursor} onClick={()=>next(history.data!.nextCursor)}>下一页历史</Button><span role="status">第 {pageNumber} 页 · {history.data?.requests.length??0} 条{live&&!paused?' · 自动刷新':''}</span></div>
    {history.loading&&!history.data&&<Spinner size="small" label="正在读取历史"/>}
    {!history.loading&&!history.error&&!history.data?.requests.length&&<p>该时间范围内暂无请求记录。</p>}
    <List navigationMode="composite" aria-label="请求历史列表" className="records">{history.data?.requests.map((r,i)=><ListItem key={history.data!.storageID+':'+r.id}><div role="gridcell" className="record-content">
      {i>0&&<Divider className="record-divider"/>}<div className="history-record">
        <div><p>收到：{date(r.at)}</p><p>结束：{r.completedAt==null?'未知':date(r.completedAt)}</p></div>
        <div><p>请求歌曲：{r.songID?`ID ${r.songID}`:'未知'}</p><p className="wrap">资源：{r.resource||'未知'}</p></div>
        <div><Badge appearance="tint" color={r.outcome==='completed'?'success':r.outcome==='canceled'?'informative':'warning'}>{requestOutcome(r)}</Badge><p>HTTP {r.status||'未返回'}</p><p>交付：{r.responseBytes!=null&&r.responseBytes>0?`${(r.bytes/r.responseBytes*100).toLocaleString(undefined,{maximumFractionDigits:1})}%`:'—'} · {deliveredBytes(r.bytes)}{r.responseBytes!=null?` / ${deliveredBytes(r.responseBytes)}`:''}</p></div>
      </div><Details title={`请求 ${r.id} 的明细`}><p>请求范围：{r.range||'完整请求'} · 处理耗时 {(r.elapsedMS/1000).toFixed(3)} 秒</p><p>首正文：{r.firstBodyNS==null?'未知':ms(r.firstBodyNS/1e6)} · {r.firstBodyBlocking==null?'分组未知':r.firstBodyBlocking?'Blocking':'缓存／本地'}</p><p>交付比例只表示本次有效响应范围内成功写出的视频字节，不是整视频下载进度或播放完成度。请求身份在处理时保存，当前曲库关联变化不会改写它。</p></Details>
    </div></ListItem>)}</List>
  </div>;
}
