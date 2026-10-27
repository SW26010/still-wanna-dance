import { useRef, useState } from 'react';
import { Button, Link, Text } from '@fluentui/react-components';
import { request, APIError } from './api';
import { useApp, useResource } from './hooks';
import { usePageFeedback } from './page-feedback';
import { Hint, Notification, RecordDetails } from './ui';
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
export function InventoryPanel({visible,files,videoError,refresh,loading}: {visible:boolean;videoError?:string;files?:Pick<LibraryPage,'fileCount'|'totalBytes'>;refresh:()=>void;loading:boolean}) {
  const app = useApp(), s=app.status.data!, t = s.traffic || {};
  const inventory = useResource<Inventory>('inventory',visible && !app.stopped,app.revision), v = inventory.data;
  const known = !videoError && v && dated(v.updated) && v.coverageKnown && v.totalSongs > 0;
  return <section id="overview" className="library-summary" aria-label="曲库概览">
    <div className="inline summary-values">
      <div><span className="secondary">本地视频</span><Text block size={500} weight="semibold">{files?`${files.fileCount} 个 · ${gib(files.totalBytes)}`:'—'}</Text></div>
      <div><span className="secondary">曲库覆盖率<Hint label="覆盖率说明">已保存视频覆盖的歌曲比例，共享视频按歌曲分别计数。</Hint></span><Text block size={500} weight="semibold">{known?`${(100*v.coveredSongs/v.totalSongs).toFixed(1)}%`:'—'}</Text></div>
      <div><span className="secondary">视频存储预算</span><Text block size={400}>{s.activeSettings.videoStorageMode==='unlimited'?'不限制':s.activeSettings.videoStorageMode==='none'?'不保留':gib(s.activeSettings.maxCacheBytes)}</Text></div>
    </div>
    <div className="inline"><Button disabled={app.unavailable||!v||v.scanning} onClick={()=>void app.action('inventory/scan')}>{v?.scanning?'正在更新…':'更新清单并统计覆盖率'}</Button><Button disabled={app.unavailable||loading} onClick={refresh}>刷新曲库与本地占用</Button><Link href="#storage-settings">存储设置</Link>
      <RecordDetails title="曲库统计"><dl className="detail-grid"><dt>清单检查</dt><dd>{date(v?.catalog?.checkedAt)}</dd><dt>清单版本</dt><dd>{v?.catalog?.revision||'未知'}</dd><dt>覆盖率更新</dt><dd>{date(v?.updated)} · {v?.coveredSongs||0} / {v?.totalSongs||0} 首{v?.catalogRevision&&v.catalogRevision!==v.catalog?.revision&&'（清单已变化，请刷新）'}</dd><dt>本地视频命中</dt><dd>{t.hits||0} 次 · {t.hitRate==null?'—':t.hitRate.toFixed(1)+'%'}</dd><dt>节省下载流量</dt><dd>{gib(t.savedBytes)}<Text block size={200}>按本地命中计量，未扣除预下载流量。</Text></dd><dt>请求</dt><dd>成功 {t.requests||0} · 未命中 {t.misses||0}</dd><dt>播放传输</dt><dd>{t.playbackTransfers||0}<Text block size={200}>同歌请求按 30 秒去重，非游戏播放次数。</Text></dd></dl></RecordDetails>
    </div>
    <Notification message={visible?(inventory.error?'曲库统计暂时不可用。':v?.error||v?.catalog?.error||t.error||s.actionErrors?.inventory):undefined} error/>
  </section>;
}
