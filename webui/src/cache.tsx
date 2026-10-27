import { useEffect, useRef, useState } from 'react';
import { Button, Link, Text, Tooltip } from '@fluentui/react-components';
import { ArrowSync20Regular } from '@fluentui/react-icons';
import { request, APIError } from './api';
import { useApp, useResource } from './hooks';
import { usePageFeedback } from './page-feedback';
import { Notification } from './ui';
import { type CacheEntry, type LibraryPage, type Inventory, date, dated, gib } from './types';

export const cacheLabel = (entry: CacheEntry) => entry.songs.length ? entry.songs.map(s => `${s.title || '未知歌名'}（ID ${s.id}）`).join(' / ') : '未知歌名 · 未关联歌曲';
const resultNames: Record<string,string> = {protected:'正在使用，暂不能删除',missing:'文件已不存在',unknown:'未知文件，未删除',changed:'文件或歌曲关联已变化，请刷新后重选',unsafe:'目录不安全，未删除',failed:'删除失败，请检查文件权限'};
export function useCacheActions(snapshot: Pick<LibraryPage,'storageID'|'resources'>|undefined, refresh:()=>void, visible:boolean, disabled=false) {
  const app=useApp(), [pending,setPending]=useState(false);
  const feedback=usePageFeedback<{text:string;error?:boolean;message?:string;entries?:{key:string;label:string;reason:string}[]}>(visible?snapshot?.storageID:undefined),setResult=feedback.setValue;
  const currentPage=useRef(snapshot); currentPage.current=disabled?undefined:snapshot;
  async function operate(action: 'delete' | 'open', picked: CacheEntry[]) {
    const pickedPage = snapshot;
    if (!pickedPage || disabled || app.unavailable || pending) return;
    if (action === 'delete') {
      if (!await app.confirm('确认删除选中的本地视频文件？', <><p>删除无法撤销；共用该文件的歌曲需要重新下载。歌曲资料和统计保留，正在使用的文件会跳过。</p><ul>{picked.map(e => <li key={e.key}>{cacheLabel(e)} · {gib(e.bytes)} · 影响 {e.songCount} 首已知关联歌曲</li>)}</ul></>, '确认删除')) return;
      const current = currentPage.current;
      if (!current || current.storageID !== pickedPage.storageID || picked.some(entry => {
        const latest = current.resources[entry.key]?.local;
        return !latest?.known || latest.protected || latest.stamp !== entry.stamp || latest.songCount !== entry.songCount || cacheLabel(latest) !== cacheLabel(entry);
      })) { setResult({text:'文件或歌曲关联已变化，请刷新后重新选择并确认。',error:true}); return; }
    }
    setPending(true);setResult(undefined);
    try {
      const response = await request<{key:string;result:string}[]>('cache/' + action,{storageID:pickedPage.storageID,entries:picked.map(e => ({key:e.key,stamp:e.stamp}))},AbortSignal.timeout(35000));
      if(action==='open')setResult({text:'已请求系统打开视频位置。'});
      else {
        const deleted=response.filter(r=>r.result==='deleted').length;
        const skipped=response.filter(r=>r.result==='protected'||r.result==='missing').length;
        const failed=response.length-deleted-skipped;
        setResult({text:`已删除 ${deleted} 个${skipped?`，跳过 ${skipped} 个`:''}${failed?`，未删除 ${failed} 个`:''}`,error:failed>0,
          entries:response.filter(r=>r.result!=='deleted').map(r=>{const entry=picked.find(e=>e.key===r.key)!;return {key:r.key,label:entry.songs.length?cacheLabel(entry):r.key,reason:resultNames[r.result]||'结果未知，请刷新后核对'};})});
      }
    } catch (e) { setResult({text:action==='open'?'无法打开视频位置。':e instanceof APIError?'删除未完成。':'删除结果未确认。',error:true,message:e instanceof APIError?e.message:'请刷新文件列表核对，勿直接重复操作。'}); }
    finally { setPending(false); if (action === 'delete') { refresh(); } }
  }

  return {operate,pending,result:feedback.value};
}
const inventoryRefreshInterval=(data:Inventory)=>data.scanning?1000:0;
export function InventoryPanel({visible,files,videoError,refresh,loading,loadedAt,readError}: {visible:boolean;videoError?:string;files?:Pick<LibraryPage,'fileCount'|'totalBytes'>;refresh:()=>void;loading:boolean;loadedAt?:number;readError?:string}) {
  const app = useApp(), s=app.status.data!;
  const [lastCheckedAt,setLastCheckedAt]=useState<number>();
  useEffect(()=>{
    if(files&&!videoError&&!readError&&loadedAt!==undefined)setLastCheckedAt(loadedAt);
  },[files,videoError,readError,loadedAt]);
  const inventory = useResource<Inventory>('inventory',visible && !app.stopped,app.revision,inventoryRefreshInterval,true), v = inventory.data;
  const catalogCheck=s.upstreamMonitor?.finished, previousCheck=useRef(catalogCheck);
  useEffect(()=>{
    if(visible&&dated(catalogCheck)&&previousCheck.current!==catalogCheck){inventory.refresh();refresh();}
    previousCheck.current=catalogCheck;
  },[visible,catalogCheck,inventory.refresh,refresh]);
  const previous=useRef<Inventory|undefined>(undefined);
  useEffect(()=>{
    if(!visible){previous.current=undefined;return;}
    if(v&&previous.current&&(previous.current.updated!==v.updated||previous.current.scanning&&!v.scanning))refresh();
    previous.current=v;
  },[visible,v,refresh]);
  const scanning=!!v?.scanning&&!inventory.error;
  const catalogBusy=scanning||inventory.loading;
  const known = !videoError && v && dated(v.updated) && v.coverageKnown && v.totalSongs > 0;
  return <section id="overview" className="library-summary" aria-label="曲库概览">
    <div className="inline summary-values">
      <div role="group" aria-label="本地视频">
        <div className="inline"><span className="secondary">本地视频</span><Tooltip content={<>检查本地视频<Text block size={200}>最后检查时间：{date(lastCheckedAt)}</Text></>} relationship="description"><Button size="small" appearance="subtle" icon={<ArrowSync20Regular className="library-refresh-icon" aria-hidden="true"/>} aria-label="检查本地视频" aria-busy={loading} disabled={app.unavailable||loading} disabledFocusable={loading} onClick={refresh}/></Tooltip></div>
        <Text block size={500} weight="semibold">{files?`${files.fileCount} 个 · ${gib(files.totalBytes)}`:'—'}</Text>
        {(readError||videoError)&&<Text block size={200} role="status">检查失败</Text>}
      </div>
      <div role="group" aria-label="曲库覆盖率">
        <div className="inline"><Tooltip content={'清单版本：'+(v?.catalog?.revision||'未知')} relationship="description"><span className="secondary" tabIndex={0}>曲库覆盖率</span></Tooltip><Tooltip content={<>更新曲库<Text block size={200}>最后检查时间：{date(v?.updated)}</Text></>} relationship="description"><Button size="small" appearance="subtle" icon={<ArrowSync20Regular className="library-refresh-icon" aria-hidden="true"/>} aria-label="更新曲库" aria-busy={catalogBusy} disabled={app.unavailable||catalogBusy} disabledFocusable={catalogBusy||app.busy} onClick={()=>void app.action('inventory/scan')}/></Tooltip></div>
        <Text block size={500} weight="semibold">{known?`${(100*v.coveredSongs/v.totalSongs).toFixed(1)}%`:'—'}</Text>
      </div>
      <div><Link href="#storage-settings">视频存储预算</Link><Text block size={400}>{s.activeSettings.videoStorageMode==='unlimited'?'不限制':s.activeSettings.videoStorageMode==='none'?'不保留':gib(s.activeSettings.maxCacheBytes)}</Text></div>
    </div>
    <Notification message={visible?(inventory.error?'曲库统计暂时不可用。':v?.error||v?.catalog?.error||s.actionErrors?.inventory):undefined} error/>
  </section>;
}
