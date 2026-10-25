import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Button, Checkbox, Field, Input, Select, Textarea, Caption1 } from '@fluentui/react-components';
import { bootstrap } from './bootstrap';
import { decode, request } from './api';
import { useApp } from './hooks';
import { Feedback, Panel, Progress, Hint, RecordDetails, useThemePreference } from './ui';
import { type Settings, type Status } from './types';
import { CacheCapacityFields, CacheReclamationFields, capacityPayload, type CapacityDraft } from './cache-capacity';
import { StorageDirectoryFields } from './storage-directory';

const numeric = ['requestRetentionDays','throughputIntervalMinutes','prepareAheadSeconds','downloadSoftLimit','downloadGrowIntervalMS','localProtectionGraceSeconds'] as const;
type NumberField = typeof numeric[number];
export interface ProxyReport { id?: string; accepted: number; skipped: number; failed: number; items: {index: number; protocol?: string; status: string; reason?: string}[] }
export type Draft = Omit<Settings, NumberField | 'maxCacheBytes' | 'theme'> & Record<NumberField, string> & CapacityDraft & { proxyImportID: string; proxyImportText: string; proxyReport?: ProxyReport };
export function draftFrom(s: Settings): Draft {
  const {maxCacheBytes, theme: _theme, ...values} = s;
  return { ...values, restoreHostsOnExit: s.restoreHostsOnExit ?? true, socks5Password: '', upstreamMode: s.upstreamMode === 'socks5' ? 'proxy' : s.upstreamMode || 'direct', proxyBackend: s.proxyBackend || 'external-socks5',
    requestRetentionDays: String(s.requestRetentionDays ?? 30), throughputIntervalMinutes: String(s.throughputIntervalMinutes ?? 20),
    prepareAheadSeconds: String(s.prepareAheadSeconds ?? 500),
    downloadSoftLimit: String(s.downloadSoftLimit ?? 16), downloadGrowIntervalMS: String(s.downloadGrowIntervalMS ?? 250),
    localProtectionGraceSeconds: String(s.localProtectionGraceSeconds ?? 300),
    maxCacheGiB: String((maxCacheBytes || 0) / 1073741824), proxyImportID: '', proxyImportText: '' };
}
export function settingsPayload(d: Draft) {
  const { videoStorageMode, aggressiveCacheReclamation, maxCacheGiB, proxyImportText: _text, proxyReport: _report, socks5Password, ...values } = d;
  return { ...values, ...Object.fromEntries(numeric.map(k => [k, Number(d[k])])), ...capacityPayload({videoStorageMode, aggressiveCacheReclamation, maxCacheGiB}),
    logDir: d.manualLogDir ? d.logDir : '', ...(socks5Password || !d.socks5Username ? {socks5Password: socks5Password || ''} : {}) };
}
export function fingerprint(d: Draft) {
  return JSON.stringify({...settingsPayload(d), proxyImportText: d.proxyImportText});
}
export const releaseImport = async (id: string) => { if (id) try { await request('proxy/import/discard', {id}); } catch { /* Local discard remains available offline; server expiry is the fallback. */ } };

