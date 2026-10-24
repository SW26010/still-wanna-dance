import {useEffect,useLayoutEffect,useState} from 'react';
import {Badge,Button,Checkbox,Divider,Field,Input,Link,List,ListItem,Select,Tab,TabList} from '@fluentui/react-components';
import {useApp,useFocusRecovery,useResource} from './hooks';
import {APIError,request} from './api';
import {Details,Feedback,Panel,Progress} from './ui';
import {InventoryPanel,useCacheActions,cacheLabel} from './cache';
import {BatchPanel} from './home-library';
import {ImportPanel} from './settings';
import {SongPreferenceTools,policyLabels} from './song-preferences';
import {VRCXImport} from './vrcx';
import {mb,type Download,type LibraryPage,type LibraryResource,type SongPolicy} from './types';

export const songHref=(id:string)=>'#/library?song='+encodeURIComponent(id);
export const resourceHref=(key:string)=>'#/library?resource='+encodeURIComponent(key);
export function Library({visible,anchor}:{visible:boolean;anchor:string}) {
  const params=new URLSearchParams(anchor.split('?')[1]||''), song=params.get('song')||'', resource=params.get('resource')||'';
  return <LibraryView key={song+':'+resource} visible={visible} anchor={anchor} song={song} resource={resource}/>;
}
function LibraryView({visible,anchor,song,resource}:{visible:boolean;anchor:string;song:string;resource:string}) {
  const app=useApp(),s=app.status.data!,focus=useFocusRecovery();
  const [view,setView]=useState(resource||anchor==='cache'||anchor==='/cache'?'resources':'songs'),[search,setSearch]=useState(''),[sort,setSort]=useState('score'),[filter,setFilter]=useState('');
  const [query,setQuery]=useState({q:'',sort:'score',filter:''}),[offset,setOffset]=useState(0),[selected,setSelected]=useState<string[]>([]);
  const [pending,setPending]=useState(false),[message,setMessage]=useState(''),[failed,setFailed]=useState(false),[importOpen,setImportOpen]=useState(anchor==='video-import');
  const [historyOpen,setHistoryOpen]=useState(false);
  const page=useResource<LibraryPage>('library?'+new URLSearchParams({...query,view,song,resource,offset:String(offset)}),visible&&!app.stopped,app.revision,10000);
  const downloads=useResource<{tasks:Download[]}>('downloads',visible&&!app.stopped,app.revision);
  const actions=useCacheActions(page.data,page.refresh,page.loading||pending);
  const disabled=app.unavailable||pending||actions.pending||page.loading||!page.data;
  const resources=page.data?.resources||{}, entries=Object.values(resources).flatMap(r=>r.local?[r.local]:[]), chosen=entries.filter(e=>selected.includes(e.key)), eligible=entries.filter(e=>e.known&&!e.protected);
  useEffect(()=>{setSelected([]);if(page.data&&offset>=page.data.total&&offset>0)setOffset(0);},[page.data,offset]);
  useEffect(()=>{if(visible)page.refresh();},[s.batch.running,s.import.running,visible,page.refresh]);
  useLayoutEffect(()=>{if(visible&&anchor==='video-import'){setImportOpen(true);const frame=requestAnimationFrame(()=>{const target=document.getElementById('video-import');target?.focus();target?.scrollIntoView();});return()=>cancelAnimationFrame(frame);}},[visible,anchor]);
  useLayoutEffect(()=>{if(visible&&['cache','/cache','song-preferences'].includes(anchor)){setView(anchor==='song-preferences'?'songs':'resources');setOffset(0);setFilter('');setQuery(q=>({...q,filter:''}));const frame=requestAnimationFrame(()=>focus.ref.current?.focus());return()=>cancelAnimationFrame(frame);}},[visible,anchor]);
  async function save(id:string,policy:SongPolicy) {
    if(disabled||!page.data)return;
    setPending(true);setMessage('');
    try {await request('songs',{storageID:page.data.storageID,id,policy},AbortSignal.timeout(35000));setFailed(false);setMessage(`歌曲 ${id} 已设为${policyLabels[policy]}。`);app.refresh();}
    catch(e){setFailed(true);setMessage(e instanceof APIError?e.message:'保存结果未确认，请刷新曲库核对。');}
    finally{setPending(false);page.refresh();}
  }
  const download=(target:{songID?:string;resource?:string})=>void app.action('batch/download',{storageID:page.data?.storageID,...target});
  const sharedState=(r:LibraryResource|undefined)=> {
    const task=downloads.data?.tasks.find(t=>t.resource===r?.key),local=r?.local;
    return <div className="content-stack"><div className="actions"><Badge appearance="tint" color={local?'success':'informative'}>{local?'完整本地视频已保存':'未保存完整视频'}</Badge>{local?.protected&&<Badge color="warning">受保护（正在使用／校验／待播）</Badge>}</div>
      {task&&<><p>{({cache_check:'检查本地视频',upstream_headers:'等待上游',download:'下载中',range_download:'分段下载中',hash:'校验完整文件',publish:'发布文件',index:'更新索引'}[task.stage]||'准备中')} · {task.size>0?`${Math.min(100,100*task.bytes/task.size).toFixed(1)}%`:'进度未知'}</p><Progress label="资源下载字节进度" value={task.bytes} max={task.size}/><p>{mb(task.bytes)} / {task.size>0?mb(task.size):'未知'} · {mb(task.bytesPerSecond)}/s；完整校验并发布后才显示已保存。</p></>}
      {local&&<p>{local.state}</p>}</div>;
  };
  const fileActions=(r:LibraryResource|undefined)=>r?.local&&<div className="actions"><Button disabled={disabled||!r.local.known||r.local.protected} onClick={()=>void actions.operate('delete',[r.local!])}>删除本地视频</Button><Button disabled={disabled||!r.local.known} onClick={()=>void actions.operate('open',[r.local!])}>定位文件</Button></div>;
  return <><InventoryPanel visible={visible} files={page.data} refresh={page.refresh} loading={page.loading}/><Panel id="library-browser" title="歌曲与视频资源" {...focus}>
    {(song||resource)&&<Feedback>正在查看历史链接中的{song?'歌曲 ID '+song:'资源 '+resource}。这里展示当前曲库对象，历史记录保持不变。<Link href="#/library">返回完整曲库</Link></Feedback>}
    <TabList aria-label="曲库视图" selectedValue={view} onTabSelect={(_,v)=>{setView(String(v.value));setOffset(0);setSort('score');setFilter('');setQuery(q=>({...q,sort:'score',filter:''}));}}><Tab value="songs" disabled={!!resource}>按歌曲</Tab><Tab value="resources" disabled={!!song}>按资源</Tab></TabList>
    <form className="actions" onSubmit={e=>{e.preventDefault();setOffset(0);setQuery({q:search,sort,filter});page.refresh();}}>
      <Field label="搜索曲库"><Input type="search" maxLength={300} placeholder="名称、ID、歌手、舞者或资源" value={search} onChange={(_,v)=>setSearch(v.value)}/></Field>
      <Field label="排序"><Select value={sort} onChange={(_,v)=>setSort(v.value)}><option value="score">需求分数优先</option><option value="title">名称顺序</option>{view==='songs'?<option value="id">歌曲 ID 从新到旧</option>:<option value="size">大小从大到小</option>}</Select></Field>
      <Field label="筛选"><Select value={filter} onChange={(_,v)=>setFilter(v.value)}><option value="">全部</option><option value="saved">已保存完整视频</option><option value="missing">缺失完整视频</option>{view==='songs'&&Object.entries(policyLabels).map(([key,label])=><option key={key} value={key}>{label}</option>)}</Select></Field>
      <Button type="submit" disabled={disabled}>查找</Button><Button disabled={disabled} onClick={()=>void actions.operate('open',[])}>打开视频目录</Button>
    </form><Feedback error>{page.error&&'曲库读取失败，请重试。'}</Feedback><Feedback error>{downloads.error&&'下载进度暂时无法读取；未将缺失进度显示为零。'}</Feedback><Feedback error={failed}>{message}</Feedback><Feedback>{actions.result}</Feedback>
    <p role="status">{page.loading&&!page.data?'正在读取曲库…':`共 ${page.data?.total||0} ${view==='songs'?'首歌曲':'个资源'} · 第 ${offset/50+1} 页`}</p>
    {page.data?.total===0&&<p>{song||resource?'该对象不在当前曲库列表范围内；历史身份仍保留，未替换为其他对象。':'没有匹配项。可更新歌曲清单或调整搜索和筛选。'}</p>}
    <Details title="列表与需求分数说明"><p>每页最多 50 项，需求分数随时间衰减，与下载补齐排序和保留取舍共用规则；屏蔽由独立偏好决定。资源视图包括本地已有或当前已关联歌曲的资源。未知大小和时长不按零计算。</p><p>删除只移除本地视频，资料、偏好、统计与关联保留。共享视频只保存一份，删除会影响所有关联歌曲。正在使用或准备播放的文件受保护。</p></Details>
    {view==='songs'&&<SongPreferenceTools disabled={disabled} save={(id,p)=>void save(id,p)}/>}
    {view==='resources'&&!!entries.length&&<div className="actions"><Button disabled={disabled||!eligible.length} onClick={()=>setSelected(eligible.every(e=>selected.includes(e.key))?[]:eligible.map(e=>e.key))}>选择本页可删除项</Button><Button disabled={disabled||!chosen.length} onClick={()=>void actions.operate('delete',chosen)}>删除选中视频</Button><span>已选择 {chosen.length} 个文件</span></div>}
    <List navigationMode="composite" aria-label={view==='songs'?'曲库歌曲':'曲库资源'} className="records">{view==='songs'?page.data?.songs.map((song,i)=>{
      const r=resources[song.resource];return <ListItem key={song.id}><div role="gridcell" className="record-content">{i>0&&<Divider className="record-divider"/>}<p><strong>{song.title||'未知名称'}</strong> · ID {song.id}</p><p>歌手：{song.artist||'未知'} · 舞者／社团：{[song.dancer,song.group].filter(Boolean).join('／')||'未知'}</p><p>需求分数：{song.score.toLocaleString(undefined,{maximumFractionDigits:2})}</p><p className="wrap">资源：{song.resource?<Link href={resourceHref(song.resource)}>{song.resource}</Link>:'未知'}</p>{sharedState(r)}
      <div className="actions"><Field label={`歌曲 ${song.id} 的偏好`}><Select value={song.policy} disabled={disabled} onChange={(_,v)=>void save(song.id,v.value as SongPolicy)}>{Object.entries(policyLabels).map(([value,label])=><option key={value} value={value}>{label}</option>)}</Select></Field><Button disabled={disabled||s.batch.running||!!r?.local||song.policy==='blocked'} onClick={()=>download({songID:song.id})}>下载歌曲</Button>{fileActions(r)}</div></div></ListItem>;
    }):page.data?.keys.map((key,i)=>{const r=resources[key],local=r.local;return <ListItem key={key}><div role="gridcell" className="record-content">{i>0&&<Divider className="record-divider"/>}<p className="wrap"><strong>资源 {key}</strong></p><p>大小：{r.size==null?'未知':r.size<1e6?r.size.toLocaleString()+' B':mb(r.size)} · 时长：{r.duration==null?'未知':`${r.duration} 秒`}</p><p>关联歌曲（{r.songCount} 首）：{r.songs.length?r.songs.map((song,i)=><span key={song.id}>{i>0?'、':''}<Link href={songHref(song.id)}>ID {song.id}</Link></span>):'无当前关联'}</p>{r.songCount>r.songs.length&&<p>展示前 {r.songs.length} 首，另有 {r.songCount-r.songs.length} 首共享此资源；可按歌曲 ID 搜索。</p>}{sharedState(r)}
      <div className="actions">{local&&<Checkbox aria-label={'选择 '+cacheLabel(local)} label="选择此本地视频" disabled={disabled||!local.known||local.protected} checked={selected.includes(key)} onChange={(_,v)=>setSelected(v.checked?[...selected,key]:selected.filter(k=>k!==key))}/>}<Button disabled={disabled||s.batch.running||!!local||r.songCount===0} onClick={()=>download({resource:key})}>下载资源</Button>{fileActions(r)}</div></div></ListItem>;})}</List>
    {(page.data?.total||0)>50&&<div className="actions"><Button disabled={disabled||offset===0} onClick={()=>{setOffset(Math.max(0,offset-50));focus.ref.current?.focus();}}>上一页曲库</Button><Button disabled={disabled||offset+50>=(page.data?.total||0)} onClick={()=>{setOffset(offset+50);focus.ref.current?.focus();}}>下一页曲库</Button></div>}
  </Panel><BatchPanel/><div><Details title="导入外部曲库" keepMounted expanded={importOpen} onToggle={setImportOpen}><ImportPanel/></Details></div><Details title="导入 VRCX 历史需求统计" keepMounted onToggle={setHistoryOpen}>{historyOpen&&<VRCXImport/>}</Details></>;
}
