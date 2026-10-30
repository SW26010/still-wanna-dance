import {useCallback,useEffect,useLayoutEffect,useRef,useState,type Ref} from 'react';
import {Button,Checkbox,Field,Input,Link,MessageBar,MessageBarBody,Spinner,Text,Tooltip,TableHeader,TableRow,TableHeaderCell,TableBody,TableCell,TableCellLayout} from '@fluentui/react-components';
import {ArrowSync20Regular} from '@fluentui/react-icons';
import {useApp,useFocusRecovery,useResource,type ResourceSubscription} from './hooks';
import {Notification,RecordDetails} from './ui';
import {RecordTable,SortHeader,DescriptionHeader,TablePagination,type TableSort} from './data-table';
import {date,mb,type RequestRecord,type ResponseSources} from './types';
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

const sourceExplanation='当前请求等过缺块的整块算下载，直接复用运行期块算缓存，读取已发布完整文件算本地视频。每块有正文写出才计入一次，按当时块大小折算，尾块按实际大小；不等于实际传输字节或节省的流量。';
function SourceShares({sources,details=false}:{sources:ResponseSources|null|undefined;details?:boolean}) {
  if(!sources)return <span>未知</span>;
  const total=sources.downloadBytes+sources.cacheBytes+sources.localBytes;
  if(!total)return <span>{details?'尚无来源统计':'—'}</span>;
  return <div className={details?'inline':'table-source-shares'}>{([['下载',sources.downloadBytes],['缓存',sources.cacheBytes],['本地视频',sources.localBytes]] as const).map(([label,bytes])=>{
    const share=<span tabIndex={details?0:undefined}>{label} {`${(bytes/total*100).toLocaleString(undefined,{maximumFractionDigits:1})}%`}</span>;
    return details?<Tooltip key={label} content={`${label}：${bytes.toLocaleString()} 字节（按块折算）`} relationship="description">{share}</Tooltip>:<div key={label}>{share}</div>;
  })}</div>;
}

function ResponseSourceSummary({visible}:{visible:boolean}) {
  const app=useApp();
  const sources=useResource<ResponseSources>('response-sources',visible&&!app.stopped,app.revision,30000,true);
  return (<section aria-label="响应来源统计">
      <div className="inline history-summary">
        <div className="inline history-summary-heading"><Tooltip content={sourceExplanation+' 累计值不随历史筛选或明细清理改变。'} relationship="description"><Text as="h2" tabIndex={0}>响应来源</Text></Tooltip><Tooltip content="刷新来源统计" relationship="label"><Button size="small" appearance="subtle" icon={<ArrowSync20Regular className="refresh-icon" aria-hidden="true"/>} aria-busy={sources.loading} disabled={sources.loading||app.unavailable} disabledFocusable={sources.loading} onClick={sources.refresh}/></Tooltip></div>
        {sources.data?<SourceShares sources={sources.data} details/>:<span>{sources.error?'读取失败':'正在读取…'}</span>}
      </div>
      {sources.error&&<MessageBar intent="error"><MessageBarBody>来源统计读取失败，请重试。{sources.data&&'当前显示上次结果。'}</MessageBarBody></MessageBar>}
    </section>);
}

