import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Button, Checkbox, Field, Input, Link, Select, Textarea, Caption1 } from '@fluentui/react-components';
import { bootstrap } from './bootstrap';
import { decode, request } from './api';
import { useApp } from './hooks';
import { Details, Feedback, Panel, Progress } from './ui';
import { type Settings, type Status } from './types';
import { CacheCapacityFields, CacheReclamationFields, capacityPayload, type CapacityDraft } from './cache-capacity';
import { StorageDirectoryFields } from './storage-directory';

const numeric = ['requestRetentionDays','throughputIntervalMinutes','prepareAheadSeconds','downloadSoftLimit','downloadGrowIntervalMS','localProtectionGraceSeconds'] as const;
type NumberField = typeof numeric[number];
export interface ProxyReport { id?: string; accepted: number; skipped: number; failed: number; items: {index: number; protocol?: string; status: string; reason?: string}[] }
export type Draft = Omit<Settings, NumberField | 'maxCacheBytes'> & Record<NumberField, string> & CapacityDraft & { proxyImportID: string; proxyImportText: string; proxyReport?: ProxyReport };
export function draftFrom(s: Settings): Draft {
  const {maxCacheBytes, ...values} = s;
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
    <Caption1 as="p" className="hint">通常使用直连；需要时再配置代理。</Caption1>
    {d.upstreamMode && d.upstreamMode !== 'direct' && <>
      <Field label="代理后端"><Select disabled={locked} value={d.proxyBackend} onChange={(_, v) => update({proxyBackend: v.value})}><option value="external-socks5">外部 SOCKS5</option><option value="mihomo">内嵌 Mihomo</option></Select></Field>
      {d.proxyBackend !== 'mihomo' ? <>
        <Field label="SOCKS5 地址" required hint="填写代理软件的主机:端口，例如 127.0.0.1:7891。"><Input disabled={locked} required value={d.socks5Address} onChange={(_, v) => update({socks5Address: v.value})} placeholder="127.0.0.1:7891" autoComplete="off" /></Field>
        <Field label="SOCKS5 用户名（可选）" hint="仅在代理要求认证时填写用户名和密码。"><Input disabled={locked} value={d.socks5Username} onChange={(_, v) => update({socks5Username: v.value})} autoComplete="off" /></Field>
        <Field label="SOCKS5 密码" hint="已有密码留空则保留；清空用户名和密码可移除认证。"><Input type="password" disabled={locked} value={d.socks5Password || ''} onChange={(_, v) => update({socks5Password: v.value})} autoComplete="new-password" /></Field>
        <Feedback error>{status.socks5CredentialError}</Feedback>
        <Details title="代理凭据与适用范围"><p>密码由 Windows 当前用户加密保护，换用户或电脑后可能需要重新输入。代理用于本应用下载，游戏 HTTPS 转发始终直连。保存后重启应用生效。</p></Details>
      </> : <>
        <p>粘贴代理节点或选择文件，检查后再保存。</p>
        <Feedback error>{[status.proxyNodes?.error, status.proxyNodes?.activeError].filter(Boolean).join('；')}</Feedback>
        <Field label="粘贴节点内容" validationMessage={d.proxyImportText.trim() ? '请先检查节点内容，或清除本次导入草稿。' : undefined} validationState="none"><Textarea rows={5} disabled={locked} value={d.proxyImportText} onChange={(_, v) => update({proxyImportText: v.value})} /></Field>
        <Field label="或选择节点文件">{props => <input {...props} type="file" disabled={locked} accept=".yaml,.yml,.txt,.json" onChange={e => { void readFile(e.target.files?.[0]); e.target.value = ''; }} />}</Field>
        <Caption1 as="p" className="hint">检查仅生成草稿；保存后替换整份节点库，重启生效。</Caption1>
        <Details title="支持的节点格式"><p>支持 YAML 的 proxies、逐行 URI 和 Base64 URI 列表，最多 2 MiB / 2048 条。支持 Mihomo 节点配置（含 VMess/WS）；代理组、链式代理和文件或系统网络选项不导入。不会自动更新订阅。</p></Details>
        <div className="actions"><Button disabled={locked || !d.proxyImportText.trim()} onClick={() => void inspect()}>检查并导入到草稿</Button><Button disabled={locked || (!d.proxyImportID && !d.proxyImportText)} onClick={() => { void releaseImport(d.proxyImportID); update({proxyImportID:'',proxyImportText:'',proxyReport:undefined}); }}>清除本次导入草稿</Button></div>
        {d.proxyReport && <><p>本次接受 {d.proxyReport.accepted} 个，重复跳过 {d.proxyReport.skipped} 个，失败 {d.proxyReport.failed} 个。{d.proxyReport.id ? '草稿 2 小时有效。' : '没有有效节点，不会替换已有节点库。请修改内容或清除本次草稿。'}</p><Details title="逐条导入结果"><ul>{d.proxyReport.items.map(row => <li key={row.index}>第 {row.index} 条 · {row.protocol} · {({accepted:'接受',skipped:'跳过',failed:'失败'}[row.status] || row.status)} {row.reason}</li>)}</ul></Details></>}
        <Details title="节点管理与存储"><p>已保存 {status.proxyNodes?.saved || 0} 个 · 当前已加载 {status.proxyNodes?.loaded || 0} 个 · 活跃 {status.proxyNodes?.active || 0} 个</p><p>节点快照由 Windows 当前用户加密保护；换用户或电脑后可能需要重新导入。默认最多 5 个活跃节点，备用节点保留。初始集合不代表全部节点的最优排名；失败时不会自动启用备用节点。</p><p>代理用于本应用下载，游戏 HTTPS 转发始终直连。</p></Details>
      </>}
    </>}
    <Feedback error>{error}</Feedback>
  </div>;
}