type FormProps = { draft: Draft; update: (values: Partial<Draft>) => void; disabled: boolean; status: Status };
export function NetworkFields({ draft: d, update, disabled, status, onPending, wizard = false }: FormProps & {wizard?: boolean; onPending: (pending: boolean) => void}) {
  const [pending, setPending] = useState(false), [error, setError] = useState('');
  const generation = useRef(0), current = useRef(d); current.current = d;
  useEffect(() => () => { generation.current++; }, []);
  const busy = (value: boolean) => { setPending(value); onPending(value); };
  async function readFile(file?: File) {
    if (!file) return;
    const id = ++generation.current; busy(true); setError('');
    try { if (file.size > 2 * 1024 * 1024) throw new Error('节点文件超过 2 MiB。'); const text = await file.text(); if (id === generation.current) update({proxyImportText: text}); }
    catch (e) { if (id === generation.current) setError(String(e)); }
    finally { if (id === generation.current) busy(false); }
  }
  async function inspect() {
    if (pending || disabled || !d.proxyImportText.trim()) return;
    const id = ++generation.current, content = d.proxyImportText; busy(true); setError('');
    try {
      const response = await fetch('/api/proxy/import', {method: 'POST', headers: {'Content-Type':'text/plain', 'X-SWD-Token':bootstrap.token},body: content, signal: AbortSignal.timeout(30000)});
      const report = await decode<ProxyReport>(response);
      if (id !== generation.current || current.current.proxyImportText !== content) { void releaseImport(report.id || ''); return; }
      void releaseImport(current.current.proxyImportID);
      update({proxyImportID: report.id || '', proxyImportText: report.id ? '' : content, proxyReport: report});
    } catch (e) { if (id === generation.current) setError(String(e)); }
    finally { if (id === generation.current) busy(false); }
  }
  const locked = disabled || pending;
  return <div className="form-grid">
    <>{wizard ? <><div className="actions">{(['direct','proxy'] as const).map(mode=><Checkbox key={mode} label={mode==='direct'?'直连':'代理'} disabled={locked} checked={d.upstreamMode===mode||d.upstreamMode==='auto'} onChange={(_,v)=>{const other=mode==='direct'?'proxy':'direct';const hasOther=d.upstreamMode===other||d.upstreamMode==='auto';update({upstreamMode:v.checked?hasOther?'auto':mode:hasOther?other:''});}}/>)}</div><p>两项都选时自动选择；只选代理时不会直连。</p></> : <Field label="上游连接"><Select disabled={locked} value={d.upstreamMode} onChange={(_, v) => update({upstreamMode: v.value})}>
      <option value="direct">直连（内置 DNS）</option><option value="proxy">强制代理</option><option value="auto">自动选择直连或代理</option></Select></Field>}</>

    {d.upstreamMode && d.upstreamMode !== 'direct' && <>
      <Field label="代理后端"><Select disabled={locked} value={d.proxyBackend} onChange={(_, v) => update({proxyBackend: v.value})}><option value="external-socks5">外部 SOCKS5</option><option value="mihomo">内嵌 Mihomo</option></Select></Field>
      {d.proxyBackend !== 'mihomo' ? <>
        <Field label="SOCKS5 地址" required hint="代理软件的主机:端口。"><Input disabled={locked} required value={d.socks5Address} onChange={(_, v) => update({socks5Address: v.value})} placeholder="127.0.0.1:7891" autoComplete="off" /></Field>
        <Field label="SOCKS5 用户名（可选）" ><Input disabled={locked} value={d.socks5Username} onChange={(_, v) => update({socks5Username: v.value})} autoComplete="off" /></Field>
        <Field label="SOCKS5 密码" hint="留空保留已有密码；清空用户名可移除认证。"><Input type="password" disabled={locked} value={d.socks5Password || ''} onChange={(_, v) => update({socks5Password: v.value})} autoComplete="new-password" /></Field>
        <Feedback error>{status.socks5CredentialError}</Feedback>
        <span className="secondary">代理仅用于 SWD 下载；游戏 HTTPS 转发仍直连。</span>
      </> : <>
        <p>粘贴代理节点或选择文件，检查后再保存。</p>
        <Feedback error>{[status.proxyNodes?.error, status.proxyNodes?.activeError].filter(Boolean).join('；')}</Feedback>
        <Field label="粘贴节点内容" validationMessage={d.proxyImportText.trim() ? '请先检查节点内容，或清除本次导入草稿。' : undefined} validationState="none"><Textarea rows={5} disabled={locked} value={d.proxyImportText} onChange={(_, v) => update({proxyImportText: v.value})} /></Field>
        <Field label="或选择节点文件">{props => <input {...props} type="file" disabled={locked} accept=".yaml,.yml,.txt,.json" onChange={e => { void readFile(e.target.files?.[0]); e.target.value = ''; }} />}</Field>
        <Caption1 as="p" className="hint">检查仅生成草稿；保存后替换整份节点库，重启生效。</Caption1>
        <div className="inline secondary">支持 YAML、URI 与 Base64 列表<Hint label="节点格式说明">最多 2 MiB / 2048 条，仅导入节点配置；不导入代理组、链式代理或系统网络选项，不自动更新订阅。</Hint></div>
        <div className="actions"><Button disabled={locked || !d.proxyImportText.trim()} onClick={() => void inspect()}>检查并导入到草稿</Button><Button disabled={locked || (!d.proxyImportID && !d.proxyImportText)} onClick={() => { void releaseImport(d.proxyImportID); update({proxyImportID:'',proxyImportText:'',proxyReport:undefined}); }}>清除本次导入草稿</Button></div>
        {d.proxyReport && <><p>本次接受 {d.proxyReport.accepted} 个，重复跳过 {d.proxyReport.skipped} 个，失败 {d.proxyReport.failed} 个。{d.proxyReport.id ? '草稿 2 小时有效。' : '没有有效节点，不会替换已有节点库。请修改内容或清除本次草稿。'}</p><RecordDetails title="逐条导入结果"><ul>{d.proxyReport.items.map(row => <li key={row.index}>第 {row.index} 条 · {row.protocol} · {({accepted:'接受',skipped:'跳过',failed:'失败'}[row.status] || row.status)} {row.reason}</li>)}</ul></RecordDetails></>}
        <span className="secondary">已保存 {status.proxyNodes?.saved || 0} 个 · 已加载 {status.proxyNodes?.loaded || 0} 个 · 活跃 {status.proxyNodes?.active || 0} 个</span>
      </>}
    </>}
    <Feedback error>{error}</Feedback>
  </div>;
}

