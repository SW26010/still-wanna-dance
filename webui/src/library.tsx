import {useEffect,useLayoutEffect,useRef,useState} from 'react';
import {Badge,Button,Checkbox,Field,Input,Link,Select,Tab,TabList,Table,TableHeader,TableRow,TableHeaderCell,TableBody,TableCell,TableCellLayout,Tooltip,tokens,useRestoreFocusTarget} from '@fluentui/react-components';
import {useApp,useFocusRecovery,useResource} from './hooks';
import {usePageFeedback} from './page-feedback';
import {APIError,request} from './api';
import {Feedback,Notification,Hint,Progress,RecordDetails,TableRegion} from './ui';
import {DeleteRegular,FolderOpenRegular,PlayRegular} from '@fluentui/react-icons';
import {LibraryPreview,type PreviewSelection} from './library-preview';
import {InventoryPanel,useCacheActions,cacheLabel} from './cache';
import {BatchPanel} from './home-library';
import {ImportPanel} from './settings';
import {VRCXImport,useVRCX} from './vrcx';
import {date,mb,type Download,type LibraryPage,type LibraryResource,type SongPolicy} from './types';

export const songHref=(id:string)=>'#/library?song='+encodeURIComponent(id);
export const resourceHref=(key:string)=>'#/library?resource='+encodeURIComponent(key);
const scoreFormat=new Intl.NumberFormat(undefined,{minimumFractionDigits:2,maximumFractionDigits:2,minimumSignificantDigits:4,maximumSignificantDigits:4,roundingPriority:'lessPrecision'});
const downloadRefreshInterval=(data:{tasks:Download[]})=>data.tasks.length?5000:0;
const policyLabels:Record<SongPolicy,string>={normal:'普通',favorite:'收藏',blocked:'屏蔽'};
export function Library({visible,anchor}:{visible:boolean;anchor:string}) {
  const [opened,setOpened]=useState(visible);
  useEffect(()=>{if(visible)setOpened(true);},[visible]);
  const params=new URLSearchParams(anchor.split('?')[1]||''), song=params.get('song')||'', resource=params.get('resource')||'';
  const view=params.get('view'),requestedView=view==='songs'||view==='resources'?view:undefined;
  return opened?<LibraryView key={song+':'+resource} visible={visible} anchor={anchor} song={song} resource={resource} requestedView={requestedView}/>:null;
}
function LibraryView({visible,anchor,song,resource,requestedView}:{visible:boolean;anchor:string;song:string;resource:string;requestedView?:'songs'|'resources'}) {
  const app=useApp(),s=app.status.data!,focus=useFocusRecovery(),toolFocus=useFocusRecovery();
  const [view,setView]=useState(resource||requestedView==='resources'?'resources':'songs'),[search,setSearch]=useState(''),[sort,setSort]=useState('score'),[filter,setFilter]=useState('');
  const [query,setQuery]=useState({q:'',sort:'score',filter:''}),[offset,setOffset]=useState(0),[selected,setSelected]=useState<Record<string,string>>({});
  const [pending,setPending]=useState(false),[tool,setTool]=useState<'video'|'history'|undefined>(anchor==='video-import'?'video':undefined);
  const previousTool=useRef(tool);
  const history=useVRCX();
  const [preview,setPreview]=useState<PreviewSelection>();
  const previewFocusTarget=useRestoreFocusTarget();
  const page=useResource<LibraryPage>('library?'+new URLSearchParams({...query,view,song,resource,offset:String(offset)}),visible&&!app.stopped,app.revision,0,true);
  const downloads=useResource<{tasks:Download[]}>('downloads',visible&&!app.stopped,app.revision,s.batch.running?5000:downloadRefreshInterval,true);
  const feedback=usePageFeedback<{text:string;failed:boolean}>(visible?page.data?.storageID:undefined);
  const actions=useCacheActions(page.data,page.refresh,visible,page.loading||pending||!!page.error);
  const fileResult=useRef<HTMLElement>(null);
  useEffect(()=>{if(!visible||app.stopped||tool)setPreview(undefined);},[visible,app.stopped,tool]);
  useEffect(()=>setPreview(undefined),[page.data?.storageID]);
  useEffect(()=>{if(visible&&(actions.result?.error||actions.result?.entries?.length))fileResult.current?.focus();},[visible,actions.result]);
  const disabled=app.unavailable||pending||actions.pending||page.loading||!page.data||!!page.error;
  const resources=page.data?.resources||{}, entries=Object.values(resources).flatMap(r=>r.local?[r.local]:[]), chosen=entries.filter(e=>selected[e.key]===e.stamp), eligible=entries.filter(e=>e.known&&!e.protected);
  useEffect(()=>{setSelected({});},[query,view,offset,page.data?.storageID]);
  useEffect(()=>{
    setSelected(previous=>Object.fromEntries(Object.values(page.data?.resources||{}).flatMap(r=>{
      const e=r.local;return e?.known&&!e.protected&&previous[e.key]===e.stamp?[[e.key,e.stamp]]:[];
    })));
    if(page.data&&offset>=page.data.total&&offset>0)setOffset(0);
  },[page.data,offset]);
  const previousTasks=useRef({batch:s.batch.running,import:s.import.running});
  useEffect(()=>{
    if(visible&&(previousTasks.current.batch&&!s.batch.running||previousTasks.current.import&&!s.import.running))page.refresh();
    previousTasks.current={batch:s.batch.running,import:s.import.running};
  },[s.batch.running,s.import.running,visible,page.refresh]);
  useLayoutEffect(()=>{if(visible&&anchor==='video-import'){setTool('video');const frame=requestAnimationFrame(()=>{const target=document.getElementById('video-import');target?.focus();target?.scrollIntoView();});return()=>cancelAnimationFrame(frame);}},[visible,anchor]);
  useLayoutEffect(()=>{
    if(!visible||(requestedView===undefined&&anchor!=='batch'))return;
    setTool(undefined);
    if(requestedView){setView(requestedView);setOffset(0);setFilter('');setQuery(q=>({...q,filter:''}));}
    const frame=requestAnimationFrame(()=>{const target=anchor==='batch'?document.getElementById('batch'):focus.ref.current;target?.focus();target?.scrollIntoView();});
    return()=>cancelAnimationFrame(frame);
  },[visible,anchor,requestedView]);
  useLayoutEffect(()=>{
    const changed=previousTool.current!==tool;previousTool.current=tool;
    // Initial page focus belongs to navigation; only tool switches move it here.
    if(!visible||!changed)return;
    const frame=requestAnimationFrame(()=>{const target=tool?toolFocus.ref.current:anchor==='batch'?document.getElementById('batch'):focus.ref.current;target?.focus({preventScroll:true});});
    return()=>cancelAnimationFrame(frame);
  },[tool,visible,anchor]);
  async function save(id:string,policy:SongPolicy) {
    if(disabled||!page.data)return;
    setPending(true);feedback.setValue(undefined);
    try {await request('songs',{storageID:page.data.databaseID,id,policy},AbortSignal.timeout(35000));feedback.setValue({failed:false,text:`歌曲 ${id} 已设为${policyLabels[policy]}。`});app.refresh();}
    catch(e){feedback.setValue({failed:true,text:e instanceof APIError?e.message:'保存结果未确认，请刷新曲库核对。'});}
    finally{setPending(false);page.refresh();}
  }
  const download=(target:{songID?:string;resource?:string})=>void app.action('batch/download',{storageID:page.data?.storageID,...target});
  async function importHistory() {
    const result=await history.inspect(true);
    if(result?.review)setTool('history');
    if(result?.imported)feedback.setValue({failed:false,text:`已根据 VRCX 历史初始化 ${result.imported.songs} 首歌曲的优先级。`});
  }
  const sharedState=(r:LibraryResource|undefined)=> {
    if(page.data?.videoError)return <Badge appearance="tint" color="warning">未知</Badge>;
    const task=downloads.data?.tasks.find(t=>t.resource===r?.key),local=r?.local;
    return <div className="cell-stack"><div className="inline"><Badge appearance="tint" color={local?'success':'informative'}>{local?'已保存':'未保存'}</Badge>{local?.protected&&<Tooltip content="正在使用、校验或准备播放，暂不能删除。" relationship="description"><Badge color="warning" tabIndex={0}>受保护</Badge></Tooltip>}</div>
      {task&&<Tooltip content={`${mb(task.bytes)} / ${task.size>0?mb(task.size):'未知'} · ${mb(task.bytesPerSecond)}/s；校验完成后保存。`} relationship="description"><div tabIndex={0} className="cell-progress"><span>{({cache_check:'检查中',upstream_headers:'等待上游',download:'下载中',range_download:'下载中',hash:'校验中',publish:'保存中',index:'保存中'}[task.stage]||'准备中')} {task.size>0?`${Math.min(100,100*task.bytes/task.size).toFixed(1)}%`:''}</span><Progress label="资源下载字节进度" value={task.bytes} max={task.size}/></div></Tooltip>}</div>;
  };
  const fileActions=(r:LibraryResource|undefined)=>r?.local&&<>
    <Tooltip content="定位文件" relationship="label"><Button size="small" appearance="subtle" icon={<FolderOpenRegular/>} aria-label="定位文件" disabled={disabled||!r.local.known} onClick={()=>void actions.operate('open',[r.local!])}/></Tooltip>
    <Tooltip content="删除本地视频" relationship="label"><Button size="small" appearance="subtle" icon={<DeleteRegular/>} aria-label="删除本地视频" disabled={disabled||!r.local.known||r.local.protected} onClick={()=>void actions.operate('delete',[r.local!])}/></Tooltip></>;
  const previewAction=(target:PreviewSelection,unavailableReason='')=><Tooltip content={unavailableReason||'预览视频，不计入历史和需求分数。'} relationship="description"><Button {...previewFocusTarget} size="small" icon={<PlayRegular/>} disabled={disabled||!!page.data?.videoError||!!unavailableReason} disabledFocusable={!!unavailableReason} onClick={()=>setPreview(target)}>预览</Button></Tooltip>;
  return <div className="page-stack">
    {visible&&!app.stopped&&preview&&page.data&&<LibraryPreview key={page.data.storageID} storageID={page.data.storageID} target={preview} onClose={()=>setPreview(undefined)}/>}
    <Notification message={feedback.value?.text} error={feedback.value?.failed}/><Notification message={!actions.result?.error&&!actions.result?.entries?.length?actions.result?.text:undefined}/>
    <Notification message={visible&&downloads.error?'下载进度暂时无法读取。':undefined} error/>
    {page.data?.videoError&&<Feedback error>视频目录不可用，本地视频状态未知。歌曲资料仍可查看。<p className="wrap">{page.data.videoError}</p></Feedback>}
    <InventoryPanel visible={visible} videoError={page.data?.videoError} files={page.data?.videoError?undefined:page.data} refresh={page.refresh} loading={page.loading} loadedAt={page.loadedAt} readError={page.error}/>
    <section hidden={!!tool} id="library-browser" className="library-browser" {...focus} tabIndex={-1}>
      {history.suggested&&<div className="content-stack" aria-label="VRCX 历史导入建议"><Feedback>
        <p>检测到 VRCX 历史，可用于安排歌曲下载与保留顺序。仅在本机处理。</p>
        <div className="actions"><Button disabled={history.locked} onClick={()=>void importHistory()}>导入历史</Button><Button disabled={history.locked} onClick={()=>void history.dismiss()}>忽略</Button></div>
        {history.pending&&<p role="status">正在处理 VRCX 历史…</p>}
      </Feedback><Feedback error>{history.message}</Feedback></div>}
      {(song||resource)&&<div className="toolbar"><span>查看{song?'歌曲 ID '+song:'资源 '+resource}</span><Link href="#/library">返回完整曲库</Link></div>}
      <div className="toolbar spread"><TabList aria-label="曲库视图" selectedValue={view} onTabSelect={(_,v)=>{setView(String(v.value));setOffset(0);setSort('score');setFilter('');setQuery(q=>({...q,sort:'score',filter:''}));}}><Tab id="library-tab-songs" aria-controls="library-results" value="songs" disabled={!!resource}>按歌曲</Tab><Tab id="library-tab-resources" aria-controls="library-results" value="resources" disabled={!!song}>按资源</Tab></TabList>
        <div className="inline"><Button onClick={()=>setTool('video')}>导入视频</Button><Button onClick={()=>setTool('history')}>导入 VRCX 历史</Button></div></div>
      <div id="library-results" role="tabpanel" aria-labelledby={'library-tab-'+view}>
      <form className="toolbar library-search" onSubmit={e=>{e.preventDefault();setOffset(0);setQuery({q:search,sort,filter});page.refresh();}}>
        <Field label="搜索曲库" className="search-field"><Input type="search" maxLength={300} placeholder="名称、ID、歌手、舞者或资源" value={search} onChange={(_,v)=>setSearch(v.value)}/></Field>
        <Field label="排序"><Select value={sort} onChange={(_,v)=>setSort(v.value)}><option value="score">需求分数优先</option><option value="title">名称顺序</option>{view==='songs'?<option value="id">歌曲 ID 从新到旧</option>:<><option value="size">大小从大到小</option><option value="recent">最近请求优先</option></>}</Select></Field>
        <Field label="筛选"><Select value={filter} onChange={(_,v)=>setFilter(v.value)}><option value="">全部</option><option value="saved">已保存视频</option><option value="missing">缺失视频</option>{view==='songs'&&Object.entries(policyLabels).map(([key,label])=><option key={key} value={key}>{label}</option>)}</Select></Field>
        <Button appearance="primary" type="submit" disabled={disabled}>查找</Button>
      </form>
      <div className="toolbar table-toolbar spread"><span role="status">{page.error?'曲库读取失败，请刷新重试。':page.loading&&!page.data?'正在读取曲库…':`共 ${page.data?.total||0} ${view==='songs'?'首歌曲':'个资源'} · 第 ${offset/50+1} 页`}</span>
        {view==='resources'&&<div className="inline"><Button size="small" disabled={disabled||!eligible.length} onClick={()=>setSelected(eligible.every(e=>selected[e.key]===e.stamp)?{}:Object.fromEntries(eligible.map(e=>[e.key,e.stamp])))}>选择本页可删除项</Button><Button size="small" disabled={disabled||!chosen.length} onClick={()=>void actions.operate('delete',chosen)}>删除选中视频</Button><span>已选择 {chosen.length} 个文件</span></div>}</div>
      {actions.result&&(actions.result.error||!!actions.result.entries?.length)&&<section ref={fileResult} tabIndex={-1} aria-label="文件操作结果" className="content-stack file-results"><Feedback error={actions.result.error}>{actions.result.text}{actions.result.message&&<p className="wrap">{actions.result.message}</p>}</Feedback>
        {!!actions.result.entries?.length&&<Table size="small" aria-label="未删除的视频"><TableHeader><TableRow><TableHeaderCell>视频</TableHeaderCell><TableHeaderCell>原因</TableHeaderCell></TableRow></TableHeader><TableBody>{actions.result.entries.map(entry=><TableRow key={entry.key}><TableCell>{entry.label}</TableCell><TableCell>{entry.reason}</TableCell></TableRow>)}</TableBody></Table>}
      </section>}
      <TableRegion label="曲库表格"><Table size="small" className="data-table library-table" aria-label={view==='songs'?'曲库歌曲':'曲库资源'}>
        <TableHeader><TableRow>{view==='songs'?<><TableHeaderCell style={{width:'30%'}}>歌曲</TableHeaderCell><TableHeaderCell style={{width:'10%'}}>需求分数<Hint label="需求分数说明">一次新需求计 100 分。灰色为初始分数；真实分数随时间衰减，按分数排序时优先显示。</Hint></TableHeaderCell><TableHeaderCell style={{width:'14%'}}>偏好<Hint label="歌曲偏好说明">收藏仅作标记；屏蔽会拒绝新的播放和下载请求。</Hint></TableHeaderCell></>:<><TableHeaderCell style={{width:44}}>选择</TableHeaderCell><TableHeaderCell style={{width:'30%'}}>视频资源</TableHeaderCell><TableHeaderCell style={{width:'16%'}}>大小 / 时长</TableHeaderCell></>}
          <TableHeaderCell style={{width:'19%'}}>本地状态</TableHeaderCell><TableHeaderCell>操作</TableHeaderCell></TableRow></TableHeader>
        <TableBody>{view==='songs'?page.data?.songs.map(song=>{
          const r=resources[song.resource],score=scoreFormat.format(song.score*100),source=song.hasDemand?'真实分数':'初始分数';return <TableRow key={song.id}>
            <TableCell><TableCellLayout appearance="primary" truncate description={`ID ${song.id} · ${song.artist||'未知歌手'}${song.dancer?' · '+song.dancer:''}${song.group?' / '+song.group:''}`}>{song.title||'未知名称'}</TableCellLayout></TableCell>
            <TableCell aria-label={`${source} ${score}`}><span style={{color:song.hasDemand?tokens.colorNeutralForeground1:tokens.colorNeutralForeground3,fontVariantNumeric:'tabular-nums'}}>{score}</span></TableCell>
            <TableCell><Select size="small" aria-label={`歌曲 ${song.id} 的偏好`} value={song.policy} disabled={disabled} onChange={(_,v)=>void save(song.id,v.value as SongPolicy)}>{Object.entries(policyLabels).map(([value,label])=><option key={value} value={value}>{label}</option>)}</Select></TableCell>
            <TableCell>{sharedState(r)}</TableCell><TableCell><div className="inline row-actions">{previewAction({songID:song.id,label:song.title||'歌曲 '+song.id},song.policy==='blocked'?'歌曲已屏蔽，请先解除屏蔽。':!song.resource?'尚不知道对应资源，请先更新曲库。':'')}<Button size="small" disabled={disabled||!!page.data?.videoError||s.batch.running||!!r?.local||song.policy==='blocked'} onClick={()=>download({songID:song.id})}>下载歌曲</Button>{fileActions(r)}<RecordDetails title={`歌曲 ${song.id} 的详情`}><dl className="detail-grid"><dt>名称</dt><dd>{song.title||'未知'}</dd><dt>歌手</dt><dd>{song.artist||'未知'}</dd><dt>舞者 / 社团</dt><dd>{[song.dancer,song.group].filter(Boolean).join(' / ')||'未知'}</dd><dt>分数来源</dt><dd>{source}</dd><dt>资源</dt><dd>{song.resource?<Link href={resourceHref(song.resource)}>{song.resource}</Link>:'未知'}</dd><dt>最近请求</dt><dd>{r?.local?.lastRequest?date(r.local.lastRequest):'无记录'}</dd></dl></RecordDetails></div></TableCell>
          </TableRow>;
        }):page.data?.keys.map(key=>{const r=resources[key],local=r.local;return <TableRow key={key}>
          <TableCell>{local&&<Checkbox aria-label={'选择 '+cacheLabel(local)} disabled={disabled||!local.known||local.protected} checked={selected[key]===local.stamp} onChange={(_,v)=>setSelected(previous=>{const next={...previous};if(v.checked)next[key]=local.stamp;else delete next[key];return next;})}/>}</TableCell>
          <TableCell><TableCellLayout truncate description={`${r.songs.map(s=>s.title||s.id).join(' / ')||'无关联歌曲'} · ${r.songCount} 首`}>{key}</TableCellLayout></TableCell>
          <TableCell><TableCellLayout description={r.duration==null?'时长未知':`${r.duration} 秒`}>{r.size==null?'未知':r.size<1e6?r.size.toLocaleString()+' B':mb(r.size)}</TableCellLayout></TableCell>
          <TableCell>{sharedState(r)}</TableCell><TableCell><div className="inline row-actions">{previewAction({resource:key,label:r.songs[0]?.title||key})}<Button size="small" disabled={disabled||s.batch.running||!!local||r.songCount===0} onClick={()=>download({resource:key})}>下载资源</Button>{fileActions(r)}<RecordDetails title={`资源 ${key} 的详情`}><p className="wrap">{key}</p><p>关联歌曲（{r.songCount} 首）</p><ul>{r.songs.map(song=><li key={song.id}><Link href={songHref(song.id)}>{song.title||'未知歌名'}（ID {song.id}）</Link></li>)}</ul>{r.songCount>r.songs.length&&<p>另有 {r.songCount-r.songs.length} 首；可按歌曲 ID 搜索。</p>}<p>最近请求：{local?.lastRequest?date(local.lastRequest):'无记录'}</p></RecordDetails></div></TableCell>
        </TableRow>;})}</TableBody>
      </Table></TableRegion>
      {page.data?.total===0&&<p className="empty-state">{song||resource?'当前条件下没有匹配项。':'没有匹配项，请调整搜索或更新清单。'}</p>}
      {(page.data?.total||0)>50&&<div className="toolbar pagination"><Button disabled={disabled||offset===0} onClick={()=>{setOffset(Math.max(0,offset-50));focus.ref.current?.focus();}}>上一页曲库</Button><Button disabled={disabled||offset+50>=(page.data?.total||0)} onClick={()=>{setOffset(offset+50);focus.ref.current?.focus();}}>下一页曲库</Button></div>}
      </div>
    </section>
    <div hidden={!!tool}><BatchPanel/></div>
    <section hidden={!tool} className="page-stack" {...toolFocus} tabIndex={-1} aria-label="曲库工具"><div className="toolbar"><Button onClick={()=>setTool(undefined)}>返回曲库</Button></div>
      <div hidden={tool!=='video'}><ImportPanel/></div>
      <div hidden={tool!=='history'}><VRCXImport model={history}/></div>
    </section>
  </div>;
}
