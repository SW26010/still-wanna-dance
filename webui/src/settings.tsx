import { useEffect, useRef, useState, useContext, type ReactNode, type Ref } from 'react';
import {flushSync} from 'react-dom';
import { Button, Checkbox, Divider, Field, Input, Select, Text, Tooltip } from '@fluentui/react-components';
import { bootstrap, type DownloadNumberField } from './bootstrap';
import {downloadNumber,hasProxyImport,type Draft} from './settings-draft';
import { useApp } from './hooks';
import { Feedback, Panel, Progress } from './ui';
import { CacheCapacityFields } from './cache-capacity';
import { StorageConfirmationNotice, StorageLocationFields } from './storage-directory';
import { DirectoryField, samePath } from './path-picker';
import { ImportSpaceHint } from './import-space';
import { RequestRetentionFields } from './request-retention';
import { displayStoragePath } from './storage-path';
import {FieldErrors,FieldIssues,ValidationField,inputError} from './field-validation';
import {NumberInput} from './number-input';
import {SettingsSource,useNormalSettingsEditor,type SettingsEditor} from './settings-source';

const restorePermissionHint='开启并保存时，可能请求 Windows 管理员授权（UAC）。';
function NetworkFields({editor}:{editor:SettingsEditor}) {
 const {values:d,update,disabled,data:status,proxy}=editor;
 const {pending,error,readFile,inspect,clearError}=proxy;
 const fileInput=useRef<HTMLInputElement>(null);
 const checking=editor.pending&&hasProxyImport(d);
  const locked = disabled || pending;
  const connectionDisabled=locked||d.upstreamMode==='direct';
  const info=d.proxyReport||status.proxyNodes?.configuration;
  const summary=info
    ? [info.name||'名称未记录',info.nodes+' 个节点',info.groups+' 个代理组',info.importedAt?'导入于 '+new Date(info.importedAt).toLocaleString('zh-CN',{hour12:false}):'导入时间未记录'].join(' · ')
    : status.proxyNodes?.error?'配置不可用':'未导入配置';
  const chooseFile=()=>{if(!locked)fileInput.current?.click();};
  return <div className="form-grid">
    <ValidationField name="upstreamMode" label="上游连接"><Select name="upstreamMode" disabled={locked} value={d.upstreamMode} onChange={(_, v) => update({upstreamMode: v.value})}>
      {!['direct','proxy'].includes(d.upstreamMode)&&<option value={d.upstreamMode}>无效值：{d.upstreamMode||'空'}</option>}
      <option value="direct">直连</option><option value="proxy">代理</option></Select></ValidationField>

    {(d.upstreamMode && d.upstreamMode !== 'direct'||!['external-socks5','mihomo'].includes(d.proxyBackend)) && <>
      <ValidationField name="proxyBackend" label="代理后端"><Select name="proxyBackend" disabled={locked} value={d.proxyBackend} onChange={(_, v) => update({proxyBackend: v.value})}>{!['external-socks5','mihomo'].includes(d.proxyBackend)&&<option value={d.proxyBackend}>无效值：{d.proxyBackend||'空'}</option>}<option value="external-socks5">外部 SOCKS5</option><option value="mihomo">内嵌 Mihomo</option></Select></ValidationField>
      {d.proxyBackend !== 'mihomo' ? <>
        <ValidationField name="socks5Address" label="SOCKS5 地址" required hint="代理软件的主机:端口。"><Input name="socks5Address" disabled={connectionDisabled} required value={d.socks5Address} onChange={(_, v) => update({socks5Address: v.value})} placeholder="127.0.0.1:7891" autoComplete="off" /></ValidationField>
        <ValidationField name="socks5Username" label="SOCKS5 用户名（可选）"><Input name="socks5Username" disabled={connectionDisabled} value={d.socks5Username} onChange={(_, v) => update({socks5Username: v.value})} autoComplete="off" /></ValidationField>
        <ValidationField name="socks5Password" label="SOCKS5 密码" hint="留空保留已有密码；清空用户名可移除认证。"><Input name="socks5Password" type="password" disabled={connectionDisabled} value={d.socks5Password || ''} onChange={(_, v) => update({socks5Password: v.value})} autoComplete="new-password" /></ValidationField>
        <Feedback error>{status.socks5CredentialError}</Feedback>
      </> : <>
        <Feedback error>{[status.proxyNodes?.error, status.proxyNodes?.activeError].filter(Boolean).join('；')}</Feedback>
        <Field label="当前配置文件"><Tooltip content={summary} relationship="description"><Input readOnly value={summary} /></Tooltip></Field>
        <ValidationField name="proxyImportText" error={error} label="导入配置文件">{props=><div className="mihomo-import-row">
          <Select id="mihomo-import-source" aria-label="配置来源" aria-labelledby="" disabled={locked} value={d.proxyImportSource} onChange={(_,v)=>{clearError();update({proxyImportSource:v.value as Draft['proxyImportSource'],proxyImportText:'',proxyImportName:''});}}>
            <option value="config">文件</option><option value="base64">Base64</option><option value="url">URL</option>
          </Select>
          <Input {...props} name="proxyImportText" readOnly={d.proxyImportSource==='config'} disabled={locked}
            value={d.proxyImportSource==='config'?d.proxyImportName:d.proxyImportText}
            placeholder={d.proxyImportSource==='config'?'点击选择文件':d.proxyImportSource==='url'?'输入配置 URL':'输入 Base64'}
            onClick={()=>{if(d.proxyImportSource==='config')chooseFile();}}
            onKeyDown={e=>{if(d.proxyImportSource==='config'&&(e.key==='Enter'||e.key===' ')){e.preventDefault();chooseFile();}}}
            onChange={(_,v)=>{clearError();update({proxyImportText:v.value});}} />
          <Button disabled={locked||!hasProxyImport(d)} onClick={()=>void inspect()}>{pending||checking?'处理中':'导入'}</Button>
          <input ref={fileInput} type="file" aria-label="选择配置文件" hidden disabled={locked} accept=".yaml,.yml,.json" onChange={e=>{void readFile(e.target.files?.[0]);e.target.value='';}} />
        </div>}</ValidationField>
      </>}
    </>}
  </div>;
}