export type SettingsSection = 'application' | 'storage' | 'downloads' | 'network';
export function SettingsPage({section='application',children}: {section?:SettingsSection;children?:(form:ReactNode,draft:Draft)=>ReactNode}) {
  const app = useApp(), s = app.status.data!;
  const theme=useThemePreference();
  const [draft, setDraft] = useState<Partial<Draft>>(), [pending, setPending] = useState(false);
  const [awaitingRevision, setAwaitingRevision] = useState<number>();
  const discard = () => { void releaseImport(draft?.proxyImportID || ''); setDraft(undefined); setPending(false); setAwaitingRevision(undefined); };
  // Global discard also remounts NetworkFields below to invalidate its async work.
  useEffect(discard, [app.settingsEpoch]);
  useEffect(() => {
    if (awaitingRevision !== undefined && app.status.loadedRevision !== undefined && app.status.loadedRevision >= awaitingRevision) {
      setDraft(undefined); setAwaitingRevision(undefined);
    }
  }, [awaitingRevision, app.status.loadedRevision]);
  const syncing = awaitingRevision !== undefined;
  const d = {...draftFrom(s.settings),...draft}, dirty = !syncing && fingerprint(d) !== fingerprint(draftFrom(s.settings));
  useEffect(()=>{app.setSettingsDirty(section,dirty);return ()=>app.setSettingsDirty(section,false);},[section,dirty,app.setSettingsDirty]);
  const prepareAheadSeconds = Number(d.prepareAheadSeconds);
  const prepareAheadEstimate = Number.isInteger(prepareAheadSeconds) && prepareAheadSeconds >= 100 && prepareAheadSeconds <= 16000
    ? `约 ${prepareAheadSeconds / 4} MB 临时缓存（按 2 Mbps）` : '';
  const update = (patch: Partial<Draft>) => setDraft(value=>({...value, ...patch}));
  const disabled = app.unavailable || pending || syncing || app.status.loadedRevision !== app.revision;
  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => { if (dirty && !app.stopped) event.preventDefault(); };
    addEventListener('beforeunload', warn); return () => removeEventListener('beforeunload', warn);
  }, [dirty, app.stopped]);
  const numberField = (key: NumberField | 'maxCacheGiB', label: string, min: number, max: number, hint?: string, step = '1') => <Field label={label} hint={hint} required><Input id={key} type="number" required min={min} max={max} step={step} disabled={disabled} value={d[key]} onChange={(_, v) => update({[key]:v.value})} /></Field>;
  const form = <Panel id={section==='storage'?'storage-settings':section==='downloads'?'download-settings':section==='network'?'proxy-settings':'preferences'} title={{application:'应用设置',storage:'视频存储',downloads:'预加载与下载设置',network:'代理设置'}[section]}><Feedback error>{s.actionErrors?.settings}</Feedback><form className="form-grid settings-form" onSubmit={async e => { e.preventDefault(); if (!dirty || disabled || d.proxyImportText.trim()) return; if (await app.action('settings', settingsPayload(d))) { setAwaitingRevision(app.revision + 1); } }}>
      <div className="toolbar spread settings-actions" role="region" aria-label="设置操作"><p role="status">{syncing ? '正在同步设置…' : dirty ? '未保存' : s.restartRequired ? '待重启' : '已生效'}</p>
      <div className="actions"><Button type="submit" appearance="primary" disabled={disabled || !dirty || !!d.proxyImportText.trim()}>保存设置</Button><Button disabled={disabled || !dirty} onClick={discard}>放弃更改</Button>
      <Button disabled={disabled || dirty || app.dirtySettings.size>0 || !s.restartRequired} onClick={() => void app.action('restart')}>重启并应用</Button></div>
      {s.restartRequired&&[...app.dirtySettings].some(value=>value!==section)&&<p>其他页面仍有未保存配置，请保存或放弃后再重启。</p>}</div>
      {section==='storage'&&<fieldset disabled={disabled}><legend>本地存储</legend>
      <CacheCapacityFields anchor="maxCacheGiB" mode={d.videoStorageMode} value={d.maxCacheGiB} disabled={disabled} onChange={update} />
      <Checkbox label="优先保留收藏视频" checked={d.retainFavorites} onChange={(_,v)=>update({retainFavorites:!!v.checked})}/>
      <span className="secondary">收藏视频可超出预算保留，不会自动下载。</span>
      <StorageDirectoryFields mode={d.videoStorageMode} disabled={disabled}>{directoryDisabled =>
        <Field label="本地存储目录" required hint="视频、歌曲资料与统计统一存放于此。"><Input required disabled={directoryDisabled} value={d.storageDir} onChange={(_, v) => update({storageDir:v.value})} /></Field>
      }</StorageDirectoryFields>
      <div className="settings-columns"><CacheReclamationFields mode={d.videoStorageMode} reclamation={d.aggressiveCacheReclamation} disabled={disabled} onChange={update} />
      {numberField('localProtectionGraceSeconds','本地延续保护时长（秒）',0,10000,'需求结束后继续保留缓存；0 表示不延续保护。')}
      </div></fieldset>}
      {section==='application'&&<><Field label="主题" hint="自动保存并立即应用于网页和托盘图标，无需重启。"><Select disabled={disabled} value={theme.preference} onChange={async(_,v)=>{const value=v.value as 'system'|'light'|'dark';if(await app.action('settings/theme',{theme:value}))theme.setPreference(value);}}><option value="system">跟随系统</option><option value="light">浅色</option><option value="dark">深色</option></Select></Field><fieldset disabled={disabled}><legend>程序启动</legend><Checkbox label="启动程序时自动打开 WebUI" checked={d.autoOpenWebUI} onChange={(_,v)=>update({autoOpenWebUI:!!v.checked})}/><Checkbox label="启动程序时自动开启视频加速" checked={d.autoAccelerate} onChange={(_, v) => update({autoAccelerate:!!v.checked})} /><span className="secondary">下次启动生效；接入时可能需要管理员授权。</span></fieldset>
      <fieldset><legend>退出</legend><Checkbox label="退出时恢复游戏直连" checked={d.restoreHostsOnExit} disabled={disabled} onChange={(_,v)=>update({restoreHostsOnExit:!!v.checked})}/><span className="secondary">保存后立即用于本次退出。关闭后需先启动 SWD 再播放；开启时可能需要管理员授权。</span></fieldset>
      {numberField('requestRetentionDays','请求明细保留天数',0,36500,'0 表示永久保留；不影响累计统计。')}</>}
      {section==='network'&&<>      <fieldset><legend>网络连接</legend><NetworkFields key={app.settingsEpoch} draft={d} update={update} disabled={disabled} status={s} onPending={setPending} />
      <div className="settings-columns"><Field label="播放地址接口选择" hint="自动优先 CF，失败后尝试 HKG。"><Select disabled={disabled} value={d.downloadUpstream} onChange={(_, v) => update({downloadUpstream:v.value})}><option value="auto">自动（CF 优先）</option><option value="cf">node=cf（播放地址接口）</option><option value="hkg">node=nya（播放地址接口）</option></Select></Field>
      {numberField('throughputIntervalMinutes','自动测速间隔（分钟）',1,1440)}</div></fieldset>
</>}
      {section==='downloads'&&<><Checkbox label="随视频网关启用队列预加载" checked={d.queueWithGateway} disabled={disabled} onChange={(_,v)=>update({queueWithGateway:!!v.checked})}/><fieldset disabled={disabled}><legend>下载</legend><div className="settings-columns">

      {numberField('downloadSoftLimit','同时下载数量',1,64,'建议 16 路；播放急需时可临时超出。')}
      {numberField('downloadGrowIntervalMS','并发增长间隔（毫秒）',0,2000,'建议 250 ms；0 表示关闭缓增。')}
      </div></fieldset>
      <fieldset disabled={disabled}><legend>房间队列与游戏日志</legend>
      {numberField('prepareAheadSeconds','提前准备时间（秒）',100,16000,`建议 200～2000 秒。${prepareAheadEstimate}`)}
      <Checkbox label="手动指定 VRChat 日志目录" checked={d.manualLogDir} onChange={(_, v) => update({manualLogDir:!!v.checked})} />
      <Field label="VRChat 日志目录" hint="通常自动识别，找不到日志时再手动指定。"><Input required={d.manualLogDir} disabled={!d.manualLogDir || disabled} value={d.manualLogDir ? d.logDir : s.defaultLogDir} onChange={(_, v) => update({logDir:v.value})} /></Field></fieldset></>}
      <span className="secondary">运行配置保存后重启生效。</span>
    </form></Panel>;
  return children?children(form,d):form;
}

