import { useEffect, useRef, useState } from 'react';
import { Button, Checkbox, Field, Input, Link, Select, Textarea, Caption1 } from '@fluentui/react-components';
import { bootstrap } from './bootstrap';
import { decode, request } from './api';
import { useApp } from './hooks';
import { Details, Feedback, Panel, Progress } from './ui';
import { type Settings, type Status } from './types';
import { CacheCapacityFields, CacheReclamationFields, capacityPayload, type CapacityDraft } from './cache-capacity';
import { StorageDirectoryFields } from './storage-directory';

const numeric = ['requestRetentionDays','throughputIntervalMinutes','prepareAheadSeconds','downloadSoftLimit','downloadGrowIntervalMS'] as const;
type NumberField = typeof numeric[number];
export interface ProxyReport { id?: string; accepted: number; skipped: number; failed: number; items: {index: number; protocol?: string; status: string; reason?: string}[] }
export type Draft = Omit<Settings, NumberField | 'maxCacheBytes'> & Record<NumberField, string> & CapacityDraft & { proxyImportID: string; proxyImportText: string; proxyReport?: ProxyReport };
export function draftFrom(s: Settings): Draft {
  const {maxCacheBytes, ...values} = s;
  return { ...values, socks5Password: '', upstreamMode: s.upstreamMode === 'socks5' ? 'proxy' : s.upstreamMode || 'direct', proxyBackend: s.proxyBackend || 'external-socks5',
    requestRetentionDays: String(s.requestRetentionDays ?? 30), throughputIntervalMinutes: String(s.throughputIntervalMinutes ?? 20),
    prepareAheadSeconds: String(s.prepareAheadSeconds ?? 500),
    downloadSoftLimit: String(s.downloadSoftLimit ?? 16), downloadGrowIntervalMS: String(s.downloadGrowIntervalMS ?? 250),
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
      await releaseImport(current.current.proxyImportID);
      update({proxyImportID: report.id || '', proxyImportText: report.id ? '' : content, proxyReport: report});
    } catch (e) { if (id === generation.current) setError(String(e)); }
    finally { if (id === generation.current) busy(false); }
  }
  const locked = disabled || pending;
  return <div className="form-grid">
    <>{wizard ? <><div className="actions">{(['direct','proxy'] as const).map(mode=><Checkbox key={mode} label={mode==='direct'?'直连':'代理'} disabled={locked} checked={d.upstreamMode===mode||d.upstreamMode==='auto'} onChange={(_,v)=>{const other=mode==='direct'?'proxy':'direct';const hasOther=d.upstreamMode===other||d.upstreamMode==='auto';update({upstreamMode:v.checked?hasOther?'auto':mode:hasOther?other:''});}}/>)}</div><p>两项都选时自动选择；只选代理时不会直连。</p></> : <Field label="上游连接"><Select disabled={locked} value={d.upstreamMode} onChange={(_, v) => update({upstreamMode: v.value})}>
      <option value="direct">直连（内置 DNS）</option><option value="proxy">强制代理</option><option value="auto">自动选择直连或代理</option></Select></Field>}</>
    <Caption1 as="p" className="hint">默认直连。需要代理时，可选择外部 SOCKS5 或导入内嵌 Mihomo 节点。</Caption1>
    {d.upstreamMode && d.upstreamMode !== 'direct' && <>
      <Field label="代理后端" hint="代理只用于本应用回源。游戏 HTTPS 兼容透传始终直连。更改保存后需重启应用。"><Select disabled={locked} value={d.proxyBackend} onChange={(_, v) => update({proxyBackend: v.value})}><option value="external-socks5">外部 SOCKS5</option><option value="mihomo">内嵌 Mihomo</option></Select></Field>
      {d.proxyBackend !== 'mihomo' ? <>
        <Field label="SOCKS5 地址" required><Input disabled={locked} required value={d.socks5Address} onChange={(_, v) => update({socks5Address: v.value})} placeholder="127.0.0.1:7891" autoComplete="off" /></Field>
        <Field label="SOCKS5 用户名（可选）"><Input disabled={locked} value={d.socks5Username} onChange={(_, v) => update({socks5Username: v.value})} autoComplete="off" /></Field>
        <Field label="SOCKS5 密码" hint="填写代理软件的主机:端口。无需认证时留空；已有密码留空则保留，清空用户名和密码可移除认证。密码由 Windows 当前用户加密保护，换用户或电脑后可能需要重新输入。"><Input type="password" disabled={locked} value={d.socks5Password || ''} onChange={(_, v) => update({socks5Password: v.value})} autoComplete="new-password" /></Field>
        <Feedback error>{status.socks5CredentialError}</Feedback>{wizard&&<><p>用户名和密码仅在代理要求认证时填写。</p><p>已有密码留空保留；清空用户名和密码可移除认证。</p></>}
      </> : <>
        <p>已保存 {status.proxyNodes?.saved || 0} 个 · 当前已加载 {status.proxyNodes?.loaded || 0} 个 · 活跃 {status.proxyNodes?.active || 0} 个</p>
        <Feedback error>{[status.proxyNodes?.error, status.proxyNodes?.activeError].filter(Boolean).join('；')}</Feedback>
        <Field label="粘贴节点内容" validationMessage={d.proxyImportText.trim() ? '请先检查节点内容，或清除本次导入草稿。' : undefined} validationState="none"><Textarea rows={5} disabled={locked} value={d.proxyImportText} onChange={(_, v) => update({proxyImportText: v.value})} /></Field>
        <Field label="或选择节点文件">{props => <input {...props} type="file" disabled={locked} accept=".yaml,.yml,.txt,.json" onChange={e => { void readFile(e.target.files?.[0]); e.target.value = ''; }} />}</Field>
        <Caption1 as="p" className="hint">支持 YAML 的 proxies、逐行 URI 和 Base64 URI 列表，最多 2 MiB / 2048 条。支持 Mihomo 节点配置（含 VMess/WS）；代理组、链式代理和文件或系统网络选项不导入。检查仅生成草稿，保存后替换整份节点库，重启生效。不会自动更新订阅。</Caption1>
        <div className="actions"><Button disabled={locked || !d.proxyImportText.trim()} onClick={() => void inspect()}>检查并导入到草稿</Button><Button disabled={locked || (!d.proxyImportID && !d.proxyImportText)} onClick={() => { void releaseImport(d.proxyImportID); update({proxyImportID:'',proxyImportText:'',proxyReport:undefined}); }}>清除本次导入草稿</Button></div>
        {d.proxyReport && <><p>本次接受 {d.proxyReport.accepted} 个，重复跳过 {d.proxyReport.skipped} 个，失败 {d.proxyReport.failed} 个。{d.proxyReport.id ? '保存时将以接受的节点替换整份节点库；草稿 2 小时有效。' : '没有有效节点，不会替换已有节点库。请修改内容或清除本次草稿。'}</p><Details title="逐条导入结果"><ul>{d.proxyReport.items.map(row => <li key={row.index}>第 {row.index} 条 · {row.protocol} · {({accepted:'接受',skipped:'跳过',failed:'失败'}[row.status] || row.status)} {row.reason}</li>)}</ul></Details></>}
      </>}
      <Caption1 as="p" className="hint" hidden={d.proxyBackend!=='mihomo'}>节点快照由 Windows 当前用户加密保护；换用户或电脑后可能需要重新导入。默认最多 5 个活跃节点，备用节点保留。初始集合不代表全部节点的最优排名。</Caption1>
    </>}
    <Feedback error>{error}</Feedback>
  </div>;
}

