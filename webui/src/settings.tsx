import { useEffect, useRef, useState, type ReactNode, type Ref } from 'react';
import { Button, Checkbox, Field, Input, Select, Textarea, Caption1, Tooltip } from '@fluentui/react-components';
import { bootstrap } from './bootstrap';
import { APIError, decode, request } from './api';
import { useApp } from './hooks';
import { Feedback, Panel, Progress, Hint, useThemePreference } from './ui';
import { type Settings, type Status } from './types';
import { CacheCapacityFields, CacheReclamationFields, capacityPayload, type CapacityDraft } from './cache-capacity';
import { StorageLocationFields, locationRequestError, locationValidationKey, type LocationValidation } from './storage-directory';
import { DirectoryField, samePath } from './path-picker';
import { ImportSpaceHint } from './import-space';
import { RequestRetentionFields } from './request-retention';
import { displayStoragePath } from './storage-path';
import {FieldErrors,ValidationField,inputError} from './field-validation';

const numeric = ['requestRetentionDays','throughputIntervalMinutes','prepareAheadSeconds','downloadConcurrencySoftLimit','downloadGrowIntervalMS','localProtectionGraceSeconds'] as const;
const restorePermissionHint='开启并保存时，可能请求 Windows 管理员授权（UAC）。';
type NumberField = typeof numeric[number];
export interface ProxyReport { id: string; nodes: number; groups: number }
export type Draft = Omit<Settings, NumberField | 'videoBudgetBytes' | 'theme'> & Record<NumberField, string> & CapacityDraft & { proxyImportID: string; proxyImportText: string; proxyImportSource: 'config'|'url'|'base64'; proxyReport?: ProxyReport };
export function draftFrom(s: Settings): Draft {
  const {videoBudgetBytes, theme: _theme, ...values} = s;
  // The backend supplies complete, normalized settings; drafts only format them.
  return { ...values, socks5Password: '',
    requestRetentionDays: String(s.requestRetentionDays), throughputIntervalMinutes: String(s.throughputIntervalMinutes),
    prepareAheadSeconds: String(s.prepareAheadSeconds),
    downloadConcurrencySoftLimit: String(s.downloadConcurrencySoftLimit), downloadGrowIntervalMS: String(s.downloadGrowIntervalMS),
    localProtectionGraceSeconds: String(s.localProtectionGraceSeconds),
    videoBudgetGiB: String(videoBudgetBytes / 1073741824), proxyImportID: '', proxyImportText: '', proxyImportSource: 'config' };
}
export function settingsPayload(d: Draft) {
  const { videoStorageMode, aggressiveBlockReclamation, videoBudgetGiB, proxyImportText: _text, proxyReport: _report, proxyImportSource: _source, socks5Password, ...values } = d;
  return { ...values, ...Object.fromEntries(numeric.map(k => [k, Number(d[k])])), ...capacityPayload({videoStorageMode, aggressiveBlockReclamation, videoBudgetGiB}),
    ...(socks5Password || !d.socks5Username ? {socks5Password: socks5Password || ''} : {}) };
}
export function fingerprint(d: Draft) {
  return JSON.stringify({...settingsPayload(d), proxyImportText: d.proxyImportText, proxyImportSource: d.proxyImportText ? d.proxyImportSource : ''});
}
export const releaseImport = async (id: string) => { if (id) try { await request('proxy/import/discard', {id}); } catch { /* Local discard remains available offline; server expiry is the fallback. */ } };

