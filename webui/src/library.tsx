import {StorageConfirmationNotice} from './storage-directory';
import {useEffect,useLayoutEffect,useRef,useState} from 'react';
import {Badge,Button,Field,SearchBox,Link,MenuItem,Select,Tab,TabList,Table,TableHeader,TableRow,TableHeaderCell,TableBody,TableCell,TableCellLayout,Text,ToggleButton,Tooltip,tokens,useRestoreFocusTarget} from '@fluentui/react-components';
import {useApp,useFocusRecovery,useResource,useRunToggle} from './hooks';
import {usePageFeedback} from './page-feedback';
import {APIError,request} from './api';
import {Feedback,Notification,RecordDetails} from './ui';
import {RecordDataTable,TableText,TablePagination,paginationSummary,type RecordColumn,type TableSort} from './data-table';
import {ArrowDownloadRegular,DeleteRegular,FolderOpenRegular,HeartFilled,HeartRegular,PlayRegular,ProhibitedRegular,CircleRegular,GridRegular,TextBulletListRegular} from '@fluentui/react-icons';
import {LibraryPreview,type PreviewSelection} from './library-preview';
import {LibraryCoverCard,useLibraryCovers} from './library-covers';
import {InventoryPanel,useCacheActions} from './cache';
import {StorageBudgetNotice} from './cache-capacity';
import {BatchPanel} from './home-library';
import {ImportPanel} from './settings';
import {VRCXImport,useVRCX} from './vrcx';
import {date,mb,type Download,type LibraryPage,type LibrarySong,type LibraryResource,type SongPolicy} from './types';
import {isLibraryAnchor,readLibraryQuery,libraryAnchor,songHref,resourceHref,type LibraryQuery} from './library-query';
import {FieldIssueIcon} from './field-validation';

