import {useEffect,useRef,useState,type PropsWithChildren,type ReactNode} from 'react';
import {Badge,Button,Dialog,DialogContent,DialogSurface,DialogTrigger,Field,Input,Select,Spinner,TableBody,TableCell,TableHeader,TableHeaderCell,TableRow,Tooltip} from '@fluentui/react-components';
import {ArrowDownload20Regular,ArrowExpand20Regular,Dismiss20Regular} from '@fluentui/react-icons';
import {downloadFile} from './api';
import {useApp,useResource} from './hooks';
import {Feedback,Notification,Panel} from './ui';
import {RecordTable,TablePagination,paginationSummary} from './data-table';
import {date} from './types';

interface LogEntry {time:string;level:string;message:string;fields:{key:string;value:string}[];file:string;line:number;raw:string}
interface LogResult {entries:LogEntry[];files:string[];matches:number;skipped:number;limit:number}
interface Filter {from:string;to:string;level:string;event:string}
const emptyFilter:Filter={from:'',to:'',level:'',event:''};
const pageSize=50;

function filterQuery(filter:Filter) {
  const from=filter.from?new Date(filter.from).getTime():undefined,to=filter.to?new Date(filter.to).getTime():undefined;
  if(from!==undefined&&!Number.isFinite(from)||to!==undefined&&!Number.isFinite(to)||from!==undefined&&to!==undefined&&to<=from)throw new Error('结束时间必须晚于开始时间。');
  const params=new URLSearchParams();
  if(from!==undefined)params.set('from',String(from));
  if(to!==undefined)params.set('to',String(to));
  if(filter.level)params.set('level',filter.level);
  if(filter.event.trim())params.set('event',filter.event.trim());
  return params;
}

export function LogReader({visible}:{visible:boolean}) {
  const app=useApp();
  const [draft,setDraft]=useState<Filter>(emptyFilter),[query,setQuery]=useState(''),[revision,setRevision]=useState(0),[error,setError]=useState('');
  const logs=useResource<LogResult>('logs?'+query,visible&&!app.stopped,revision,0);
  const [exporting,setExporting]=useState(false),[notice,setNotice]=useState<{message:string;error?:boolean}>();
  const exportController=useRef<AbortController|null>(null);
  const exportVisible=useRef(visible);exportVisible.current=visible;
  useEffect(()=>{
    if(!visible)setNotice(undefined);
    if(app.stopped)exportController.current?.abort();
  },[visible,app.stopped]);
  useEffect(()=>()=>exportController.current?.abort(),[]);
  const apply=(filter:Filter)=>{
    try {setQuery(filterQuery(filter).toString());setError('');setRevision(value=>value+1);}
    catch(problem){setError((problem as Error).message);}
  };
  const exportLogs=async()=>{
    if(exportController.current||app.stopped)return;
    let params:URLSearchParams;
    try {params=filterQuery(draft);setError('');}catch(problem){setError((problem as Error).message);return;}
    const controller=new AbortController();exportController.current=controller;
    setExporting(true);setNotice(undefined);
    try {
      const headers=await downloadFile('logs/export?'+params,'swd-logs.txt',AbortSignal.any([controller.signal,AbortSignal.timeout(35000)]));
      const matches=Number(headers.get('X-SWD-Log-Matches')),skipped=Number(headers.get('X-SWD-Log-Skipped'));
      if(exportVisible.current)setNotice({message:`已开始下载 ${matches.toLocaleString()} 条日志。${skipped?`另有 ${skipped} 行无法解析，未包含在导出中。`:''}`});
    }catch(problem){if(!controller.signal.aborted&&exportVisible.current)setNotice({message:'日志导出失败，请重试。'+String(problem),error:true});}
    finally {exportController.current=null;setExporting(false);}
  };
  return <LogResults result={logs.loadedRevision===revision?logs.data:undefined} queryRevision={revision} loading={logs.loading} error={logs.error} visible={visible} action={<Tooltip content="按当前筛选导出全部匹配记录为可读文本" relationship="description"><Button type="button" icon={<ArrowDownload20Regular/>} disabled={app.stopped||exporting} aria-busy={exporting} onClick={()=>void exportLogs()}>{exporting?'正在导出…':'导出日志'}</Button></Tooltip>}>
    <form className="toolbar log-filter" onSubmit={event=>{event.preventDefault();apply(draft);}}>
      <Field label="开始时间" className="log-time-filter"><Input type="datetime-local" step={1} value={draft.from} onChange={(_,data)=>{setDraft({...draft,from:data.value});setError('');}}/></Field>
      <Field label="结束时间" className="log-time-filter" validationState={error?'error':'none'} validationMessage={error||undefined}><Input type="datetime-local" step={1} value={draft.to} onChange={(_,data)=>{setDraft({...draft,to:data.value});setError('');}}/></Field>
      <Field label="级别"><Select value={draft.level} onChange={(_,data)=>setDraft({...draft,level:data.value})}><option value="">全部级别</option><option value="DEBUG">Debug</option><option value="INFO">Info</option><option value="WARN">Warn</option><option value="ERROR">Error</option></Select></Field>
      <Field label="事件关键词" className="log-keyword-filter"><Input type="search" placeholder="输入事件名关键词" value={draft.event} onChange={(_,data)=>setDraft({...draft,event:data.value})}/></Field>
      <div className="inline"><Button appearance="primary" type="submit" disabled={app.stopped}>查询日志</Button><Button disabled={app.stopped} onClick={()=>{setDraft(emptyFilter);apply(emptyFilter);}}>重置筛选</Button></div>
    </form>
    <Notification message={visible?notice?.message:undefined} error={notice?.error}/>
  </LogResults>;
}