type FormProps = { draft: Draft; update: (values: Partial<Draft>) => void; disabled: boolean; status: Status };
export function NetworkFields({ draft: d, update, disabled, status, onPending }: FormProps & {onPending: (pending: boolean) => void}) {
  const [pending, setPending] = useState(false), [error, setError] = useState('');
  const [errorField,setErrorField]=useState<'proxyImportText'|'proxyFile'>('proxyImportText');
  const generation = useRef(0), current = useRef(d); current.current = d;
  useEffect(() => () => { generation.current++; }, []);
  const busy = (value: boolean) => { setPending(value); onPending(value); };
  async function readFile(file?: File) {
    if (!file) return;
    const id = ++generation.current; busy(true); setError('');setErrorField('proxyFile');
    try { if (file.size > 2 * 1024 * 1024) throw new Error('配置文件超过 2 MiB。'); const text = await file.text(); if (id === generation.current) update({proxyImportText: text,proxyImportSource: 'config'}); }
    catch (e) { if (id === generation.current) setError(String(e)); }
    finally { if (id === generation.current) busy(false); }
  }
  async function inspect() {
    if (pending || disabled || !d.proxyImportText.trim()) return;
    const id = ++generation.current, content = d.proxyImportText; busy(true); setError('');setErrorField('proxyImportText');
    try {
      const response = await fetch('/api/proxy/import', {method: 'POST', headers: {'Content-Type':'text/plain', 'X-SWD-Token':bootstrap.token,'X-SWD-Import-Source':d.proxyImportSource},body: content, signal: AbortSignal.timeout(100000)});
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
      <option value="direct">直连（内置 DNS）</option><option value="proxy">代理</option></Select></Field>

    {d.upstreamMode && d.upstreamMode !== 'direct' && <>
      <Field label="代理后端"><Select disabled={locked} value={d.proxyBackend} onChange={(_, v) => update({proxyBackend: v.value})}><option value="external-socks5">外部 SOCKS5</option><option value="mihomo">内嵌 Mihomo</option></Select></Field>
      {d.proxyBackend !== 'mihomo' ? <>
        <ValidationField name="socks5Address" label="SOCKS5 地址" required hint="代理软件的主机:端口。"><Input name="socks5Address" disabled={locked} required value={d.socks5Address} onChange={(_, v) => update({socks5Address: v.value})} placeholder="127.0.0.1:7891" autoComplete="off" /></ValidationField>
        <ValidationField name="socks5Username" label="SOCKS5 用户名（可选）"><Input name="socks5Username" disabled={locked} value={d.socks5Username} onChange={(_, v) => update({socks5Username: v.value})} autoComplete="off" /></ValidationField>
        <ValidationField name="socks5Password" label="SOCKS5 密码" hint="留空保留已有密码；清空用户名可移除认证。"><Input name="socks5Password" type="password" disabled={locked} value={d.socks5Password || ''} onChange={(_, v) => update({socks5Password: v.value})} autoComplete="new-password" /></ValidationField>
        <Feedback error>{status.socks5CredentialError}</Feedback>
      </> : <>
        <Feedback error>{[status.proxyNodes?.error, status.proxyNodes?.activeError].filter(Boolean).join('；')}</Feedback>
        <Field label="配置来源"><Select disabled={locked} value={d.proxyImportSource} onChange={(_,v)=>{setError('');update({proxyImportSource:v.value as Draft['proxyImportSource'],proxyImportText:''});}}>
          <option value="config">配置文件或内容</option><option value="url">URL</option><option value="base64">Base64</option>
        </Select></Field>
        <ValidationField name="proxyImportText" error={errorField==='proxyImportText'?error:undefined} label={d.proxyImportSource==='url'?'配置 URL':d.proxyImportSource==='base64'?'Base64 配置':'配置内容'}><Textarea name="proxyImportText" rows={d.proxyImportSource==='url'?2:5} disabled={locked} value={d.proxyImportText} onChange={(_, v) => {setError('');update({proxyImportText: v.value});}} /></ValidationField>
        {d.proxyImportSource==='config'&&<ValidationField error={errorField==='proxyFile'?error:undefined} label="选择配置文件">{props=><input {...props} type="file" disabled={locked} accept=".yaml,.yml,.json" onChange={e => { void readFile(e.target.files?.[0]); e.target.value = ''; }} />}</ValidationField>}
        <Caption1 as="p" className="hint">保存后替换整份配置，重启生效。URL 仅下载一次，不保存地址。</Caption1>
        <div className="inline secondary">Mihomo 原生配置<Hint label="配置格式说明">文件或 Base64 解码后的内容须为合法配置，最多 2 MiB。原有组与 DNS 等设置保留，接入设置由 SWD 管理。</Hint></div>
        <div className="actions"><Button disabled={locked || !d.proxyImportText.trim()} onClick={() => void inspect()}>检查并导入到草稿</Button><Button disabled={locked || (!d.proxyImportID && !d.proxyImportText)} onClick={() => { void releaseImport(d.proxyImportID); update({proxyImportID:'',proxyImportText:'',proxyReport:undefined}); }}>清除本次导入草稿</Button></div>
        {d.proxyReport && <p>配置检查通过：{d.proxyReport.nodes} 个内置节点，{d.proxyReport.groups} 个原有代理组。</p>}
        <span className="secondary">配置内置节点：已保存 {status.proxyNodes?.saved || 0} 个 · 已加载 {status.proxyNodes?.loaded || 0} 个</span>
      </>}
    </>}
  </div>;
}

export type SettingsSection = 'application' | 'storage' | 'downloads' | 'network';
export function SettingsPage({section='application',children}: {section?:SettingsSection;children:(form:ReactNode,actions:ReactNode)=>ReactNode}) {
  const app = useApp(), s = app.status.data!;
  const theme=useThemePreference();
  const [draft, setDraft] = useState<Partial<Draft>>(), [pending, setPending] = useState(false);
  const [awaitingRevision, setAwaitingRevision] = useState<number>();
  const [locationError, setLocationError] = useState('');
  const [errors,setErrors]=useState<Record<string,string>>({});
  const formRef=useRef<HTMLFormElement>(null);
  const [locationValidation, setLocationValidation] = useState<LocationValidation>({});
  const discard = () => { void releaseImport(draft?.proxyImportID || ''); setDraft(undefined); setPending(false); setAwaitingRevision(undefined); setLocationValidation({}); setErrors({});setLocationError(''); };
  useEffect(() => {
    if (awaitingRevision !== undefined && app.status.loadedRevision !== undefined && app.status.loadedRevision >= awaitingRevision) {
      setDraft(undefined); setAwaitingRevision(undefined);setErrors({});
    }
  }, [awaitingRevision, app.status.loadedRevision]);
  const syncing = awaitingRevision !== undefined;
  const d = {...draftFrom(s.settings),...draft}, dirty = !syncing && fingerprint(d) !== fingerprint(draftFrom(s.settings));
  useEffect(()=>{app.setSettingsDirty(section,dirty);return ()=>app.setSettingsDirty(section,false);},[section,dirty,app.setSettingsDirty]);
  const prepareAheadSeconds = Number(d.prepareAheadSeconds);
  const prepareAheadEstimate = Number.isInteger(prepareAheadSeconds) && prepareAheadSeconds >= 100 && prepareAheadSeconds <= 16000
    ? `约 ${prepareAheadSeconds / 4} MB 临时缓存（按 2 Mbps）` : '';
  const update = (patch: Partial<Draft>) => {
    setDraft(value=>({...value,...patch}));
    setErrors(previous=>{
      const next={...previous};
      for(const key of Object.keys(patch)) {
        const name=key==='videoBudgetGiB'?'videoBudgetBytes':key;
        if(!(name in next))continue;
        const input=formRef.current?.elements.namedItem(name);
        const message=isInput(input)?inputError(input):'';
        if(message)next[name]=message;else delete next[name];
      }
      if('proxyImportID' in patch||'proxyImportSource' in patch)delete next.proxyImportText;
      if('upstreamMode' in patch||'proxyBackend' in patch)for(const name of ['socks5Address','socks5Username','socks5Password','proxyImportText'])delete next[name];
      if('requestRetentionMode' in patch)delete next.requestRetentionDays;
      if('videoStorageMode' in patch)delete next.videoBudgetBytes;
      return next;
    });
  };
  function isInput(value:unknown):value is HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement {
    return value instanceof HTMLInputElement||value instanceof HTMLSelectElement||value instanceof HTMLTextAreaElement;
  }
  function validateBlur(target:EventTarget) {
    if(!isInput(target)||!target.name)return;
    // Backend/combined errors survive focus changes; only an edit or a new
    // validation result can clear them. Read-only paths have their own check.
    if(target instanceof HTMLInputElement&&target.readOnly)return;
    const message=inputError(target);
    if(!message&&target.name!=='socks5Address'&&!(target instanceof HTMLInputElement&&target.type==='number'))return;
    setErrors(previous=>{const next={...previous};if(message)next[target.name]=message;else delete next[target.name];return next;});
  }
  const disabled = app.unavailable || pending || syncing || app.status.loadedRevision !== app.revision;
  const locationChecking = (section === 'storage' || section === 'downloads') && !!locationValidation.checked && locationValidation.checked !== locationValidationKey(d,s.settings);
  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => { if (dirty && !app.stopped) event.preventDefault(); };
    addEventListener('beforeunload', warn); return () => removeEventListener('beforeunload', warn);
  }, [dirty, app.stopped]);
  const numberField = (key: NumberField | 'videoBudgetGiB', label: string, min: number, max: number, hint?: string, step = '1') => <ValidationField name={key} label={label} hint={hint} required><Input name={key} id={key} type="number" required min={min} max={max} step={step} disabled={disabled} value={d[key]} onChange={(_, v) => update({[key]:v.value})} /></ValidationField>;
  const saveButton=<Button type="submit" form={'settings-form-'+section} appearance="primary" disabled={disabled || locationChecking || !dirty}>保存设置</Button>;
  const restartNeedsSaving=s.restartRequired&&(dirty||app.dirtySettings.size>0);
  const restartHint=restartNeedsSaving
    ? [...app.dirtySettings].some(value=>value!==section)?'其他页面仍有未保存配置，请保存或放弃后再重启。':'请先保存或放弃本页更改，再重启。'
    : s.settings.autoAccelerate?'重启后将自动开启视频加速，接入时可能请求 Windows 管理员授权（UAC）。':undefined;
  const restartAction=<Button disabled={disabled || dirty || app.dirtySettings.size>0 || !s.restartRequired} disabledFocusable={restartNeedsSaving} onClick={() => void app.action('restart')}>重启并应用</Button>;
  const restartButton=restartHint?<Tooltip content={restartHint} relationship="description">{restartAction}</Tooltip>:restartAction;
  async function save() {
    if (!dirty || disabled || locationChecking) return;
    const checked:Record<string,string>={};
    for(const input of Array.from(formRef.current?.elements||[]))if(isInput(input)&&input.name){const error=inputError(input);if(error)checked[input.name]=error;}
    setErrors(checked);
    if(Object.keys(checked).length) {
      const first=formRef.current?.elements.namedItem(Object.keys(checked)[0]);
      if(isInput(first))first.focus();
      return;
    }
    if(locationValidation.warning)return;
    setPending(true); setLocationError('');
    try {
      const saved = settingsPayload(draftFrom(s.settings)), values = settingsPayload(d);
      const patch = Object.fromEntries(Object.entries(values).filter(([key,value]) => value !== saved[key as keyof typeof saved]));
      if (locationValidation.pendingDatabase) {
        if (!samePath(locationValidation.pendingDatabase, values.databasePath)) {
          setLocationValidation(current=>({...current,warning:{field:'database',message:'数据库位置已变化，请重新选择'}}));
          return;
        }
        let preparingDatabase = false;
        try {
          const check = await request<{valid:boolean;field?:string;message?:string}>('storage/validate-location', {databasePath:values.databasePath,videoDir:values.videoDir,blockCacheDir:values.blockCacheDir,selection:'database'});
          if (!check.valid) {
            const field = check.field === 'video' || check.field === 'cache' ? check.field : 'database';
            setLocationValidation(current=>({...current,warning:{field,message:check.message || '所选存储位置不可用'}}));
            return;
          }
          preparingDatabase = true;
          await request('storage/prepare-database', {databasePath:values.databasePath,videoDir:values.videoDir,blockCacheDir:values.blockCacheDir});
          setLocationValidation(current=>({...current,warning:undefined,pendingDatabase:undefined,databaseError:undefined}));
        } catch (error) {
          const message = locationRequestError(error,preparingDatabase);
          setLocationValidation(current=>({...current,warning:{field:'database',message},databaseError:message}));
          return;
        }
      }
      let confirmed: Pick<Settings,'databasePath'|'videoDir'|'videoStorageMode'|'videoBudgetBytes'|'retainFavorites'> | undefined;
      if (d.databasePath !== s.settings.databasePath || d.videoDir !== s.settings.videoDir) {
        const check = await request<{confirmVideoPolicy:boolean}>('storage/check', {databasePath:d.databasePath,videoDir:d.videoDir,blockCacheDir:d.blockCacheDir});
        if (check.confirmVideoPolicy) {
          const accepted = await app.confirm('确认此目录的视频保留方式', <><p className="wrap">视频目录：{displayStoragePath(d.videoDir,s)}</p><p className="wrap">数据库：{displayStoragePath(d.databasePath,s)||'尚未指定'}</p><p>此目录的视频将按本次设置管理：{d.videoStorageMode==='none'?'不保留普通视频':d.videoStorageMode==='unlimited'?'不限制视频空间':d.videoBudgetGiB+' GiB 视频预算'}。不符合保留策略的视频可能被删除。</p><p>{d.retainFavorites?'优先保留收藏视频。':'收藏不提供额外保留保护。'}</p></>, '确认并保存');
          if (!accepted) return;
          const {databasePath,videoDir,videoStorageMode,videoBudgetBytes,retainFavorites} = values;
          confirmed = {databasePath,videoDir,videoStorageMode,videoBudgetBytes,retainFavorites};
        }
      }
      if (await app.action('settings', {...patch,confirmVideoPolicy:confirmed},error=>{
        if(error.code!=='settings_validation'||!error.fields||!Object.keys(error.fields).some(name=>formRef.current?.elements.namedItem(name)))return false;
        setErrors(error.fields);return true;
      })) setAwaitingRevision(app.revision + 1);
    } catch (e) {
      if(e instanceof APIError&&e.code==='settings_validation'&&e.fields)setErrors(e.fields);
      else setLocationError(String(e));
    }
    finally { setPending(false); }
  }
  const actions = <div className="settings-actions" role="region" aria-label="设置操作"><p role="status">{syncing ? '正在同步设置…' : dirty ? '未保存' : s.restartRequired ? '待重启' : '已生效'}</p>
      <div className="actions">{d.restoreHostsOnStopOrExit&&!s.settings.restoreHostsOnStopOrExit?<Tooltip content={restorePermissionHint} relationship="description">{saveButton}</Tooltip>:saveButton}<Button disabled={disabled || (!dirty && !locationValidation.warning)} onClick={discard}>放弃更改</Button>
      {restartButton}</div></div>;
  const form = <FieldErrors.Provider value={errors}><Panel id={section==='storage'?'storage-settings':section==='downloads'?'download-settings':section==='network'?'proxy-settings':'preferences'}><Feedback error>{locationError||s.actionErrors?.settings}</Feedback><form ref={formRef} noValidate onBlurCapture={e=>validateBlur(e.target)} id={'settings-form-'+section} className="form-grid settings-form" onSubmit={e => { e.preventDefault(); void save(); }}>
      {section==='storage'&&<fieldset disabled={disabled} aria-label="本地存储">
      <StorageLocationFields section="storage" value={d} update={update} disabled={disabled} status={s} validation={locationValidation} onValidation={setLocationValidation} onPending={setPending} />
      <CacheCapacityFields anchor="videoBudgetGiB" mode={d.videoStorageMode} value={d.videoBudgetGiB} disabled={disabled} onChange={update} />
      <Checkbox label="优先保留收藏视频" checked={d.retainFavorites} disabled={disabled || d.videoStorageMode==='none'} onChange={(_,v)=>update({retainFavorites:!!v.checked})}/>
      <span className="secondary">收藏视频在预算内优先保留，不会自动下载。</span>
      </fieldset>}
      {section==='application'&&<><Field label="主题" hint="自动保存并立即应用，无需重启。"><Select disabled={disabled} value={theme.preference} onChange={async(_,v)=>{const value=v.value as 'system'|'light'|'dark';if(await app.action('settings/theme',{theme:value}))theme.setPreference(value);}}><option value="system">跟随系统</option><option value="light">浅色</option><option value="dark">深色</option></Select></Field><fieldset disabled={disabled}><legend>程序启动</legend><Checkbox label="启动程序时自动打开 WebUI" checked={d.autoOpenWebUI} onChange={(_,v)=>update({autoOpenWebUI:!!v.checked})}/>
      {/* Checkbox forwards trigger props to its input; put pointer handlers on the root to include the label. */}
      <Tooltip content="下次启动自动加速时，可能请求 Windows 管理员授权（UAC）。" relationship="description">{({onPointerEnter,onPointerLeave,ref,...trigger})=><Checkbox {...trigger} ref={ref as Ref<HTMLInputElement>} root={{onPointerEnter,onPointerLeave}} label="启动程序时自动开启视频加速" checked={d.autoAccelerate} onChange={(_, v) => update({autoAccelerate:!!v.checked})} />}</Tooltip></fieldset>
      <fieldset><legend>停止与退出</legend><Checkbox label="随 VRChat 退出" checked={d.exitWithVRChat} disabled={disabled} onChange={(_,v)=>update({exitWithVRChat:!!v.checked})}/>
      <Tooltip content={restorePermissionHint} relationship="description">{({onPointerEnter,onPointerLeave,ref,...trigger})=><Checkbox {...trigger} ref={ref as Ref<HTMLInputElement>} root={{onPointerEnter,onPointerLeave}} id="restoreHostsOnStopOrExit" label="停止加速或退出时恢复游戏直连" checked={d.restoreHostsOnStopOrExit} disabled={disabled} onChange={(_,v)=>update({restoreHostsOnStopOrExit:!!v.checked})}/>}</Tooltip><span className="secondary">保存后立即用于停止加速和退出。关闭后需开启加速才能播放。</span></fieldset>
      <RequestRetentionFields mode={d.requestRetentionMode} value={d.requestRetentionDays} disabled={disabled} onChange={update} />
      <Field label="程序日志级别"><Select disabled={disabled} value={d.logLevel} onChange={(_,v)=>update({logLevel:v.value as Settings['logLevel']})}><option value="off">关闭</option><option value="error">仅错误（Error）</option><option value="warn">警告及错误（Warn）</option><option value="info">信息（Info，默认）</option><option value="debug">详细调试（Debug）</option></Select></Field></>}
      {section==='network'&&<>      <fieldset aria-label="网络连接"><NetworkFields draft={d} update={update} disabled={disabled} status={s} onPending={setPending} />
      {d.upstreamMode === 'direct' && numberField('throughputIntervalMinutes','自动测速间隔（分钟）',1,1440)}</fieldset>
</>}
      {section==='downloads'&&<><fieldset disabled={disabled}><legend>视频缓存</legend>
      <StorageLocationFields section="cache" value={d} update={update} disabled={disabled} status={s} validation={locationValidation} onValidation={setLocationValidation} onPending={setPending} />
      <div className="settings-columns"><CacheReclamationFields mode={d.videoStorageMode} reclamation={d.aggressiveBlockReclamation} disabled={disabled} onChange={update} />
      {numberField('localProtectionGraceSeconds','本地延续保护时长（秒）',0,10000,'需求结束后继续保留缓存；0 表示不延续保护。')}
      </div></fieldset>
      <Checkbox label="随视频网关启用队列预加载" checked={d.queueWithGateway} disabled={disabled} onChange={(_,v)=>update({queueWithGateway:!!v.checked})}/><fieldset disabled={disabled}><legend>下载</legend><div className="settings-columns">

      {numberField('downloadConcurrencySoftLimit','同时下载数量',1,64,'建议 16 路；播放急需时可临时超出。')}
      {numberField('downloadGrowIntervalMS','并发增长间隔（毫秒）',0,2000,'建议 250 ms；0 表示关闭缓增。')}
      </div></fieldset>
      <fieldset disabled={disabled}><legend>房间队列与游戏日志</legend>
      {numberField('prepareAheadSeconds','提前准备时间（秒）',100,16000,`建议 200～2000 秒。${prepareAheadEstimate}`)}
      <DirectoryField purpose="log-directory" label="VRChat 日志目录" hint="默认自动识别，找不到日志时点击选择。" disabled={disabled} value={d.vrchatLogDir || s.defaultLogDir} defaultValue={s.defaultLogDir} isDefault={!d.vrchatLogDir}
        onChange={vrchatLogDir=>update({vrchatLogDir})} onReset={()=>update({vrchatLogDir:''})} onPending={setPending}/></fieldset></>}
      <span className="secondary">运行配置保存后重启生效。</span>
    </form></Panel></FieldErrors.Provider>;
  return children(form,actions);
}