export {songHref,resourceHref} from './library-query';
const scoreFormat=new Intl.NumberFormat(undefined,{minimumFractionDigits:2,maximumFractionDigits:2,minimumSignificantDigits:4,maximumSignificantDigits:4,roundingPriority:'lessPrecision'});
const downloadRefreshInterval=(data:{tasks:Download[]})=>data.tasks.length?5000:0;
const policyLabels:Record<SongPolicy,string>={normal:'普通',favorite:'收藏',blocked:'屏蔽'};
function focusSection(id:string) {
  const target=document.getElementById(id);target?.focus({preventScroll:true});target?.scrollIntoView();
}
type LibraryNavigation=(anchor:string,replace?:boolean)=>void;
export function Library({visible,anchor,onNavigate}:{visible:boolean;anchor:string;onNavigate:LibraryNavigation}) {
  const [opened,setOpened]=useState(visible);
  const [browserAnchor,setBrowserAnchor]=useState(isLibraryAnchor(anchor)?anchor:'/library');
  if(visible&&isLibraryAnchor(anchor)&&anchor!==browserAnchor)setBrowserAnchor(anchor);
  useEffect(()=>{if(visible)setOpened(true);},[visible]);
  return opened?<LibraryView visible={visible} anchor={anchor} browserAnchor={browserAnchor} onNavigate={onNavigate}/>:null;
}
function LibraryView({visible,anchor,browserAnchor,onNavigate}:{visible:boolean;anchor:string;browserAnchor:string;onNavigate:LibraryNavigation}) {
  const app=useApp(),s=app.status.data!,focus=useFocusRecovery();
  const waitingForDatabase=!!s.storageConfirmation&&!s.activeSettings.databasePath&&!s.storageError;
  const query=readLibraryQuery(browserAnchor),{view,offset}=query;
  const [draft,setDraft]=useState({anchor:browserAnchor,search:query.q,filter:query.filter});
  if(draft.anchor!==browserAnchor)setDraft({anchor:browserAnchor,search:query.q,filter:query.filter});
  const {search,filter}=draft;
  const localNavigation=useRef('');
  const navigate=(change:Partial<LibraryQuery>,replace=false)=>{
    const next=libraryAnchor({...query,...change});
    setDraft(current=>({anchor:next,search:change.q??current.search,filter:change.filter??current.filter}));
    localNavigation.current=next;onNavigate(next,replace);
  };
  const [pending,setPending]=useState(false);
  const [list,toggleCovers]=useRunToggle('swd-library-list-run'),covers=!list;
  const [coverDetails,setCoverDetails]=useState<string>();
  const history=useVRCX();
  const [preview,setPreview]=useState<PreviewSelection>();
  const dialogFocusTarget=useRestoreFocusTarget();
  const pagePath='library?'+new URLSearchParams({...query,offset:String(offset)});
  useEffect(()=>setCoverDetails(undefined),[covers,view,visible,app.stopped,pagePath]);
  const page=useResource<LibraryPage>(pagePath,visible&&!app.stopped&&!waitingForDatabase,app.revision,0,true);
  const resultQuery=readLibraryQuery(page.loadedPath||pagePath);
  const searchError=page.errorCode==='invalid_library_search'?page.error.replace(/^Error: /,''):'';
  const resultUnit=resultQuery.view==='songs'?'首歌曲':'个资源';
  const resultCount=page.data&&!waitingForDatabase&&(!page.error||!!searchError)?<>共 <Text weight="bold">{page.data.total.toLocaleString()}</Text> {resultUnit}</>:'—';
  const downloads=useResource<{tasks:Download[]}>('downloads',visible&&!app.stopped,app.revision,s.batch.running?5000:downloadRefreshInterval,true);
  const feedback=usePageFeedback<{text:string;failed:boolean}>(visible?page.data?.storageID:undefined);
  // Same-query reads retain an actionable snapshot; query/save changes must wait for their own result.
  const pageUnavailable=!page.data||page.loadedPath!==pagePath||page.loadedRevision!==app.revision||waitingForDatabase||!!page.error;
  const actions=useCacheActions(page.data,page.refresh,visible,pageUnavailable||pending);
  const fileResult=useRef<HTMLElement>(null);
  useEffect(()=>{if(!visible||app.stopped)setPreview(undefined);},[visible,app.stopped]);
  useEffect(()=>setPreview(undefined),[page.data?.storageID]);
  useEffect(()=>{if(visible&&(actions.result?.error||actions.result?.entries?.length))fileResult.current?.focus();},[visible,actions.result]);
  const disabled=app.unavailable||pending||actions.pending||pageUnavailable;
  const resources=page.data?.resources||{};
  const resourceRows=page.data?.keys.map(key=>resources[key])??[];
  const nextCoverPage=offset+50<(page.data?.total||0)?'library?'+new URLSearchParams({...query,offset:String(offset+50)}):'';
  const coverImages=useLibraryCovers(page.data?.songs.map(song=>song.id).join(',')||'',nextCoverPage,visible&&view==='songs'&&covers&&!app.stopped&&!page.error&&page.loadedPath===pagePath);
  useEffect(()=>{
    if(visible&&page.loadedPath===pagePath&&page.data&&offset>=page.data.total&&offset>0)navigate({offset:0},true);
  },[visible,page.data,page.loadedPath,pagePath,offset]);
  const changeSort=(sort:TableSort)=>navigate({...sort,offset:0});
  const previousTasks=useRef({batch:s.batch.running,import:s.import.running,importID:s.import.id,imported:s.import.imported,downloads:downloads.data?.tasks});
  useEffect(()=>{
    const tasks=downloads.data?.tasks;
    const downloadEnded=tasks&&previousTasks.current.downloads?.some(previous=>!tasks.some(task=>task.id===previous.id));
    const imported=!!s.import.imported&&(previousTasks.current.importID!==s.import.id||previousTasks.current.imported!==s.import.imported);
    if(visible&&(previousTasks.current.batch&&!s.batch.running||previousTasks.current.import&&!s.import.running||imported||downloadEnded))page.refresh();
    previousTasks.current={batch:s.batch.running,import:s.import.running,importID:s.import.id,imported:s.import.imported,downloads:visible?tasks:undefined};
  },[s.batch.running,s.import.running,s.import.id,s.import.imported,downloads.data,visible,page.refresh]);
  useLayoutEffect(()=>{
    const section=['vrcx-import','video-import','batch'].includes(anchor);
    if(!visible||(!section&&(!isLibraryAnchor(anchor)||!anchor.includes('?'))))return;
    setPreview(undefined);
    if(localNavigation.current===anchor){localNavigation.current='';return;}
    const frame=requestAnimationFrame(()=>focusSection(section?anchor:'library-browser'));
    return()=>cancelAnimationFrame(frame);
  },[visible,anchor]);
  useEffect(()=>{
    const params=new URLSearchParams(browserAnchor.split('?')[1]||'');
    if(visible&&isLibraryAnchor(anchor)&&(params.has('song')||params.has('resource')))navigate({},true);
  },[visible,anchor,browserAnchor]);
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
    if(result?.review)focusSection('vrcx-import');
    if(result?.imported)feedback.setValue({failed:false,text:`已根据 VRCX 历史初始化 ${result.imported.songs} 首歌曲的优先级。`});
  }
  const sharedState=(r:LibraryResource|undefined)=> {
    if(page.data?.videoError)return <Badge appearance="tint" color="warning">未知</Badge>;
    const task=downloads.data?.tasks.find(t=>t.resource===r?.key),local=r?.local;
    if(local?.protected)return <Tooltip content="正在使用、校验或准备播放，暂不能删除。" relationship="description"><Badge appearance="tint" color="warning" tabIndex={0}>受保护</Badge></Tooltip>;
    return <Badge appearance="tint" color={task?'brand':local?'success':'informative'}>{task?'下载中':local?'已保存':'未保存'}</Badge>;
  };
  const fileActions=(r:LibraryResource|undefined,menu=false)=>r?.local&&(menu?<>
    <MenuItem icon={<FolderOpenRegular/>} disabled={disabled||!r.local.known} onClick={()=>void actions.operate('open',[r.local!])}>定位文件</MenuItem>
    <MenuItem icon={<DeleteRegular/>} disabled={disabled||!r.local.known||r.local.protected} onClick={()=>void actions.operate('delete',[r.local!])}>删除本地视频</MenuItem></>:<>
    <Tooltip content="定位文件" relationship="label"><Button size="small" appearance="subtle" icon={<FolderOpenRegular/>} aria-label="定位文件" disabled={disabled||!r.local.known} onClick={()=>void actions.operate('open',[r.local!])}/></Tooltip>
    <Tooltip content="删除本地视频" relationship="label"><Button {...dialogFocusTarget} size="small" appearance="subtle" icon={<DeleteRegular/>} aria-label="删除本地视频" disabled={disabled||!r.local.known||r.local.protected} onClick={()=>void actions.operate('delete',[r.local!])}/></Tooltip></>);
  const previewAction=(target:PreviewSelection,unavailableReason='',menu=false)=>{
    const action=menu?<MenuItem icon={<PlayRegular/>} disabled={disabled||!!page.data?.videoError||!!unavailableReason} disabledFocusable={!!unavailableReason} onClick={()=>setPreview(target)}>预览</MenuItem>:<Button {...dialogFocusTarget} size="small" appearance="subtle" icon={<PlayRegular/>} aria-label="预览" disabled={disabled||!!page.data?.videoError||!!unavailableReason} disabledFocusable={!!unavailableReason} onClick={()=>setPreview(target)}/>;
    return unavailableReason?<Tooltip content={unavailableReason} relationship="description">{action}</Tooltip>:action;
  };
  const songPreview=(song:LibrarySong):PreviewSelection=>({songID:song.id,label:song.title||'歌曲 '+song.id});
  const songPreviewReason=(song:LibrarySong)=>song.policy==='blocked'?'歌曲已屏蔽，请先解除屏蔽。':!song.resource?'尚不知道对应资源，请先更新曲库。':'';
  const songOperations=(song:LibrarySong,menu=false)=>{
    const r=resources[song.resource],unavailable=disabled||!!page.data?.videoError||s.batch.running||!!r?.local||song.policy==='blocked';
    return <>{previewAction(songPreview(song),songPreviewReason(song),menu)}{menu?<MenuItem icon={<ArrowDownloadRegular/>} disabled={unavailable} onClick={()=>download({songID:song.id})}>下载歌曲</MenuItem>:<Tooltip content="下载歌曲" relationship="label"><Button size="small" appearance="subtle" icon={<ArrowDownloadRegular/>} aria-label="下载歌曲" disabled={unavailable} onClick={()=>download({songID:song.id})}/></Tooltip>}{fileActions(r,menu)}</>;
  };
  const songPreferences=(song:LibrarySong,transparent=false)=><div className="inline row-actions" role="group" aria-label={`歌曲 ${song.id} 的偏好`}>
    <Tooltip content={song.policy==='favorite'?'取消收藏':'收藏'} relationship="description"><ToggleButton size="small" appearance={transparent?'transparent':'subtle'} aria-label="收藏" checked={song.policy==='favorite'} disabled={disabled} disabledFocusable={pending||pageUnavailable} icon={{className:'song-favorite-icon',children:song.policy==='favorite'?<HeartFilled/>:<HeartRegular/>}} onClick={()=>void save(song.id,song.policy==='favorite'?'normal':'favorite')}/></Tooltip>
    <Tooltip content={song.policy==='blocked'?'取消屏蔽':'屏蔽'} relationship="description"><ToggleButton size="small" appearance="transparent" aria-label="屏蔽" checked={song.policy==='blocked'} disabled={disabled} disabledFocusable={pending||pageUnavailable} icon={{className:'song-blocked-icon',children:song.policy==='blocked'?<ProhibitedRegular/>:<CircleRegular/>}} onClick={()=>void save(song.id,song.policy==='blocked'?'normal':'blocked')}/></Tooltip>
  </div>;
  const songDetails=(song:LibrarySong,full=false)=>{
    const r=resources[song.resource];
    return <dl className="detail-grid"><dt>名称</dt><dd>{song.title||'未知'}</dd><dt>歌手</dt><dd>{song.artist||'未知'}</dd><dt>舞者 / 社团</dt><dd>{[song.dancer,song.group].filter(Boolean).join(' / ')||'未知'}</dd>{full&&<><dt>ID</dt><dd>{song.id}</dd><dt>需求分</dt><dd>{scoreFormat.format(song.score*100)}</dd></>}<dt>分数来源</dt><dd>{song.hasDemand?'真实分数':'初始分数'}</dd>{full&&<><dt>偏好</dt><dd>{policyLabels[song.policy]}</dd><dt>本地状态</dt><dd>{sharedState(r)}</dd></>}<dt>资源</dt><dd>{song.resource?<Link className="resource-id" href={resourceHref(song.resource)}>{song.resource}</Link>:'未知'}</dd><dt>最近请求</dt><dd>{r?.local?.lastRequest?date(r.local.lastRequest):'无记录'}</dd></dl>;
  };
  const songColumns:RecordColumn<LibrarySong>[]=[
    {columnId:'id',label:'ID',sortable:true,renderCell:song=>song.id},
    {columnId:'title',label:'名称',sortable:true,className:'table-text-column table-name-column',renderCell:song=><TableText primary>{song.title||'未知名称'}</TableText>},
    {columnId:'artist',label:'歌手',className:'table-text-column',renderCell:song=><TableText>{song.artist||'未知歌手'}</TableText>},
    {columnId:'dancer',label:'舞者',className:'table-text-column',renderCell:song=><TableText>{song.dancer||'未知'}</TableText>},
    {columnId:'score',label:'需求分',sortable:true,defaultDirection:'descending',description:'反映歌曲需求程度，真实分数随时间衰减；灰色为尚无需求记录的初始分数。',cellProps:song=>({'aria-label':(song.hasDemand?'真实分数':'初始分数')+' '+scoreFormat.format(song.score*100)}),renderCell:song=>{const score=scoreFormat.format(song.score*100);return <span style={{color:song.hasDemand?tokens.colorNeutralForeground1:tokens.colorNeutralForeground3,fontVariantNumeric:'tabular-nums'}}>{score}</span>;}},
    {columnId:'policy',label:'偏好',description:'屏蔽会拒绝新的播放和下载请求。再次点击可取消。',renderCell:song=>songPreferences(song)},
    {columnId:'local',label:'本地状态',renderCell:song=>sharedState(resources[song.resource])},
    {columnId:'actions',label:'操作',renderCell:song=><div className="inline row-actions">{songOperations(song)}<RecordDetails openOnHover title={`歌曲 ${song.id} 的详情`}>{songDetails(song)}</RecordDetails></div>},
  ];
  const resourceColumns:RecordColumn<LibraryResource>[]=[
    {columnId:'resource',label:'视频资源',sortable:true,className:'table-resource-column',renderCell:r=><span className="resource-id">{r.key}</span>},
    {columnId:'songs',label:'关联歌曲',className:'table-fill-column table-name-column',renderCell:r=><TableCellLayout truncate description={`${r.songCount} 首`}>{r.songs.length?r.songs.map((s,i)=><span key={s.id}>{i>0&&' / '}<Tooltip content={s.title||s.id} relationship="label"><Link href={songHref(s.id)}>{s.title||s.id}</Link></Tooltip></span>):'无关联歌曲'}</TableCellLayout>},
    {columnId:'size',label:'大小',sortable:true,defaultDirection:'descending',renderCell:r=><TableCellLayout>{r.size==null?'未知':r.size<1e6?r.size.toLocaleString()+' B':mb(r.size)}</TableCellLayout>},
    {columnId:'local',label:'本地状态',renderCell:r=>sharedState(r)},
    {columnId:'actions',label:'操作',renderCell:r=>{const key=r.key,local=r.local;return <div className="inline row-actions">{previewAction({resource:key,label:r.songs[0]?.title||key})}<Tooltip content="下载资源" relationship="label"><Button size="small" appearance="subtle" icon={<ArrowDownloadRegular/>} aria-label="下载资源" disabled={disabled||s.batch.running||!!local||r.songCount===0} onClick={()=>download({resource:key})}/></Tooltip>{fileActions(r)}<RecordDetails openOnHover title={`资源 ${key} 的详情`} heading={<>资源 <span className="resource-id">{key}</span> 的详情</>}><p className="wrap resource-id">{key}</p><p>关联歌曲（{r.songCount} 首）</p><ul>{r.songs.map(song=><li key={song.id}><Link href={songHref(song.id)}>{song.title||'未知歌名'}（ID {song.id}）</Link></li>)}</ul>{r.songCount>r.songs.length&&<p>另有 {r.songCount-r.songs.length} 首；可按歌曲 ID 搜索。</p>}<p>最近请求：{local?.lastRequest?date(local.lastRequest):'无记录'}</p></RecordDetails></div>;}},
  ];
  const storageBudget=<StorageBudgetNotice settings={s.activeSettings} usedBytes={page.error&&!searchError||page.data?.videoError?undefined:page.data?.totalBytes} checkedAt={page.loadedAt}/>;
  return <div className="page-stack library-page">
    {visible&&!app.stopped&&preview&&page.data&&<LibraryPreview key={page.data.storageID} storageID={page.data.storageID} target={preview} onClose={()=>setPreview(undefined)}/>}
    <Notification message={feedback.value?.text} error={feedback.value?.failed}/><Notification message={!actions.result?.error&&!actions.result?.entries?.length?actions.result?.text:undefined}/>
    <Notification message={visible&&downloads.error?'下载状态暂时无法读取。':undefined} error/>
    <StorageConfirmationNotice message={s.storageConfirmation}/>{s.storageConfirmation&&<Link href="#storage-settings">确认存储设置</Link>}
    {page.data?.videoError&&s.activeSettings.videoDir&&<Feedback error>视频目录不可用，本地视频状态未知。<p className="wrap">{page.data.videoError}</p></Feedback>}
    <InventoryPanel visible={visible} waitingForDatabase={waitingForDatabase} videoError={page.data?.videoError} files={page.data?.videoError?undefined:page.data} refresh={page.refresh} loading={page.loading} loadedAt={page.loadedAt} readError={searchError?'':page.error}
      actions={<><Button onClick={()=>focusSection('video-import')}>导入视频</Button><Button onClick={()=>focusSection('batch')}>下载视频</Button></>}/>
    <section id="library-browser" className="library-browser" {...focus} tabIndex={-1}>
      {history.suggested&&<div className="content-stack" aria-label="VRCX 历史导入建议"><Feedback>
        <p>检测到 VRCX 历史，可用于安排歌曲下载与保留顺序。</p>
        <div className="actions"><Button disabled={history.locked} onClick={()=>void importHistory()}>导入历史</Button><Button disabled={history.locked} onClick={()=>void history.dismiss()}>忽略</Button></div>
        {history.pending&&<p role="status">正在处理 VRCX 历史…</p>}
      </Feedback><Feedback error>{history.message}</Feedback></div>}
      <div className="toolbar spread"><div className="inline library-view-options"><TabList aria-label="曲库视图" selectedValue={view} onTabSelect={(_,v)=>navigate({view:v.value==='resources'?'resources':'songs',offset:0,filter:'',sort:'score',direction:'descending'})}><Tab id="library-tab-songs" aria-controls="library-results" value="songs">按歌曲</Tab><Tab id="library-tab-resources" aria-controls="library-results" value="resources">按资源</Tab></TabList>
        <Tooltip content={covers?'切换到列表视图':'切换到封面视图'} relationship="label"><Button size="small" appearance="subtle" className={view==='songs'?undefined:'library-view-toggle-hidden'} icon={covers?<TextBulletListRegular/>:<GridRegular/>} aria-label={covers?'切换到列表视图':'切换到封面视图'} aria-controls="library-results" onClick={toggleCovers}/></Tooltip></div>
      <form className="inline library-search" onSubmit={e=>{e.preventDefault();navigate({q:search.trim(),filter,offset:0});if(query.q===search.trim()&&query.filter===filter&&offset===0)page.refresh();}}>
        <div className="inline library-search-query">
        <Text className="library-result-count">{resultCount}</Text>
        <Field label="搜索曲库" orientation="horizontal" className="search-field" validationState={searchError?'error':'none'} validationMessage={searchError?{children:searchError,className:'field-error-description'}:undefined} validationMessageIcon={null}>{controlProps=><Tooltip content="输入名称、ID、歌手、舞者或资源；精确查询可用 id:2367 或 md5:完整的32位资源标识。" relationship="description"><SearchBox {...controlProps} className="library-search-box" contentBefore={null} contentAfter={{className:'library-search-actions',children:searchError?<span className="library-search-error"><FieldIssueIcon label="搜索曲库" message={searchError}/></span>:null}} dismiss={{'aria-label':'清空搜索'}} maxLength={300} placeholder="名称、ID、歌手、舞者或资源" value={search} onChange={(_,v)=>setDraft(d=>({...d,search:v.value}))}/></Tooltip>}</Field>
        </div>
        <Field label="筛选" orientation="horizontal"><Select value={filter} onChange={(_,v)=>setDraft(d=>({...d,filter:v.value}))}><option value="">全部</option><option value="saved">已保存视频</option><option value="missing">缺失视频</option>{view==='songs'&&Object.entries(policyLabels).map(([key,label])=><option key={key} value={key}>{label}</option>)}</Select></Field>
        <Button appearance="primary" type="submit" disabled={app.unavailable||pending||actions.pending||waitingForDatabase||!page.error&&pageUnavailable}>查找</Button>
      </form>
      </div>
      <Feedback error>{searchError?'':page.error}</Feedback>
      <div id="library-results" role="tabpanel" aria-labelledby={'library-tab-'+view}>
      {view==='resources'&&query.sort!=='score'&&<div className="toolbar table-toolbar"><Button size="small" disabled={disabled} onClick={()=>changeSort({sort:'score',direction:'descending'})}>恢复默认排序</Button></div>}
      {actions.result&&(actions.result.error||!!actions.result.entries?.length)&&<section ref={fileResult} tabIndex={-1} aria-label="文件操作结果" className="content-stack file-results"><Feedback error={actions.result.error}>{actions.result.text}{actions.result.message&&<p className="wrap">{actions.result.message}</p>}</Feedback>
        {!!actions.result.entries?.length&&<Table size="small" aria-label="未删除的视频"><TableHeader><TableRow><TableHeaderCell>视频</TableHeaderCell><TableHeaderCell>原因</TableHeaderCell></TableRow></TableHeader><TableBody>{actions.result.entries.map(entry=><TableRow key={entry.key}><TableCell><span className={entry.label===entry.key?'resource-id':undefined}>{entry.label}</span></TableCell><TableCell>{entry.reason}</TableCell></TableRow>)}</TableBody></Table>}
      </section>}
      {view==='songs'&&covers?<ul className="library-cover-grid" aria-label="曲库封面" aria-busy={page.loading}>{page.data?.songs.map(song=><li key={song.id}>
        <LibraryCoverCard id={song.id} title={song.title||'歌曲 '+song.id} enabled={visible&&!app.stopped&&!page.error&&page.loadedPath===pagePath} open={coverDetails===song.id} onOpenChange={open=>setCoverDetails(current=>open?song.id:current===song.id?undefined:current)} cover={coverImages[song.id]} preferences={songPreferences(song,true)} details={songDetails(song,true)} operations={songOperations(song,true)} previewDisabled={disabled||!!page.data?.videoError||!!songPreviewReason(song)} previewReason={songPreviewReason(song)} onPreview={()=>setPreview(songPreview(song))}/>
      </li>)}</ul>:view==='songs'?<RecordDataTable key="songs" busy={page.loading} regionLabel="曲库表格" label="曲库歌曲" className="song-table" columns={songColumns} items={page.data?.songs} getRowId={song=>song.id} sort={query} onSortChange={changeSort} sortDisabled={disabled}/>:<RecordDataTable key="resources" busy={page.loading} regionLabel="曲库表格" label="曲库资源" columns={resourceColumns} items={resourceRows} getRowId={resource=>resource.key} sort={query} onSortChange={changeSort} sortDisabled={disabled}/>}
      {!page.error&&page.data?.total===0&&<p className="empty-state">没有匹配项，请调整搜索或更新清单。</p>}
      <TablePagination label="曲库" disabled={disabled} canPrevious={offset>0} canNext={offset+50<(page.data?.total||0)}
        previous={()=>{navigate({offset:Math.max(0,offset-50)});focus.ref.current?.focus();focus.ref.current?.scrollIntoView({block:'start'});}} next={()=>{navigate({offset:offset+50});focus.ref.current?.focus();focus.ref.current?.scrollIntoView({block:'start'});}}
        summary={waitingForDatabase?'待确认存储':page.error&&!searchError?'曲库读取失败，请刷新重试。':!page.data?searchError?'查询格式有误，请修改后重试。':'正在读取曲库…':paginationSummary(resultQuery.offset/50+1,page.data.total,50,resultUnit)}/>
      </div>
    </section>
    <VRCXImport model={history}/>
    <ImportPanel storageBudget={storageBudget}/>
    <BatchPanel storageBudget={storageBudget}/>
  </div>;
}