export type SettingsSection = 'application' | 'storage' | 'downloads' | 'network';
export function SettingsPage({section='application',children}: {section?:SettingsSection;children?:(form:ReactNode,draft:Draft)=>ReactNode}) {
  const app = useApp(), s = app.status.data!;
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
    ? `（当前值约占 ${prepareAheadSeconds / 4} MB 临时缓存，按 2 Mbps 估算）` : '';
  const update = (patch: Partial<Draft>) => setDraft(value=>({...value, ...patch}));
  const disabled = app.unavailable || pending || syncing || app.status.loadedRevision !== app.revision;
  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => { if (dirty && !app.stopped) event.preventDefault(); };
    addEventListener('beforeunload', warn); return () => removeEventListener('beforeunload', warn);
  }, [dirty, app.stopped]);
  const numberField = (key: NumberField | 'maxCacheGiB', label: string, min: number, max: number, hint: string, step = '1') => <Field label={label} hint={hint} required><Input id={key} type="number" required min={min} max={max} step={step} disabled={disabled} value={d[key]} onChange={(_, v) => update({[key]:v.value})} /></Field>;
  const form = <Panel id={section==='storage'?'storage-settings':section==='downloads'?'download-settings':section==='network'?'proxy-settings':'preferences'} title={{application:'应用设置',storage:'视频存储',downloads:'预加载与下载设置',network:'代理设置'}[section]}><Feedback error>{s.actionErrors?.settings}</Feedback><form className="form-grid settings-form" onSubmit={async e => { e.preventDefault(); if (!dirty || disabled || d.proxyImportText.trim()) return; if (await app.action('settings', settingsPayload(d))) { setAwaitingRevision(app.revision + 1); } }}>
      <div className="content-stack settings-actions" role="region" aria-label="设置操作"><p role="status">{syncing ? '正在核对已保存的设置…' : dirty ? '有未保存的更改。保存并重启应用后生效。' : s.restartRequired ? '设置已保存，重启应用后生效。' : '配置与当前运行一致。更改后需保存并重启应用。'}</p>
      <div className="actions"><Button type="submit" appearance="primary" disabled={disabled || !dirty || !!d.proxyImportText.trim()}>保存设置</Button><Button disabled={disabled || !dirty} onClick={discard}>放弃更改</Button>
      {s.restartRequired && <Button disabled={disabled || dirty || app.dirtySettings.size>0} onClick={() => void app.action('restart')}>立即重启并应用</Button>}</div>
      {s.restartRequired&&[...app.dirtySettings].some(value=>value!==section)&&<p>其他页面仍有未保存配置，请保存或放弃后再重启。</p>}</div>
      {section==='storage'&&<fieldset disabled={disabled}><legend>本地存储</legend>
      <CacheCapacityFields anchor="maxCacheGiB" mode={d.videoStorageMode} value={d.maxCacheGiB} disabled={disabled} onChange={update} />
      <StorageDirectoryFields mode={d.videoStorageMode} disabled={disabled}>{directoryDisabled =>
        <Field label="本地存储目录" required hint="统一保存视频、歌曲资料和统计记录。下载中的文件也保存在此目录。"><Input required disabled={directoryDisabled} value={d.storageDir} onChange={(_, v) => update({storageDir:v.value})} /></Field>
      }</StorageDirectoryFields>
      <Details title="高级存储设置"><CacheReclamationFields mode={d.videoStorageMode} reclamation={d.aggressiveCacheReclamation} disabled={disabled} onChange={update} />
      {numberField('localProtectionGraceSeconds','本地延续保护时长（秒）',0,10000,'默认 300 秒，可设为 0～10000 的整数。最后一份需求结束后，继续保护本地缓存的时间。0 表示不延续保护；时间越长，占用空间可能越多。')}
      </Details></fieldset>}
      {section==='application'&&<><fieldset disabled={disabled}><legend>控制台启动</legend><Checkbox label="运行控制台时自动启用播放网关" checked={d.autoStartCDN} onChange={(_, v) => update({autoStartCDN:!!v.checked})} /><Checkbox label="运行控制台时自动启用房间队列预加载" checked={d.autoStartQueue} onChange={(_, v) => update({autoStartQueue:!!v.checked})} /><p>两项默认启用，分别控制，互不跟随。保存后在下次启动控制台程序时生效，重新打开网页不会触发。</p><p>自动启用需已同意使用条款，不自动修改游戏接入（hosts）。手动启停不会改变这两项设置。</p></fieldset>
      <Details title="高级游戏接入设置"><Checkbox label="退出时恢复游戏直连" checked={d.restoreHostsOnExit} disabled={disabled} onChange={(_,v)=>update({restoreHostsOnExit:!!v.checked})}/><p>默认开启。关闭后，退出会保留游戏接入，后续使用可减少管理员确认；玩游戏前需要启动 SWD，不再使用时请恢复游戏直连。保存并重启应用后生效。</p></Details>
      {numberField('requestRetentionDays','请求明细保留天数',0,36500,'默认 30 天，0 表示永久保留。只清理请求明细，累计统计保留。')}</>}
      {section==='network'&&<>      <fieldset><legend>网络连接</legend><NetworkFields key={app.settingsEpoch} draft={d} update={update} disabled={disabled} status={s} onPending={setPending} />
      <Details title="高级线路与测速设置"><Field label="播放地址接口选择" hint="忽略游戏内 CDN 选择。自动模式优先 CF，失败后尝试 HKG；固定模式只使用指定接口，视频来源由接口决定。"><Select disabled={disabled} value={d.downloadUpstream} onChange={(_, v) => update({downloadUpstream:v.value})}><option value="auto">自动（CF 优先）</option><option value="cf">node=cf（播放地址接口）</option><option value="hkg">node=nya（播放地址接口）</option></Select></Field>
      {numberField('throughputIntervalMinutes','自动测速间隔（分钟）',1,1440,'默认 20 分钟，按上次测速时间安排下一次检测。')}</Details></fieldset>
</>}
      {section==='downloads'&&<><Details title="高级下载设置"><fieldset disabled={disabled}>
      <p><strong>为减小 CPU 占用，强烈不建议增加并发数或减小增长间隔。</strong>建议保留默认的 16 路和 250 毫秒。保存并重启应用后生效。</p>
      {numberField('downloadSoftLimit','同时下载数量',1,64,'默认 16 路，可设为 1～64 的整数。按需开放下载额度；播放正在等待的缺块可临时突破此上限。')}
      {numberField('downloadGrowIntervalMS','并发增长间隔（毫秒）',0,2000,'默认 250 毫秒，可设为 0～2000 的整数。有可下载任务积压且额度占满时，每隔此时间增加一路；0 表示关闭缓增，按需直接使用上限额度。')}
      </fieldset></Details>
      <fieldset disabled={disabled}><legend>房间队列与游戏日志</legend><Caption1 as="p" className="hint">手动下载与房间队列预加载共同调度，互不暂停。</Caption1>
      {numberField('prepareAheadSeconds','提前准备时间（秒）',100,16000,`默认 500 秒，建议 200～2000 秒${prepareAheadEstimate}。数值越大，越早下载预计播放的内容，也会更早占用带宽和缓存空间。保存并重启应用后生效。`)}
      <Checkbox label="手动指定 VRChat 日志目录" checked={d.manualLogDir} onChange={(_, v) => update({manualLogDir:!!v.checked})} />
      <Field label="VRChat 日志目录" hint="默认自动识别。仅在无法找到游戏日志时手动指定，取消勾选可恢复自动。"><Input required={d.manualLogDir} disabled={!d.manualLogDir || disabled} value={d.manualLogDir ? d.logDir : s.defaultLogDir} onChange={(_, v) => update({logDir:v.value})} /></Field></fieldset></>}
      <Details title="保存与重启说明"><p>重启会停止当前服务和任务，按已保存的自动启动设置重新启动。批量任务需手动重新开始。旧进程按当前退出设置处理游戏接入；默认恢复直连，重启后请点击「开始加速」重新接入。</p></Details>
    </form></Panel>;
  return children?children(form,d):form;
}

