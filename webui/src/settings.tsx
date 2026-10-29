import { useEffect, useRef, useState, type ReactNode, type Ref } from 'react';
import { Button, Checkbox, Field, Input, Select, Textarea, Caption1, Tooltip } from '@fluentui/react-components';
import { bootstrap } from './bootstrap';
import { decode, request } from './api';
import { useApp } from './hooks';
import { Feedback, Panel, Progress, Hint, RecordDetails, useThemePreference } from './ui';
import { type Settings, type Status } from './types';
import { CacheCapacityFields, CacheReclamationFields, StorageBudgetNotice, capacityPayload, type CapacityDraft } from './cache-capacity';
import { StorageLocationFields } from './storage-directory';
import { DirectoryField } from './path-picker';
import { RequestRetentionFields } from './request-retention';

const numeric = ['requestRetentionDays','throughputIntervalMinutes','prepareAheadSeconds','downloadConcurrencySoftLimit','downloadGrowIntervalMS','localProtectionGraceSeconds'] as const;
const restorePermissionHint='开启并保存时，可能请求 Windows 管理员授权（UAC），以准备恢复权限；退出时不再请求。';
type NumberField = typeof numeric[number];
export interface ProxyReport { id?: string; accepted: number; skipped: number; failed: number; items: {index: number; protocol?: string; status: string; reason?: string}[] }
export type Draft = Omit<Settings, NumberField | 'videoBudgetBytes' | 'theme'> & Record<NumberField, string> & CapacityDraft & { proxyImportID: string; proxyImportText: string; proxyReport?: ProxyReport };
export function draftFrom(s: Settings): Draft {
  const {videoBudgetBytes, theme: _theme, ...values} = s;
  return { ...values, restoreHostsOnStopOrExit: s.restoreHostsOnStopOrExit ?? true, socks5Password: '',
    requestRetentionDays: String(s.requestRetentionDays ?? 0), throughputIntervalMinutes: String(s.throughputIntervalMinutes ?? 20),
    prepareAheadSeconds: String(s.prepareAheadSeconds ?? 500),
    downloadConcurrencySoftLimit: String(s.downloadConcurrencySoftLimit ?? 16), downloadGrowIntervalMS: String(s.downloadGrowIntervalMS ?? 250),
    localProtectionGraceSeconds: String(s.localProtectionGraceSeconds ?? 300),
    videoBudgetGiB: String((videoBudgetBytes || 0) / 1073741824), proxyImportID: '', proxyImportText: '' };
}
export function settingsPayload(d: Draft) {
  const { videoStorageMode, aggressiveBlockReclamation, videoBudgetGiB, proxyImportText: _text, proxyReport: _report, socks5Password, ...values } = d;
  return { ...values, ...Object.fromEntries(numeric.map(k => [k, Number(d[k])])), ...capacityPayload({videoStorageMode, aggressiveBlockReclamation, videoBudgetGiB}),
    ...(socks5Password || !d.socks5Username ? {socks5Password: socks5Password || ''} : {}) };
}
export function fingerprint(d: Draft) {
  return JSON.stringify({...settingsPayload(d), proxyImportText: d.proxyImportText});
}
export const releaseImport = async (id: string) => { if (id) try { await request('proxy/import/discard', {id}); } catch { /* Local discard remains available offline; server expiry is the fallback. */ } };