export function RequestTimeline({visible}:{visible:boolean}) {
  const app=useApp(),storage=app.status.data!.activeSettings.databasePath;
  const [range,setRange]=useState<Range>(()=>({from:localDate(Date.now()-24*3600000),to:''}));
  const [applied,setApplied]=useState(range),[cursors,setCursors]=useState(['']),[error,setError]=useState(''),[queryRevision,setQueryRevision]=useState(0);
  const [sort,setSort]=useState<TableSort>({sort:'time',direction:'descending'});
  const [hideAdjacentCanceled,setHideAdjacentCanceled]=useState(true);
  const sortFocus=useRef(false);
  const previousStorage=useRef(storage);
  useEffect(()=>{if(previousStorage.current!==storage){previousStorage.current=storage;setCursors(['']);}},[storage]);
  const latency=useResource<Latency>('latency',visible&&!app.stopped,app.revision,30000);
  const query=new URLSearchParams({...sort,limit:'50',cursor:cursors.at(-1)!,hideAdjacentCanceled:String(hideAdjacentCanceled)});
  for(const key of ['from','to'] as const){const value=milliseconds(applied[key]);if(value!==undefined)query.set(key,String(value));}
  return <div className="page-stack">
    <ResponseSourceSummary key={storage} visible={visible}/>
    <section aria-label="首正文等待时间">
      <div className="inline history-summary">
        <div className="inline history-summary-heading"><Tooltip content={<>最近 72 小时{latency.data?.partial?' · 数据不足 72 小时':''}<br/>收到请求至写出首个视频字节。P50 为中位数，P95 覆盖 95% 的样本；不等于游戏起播时间。</>} relationship="description"><Text as="h2" tabIndex={0}>首正文等待时间</Text></Tooltip><Tooltip content="刷新首正文统计" relationship="label"><Button size="small" appearance="subtle" icon={<ArrowSync20Regular className="refresh-icon" aria-hidden="true"/>} aria-busy={latency.loading} disabled={latency.loading||app.unavailable} disabledFocusable={latency.loading} onClick={latency.refresh}/></Tooltip></div>
        {([['等待下载',latency.data?.blocking,'首字节前等待过缺失的视频块。'],['缓存／本地',latency.data?.ready,'首字节前直接读取可用的视频块或完整视频。']] as const).map(([name,g,hint])=><Tooltip key={name} content={`${hint} 有效样本 ${g?.samples??'—'} 个`} relationship="description"><span className="inline" tabIndex={0}><span>{name}</span><span>{!g?latency.error?'读取失败':'正在读取…':g.samples?`P50 ${ms(g.p50MS)} · P95 ${ms(g.p95MS)}`:'暂无数据'}</span></span></Tooltip>)}
      </div>
      <Notification message={visible&&latency.error?'首正文统计读取失败，请重试。':undefined} error/>
    </section><section id="recent" className="history-section" tabIndex={-1}>
    <form className="form-grid history-filter" onSubmit={e=>{e.preventDefault();const from=milliseconds(range.from),to=milliseconds(range.to);if((from!==undefined&&!Number.isFinite(from))||(to!==undefined&&!Number.isFinite(to))||(from!==undefined&&to!==undefined&&to<=from)){setError('结束时间必须晚于开始时间。');return;}sortFocus.current=false;setError('');setApplied({...range});setCursors(['']);setQueryRevision(v=>v+1);}}>
      <div className="history-range">
      <Field label="开始" orientation="horizontal"><Input type="datetime-local" value={range.from} onChange={(_,v)=>{setRange({...range,from:v.value});setError('');}}/></Field>
      <Field label="结束" orientation="horizontal" validationState={error?'error':'none'} validationMessage={error||undefined}><Input type="datetime-local" value={range.to} onChange={(_,v)=>{setRange({...range,to:v.value});setError('');}}/></Field>
      </div><div className="inline history-actions"><div className="inline"><Button type="submit" disabled={app.unavailable}>查询历史</Button><Button disabled={app.unavailable} onClick={()=>{sortFocus.current=false;const all={from:'',to:''};setRange(all);setApplied(all);setCursors(['']);setQueryRevision(v=>v+1);setError('');}}>全部保留期</Button></div>
      <Tooltip positioning="above" content="同一视频的前一次请求中断、后一次完成，通常是游戏播放器的正常行为。" relationship="description">{({onPointerEnter,onPointerLeave,ref,...trigger})=><Checkbox {...trigger} className="history-option" ref={ref as Ref<HTMLInputElement>} root={{onPointerEnter,onPointerLeave}} label="隐藏相邻中断请求" checked={hideAdjacentCanceled} onChange={(_,v)=>{sortFocus.current=false;setHideAdjacentCanceled(!!v.checked);setCursors(['']);setQueryRevision(0);}}/>}</Tooltip></div>
    </form>
    <HistoryPage focusOnMount={queryRevision>0} focusSort={sortFocus.current} sort={sort} onSortChange={next=>{sortFocus.current=true;setSort(next);setCursors(['']);setQueryRevision(v=>v+1);}} key={storage+':'+query+':'+queryRevision} query={query.toString()} visible={visible} pageNumber={cursors.length} live={cursors.length===1&&!applied.to&&sort.sort==='time'&&sort.direction==='descending'} previous={()=>{sortFocus.current=false;setCursors(v=>v.slice(0,-1));setQueryRevision(v=>v+1);}} next={cursor=>{sortFocus.current=false;setCursors(v=>[...v,cursor]);setQueryRevision(v=>v+1);}}/>
  </section></div>;
}