export type SettingsSection = 'application' | 'storage' | 'downloads' | 'network';
type SettingsPageProps = {section?:SettingsSection;children:(form:ReactNode,actions:ReactNode)=>ReactNode};
export function SettingsPage(props:SettingsPageProps) {
  const editor=useContext(SettingsSource);
  return editor?<SettingsForm {...props} editor={editor}/>:<NormalSettingsPage {...props}/>;
}
function NormalSettingsPage(props:SettingsPageProps) {
  const editor=useNormalSettingsEditor();
  return <SettingsForm {...props} editor={editor}/>;
}
function SettingsForm({section='application',children,editor}:{editor:SettingsEditor}&SettingsPageProps) {
  const app=useApp(),s=editor.data;
  const {values:d,syncing,dirty,disabled,errors,locationError,locationWarning,locationChecking,update}=editor;
  const formRef=useRef<HTMLFormElement>(null);
  const [invalidTarget,setInvalidTarget]=useState<HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement>();
  const save=(confirmedStorage?:Draft)=>editor.save(section,confirmedStorage);
  const discard=()=>{editor.discard();setInvalidTarget(undefined);};
  const storageIssues=Object.fromEntries(Object.entries(s.storageIssues||{}).filter(([name,issue])=>{
    const value=d[name as keyof Draft];
    return typeof value==='string'&&(value===issue.value||!!value&&!!issue.value&&samePath(value,issue.value));
  }));
  useEffect(()=>{app.setSettingsDirty(section,dirty);return ()=>app.setSettingsDirty(section,false);},[section,dirty,app.setSettingsDirty]);
  const prepareAheadSeconds = downloadNumber(d, 'prepareAheadSeconds');
  const localProtectionGraceSeconds = downloadNumber(d, 'localProtectionGraceSeconds');
  const cacheEstimate = Number.isInteger(prepareAheadSeconds) && prepareAheadSeconds >= 100 && prepareAheadSeconds <= 16000
    && Number.isInteger(localProtectionGraceSeconds) && localProtectionGraceSeconds >= 0 && localProtectionGraceSeconds <= 10000
    ? `预计 ${((prepareAheadSeconds + localProtectionGraceSeconds) * 2_000_000 / 8 / 1024 ** 2).toFixed(2)} MiB` : '';
  function isInput(value:unknown):value is HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement {
    return value instanceof HTMLInputElement||value instanceof HTMLSelectElement||value instanceof HTMLTextAreaElement;
  }
  function focusInvalid(name:string) {
    const input=formRef.current?.elements.namedItem(name);
    if(!isInput(input))return;
    const panel=input.closest<HTMLElement>('[role="tabpanel"]');
    if(panel?.hidden){
      const anchors:Record<string,string>={'settings-panel-application':'/settings','settings-panel-storage':'storage-settings','settings-panel-downloads':'download-settings','settings-panel-network':'network-proxy'};
      const anchor=anchors[panel.id];
      if(anchor&&editor.selectTab)flushSync(()=>editor.selectTab?.(anchor));
    }
    setInvalidTarget(input);
  }
  function validateBlur(target:EventTarget) {
    if(!isInput(target)||!target.name)return;
    // Backend/combined errors survive focus changes; only an edit or a new
    // validation result can clear them. Read-only paths have their own check.
    if(target instanceof HTMLInputElement&&target.readOnly)return;
    const message=inputError(target);
    if(!message&&target.name!=='socks5Address'&&!(target instanceof HTMLInputElement&&(target.type==='number'||target.getAttribute('role')==='spinbutton')))return;
    editor.validateField(target.name,message);
  }
  // A rejected save still clears busy and refreshes its snapshot. Focus only
  // after React has committed the enabled controls for that revision.
  useEffect(()=>{
    if(!invalidTarget||disabled)return;
    invalidTarget.focus();setInvalidTarget(undefined);
  },[invalidTarget,disabled]);
  useEffect(()=>editor.registerForm(section,{
    validate:()=>{
      const checked:Record<string,string>={};
      for(const input of Array.from(formRef.current?.elements||[]))if(isInput(input)&&input.name){const error=inputError(input);if(error)checked[input.name]=error;}
      return checked;
    },
    fieldError:name=>{const input=formRef.current?.elements.namedItem(name);return isInput(input)?inputError(input):'';},
    hasField:name=>!!formRef.current?.elements.namedItem(name),focus:focusInvalid,
  }),[editor.registerForm,editor.selectTab,section]);
  const numberField = (key: DownloadNumberField, label: string, min: number, max: number, unit?: string, hint?: string) => <ValidationField name={key} label={label} hint={hint}><NumberInput name={key} id={key} aria-label={unit ? `${label}（${unit}）` : label} unit={unit} stepLabel={label} placeholder={String(bootstrap.settingsDefaults[key])} min={min} max={max} disabled={disabled} value={d[key]} onChange={value => update({[key]:value})} /></ValidationField>;
  const saveButton=<Button type="submit" form={'settings-form-'+section} appearance="primary" disabled={disabled || locationChecking || !dirty}>保存设置</Button>;
  const restartNeedsSaving=s.restartRequired&&(dirty||app.dirtySettings.size>0);
  const restartHint=restartNeedsSaving
    ? [...app.dirtySettings].some(value=>value!==section)?'其他页面仍有未保存配置，请保存或放弃后再重启。':'请先保存或放弃本页更改，再重启。'
    : s.settings.autoAccelerate?'重启后将自动开启视频加速，接入时可能请求 Windows 管理员授权（UAC）。':undefined;
  const restartAction=<Button disabled={disabled || dirty || app.dirtySettings.size>0 || !s.restartRequired} disabledFocusable={restartNeedsSaving} onClick={() => void app.action('restart')}>重启并应用</Button>;
  const restartButton=restartHint?<Tooltip content={restartHint} relationship="description">{restartAction}</Tooltip>:restartAction;
  const actions = <div className="settings-actions" role="region" aria-label="设置操作"><p role="status">{syncing ? '正在同步设置…' : dirty ? '未保存' : s.restartRequired ? '待重启' : '已生效'}</p>
      <div className="actions">{d.restoreHostsOnStopOrExit&&!s.settings.restoreHostsOnStopOrExit?<Tooltip content={restorePermissionHint} relationship="description">{saveButton}</Tooltip>:saveButton}<Button disabled={disabled || (!dirty && !locationWarning)} onClick={discard}>放弃更改</Button>
      {restartButton}</div></div>;
  const needsStorageConfirmation=!!s.storageConfirmation&&(!s.settings.databasePath||!s.settings.videoDir);
  const confirmStorage=<Button disabled={disabled||locationChecking||!!locationWarning} onClick={()=>void save({...d,databasePath:d.databasePath||s.defaultDatabasePath,videoDir:d.videoDir||s.defaultVideoDir})}>确认并保存</Button>;
  const form = <FieldIssues.Provider value={storageIssues}><FieldErrors.Provider value={errors}><Panel id={section==='storage'?'storage-settings':section==='downloads'?'download-settings':section==='network'?'proxy-settings':'preferences'}><Feedback error>{locationError||s.actionErrors?.settings}</Feedback>{section==='storage'&&<StorageConfirmationNotice message={needsStorageConfirmation?s.storageConfirmation:s.storageConfirmation?'存储设置已保存，请重启后应用。':undefined} action={needsStorageConfirmation?confirmStorage:undefined}/>}<form ref={formRef} noValidate onBlurCapture={e=>validateBlur(e.target)} id={'settings-form-'+section} className="form-grid settings-form" onSubmit={e => { e.preventDefault(); void save(); }}>
      {section==='storage'&&<fieldset disabled={disabled} aria-label="本地存储">
      <StorageLocationFields section="storage" editor={editor} />
      <CacheCapacityFields anchor="videoBudgetGiB" mode={d.videoStorageMode} value={d.videoBudgetGiB} disabled={disabled} onChange={update} />
      <Checkbox label="优先保留收藏视频" checked={d.retainFavorites} disabled={disabled || d.videoStorageMode==='none'} onChange={(_,v)=>update({retainFavorites:!!v.checked})}/>
      </fieldset>}
      {section==='application'&&<><ValidationField name="theme" label="主题"><Select name="theme" disabled={disabled} value={editor.theme.value} onChange={(_,v)=>editor.theme.change(v.value as typeof s.settings.theme)}>{!['system','light','dark'].includes(editor.theme.value)&&<option value={editor.theme.value}>无效值：{editor.theme.value||'空'}</option>}<option value="system">跟随系统</option><option value="light">浅色</option><option value="dark">深色</option></Select></ValidationField><Divider /><fieldset disabled={disabled}><legend>程序启动</legend><Checkbox label="启动程序时自动打开 WebUI" checked={d.autoOpenWebUI} onChange={(_,v)=>update({autoOpenWebUI:!!v.checked})}/>
      {/* Checkbox forwards trigger props to its input; put pointer handlers on the root to include the label. */}
      <Tooltip content="下次启动自动加速时，可能请求 Windows 管理员授权（UAC）。" relationship="description">{({onPointerEnter,onPointerLeave,ref,...trigger})=><Checkbox {...trigger} ref={ref as Ref<HTMLInputElement>} root={{onPointerEnter,onPointerLeave}} label="启动程序时自动开启视频加速" checked={d.autoAccelerate} onChange={(_, v) => update({autoAccelerate:!!v.checked})} />}</Tooltip></fieldset>
      <Divider /><fieldset><legend>停止与退出</legend><Checkbox label="随 VRChat 退出" checked={d.exitWithVRChat} disabled={disabled} onChange={(_,v)=>update({exitWithVRChat:!!v.checked})}/>
      <Tooltip content={restorePermissionHint} relationship="description">{({onPointerEnter,onPointerLeave,ref,...trigger})=><Checkbox {...trigger} ref={ref as Ref<HTMLInputElement>} root={{onPointerEnter,onPointerLeave}} id="restoreHostsOnStopOrExit" label="停止加速或退出时恢复游戏直连" checked={d.restoreHostsOnStopOrExit} disabled={disabled} onChange={(_,v)=>update({restoreHostsOnStopOrExit:!!v.checked})}/>}</Tooltip></fieldset>
      <Divider /><RequestRetentionFields mode={d.requestRetentionMode} value={d.requestRetentionDays} disabled={disabled} onChange={update} />
      <Divider /><ValidationField name="logLevel" label="程序日志级别"><Select name="logLevel" disabled={disabled} value={d.logLevel} onChange={(_,v)=>update({logLevel:v.value as typeof s.settings.logLevel})}>{!['off','error','warn','info','debug'].includes(d.logLevel)&&<option value={d.logLevel}>无效值：{d.logLevel||'空'}</option>}<option value="off">关闭</option><option value="error">仅错误（Error）</option><option value="warn">警告及错误（Warn）</option><option value="info">信息（Info，默认）</option><option value="debug">详细调试（Debug）</option></Select></ValidationField></>}
      {section==='network'&&<>      <fieldset aria-label="网络连接"><NetworkFields editor={editor} />
      </fieldset>
</>}
      {section==='downloads'&&<><fieldset disabled={disabled}><legend>队列预加载</legend>
      <Checkbox label="随视频服务启用队列预加载" checked={d.queueWithGateway} disabled={disabled} onChange={(_,v)=>update({queueWithGateway:!!v.checked})}/>
      <DirectoryField purpose="log-directory" name="vrchatLogDir" label="VRChat 日志目录" hint="默认自动识别，找不到日志时点击选择。" disabled={disabled} value={d.vrchatLogDir || s.defaultLogDir} defaultValue={s.defaultLogDir} isDefault={!d.vrchatLogDir}
        onChange={vrchatLogDir=>update({vrchatLogDir})} onReset={()=>update({vrchatLogDir:''})} onPending={editor.pathPending}/></fieldset>
      <Divider /><fieldset disabled={disabled}><legend>下载</legend><div className="settings-columns">
      {numberField('downloadConcurrencySoftLimit','同时下载数量',1,64)}
      {numberField('downloadGrowIntervalMS','并发增长间隔',0,2000,'毫秒')}
      </div></fieldset>
      <Divider /><fieldset disabled={disabled} aria-label="缓存"><legend className="inline">缓存<Text size={200} weight="regular" className="secondary" aria-live="polite">{cacheEstimate}</Text></legend>
      <StorageLocationFields section="cache" editor={editor} />
      <div className="settings-columns">
      {numberField('prepareAheadSeconds','提前准备时间',100,16000,'秒')}
      {numberField('localProtectionGraceSeconds','延续保护时间',0,10000,'秒','需求结束后继续保留缓存。')}
      </div>
      </fieldset></>}
    </form></Panel></FieldErrors.Provider></FieldIssues.Provider>;
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