type FormProps = { draft: Draft; update: (values: Partial<Draft>) => void; disabled: boolean; status: Status };
export function NetworkFields({ draft: d, update, disabled, status, onPending }: FormProps & {onPending: (pending: boolean) => void}) {
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
    <Field label="上游连接"><Select disabled={locked} value={d.upstreamMode} onChange={(_, v) => update({upstreamMode: v.value})}>
      <option value="direct">直连（内置 DNS）</option><option value="proxy">强制代理</option><option value="auto">自动选择直连或代理</option></Select></Field>

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
  const [locationError, setLocationError] = useState('');
  const discard = () => { void releaseImport(draft?.proxyImportID || ''); setDraft(undefined); setPending(false); setAwaitingRevision(undefined); };
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
  const numberField = (key: NumberField | 'videoBudgetGiB', label: string, min: number, max: number, hint?: string, step = '1') => <Field label={label} hint={hint} required><Input id={key} type="number" required min={min} max={max} step={step} disabled={disabled} value={d[key]} onChange={(_, v) => update({[key]:v.value})} /></Field>;
  const saveButton=<Button type="submit" appearance="primary" disabled={disabled || !dirty || !!d.proxyImportText.trim()}>保存设置</Button>;
  const restartButton=<Button disabled={disabled || dirty || app.dirtySettings.size>0 || !s.restartRequired} onClick={() => void app.action('restart')}>重启并应用</Button>;
  async function save() {
    if (!dirty || disabled || d.proxyImportText.trim()) return;
    setPending(true); setLocationError('');
    try {
      let confirmed = false;
      if (d.databasePath !== s.settings.databasePath || d.videoDir !== s.settings.videoDir) {
        const check = await request<{confirmVideoPolicy:boolean}>('storage/check', {databasePath:d.databasePath,videoDir:d.videoDir});
        if (check.confirmVideoPolicy) {
          confirmed = await app.confirm('确认此目录的视频保留方式', <><p className="wrap">视频目录：{d.videoDir}</p><p className="wrap">数据库：{d.databasePath||'尚未指定'}</p><p>此目录的视频将按本次设置管理：{d.videoStorageMode==='none'?'不保留普通视频':d.videoStorageMode==='unlimited'?'不限制视频空间':d.videoBudgetGiB+' GiB 视频预算'}。不符合保留策略的视频可能被删除。</p><p>{d.retainFavorites?'优先保留收藏视频，收藏以所选数据库中的记录为准。':'收藏不提供额外保留保护。'}换数据库不会带入原数据库的收藏。</p></>, '确认并保存');
          if (!confirmed) return;
        }
      }
      if (await app.action('settings', {...settingsPayload(d),confirmVideoPolicy:confirmed})) setAwaitingRevision(app.revision + 1);
    } catch (e) { setLocationError(String(e)); }
    finally { setPending(false); }
  }
  const form = <Panel id={section==='storage'?'storage-settings':section==='downloads'?'download-settings':section==='network'?'proxy-settings':'preferences'} title={{application:'应用设置',storage:'数据与视频存储',downloads:'预加载与下载设置',network:'代理设置'}[section]}><Feedback error>{locationError||s.actionErrors?.settings}</Feedback><form className="form-grid settings-form" onSubmit={e => { e.preventDefault(); void save(); }}>
      <div className="toolbar spread settings-actions" role="region" aria-label="设置操作"><p role="status">{syncing ? '正在同步设置…' : dirty ? '未保存' : s.restartRequired ? '待重启' : '已生效'}</p>
      <div className="actions">{d.restoreHostsOnStopOrExit&&!s.settings.restoreHostsOnStopOrExit?<Tooltip content={restorePermissionHint} relationship="description">{saveButton}</Tooltip>:saveButton}<Button disabled={disabled || !dirty} onClick={discard}>放弃更改</Button>
      {s.settings.autoAccelerate?<Tooltip content="重启后将自动开启视频加速，接入时可能请求 Windows 管理员授权（UAC）。" relationship="description">{restartButton}</Tooltip>:restartButton}</div>
      {s.restartRequired&&[...app.dirtySettings].some(value=>value!==section)&&<p>其他页面仍有未保存配置，请保存或放弃后再重启。</p>}</div>
      {section==='storage'&&<fieldset disabled={disabled}><legend>本地存储</legend>
      <StorageLocationFields value={d} update={update} disabled={disabled} status={s} onPending={setPending} />
      <CacheCapacityFields anchor="videoBudgetGiB" mode={d.videoStorageMode} value={d.videoBudgetGiB} disabled={disabled} onChange={update} />
      <Checkbox label="优先保留收藏视频" checked={d.retainFavorites} onChange={(_,v)=>update({retainFavorites:!!v.checked})}/>
      <span className="secondary">收藏视频可超出预算保留，不会自动下载。</span>
      <div className="settings-columns"><CacheReclamationFields mode={d.videoStorageMode} reclamation={d.aggressiveBlockReclamation} disabled={disabled} onChange={update} />
      {numberField('localProtectionGraceSeconds','本地延续保护时长（秒）',0,10000,'需求结束后继续保留缓存；0 表示不延续保护。')}
      </div></fieldset>}
      {section==='application'&&<><Field label="主题" hint="自动保存并立即应用于网页和托盘图标，无需重启。"><Select disabled={disabled} value={theme.preference} onChange={async(_,v)=>{const value=v.value as 'system'|'light'|'dark';if(await app.action('settings/theme',{theme:value}))theme.setPreference(value);}}><option value="system">跟随系统</option><option value="light">浅色</option><option value="dark">深色</option></Select></Field><fieldset disabled={disabled}><legend>程序启动</legend><Checkbox label="启动程序时自动打开 WebUI" checked={d.autoOpenWebUI} onChange={(_,v)=>update({autoOpenWebUI:!!v.checked})}/>
      {/* Checkbox forwards trigger props to its input; put pointer handlers on the root to include the label. */}
      <Tooltip content="下次启动自动加速时，可能请求 Windows 管理员授权（UAC）。" relationship="description">{({onPointerEnter,onPointerLeave,ref,...trigger})=><Checkbox {...trigger} ref={ref as Ref<HTMLInputElement>} root={{onPointerEnter,onPointerLeave}} label="启动程序时自动开启视频加速" checked={d.autoAccelerate} onChange={(_, v) => update({autoAccelerate:!!v.checked})} />}</Tooltip><span className="secondary">下次启动生效；接入时可能需要管理员授权。</span></fieldset>
      <fieldset><legend>停止与退出</legend><Tooltip content={restorePermissionHint} relationship="description">{({onPointerEnter,onPointerLeave,ref,...trigger})=><Checkbox {...trigger} ref={ref as Ref<HTMLInputElement>} root={{onPointerEnter,onPointerLeave}} id="restoreHostsOnStopOrExit" label="停止加速或退出时恢复游戏直连" checked={d.restoreHostsOnStopOrExit} disabled={disabled} onChange={(_,v)=>update({restoreHostsOnStopOrExit:!!v.checked})}/>}</Tooltip><span className="secondary">保存后立即用于停止加速和退出。关闭后需开启加速才能播放；开启并保存时可能需要管理员授权。</span></fieldset>
      <RequestRetentionFields mode={d.requestRetentionMode} value={d.requestRetentionDays} disabled={disabled} onChange={update} />
      <Field label="程序日志级别" hint="保存后重启生效。Debug 用于详细排障；关闭不会删除已有日志。"><Select disabled={disabled} value={d.logLevel} onChange={(_,v)=>update({logLevel:v.value as Settings['logLevel']})}><option value="off">关闭</option><option value="error">仅错误（Error）</option><option value="warn">警告及错误（Warn）</option><option value="info">信息（Info，默认）</option><option value="debug">详细调试（Debug）</option></Select></Field></>}
      {section==='network'&&<>      <fieldset><legend>网络连接</legend><NetworkFields draft={d} update={update} disabled={disabled} status={s} onPending={setPending} />
      {numberField('throughputIntervalMinutes','自动测速间隔（分钟）',1,1440)}</fieldset>
</>}
      {section==='downloads'&&<><Checkbox label="随视频网关启用队列预加载" checked={d.queueWithGateway} disabled={disabled} onChange={(_,v)=>update({queueWithGateway:!!v.checked})}/><fieldset disabled={disabled}><legend>下载</legend><div className="settings-columns">

      {numberField('downloadConcurrencySoftLimit','同时下载数量',1,64,'建议 16 路；播放急需时可临时超出。')}
      {numberField('downloadGrowIntervalMS','并发增长间隔（毫秒）',0,2000,'建议 250 ms；0 表示关闭缓增。')}
      </div></fieldset>
      <fieldset disabled={disabled}><legend>房间队列与游戏日志</legend>
      {numberField('prepareAheadSeconds','提前准备时间（秒）',100,16000,`建议 200～2000 秒。${prepareAheadEstimate}`)}
      <DirectoryField purpose="log-directory" label="VRChat 日志目录" hint="默认自动识别，找不到日志时点击选择。" disabled={disabled} value={d.vrchatLogDir || s.defaultLogDir} defaultValue={s.defaultLogDir} isDefault={!d.vrchatLogDir}
        onChange={vrchatLogDir=>update({vrchatLogDir})} onReset={()=>update({vrchatLogDir:''})} onPending={setPending}/></fieldset></>}
      <span className="secondary">运行配置保存后重启生效。</span>
    </form></Panel>;
  return children?children(form,d):form;
}