function HistoryPage({query,visible,pageNumber,live,focusOnMount,focusSort,sort,onSortChange,previous,next}:{focusOnMount:boolean;focusSort:boolean;sort:TableSort;onSortChange:(sort:TableSort)=>void;query:string;visible:boolean;pageNumber:number;live:boolean;previous:()=>void;next:(cursor:string)=>void}) {
  const app=useApp(),focus=useFocusRecovery();
  const [connectionError,setConnectionError]=useState('');
  const subscribe=useCallback<ResourceSubscription>(invalidate=>{
    const source=new EventSource('/api/history/events');
    let disposed=false;
    setConnectionError('');
    source.addEventListener('history',event=>{
      if(disposed)return;
      try {
        if(JSON.parse((event as MessageEvent<string>).data).closed){source.close();setConnectionError('历史更新已停止。');return;}
        setConnectionError('');invalidate();
      }catch{source.close();setConnectionError('历史更新无法读取，请重新查询。');}
    });
    source.onerror=()=>{if(!disposed)setConnectionError(source.readyState===EventSource.CLOSED?'历史更新连接不可用，请重新查询。':'历史更新连接已断开，正在重新连接…');};
    return()=>{disposed=true;source.close();};
  },[]);
  const history=useResource<History>('history?'+query,visible&&!app.stopped,app.revision,0,false,live?subscribe:undefined);
  useLayoutEffect(()=>{if(focusOnMount&&visible){const target=focusSort?focus.ref.current?.querySelector<HTMLElement>('th[aria-sort="ascending"] [role="button"],th[aria-sort="descending"] [role="button"]'):focus.ref.current;target?.focus({preventScroll:true});if(!focusSort)target?.scrollIntoView({block:'start'});}},[]);
  const disabled=app.unavailable||(history.loading&&!history.data);
  return <div {...focus} tabIndex={-1} aria-label="历史查询结果" className="content-stack">
    <Notification message={visible?(history.error?'历史读取失败，请重新查询。':connectionError||undefined):undefined} error/>
    {history.loading&&!history.data&&!connectionError&&<Spinner size="small" label="正在读取历史"/>}
    <RecordTable busy={history.loading} regionLabel="请求历史表格" label="请求历史列表">
      <TableHeader><TableRow><SortHeader column="time" label="收到时间" sort={sort} onSortChange={onSortChange} defaultDirection="descending" disabled={disabled}/><SortHeader column="song" label="请求歌曲" sort={sort} onSortChange={onSortChange} disabled={disabled} className="table-fill-column" description="按请求时记录的歌曲 ID 排序；名称来自当前曲库，完整名称可在详情中查看。"/><SortHeader column="resource" label="视频资源" sort={sort} onSortChange={onSortChange} disabled={disabled} className="table-fill-column"/><TableHeaderCell>结果</TableHeaderCell><DescriptionHeader label="交付">已写出字节占本次响应范围的比例。</DescriptionHeader><DescriptionHeader label="来源">{sourceExplanation}</DescriptionHeader><TableHeaderCell>详情</TableHeaderCell></TableRow></TableHeader>
      <TableBody>{history.data?.requests.map(r=><TableRow key={history.data!.storageID+':'+r.id}>
        <TableCell><TableCellLayout description={r.completedAt==null?'结束：未知':date(r.completedAt)}>{date(r.at)}</TableCellLayout></TableCell>
        <TableCell className="table-fill-column">{r.songID?<TableCellLayout appearance="primary" truncate description={<Link href={songHref(r.songID)}>ID {r.songID}</Link>}>{r.songTitle||'未知名称'}</TableCellLayout>:'未知'}</TableCell>
        <TableCell className="table-fill-column"><TableCellLayout truncate>{r.resource?<Link title={r.resource} href={resourceHref(r.resource)}>{r.resource}</Link>:'未知'}</TableCellLayout></TableCell>
        <TableCell><TableCellLayout description={`HTTP ${r.status||'未返回'}`}>{requestOutcome(r)}</TableCellLayout></TableCell>
        <TableCell><div className="cell-stack"><span>{r.responseBytes!=null&&r.responseBytes>0?`${(r.bytes/r.responseBytes*100).toLocaleString(undefined,{maximumFractionDigits:1})}%`:'—'}</span><span className="secondary">{deliveredBytes(r.bytes)}{r.responseBytes!=null?` / ${deliveredBytes(r.responseBytes)}`:''}</span></div></TableCell>
        <TableCell><SourceShares sources={r.sources}/></TableCell>
        <TableCell><RecordDetails title={`请求 ${r.id} 的明细`}><dl className="detail-grid">{r.songID&&<><dt>歌曲名称（当前曲库）</dt><dd>{r.songTitle||'未知名称'}</dd></>}<dt>请求范围</dt><dd>{r.range||'完整请求'}</dd><dt>处理耗时</dt><dd>{(r.elapsedMS/1000).toFixed(3)} 秒</dd><dt>首正文</dt><dd>{r.firstBodyNS==null?'未知':ms(r.firstBodyNS/1e6)} · {r.firstBodyBlocking==null?'分组未知':r.firstBodyBlocking?'Blocking':'缓存／本地'}</dd><dt>资源</dt><dd>{r.resource||'未知'}</dd><dt>按块折算字节</dt><dd>{r.sources?`下载 ${deliveredBytes(r.sources.downloadBytes)} · 缓存 ${deliveredBytes(r.sources.cacheBytes)} · 本地视频 ${deliveredBytes(r.sources.localBytes)}`:'未知'}</dd></dl></RecordDetails></TableCell>
      </TableRow>)}</TableBody>
    </RecordTable>
    {!history.error&&history.data?.requests.length===0&&<p className="empty-state">当前筛选条件下暂无请求记录。</p>}
    <TablePagination label="历史" disabled={disabled||!!history.error} canPrevious={pageNumber>1} canNext={!!history.data?.hasMore&&!!history.data.nextCursor}
      previous={previous} next={()=>next(history.data!.nextCursor)}
      summary={history.error?'历史读取失败，请重新查询。':!history.data?'正在读取历史…':`第 ${pageNumber} 页 · ${history.data.requests.length} 条${live&&!connectionError?' · 自动刷新':''}`}/>
  </div>;
}