export function ImportPanel({storageBudget}: {storageBudget:ReactNode}) {
  const app = useApp(), s = app.status.data!, job = s.import;
  const [source, setSource] = useState(''), [cleanup, setCleanup] = useState(false);
  const [selecting, setSelecting] = useState(false);
  useEffect(() => setCleanup(false), [job.id,job.running]);
  return <Panel id="video-import" title="导入视频">{storageBudget}
    <p>导入缺少的视频。请勿修改源视频。</p>
    <p className="wrap">本次导入到完整视频目录：{displayStoragePath(job.videoDir || s.activeSettings.videoDir,s)}</p>
    <form className="form-grid" onSubmit={async e => { e.preventDefault(); if ((s.settings.videoDir !== s.activeSettings.videoDir || s.settings.databasePath !== s.activeSettings.databasePath) && !await app.confirm('新的数据位置尚未生效', <>本次将导入到当前完整视频目录：{displayStoragePath(s.activeSettings.videoDir,s)}。若要使用新目录，请取消并先重启应用。</>)) return; await app.action('import/start',{source:source.trim()}); }}>
      <DirectoryField purpose="import-directory" label="外部目录" value={job.source || source} disabled={app.unavailable || !!job.present || !!job.running} onChange={setSource} onPending={setSelecting}
        labelSuffix={<ImportSpaceHint source={job.source || source} videoDir={job.videoDir || s.activeSettings.videoDir} unavailable={app.unavailable}/>}/>
      <div className="actions">
        {!job.present && <Button type="submit" disabled={app.unavailable || job.running || selecting || !source.trim()}>扫描并导入</Button>}
        {job.running && <Button disabled={app.unavailable || job.pausing || app.status.loadedRevision !== app.revision} onClick={()=>void app.action('import/pause',{id:job.id})}>暂停</Button>}
        {job.present && !job.running && <>
          {job.paused && <Button disabled={app.unavailable || job.scopeMismatch} onClick={()=>void app.action('import/resume',{id:job.id})}>继续导入</Button>}
          <Button disabled={app.unavailable} onClick={async()=>{
            const id=job.id;
            if(await app.confirm('结束本次导入？','将清除本次任务记录。视频和源文件均保留。')) await app.action('import/discard',{id,confirmed:true});
          }}>{job.paused || job.scopeMismatch || !job.total?'放弃本次':'结束本次'}</Button>
        </>}
      </div>
    </form>{(job.running||!!job.total)&&<Progress value={job.checked} max={job.total} label="视频文件处理进度" />}
    <p role="status">{job.phase?`${job.phase} · ${job.checked || 0}/${job.total || 0} 个视频 · 已导入 ${job.imported || 0} · 跳过 ${job.skipped || 0} · 失败 ${job.failed || 0} · 已清除 ${job.deleted || 0}`:''}</p>
    <Feedback error>{job.error || s.actionErrors?.import}</Feedback>
    {job.detail && <p className="secondary wrap">{job.detail}</p>}
    {job.cleanupAvailable && !job.running && <><Checkbox checked={cleanup} onChange={(_, v) => setCleanup(!!v.checked)} label="清除原位置本次成功导入的资源（默认保留）" disabled={app.unavailable} /><Button disabled={app.unavailable || !cleanup} onClick={async () => {
      const id = job.id;
      if (await app.confirm('永久删除源视频？', '将永久删除原位置本次成功导入的源视频，不进入回收站。')) { setCleanup(false); await app.action('import/cleanup',{id,confirmed:true}); }
    }}>清除已导入的源文件</Button></>}
  </Panel>;
}