export function ImportPanel() {
  const app = useApp(), s = app.status.data!, job = s.import;
  const [source, setSource] = useState(''), [cleanup, setCleanup] = useState(false);
  const [selecting, setSelecting] = useState(false);
  useEffect(() => setCleanup(false), [job.id]);
  return <Panel id="video-import" title="导入视频"><StorageBudgetNotice settings={s.activeSettings}/>
    <p>离线扫描子文件夹，校验并导入已知资源中缺少的视频。同盘优先使用硬链接，导入后请勿修改源视频。</p>
    <p>导入到当前完整视频目录：{s.activeSettings.videoDir}</p>
    <form className="form-grid" onSubmit={async e => { e.preventDefault(); if ((s.settings.videoDir !== s.activeSettings.videoDir || s.settings.databasePath !== s.activeSettings.databasePath) && !await app.confirm('新的数据位置尚未生效', <>已保存的新的数据位置尚未生效。本次将导入到当前完整视频目录：{s.activeSettings.videoDir}。若要使用新目录，请取消并先重启应用。仍要导入当前目录吗？</>)) return; await app.action('import/start',{source:source.trim()}); }}>
      <DirectoryField purpose="import-directory" label="外部目录" value={source} disabled={app.unavailable || !!job.running} onChange={setSource} onPending={setSelecting}/><Button type="submit" disabled={app.unavailable || job.running || selecting || !source.trim()}>扫描并导入</Button>
    </form>{(job.running||!!job.total)&&<Progress value={job.checked} max={job.total} label="视频文件处理进度" />}
    <p role="status">{job.phase?`${job.phase} · ${job.checked || 0}/${job.total || 0} 个视频 · 已导入 ${job.imported || 0} · 跳过 ${job.skipped || 0} · 失败 ${job.failed || 0} · 已清除 ${job.deleted || 0}`:'尚未开始'}</p>
    <Feedback error>{job.error || s.actionErrors?.import}</Feedback>
    {job.cleanupAvailable && !job.running && <><Checkbox checked={cleanup} onChange={(_, v) => setCleanup(!!v.checked)} label="清除原位置本次成功导入的资源（默认保留）" disabled={app.unavailable} /><Button disabled={app.unavailable || !cleanup} onClick={async () => {
      const id = job.id;
      if (await app.confirm('永久删除源视频？', '不可逆操作：将永久删除原位置本次成功导入的源视频，不进入回收站。仅在目标视频仍完整且源文件未改变时删除。确定继续吗？')) { setCleanup(false); await app.action('import/cleanup',{id,confirmed:true}); }
    }}>清除已导入的源文件</Button></>}
  </Panel>;
}
