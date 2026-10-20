import { useEffect, useRef, useState } from 'react';
import { Button, Checkbox, Field, Input, Link, Select, Subtitle2, List, ListItem, Divider, Caption1 } from '@fluentui/react-components';
import { SettingsPage, ImportPanel } from './settings';
import { request, APIError } from './api';
import { useApp, useResource, useFocusRecovery } from './hooks';
import { Details, Feedback, Panel } from './ui';
import { type CacheEntry, type CachePage, type Inventory, date, dated, gib } from './types';

export const cacheLabel = (entry: CacheEntry) => entry.songs.length ? entry.songs.map(s => `${s.title || '未知歌名'}（ID ${s.id}）`).join(' / ') : '未知歌名 · 未关联歌曲';
const resultNames: Record<string,string> = {deleted:'已删除',protected:'受保护，已跳过',missing:'文件已不存在',unknown:'未知文件，未删除',changed:'文件或歌曲关联已变化，请刷新后重选',unsafe:'目录不安全，未删除',failed:'操作失败，未删除'};
export function Cache({ visible, section }: {visible: boolean;section?:string}) {
  const app = useApp(), s = app.status.data!, focus = useFocusRecovery();
  const [search, setSearch] = useState(''), [sort, setSort] = useState('recent'), [offset, setOffset] = useState(0);
  const [query, setQuery] = useState({q:'',sort:'recent'}), [selected, setSelected] = useState<string[]>([]), [pending, setPending] = useState(false), [result, setResult] = useState('');
  const page = useResource<CachePage>('cache?' + new URLSearchParams({...query,offset:String(offset)}), visible && !app.stopped, app.revision, 0);
  const currentPage = useRef(page.data); currentPage.current = page.data;
  const priorStorage = useRef(s.activeSettings.storageDir);
  const retries = useRef(0);
  useEffect(() => {
    if (priorStorage.current !== s.activeSettings.storageDir) { priorStorage.current = s.activeSettings.storageDir; setOffset(0); setSelected([]); setResult(''); page.refresh(); }
  }, [s.activeSettings.storageDir, page.refresh]);
  useEffect(() => {
    setSelected([]);
    if (page.data && offset > 0 && offset >= page.data.total) {
      const last = Math.floor(Math.max(0,page.data.total - 1) / 50) * 50;
      setOffset(retries.current++ === 0 ? last : 0);
    } else if (page.data) retries.current = 0;
  }, [page.data, offset]);
  const entries = page.data?.entries || [], chosen = entries.filter(e => selected.includes(e.key)), eligible = entries.filter(e => e.known && !e.protected);
  const disabled = app.unavailable || pending || page.loading;
  async function operate(action: 'delete' | 'open', picked: CacheEntry[]) {
    const snapshot = page.data;
    if (!snapshot || disabled) return;
    if (action === 'delete') {
      if (!await app.confirm('确认删除选中的本地视频文件？', <><p>删除无法撤销；共用该文件的歌曲需要重新下载。歌曲资料和统计保留，正在使用的文件会跳过。</p><ul>{picked.map(e => <li key={e.key}>{cacheLabel(e)} · {gib(e.bytes)} · 影响 {e.songCount} 首已知关联歌曲</li>)}</ul></>, '确认删除')) return;
      if (currentPage.current !== snapshot) { setResult('文件列表已刷新，请重新选择并确认。'); return; }
    }
    setPending(true);
    try {
      const response = await request<{key:string;result:string}[]>('cache/' + action,{storageID:snapshot.storageID,entries:picked.map(e => ({key:e.key,stamp:e.stamp}))},AbortSignal.timeout(35000));
      setResult(action === 'open' ? '已请求系统打开视频位置。' : response.map(r => `${cacheLabel(picked.find(e => e.key === r.key)!)}：${resultNames[r.result] || '结果未知'}`).join('；'));
    } catch (e) { setResult(e instanceof APIError ? e.message : '操作响应未确认，请刷新文件列表核对，勿直接重复删除。'); }
    finally { setPending(false); if (action === 'delete') { setSelected([]); page.refresh(); } }
  }
  return <><div className="actions"><Link href="#video-import">导入视频</Link><Link href="#storage-settings">存储设置</Link></div><div hidden={section!=='video-import'}><ImportPanel/></div><div hidden={!['storage-settings','maxCacheGiB'].includes(section || '')}><SettingsPage section="storage"/></div><InventoryPanel visible={visible}/><Panel id="cache" {...focus} title="本地文件"><p>按文件管理 · 每页 50 项</p><p>按歌曲查找并清理本地视频。正在使用或准备播放的文件不可删除。</p><p>刷新文件列表只读取本机文件；上方的覆盖率统计会联网更新歌曲清单。</p>
    <p>{page.data ? `${page.data.fileCount} 个视频文件 · 占用 ${gib(page.data.totalBytes)} · 当前视频存储预算：${s.activeSettings.videoStorageMode === 'unlimited' ? '不限制' : s.activeSettings.videoStorageMode === 'none' ? '0 GiB（不保留持久化视频文件）' : gib(s.activeSettings.maxCacheBytes)}` : '视频占用待读取'}</p>
    <form className="actions" onSubmit={e => { e.preventDefault(); setOffset(0); setQuery({q:search,sort}); page.refresh(); }}>
      <Field label="歌名或歌曲 ID"><Input type="search" maxLength={300} value={search} onChange={(_, v) => setSearch(v.value)} /></Field>
      <Field label="排序"><Select value={sort} onChange={(_, v) => setSort(v.value)}><option value="recent">最近请求优先</option><option value="size">占用从大到小</option><option value="title">歌名顺序</option></Select></Field>
      <Button type="submit" disabled={disabled}>查找</Button><Button disabled={disabled} onClick={page.refresh}>刷新文件列表</Button><Button disabled={disabled || !page.data} onClick={() => void operate('open',[])}>打开视频目录</Button>
    </form><Feedback error>{page.error && '本地视频明细读取失败，请重试；未启动服务或联网扫描。'}</Feedback>
    <p role="status">{page.loading ? '正在读取本地视频明细…' : page.data?.total ? `共 ${page.data.total} 个文件 · 第 ${offset / 50 + 1} 页 · 显示 ${entries.length} 项（手动刷新）` : query.q ? '没有匹配的本地视频文件。' : '暂无本地视频。'}</p>
    {page.data?.total===0 && !query.q && (s.activeSettings.videoStorageMode==='none'
      ? <><p>当前选择「不保留」：适合边播边下载，本页不会积累已保存的视频。房间队列仍可自动提前准备。</p><div className="actions"><Link href="#queue">自动准备房间队列</Link><Link href="#maxCacheGiB">设置视频保留空间</Link></div></>
      : <Link href="#batch">保存视频供以后使用</Link>)}
    {!!entries.length && <div className="actions"><Button disabled={disabled || !eligible.length} onClick={() => setSelected(eligible.every(e => selected.includes(e.key)) ? [] : eligible.map(e => e.key))}>选择本页可删除项</Button><Button disabled={disabled || !chosen.length} onClick={() => void operate('delete', chosen)}>删除选中视频</Button><span role="status">已选择 {chosen.length} 个文件</span></div>}
    <Feedback>{result}</Feedback>
    <List navigationMode="composite" className="records" aria-label="本地视频文件">{entries.map((e,i) => <ListItem key={e.key}><div role="gridcell" className="record-content">
      {i > 0 && <Divider className="record-divider" />}
      <Checkbox aria-label={'选择 '+cacheLabel(e)} label={e.songs.length>2?`${e.songs.slice(0,2).map(s=>`${s.title || '未知歌名'}（ID ${s.id}）`).join(' / ')} 等 ${e.songCount} 首`:cacheLabel(e)} checked={selected.includes(e.key)} disabled={disabled || !e.known || e.protected} onChange={(_, v) => setSelected(v.checked ? [...selected,e.key] : selected.filter(k => k !== e.key))} />
      <p>{gib(e.bytes)} · 最近请求：{e.lastRequest>0?date(e.lastRequest):'无记录'} · {e.state}{e.protected && ' · 受保护（正在使用 / 校验 / 待播）'}</p>
      {e.songCount > 1 && <p>共享文件 · 关联 {e.songCount} 首歌曲；删除会影响全部关联歌曲。</p>}
      <Details title="资源与版本详情"><p>资源指纹：{e.key}</p><p>文件大小：{e.bytes.toLocaleString()} 字节</p>{e.songs.map(song => <p key={song.id}>{song.title || '未知歌名'} · ID {song.id} · {song.current ? '本地当前引用' : '历史关联（非当前引用）'}</p>)}{e.songCount > e.songs.length && <p>仅展示前 {e.songs.length} 首；另有 {e.songCount - e.songs.length} 首关联歌曲，删除同样受影响。可按歌曲 ID 搜索。</p>}</Details>
      <Button disabled={disabled || !e.known} onClick={() => void operate('open',[e])}>定位文件</Button>
    </div></ListItem>)}</List>
    {(page.data?.total || 0) > 50 && <div className="actions"><Button disabled={disabled || offset === 0} onClick={() => setOffset(Math.max(0,offset-50))}>上一页</Button><Button disabled={disabled || offset+50 >= (page.data?.total || 0)} onClick={() => setOffset(offset+50)}>下一页</Button></div>}
    </Panel></>;
}
function InventoryPanel({visible}: {visible:boolean}) {
  const app = useApp(), t = app.status.data!.traffic || {};
  const inventory = useResource<Inventory>('inventory',visible && !app.stopped,app.revision), v = inventory.data;
  const known = v && dated(v.updated) && v.coverageKnown && v.totalSongs > 0;
  return <Panel id="overview" title="曲库覆盖率"><Subtitle2 as="h3">歌曲本地视频覆盖率</Subtitle2><p>{known ? `${(100*v.coveredSongs/v.totalSongs).toFixed(1)}% · ${v.coveredSongs} / ${v.totalSongs} 首曲目已覆盖` : '覆盖率待刷新'}</p>
<Details title="覆盖率与复用详情"><Details title="本地视频复用统计"><Subtitle2 as="h3">请求统计</Subtitle2><p>当前存储目录累计</p><p>已命中次数 {t.hits || 0} · 请求命中率 {t.hitRate == null ? '—' : t.hitRate.toFixed(1)+'%'} · 本地视频命中节省流量 {gib(t.savedBytes)} · 播放传输次数（30 秒去重） {t.playbackTransfers || 0}</p>
    <p>成功请求 {t.requests || 0} 次 · 未命中 {t.misses || 0} 次 · 未命中播放首正文 {t.coldBodyMS?.toFixed(1) || '—'} ms（{t.coldBodySamples || 0} 个样本）· 本地命中播放首正文 {t.localBodyMS?.toFixed(1) || '—'} ms（{t.localBodySamples || 0} 个样本）</p>
    <Details title="统计口径与计算方式"><p>仅统计成功返回视频内容的请求，预下载不计入。节省流量是本地视频命中返回的数据量，未扣除提前下载的流量。播放传输按同一歌曲 30 秒去重，不代表游戏实际播放次数。</p></Details><Feedback error>{t.error}</Feedback></Details>
        <p role="status">{inventory.error ? '统计暂不可用' : v?.scanning ? '正在扫描，保留上次结果' : `上次成功刷新：${date(v?.updated)}`}</p>
    <p>歌曲清单检查时间：{date(v?.catalog?.checkedAt)}</p>
    <Details title="歌曲清单详情"><p>歌曲清单版本：{v?.catalog?.revision || '尚无有效清单'} {v?.catalogRevision && v.catalogRevision !== v.catalog?.revision && `· 覆盖率基于 ${v.catalogRevision}，请刷新`}</p><p>{v?.catalog?.message}</p><Feedback error>{v?.catalog?.error}</Feedback></Details>
    <Details title="覆盖率说明"><p>按本地歌曲清单检查视频文件是否存在，不检查文件内容。刷新时会联网更新清单，失败则使用本地清单。共用一个视频的歌曲分别计入覆盖率。</p></Details>
    <Button disabled={app.unavailable || !v || v.scanning} onClick={() => void app.action('inventory/scan')}>更新清单并统计覆盖率</Button>
    <Feedback error>{v?.error || app.status.data!.actionErrors?.inventory}</Feedback></Details></Panel>;
}
