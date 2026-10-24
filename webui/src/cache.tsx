import { useRef, useState } from 'react';
import { Button, Link, Subtitle2 } from '@fluentui/react-components';
import { request, APIError } from './api';
import { useApp, useResource } from './hooks';
import { Details, Feedback, Panel } from './ui';
import { type CacheEntry, type CachePage, type Inventory, date, dated, gib } from './types';

export const cacheLabel = (entry: CacheEntry) => entry.songs.length ? entry.songs.map(s => `${s.title || '未知歌名'}（ID ${s.id}）`).join(' / ') : '未知歌名 · 未关联歌曲';
const resultNames: Record<string,string> = {deleted:'已删除',protected:'受保护，已跳过',missing:'文件已不存在',unknown:'未知文件，未删除',changed:'文件或歌曲关联已变化，请刷新后重选',unsafe:'目录不安全，未删除',failed:'操作失败，未删除'};
export function useCacheActions(snapshot: Pick<CachePage,'storageID'>|undefined, refresh:()=>void, disabled=false) {
  const app=useApp(), [pending,setPending]=useState(false), [result,setResult]=useState('');
  const currentPage=useRef(snapshot); currentPage.current=snapshot;
  async function operate(action: 'delete' | 'open', picked: CacheEntry[]) {
    const pickedPage = snapshot;
    if (!pickedPage || disabled || app.unavailable || pending) return;
    if (action === 'delete') {
      if (!await app.confirm('确认删除选中的本地视频文件？', <><p>删除无法撤销；共用该文件的歌曲需要重新下载。歌曲资料和统计保留，正在使用的文件会跳过。</p><ul>{picked.map(e => <li key={e.key}>{cacheLabel(e)} · {gib(e.bytes)} · 影响 {e.songCount} 首已知关联歌曲</li>)}</ul></>, '确认删除')) return;
      if (currentPage.current !== pickedPage) { setResult('文件列表已刷新，请重新选择并确认。'); return; }
    }
    setPending(true);
    try {
      const response = await request<{key:string;result:string}[]>('cache/' + action,{storageID:pickedPage.storageID,entries:picked.map(e => ({key:e.key,stamp:e.stamp}))},AbortSignal.timeout(35000));
      setResult(action === 'open' ? '已请求系统打开视频位置。' : response.map(r => `${cacheLabel(picked.find(e => e.key === r.key)!)}：${resultNames[r.result] || '结果未知'}`).join('；'));
    } catch (e) { setResult(e instanceof APIError ? e.message : '操作响应未确认，请刷新文件列表核对，勿直接重复删除。'); }
    finally { setPending(false); if (action === 'delete') { refresh(); } }
  }

  return {operate,pending,result};
}
export function InventoryPanel({visible,files,refresh,loading}: {visible:boolean;files?:Pick<CachePage,'fileCount'|'totalBytes'>;refresh:()=>void;loading:boolean}) {
  const app = useApp(), s=app.status.data!, t = s.traffic || {};
  const inventory = useResource<Inventory>('inventory',visible && !app.stopped,app.revision), v = inventory.data;
  const known = v && dated(v.updated) && v.coverageKnown && v.totalSongs > 0;
  return <Panel id="overview" title="曲库概览"><p>歌曲清单检查时间：{date(v?.catalog?.checkedAt)}</p><p role="status">{v?.scanning?'正在更新清单并统计…':v?.catalog?.message||'可更新清单，检查本地覆盖情况。'}</p><div className="actions"><Button disabled={app.unavailable||!v||v.scanning} onClick={()=>void app.action('inventory/scan')}>更新清单并统计覆盖率</Button><Button disabled={app.unavailable||loading} onClick={refresh}>刷新曲库与本地占用</Button></div>    <p>{files ? `${files.fileCount} 个视频文件 · 占用 ${gib(files.totalBytes)} · 当前视频存储预算：${s.activeSettings.videoStorageMode === 'unlimited' ? '不限制' : s.activeSettings.videoStorageMode === 'none' ? s.activeSettings.retainFavorites?'0 GiB（仅收藏可突破预算保留）':'0 GiB' : gib(s.activeSettings.maxCacheBytes)}` : '视频占用待读取'}</p><p>收藏视频计入实际占用，{s.activeSettings.retainFavorites?'可以超过预算':'按普通预算处理'}；收藏标记不代表文件已保存。</p>{files?.fileCount===0&&s.activeSettings.videoStorageMode==='none'&&<><p>当前选择「不保留」：适合边播边下载，普通视频不会长期保留，{s.activeSettings.retainFavorites?'收藏的完整视频仍可保存。':'收藏视频也按普通预算处理。'}房间队列仍可自动提前准备。</p><Link href="#queue">自动准备房间队列</Link></>}<Subtitle2 as="h3">曲库覆盖率</Subtitle2><p>{known ? `${(100*v.coveredSongs/v.totalSongs).toFixed(1)}% · ${v.coveredSongs} / ${v.totalSongs} 首曲目已覆盖` : '覆盖率待刷新'}</p>
<Details title="覆盖率与复用详情"><Details title="本地视频复用统计"><Subtitle2 as="h3">请求统计</Subtitle2><p>当前存储目录累计</p><p>已命中次数 {t.hits || 0} · 请求命中率 {t.hitRate == null ? '—' : t.hitRate.toFixed(1)+'%'} · 本地视频命中节省流量 {gib(t.savedBytes)} · 播放传输次数（30 秒去重） {t.playbackTransfers || 0}</p>
    <p>成功请求 {t.requests || 0} 次 · 未命中 {t.misses || 0} 次</p>
    <Details title="统计口径与计算方式"><p>仅统计成功返回视频内容的请求，预下载不计入。节省流量是本地视频命中返回的数据量，未扣除提前下载的流量。播放传输按同一歌曲 30 秒去重，不代表游戏实际播放次数。</p></Details><Feedback error>{t.error}</Feedback></Details>
        <p role="status">{inventory.error ? '统计暂不可用' : v?.scanning ? '正在扫描，保留上次结果' : `上次成功刷新：${date(v?.updated)}`}</p>
    <Details title="歌曲清单详情"><p>歌曲清单版本：{v?.catalog?.revision || '尚无有效清单'} {v?.catalogRevision && v.catalogRevision !== v.catalog?.revision && `· 覆盖率基于 ${v.catalogRevision}，请刷新`}</p><p>{v?.catalog?.message}</p><Feedback error>{v?.catalog?.error}</Feedback></Details>
    <Details title="覆盖率说明"><p>按本地歌曲清单检查视频文件是否存在，不检查文件内容。刷新时会联网更新清单，失败则使用本地清单。共用一个视频的歌曲分别计入覆盖率。</p></Details>

    <Feedback error>{v?.error || app.status.data!.actionErrors?.inventory}</Feedback></Details></Panel>;
}