function LogResults({result,queryRevision,loading,error,visible,children,action}:PropsWithChildren<{result?:LogResult;queryRevision:number;loading:boolean;error:string;visible:boolean;action:ReactNode}>) {
  const focus=useRef<HTMLDivElement>(null);
  const [page,setPage]=useState(0);
  const [expanded,setExpanded]=useState(false);
  const lastPage=result?Math.max(0,Math.ceil(result.entries.length/pageSize)-1):undefined;
  useEffect(()=>setPage(0),[queryRevision]);
  useEffect(()=>{if(lastPage!==undefined)setPage(current=>Math.min(current,lastPage));},[lastPage]);
  useEffect(()=>{if(!visible)setExpanded(false);},[visible]);
  const open=expanded&&visible;
  const currentPage=lastPage===undefined?page:Math.min(page,lastPage);
  const entries=result?(open?result.entries:result.entries.slice(currentPage*pageSize,(currentPage+1)*pageSize)):[];
  const move=(next:number)=>{setPage(next);focus.current?.focus({preventScroll:true});focus.current?.scrollIntoView({block:'start'});};
  const content=<div ref={focus} role="region" tabIndex={-1} aria-label="日志查询结果" className="content-stack log-results">
    {!open&&<>
      <Notification message={visible&&error?'日志读取失败，请重新查询。':undefined} body={error} error/>
      {loading&&!result&&<Spinner size="small" label="正在读取日志"/>}
      {result&&result.matches>result.limit&&<Feedback>匹配 {result.matches.toLocaleString()} 条，仅展示最近 {result.limit} 条。请缩小时间范围以查看更早记录。</Feedback>}
      {!!result?.skipped&&<Feedback>有 {result.skipped} 行暂无法解析，未展示。</Feedback>}
    </>}
    <RecordTable className="log-table" label="程序日志记录" regionLabel="程序日志表格" busy={loading}>
      <TableHeader><TableRow><TableHeaderCell className="log-metadata-cell">时间</TableHeaderCell><TableHeaderCell className="log-metadata-cell">级别</TableHeaderCell><TableHeaderCell className="log-metadata-cell">事件</TableHeaderCell><TableHeaderCell>内容</TableHeaderCell></TableRow></TableHeader>
      <TableBody>{entries.map(entry=><TableRow key={entry.file+':'+entry.line}>
        <TableCell className="log-metadata-cell"><time dateTime={entry.time} title={entry.time}>{date(entry.time)}</time></TableCell>
        <TableCell className="log-metadata-cell"><Badge appearance="tint" color={entry.level==='ERROR'?'danger':entry.level==='WARN'?'warning':'informative'}>{entry.level}</Badge></TableCell>
        <TableCell className="log-metadata-cell">{entry.message}</TableCell>
        <TableCell><span className="log-content">{entry.fields.length?entry.fields.map((field,index)=><span key={field.key}>{index?' ':''}<strong>{field.key}</strong>={field.value.replace(/\n/g,' ')}</span>):'—'}</span></TableCell>
      </TableRow>)}</TableBody>
    </RecordTable>
    {!open&&result&&!entries.length&&<p className="empty-state">{result.files.length?'当前筛选条件下没有日志。':'尚无程序日志文件。'}</p>}
    {!open&&<TablePagination label="日志" disabled={loading||!!error||!result} canPrevious={currentPage>0} canNext={!!result&&(currentPage+1)*pageSize<result.entries.length} previous={()=>move(currentPage-1)} next={()=>move(currentPage+1)} summary={error?'日志读取失败':!result?'正在读取日志…':paginationSummary(currentPage+1,result.entries.length,pageSize)+(result.matches>result.limit?'（最近记录）':'')}/>}
  </div>;
  return <Dialog open={open} onOpenChange={(_,data)=>setExpanded(data.open)}>
    <Panel title="程序日志" id="logs" action={<div className="inline">{action}<Tooltip content="展开日志表格" relationship="label"><DialogTrigger disableButtonEnhancement><Button appearance="subtle" icon={<ArrowExpand20Regular/>} aria-label="展开日志表格" aria-haspopup="dialog"/></DialogTrigger></Tooltip></div>}>
      {children}{!open&&content}
    </Panel>
    <DialogSurface className="log-expanded" aria-label="程序日志表格">
      <DialogContent className="log-expanded-content">{open&&content}</DialogContent>
      <DialogTrigger action="close" disableButtonEnhancement><Button className="log-expanded-exit" appearance="secondary" icon={<Dismiss20Regular/>} aria-label="退出日志展开视图"/></DialogTrigger>
    </DialogSurface>
  </Dialog>;
}