export function ImportPanel() {
  const app = useApp(), s = app.status.data!, job = s.import;
  const [source, setSource] = useState(''), [cleanup, setCleanup] = useState(false);
  useEffect(() => setCleanup(false), [job.id]);
  return <Panel id="video-import" title="导入视频"><p>先更新歌曲清单，再递归扫描并逐个校验 MD5，仅导入本地缺少的匹配视频。开始扫描后固定文件列表，不追踪改名或移动的文件。同盘使用 NTFS 硬链接，异盘复制；硬链接的两处文件共享内容，请勿修改源视频。</p>
    <p>导入到当前生效目录：{s.activeSettings.storageDir}（更改保存目录后需重启生效）</p>
    <form className="form-grid" onSubmit={async e => { e.preventDefault(); if (s.settings.storageDir !== s.activeSettings.storageDir && !await app.confirm('新存储目录尚未生效', <>已保存的新存储目录尚未生效。本次将导入到当前生效目录：{s.activeSettings.storageDir}。若要使用新目录，请取消并先重启应用。仍要导入当前目录吗？</>)) return; await app.action('import/start',{source:source.trim()}); }}>
      <Field label="外部目录" required><Input required disabled={app.unavailable || job.running} value={source} onChange={(_, v) => setSource(v.value)} /></Field><Button type="submit" disabled={app.unavailable || job.running || !source.trim()}>扫描并导入</Button>
    </form><Progress value={job.checked} max={job.total} label="视频文件处理进度" />
    <p role="status">{job.phase?`${job.phase} · ${job.checked || 0}/${job.total || 0} 个视频 · 已导入 ${job.imported || 0} · 跳过 ${job.skipped || 0} · 失败 ${job.failed || 0} · 已清除 ${job.deleted || 0}`:'尚未开始'}</p>
    <Feedback error>{job.error || s.actionErrors?.import}</Feedback>
    {job.cleanupAvailable && !job.running && <><Checkbox checked={cleanup} onChange={(_, v) => setCleanup(!!v.checked)} label="清除原位置本次成功导入的资源（默认保留）" disabled={app.unavailable} /><Button disabled={app.unavailable || !cleanup} onClick={async () => {
      const id = job.id;
      if (await app.confirm('永久删除源视频？', '不可逆操作：将永久删除原位置本次成功导入的源视频，不进入回收站。仅在目标视频仍完整且源文件未改变时删除。确定继续吗？')) { setCleanup(false); await app.action('import/cleanup',{id,confirmed:true}); }
    }}>清除已导入的源文件</Button></>}
  </Panel>;
}
