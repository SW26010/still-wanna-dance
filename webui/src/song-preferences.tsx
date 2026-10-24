import { useEffect, useRef, useState } from 'react';
import { Button, Divider, Field, Input, List, ListItem, Select } from '@fluentui/react-components';
import { APIError, request } from './api';
import { useApp, useFocusRecovery, useResource } from './hooks';
import { Details, Feedback, Panel } from './ui';
import type { SongPolicy, SongPage } from './types';

const labels: Record<SongPolicy,string> = {normal:'普通',favorite:'收藏',blocked:'屏蔽'};
export function SongPreferences({visible}:{visible:boolean}) {
  const app=useApp(), focus=useFocusRecovery(), storage=app.status.data!.activeSettings.storageDir;
  const [search,setSearch]=useState(''), [filter,setFilter]=useState(''), [offset,setOffset]=useState(0);
  const [query,setQuery]=useState({q:'',policy:''}), [id,setID]=useState(''), [policy,setPolicy]=useState<SongPolicy>('favorite');
  const [pending,setPending]=useState(false), [feedback,setFeedback]=useState(''), [failed,setFailed]=useState(false);
  const page=useResource<SongPage>('songs?'+new URLSearchParams({...query,offset:String(offset)}),visible&&!app.stopped,app.revision,0);
  const epoch=useRef(0), priorStorage=useRef(storage);
  useEffect(()=>{epoch.current++;setFeedback('');},[visible,storage]);
  useEffect(()=>{
    if(priorStorage.current!==storage){priorStorage.current=storage;setOffset(0);page.refresh();}
  },[storage,page.refresh]);
  useEffect(()=>{
    if(page.data&&offset>0&&offset>=page.data.total)setOffset(0);
  },[page.data,offset]);
  const disabled=app.unavailable||pending||page.loading||!page.data;
  async function save(songID:string,next:SongPolicy) {
    if(disabled||!page.data)return;
    const started=epoch.current;
    setPending(true);setFeedback('');
    try {
      await request('songs',{storageID:page.data.storageID,id:songID,policy:next},AbortSignal.timeout(35000));
      if(started===epoch.current){setFailed(false);setFeedback(`歌曲 ${songID} 已设为${labels[next]}。`);}
      app.refresh();
    } catch(error) {
      if(started===epoch.current){setFailed(true);setFeedback(error instanceof APIError?error.message:'保存结果未确认，请刷新歌曲列表核对。');}
    } finally {setPending(false);page.refresh();}
  }
  return <Panel id="song-preferences" title="歌曲偏好" {...focus}>
    <p>收藏只做标记，不开始下载；开启「优先保留收藏视频」时，完整收藏视频不因预算被淘汰。屏蔽会拒绝新的播放请求，并跳过预加载和下载补齐；已开始的任务继续。</p>
    <Details title="共享视频与空间占用"><p>多首歌曲共享视频时，只要仍有未屏蔽的关联歌曲，就保留其正常使用资格。开启「优先保留收藏视频」时，收藏视频计入同一存储预算，但可以超过预算，包括选择「不保留」时。收藏不保证临时视频块一定收齐，删除或丢失文件不会自动补下载。</p></Details>
    <form className="actions" onSubmit={e=>{e.preventDefault();setOffset(0);setQuery({q:search,policy:filter});page.refresh();}}>
      <Field label="查找歌曲"><Input type="search" maxLength={300} placeholder="歌名或歌曲 ID" value={search} onChange={(_,v)=>setSearch(v.value)}/></Field>
      <Field label="偏好筛选"><Select value={filter} onChange={(_,v)=>setFilter(v.value)}><option value="">全部</option>{Object.entries(labels).map(([value,label])=><option key={value} value={value}>{label}</option>)}</Select></Field>
      <Button type="submit" disabled={pending||app.unavailable}>查找歌曲</Button><Button disabled={pending||app.unavailable} onClick={page.refresh}>刷新歌曲列表</Button>
    </form>
    <Details title="按歌曲 ID 设置"><form className="actions" onSubmit={e=>{e.preventDefault();void save(id,policy);}}>
      <Field label="歌曲 ID" required><Input inputMode="numeric" pattern="[1-9][0-9]*" maxLength={19} required value={id} onChange={(_,v)=>setID(v.value)}/></Field>
      <Field label="歌曲偏好"><Select value={policy} onChange={(_,v)=>{if(v.value==='normal'||v.value==='favorite'||v.value==='blocked')setPolicy(v.value);}}>{Object.entries(labels).map(([value,label])=><option key={value} value={value}>{label}</option>)}</Select></Field>
      <Button type="submit" disabled={disabled}>保存标记</Button>
    </form></Details>
    <Feedback error>{page.error&&'歌曲列表读取失败，请重试。'}</Feedback><Feedback error={failed}>{feedback}</Feedback>
    <p role="status">{page.loading?'正在读取歌曲…':page.data?.total?`共 ${page.data.total} 首 · 第 ${offset/50+1} 页`:'没有匹配的歌曲；可按歌曲 ID 设置标记，或更新本地曲库清单。'}</p>
    <List navigationMode="composite" className="records" aria-label="歌曲偏好列表">{page.data?.entries.map((song,i)=><ListItem key={song.id}><div role="gridcell" className="record-content">
      {i>0&&<Divider className="record-divider"/>}<p>{song.title||'未知歌名'} · ID {song.id}</p><p>本地视频：{song.saved?'文件存在':'未保存完整视频'}</p>
      <Field label={`歌曲 ${song.id} 的偏好`}><Select value={song.policy} disabled={disabled} onChange={(_,v)=>{if(v.value==='normal'||v.value==='favorite'||v.value==='blocked')void save(song.id,v.value);}}>{Object.entries(labels).map(([value,label])=><option key={value} value={value}>{label}</option>)}</Select></Field>
    </div></ListItem>)}</List>
    {(page.data?.total||0)>50&&<div className="actions"><Button disabled={disabled||offset===0} onClick={()=>setOffset(Math.max(0,offset-50))}>上一页歌曲</Button><Button disabled={disabled||offset+50>=(page.data?.total||0)} onClick={()=>setOffset(offset+50)}>下一页歌曲</Button></div>}
  </Panel>;
}