export function SettingsPage() {
  const app = useApp(), s = app.status.data!;
  const [draft, setDraft] = useState<Draft>(), [pending, setPending] = useState(false);
  const [awaitingRevision, setAwaitingRevision] = useState<number>();
  useEffect(() => { setDraft(undefined); setAwaitingRevision(undefined); }, [app.settingsEpoch]);
  useEffect(() => {
    if (awaitingRevision !== undefined && app.status.loadedRevision !== undefined && app.status.loadedRevision >= awaitingRevision) {
      setDraft(undefined); setAwaitingRevision(undefined);
    }
  }, [awaitingRevision, app.status.loadedRevision]);
  const syncing = awaitingRevision !== undefined;
  const d = draft || draftFrom(s.settings), dirty = !syncing && fingerprint(d) !== fingerprint(draftFrom(s.settings));
  const prepareAheadSeconds = Number(d.prepareAheadSeconds);
  const prepareAheadEstimate = Number.isInteger(prepareAheadSeconds) && prepareAheadSeconds >= 100 && prepareAheadSeconds <= 16000
    ? `（当前值约占 ${prepareAheadSeconds / 4} MB 临时缓存，按 2 Mbps 估算）` : '';
  const update = (patch: Partial<Draft>) => setDraft({...d, ...patch});
  const disabled = app.unavailable || pending || syncing;
  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => { if (dirty && !app.stopped) event.preventDefault(); };
    addEventListener('beforeunload', warn); return () => removeEventListener('beforeunload', warn);
  }, [dirty, app.stopped]);
  const numberField = (key: NumberField | 'maxCacheGiB', label: string, min: number, max: number, hint: string, step = '1') => <Field label={label} hint={hint} required><Input id={key} type="number" required min={min} max={max} step={step} disabled={disabled} value={d[key]} onChange={(_, v) => update({[key]:v.value})} /></Field>;
  return <><Panel title="使用引导"><p>整理存储、网络与导入清单，最后核对并应用。</p><Link href="#/setup">重新打开使用引导</Link></Panel>
    <ImportPanel />
    <Panel id="preferences" title="偏好设置"><Feedback error>{s.actionErrors?.settings}</Feedback><form className="form-grid" onSubmit={async e => { e.preventDefault(); if (!dirty || disabled || d.proxyImportText.trim()) return; if (await app.action('settings', settingsPayload(d))) { setDraft(d); setAwaitingRevision(app.revision + 1); } }}>
      <fieldset disabled={disabled}><legend>本地存储</legend>
      <CacheCapacityFields anchor="maxCacheGiB" mode={d.videoStorageMode} value={d.maxCacheGiB} disabled={disabled} onChange={update} />
      <CacheReclamationFields mode={d.videoStorageMode} reclamation={d.aggressiveCacheReclamation} disabled={disabled} onChange={update} />
      <StorageDirectoryFields mode={d.videoStorageMode} disabled={disabled}>{directoryDisabled =>
        <Field label="本地存储目录" required hint="统一保存视频、歌曲资料和统计记录。下载中的文件也保存在此目录。"><Input required disabled={directoryDisabled} value={d.storageDir} onChange={(_, v) => update({storageDir:v.value})} /></Field>
      }</StorageDirectoryFields>
      {numberField('requestRetentionDays','请求明细保留天数',0,36500,'默认 30 天，0 表示永久保留。只清理请求明细，累计统计保留。')}</fieldset>
      <fieldset disabled={disabled}><legend>控制台启动</legend><Checkbox label="运行控制台时自动启用播放网关" checked={d.autoStartCDN} onChange={(_, v) => update({autoStartCDN:!!v.checked})} /><Checkbox label="运行控制台时自动启用房间队列预加载" checked={d.autoStartQueue} onChange={(_, v) => update({autoStartQueue:!!v.checked})} /><p>两项默认启用，分别控制，互不跟随。保存后在下次启动控制台程序时生效，重新打开网页不会触发。</p><p>自动启用需已同意使用条款，不自动修改游戏接入（hosts）。手动启停不会改变这两项设置。</p></fieldset>
      <fieldset><legend>网络连接</legend><NetworkFields draft={d} update={update} disabled={app.unavailable || syncing} status={s} onPending={setPending} />
      <Field label="播放地址接口选择" hint="忽略游戏内 CDN 选择。自动模式优先 CF，失败后尝试 HKG；固定模式只使用指定接口，视频来源由接口决定。"><Select disabled={disabled} value={d.downloadUpstream} onChange={(_, v) => update({downloadUpstream:v.value})}><option value="auto">自动（CF 优先）</option><option value="cf">node=cf（播放地址接口）</option><option value="hkg">node=nya（播放地址接口）</option></Select></Field>
      {numberField('throughputIntervalMinutes','自动测速间隔（分钟）',1,1440,'默认 20 分钟，按上次测速时间安排下一次检测。')}</fieldset>
      <fieldset disabled={disabled}><legend>高级下载设置</legend>
      <p><strong>为减小 CPU 占用，强烈不建议增加并发数或减小增长间隔。</strong>建议保留默认的 16 路和 250 毫秒。保存并重启应用后生效。</p>
      {numberField('downloadSoftLimit','下载并发上限（软）',1,64,'默认 16 路，可设为 1～64 的整数。按需开放下载额度；播放正在等待的缺块可临时突破此上限。')}
      {numberField('downloadGrowIntervalMS','并发增长间隔（毫秒）',0,2000,'默认 250 毫秒，可设为 0～2000 的整数。有可下载任务积压且额度占满时，每隔此时间增加一路；0 表示关闭缓增，按需直接使用上限额度。')}
      </fieldset>
      <fieldset disabled={disabled}><legend>房间队列与游戏日志</legend><Caption1 as="p" className="hint">下载补齐期间，房间队列预加载会临时暂停。</Caption1>
      {numberField('prepareAheadSeconds','提前准备时间 M（秒）',100,16000,`默认 500 秒，建议 200～2000 秒${prepareAheadEstimate}。数值越大，越早下载预计播放的内容，也会更早占用带宽和缓存空间。保存并重启应用后生效。`)}
      <Checkbox label="手动指定 VRChat 日志目录" checked={d.manualLogDir} onChange={(_, v) => update({manualLogDir:!!v.checked})} />
      <Field label="VRChat 日志目录" hint="默认自动识别。仅在无法找到游戏日志时手动指定，取消勾选可恢复自动。"><Input required={d.manualLogDir} disabled={!d.manualLogDir || disabled} value={d.manualLogDir ? d.logDir : s.defaultLogDir} onChange={(_, v) => update({logDir:v.value})} /></Field></fieldset>
      <p role="status">{syncing ? '正在核对已保存的设置…' : dirty ? '有未保存的更改。保存并重启应用后生效。' : s.restartRequired ? '设置已保存，重启应用后生效。' : '配置与当前运行一致。更改后需保存并重启应用。'}</p>
      <div className="actions"><Button type="submit" appearance="primary" disabled={disabled || !dirty || !!d.proxyImportText.trim()}>保存设置</Button><Button disabled={disabled || !dirty} onClick={() => { void releaseImport(d.proxyImportID); setDraft(undefined); }}>放弃更改</Button>
      {s.restartRequired && <Button disabled={disabled || dirty} onClick={() => void app.action('restart')}>立即重启并应用</Button>}</div>
      <Caption1 as="p" className="hint">重启会停止当前服务和任务，按已保存的自动启动设置重新启动。批量任务需手动重新开始，hosts 映射保留。</Caption1>
    </form></Panel></>;
}

function ImportPanel() {
  const app = useApp(), s = app.status.data!, job = s.import;
  const [source, setSource] = useState(''), [cleanup, setCleanup] = useState(false);
  useEffect(() => setCleanup(false), [job.id]);
  return <Panel title="从外部目录导入视频"><p>先更新歌曲清单，再递归扫描并逐个校验 MD5，仅导入本地缺少的匹配视频。开始扫描后固定文件列表，不追踪改名或移动的文件。同盘使用 NTFS 硬链接，异盘复制；硬链接的两处文件共享内容，请勿修改源视频。</p>
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