export function ImportPanel() {
  const app = useApp(), s = app.status.data!, job = s.import;
  const [source, setSource] = useState(''), [cleanup, setCleanup] = useState(false);
  useEffect(() => setCleanup(false), [job.id]);
  return <Panel id="video-import" title="导入视频"><p>扫描子文件夹，校验并导入缺少的视频。同盘优先使用硬链接，导入后请勿修改源视频。</p>
    <p>导入到当前生效目录：{s.activeSettings.storageDir}</p>
    <form className="form-grid" onSubmit={async e => { e.preventDefault(); if (s.settings.storageDir !== s.activeSettings.storageDir && !await app.confirm('新存储目录尚未生效', <>已保存的新存储目录尚未生效。本次将导入到当前生效目录：{s.activeSettings.storageDir}。若要使用新目录，请取消并先重启应用。仍要导入当前目录吗？</>)) return; await app.action('import/start',{source:source.trim()}); }}>
      <Field label="外部目录" required><Input required disabled={app.unavailable || job.running} value={source} onChange={(_, v) => setSource(v.value)} /></Field><Button type="submit" disabled={app.unavailable || job.running || !source.trim()}>扫描并导入</Button>
    </form>{(job.running||!!job.total)&&<Progress value={job.checked} max={job.total} label="视频文件处理进度" />}
    <p role="status">{job.phase?`${job.phase} · ${job.checked || 0}/${job.total || 0} 个视频 · 已导入 ${job.imported || 0} · 跳过 ${job.skipped || 0} · 失败 ${job.failed || 0} · 已清除 ${job.deleted || 0}`:'尚未开始'}</p>
    <Feedback error>{job.error || s.actionErrors?.import}</Feedback>
    {job.cleanupAvailable && !job.running && <><Checkbox checked={cleanup} onChange={(_, v) => setCleanup(!!v.checked)} label="清除原位置本次成功导入的资源（默认保留）" disabled={app.unavailable} /><Button disabled={app.unavailable || !cleanup} onClick={async () => {
      const id = job.id;
      if (await app.confirm('永久删除源视频？', '不可逆操作：将永久删除原位置本次成功导入的源视频，不进入回收站。仅在目标视频仍完整且源文件未改变时删除。确定继续吗？')) { setCleanup(false); await app.action('import/cleanup',{id,confirmed:true}); }
    }}>清除已导入的源文件</Button></>}
  </Panel>;
}
