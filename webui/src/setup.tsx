import { useEffect, useRef, useState } from 'react';
import { Badge, Button, Checkbox, Dialog, DialogActions, DialogBody, DialogContent, DialogSurface, DialogTitle, Field, Input, Link, Spinner, Text, Subtitle1, Card, useRestoreFocusTarget } from '@fluentui/react-components';
import { bootstrap } from './bootstrap';
import { APIError, request } from './api';
import { reconnect, useApp } from './hooks';
import { type Application, type Status, gib, accelerationLabel } from './types';
import { draftFrom, fingerprint, NetworkFields, releaseImport, type Draft } from './settings';
import { Brand, Details, Feedback, Panel, NavigationLayout } from './ui';
import { Diagnostics } from './diagnostics';
import { CacheCapacityFields, capacityPayload, reclamationLabel } from './cache-capacity';
import { StorageDirectoryFields } from './storage-directory';
import { useVRCX, VRCXOption, VRCXCheck } from './vrcx';

const steps = ['welcome','storage','proxy','finish'] as const;
type Step = typeof steps[number];
const names:Record<Step,string>={welcome:'开始',storage:'存储',proxy:'网络',finish:'启用'};
interface Terms {text:string;version:string;sha256:string;accepted:boolean;needsConfirmation?:boolean}
interface Storage {space:{state:string;message:string};writable:{state:string;message:string};library:{state:string;message:string};freeBytes:number;totalBytes:number}
interface Logs {state:string;message:string;directory:string;path?:string}
interface SetupEffects {restartRequired:boolean;applyStartup:boolean}
const pathKey=(value:string)=>value.replaceAll('/','\\').replace(/\\+$/,'').toLowerCase();
const savedApplication=()=>{try{return sessionStorage.getItem('swd-setup-application') || '';}catch{return '';}};
const rememberApplication=(id:string)=>{try{if(id)sessionStorage.setItem('swd-setup-application',id);else sessionStorage.removeItem('swd-setup-application');}catch{/* Optional recovery hint. */}};
export function setupPlan(d:Draft, s:Status, baseVersion:string, source:string) {
  return {baseVersion,storageDir:d.storageDir,customStorage:pathKey(d.storageDir)!==pathKey(s.defaultStorageDir),...capacityPayload(d),
    upstreamMode:d.upstreamMode,proxyBackend:d.proxyBackend,proxyImportID:d.proxyImportID,socks5Address:d.socks5Address,socks5Username:d.socks5Username,
    ...(d.socks5Password || !d.socks5Username ? {socks5Password:d.socks5Password || ''} : {}),importSource:source.trim()};
}

function SetupReview({draft:d,status:s,source,effects}:{draft:Draft;status:Status;source:string;effects?:SetupEffects}) {
  const current=s.activeSettings, p=setupPlan(d,s,'',source);
  const change=(before:string,after:string)=>before===after?after:`${before} → ${after}`;
  const storage=(path:string)=>pathKey(path)===pathKey(s.defaultStorageDir)?'默认':'自定义 · '+path;
  const capacity=(settings:Pick<Draft,'videoStorageMode'> & {maxCacheBytes:number})=>settings.videoStorageMode==='unlimited'?'不限制':settings.videoStorageMode==='none'?'不保留（视频存储预算为 0）':settings.maxCacheBytes/1073741824+' GiB';
  const mode=(value:string)=>({direct:'直连',proxy:'强制代理',socks5:'强制代理',auto:'直连 + 代理'}[value] || '未选择');
  const rows:[string,string][]=[['本地存储目录',change(storage(current.storageDir),storage(p.storageDir))],['视频存储预算',change(capacity(current),capacity(p))],['视频导入',source || '不执行']];
  if(source)rows.push(['目标',p.storageDir+' · 保留源文件']);
  rows.push(['网络连接',change(mode(current.upstreamMode),mode(p.upstreamMode))]);
  if(p.upstreamMode!=='direct')rows.push(['代理后端',p.proxyBackend==='mihomo'?'内嵌 Mihomo':'外部 SOCKS5']);
  if(p.proxyImportID)rows.push(['节点库',`将替换为本次导入的 ${d.proxyReport?.accepted} 个节点（保存后重启生效）`]);
  if(p.upstreamMode!=='direct'&&p.proxyBackend!=='mihomo'){
    rows.push(['代理地址',change(current.socks5Address || '未设置',p.socks5Address || '未设置')],['认证用户',change(current.socks5Username || '无',p.socks5Username || '无')]);
    if(p.socks5Password)rows.push(['代理密码','将更新']);
    else if(p.socks5Password===''&&s.socks5PasswordSet)rows.push(['代理密码','将清除']);
  }
  const labels={retainFavorites:'优先保留收藏视频',autoStartCDN:'自动启动播放网关',autoStartQueue:'自动启动队列',restoreHostsOnExit:'退出时恢复游戏直连',prepareAheadSeconds:'提前准备时间（秒）',downloadSoftLimit:'同时下载数量',downloadGrowIntervalMS:'并发增长间隔（毫秒）',localProtectionGraceSeconds:'本地延续保护时长（秒）',requestRetentionDays:'请求保留天数',throughputIntervalMinutes:'测速间隔（分钟）',downloadUpstream:'回源线路',manualLogDir:'手动日志位置',logDir:'日志目录'};
  for(const key of Object.keys(labels) as (keyof typeof labels)[]){if(current[key]!==s.settings[key])rows.push([labels[key],change(String(current[key]),String(s.settings[key]))+'（此前已保存）']);}
  if(current.aggressiveCacheReclamation!==p.aggressiveCacheReclamation)rows.push(['激进的缓存回收策略',change(reclamationLabel(current),reclamationLabel(p))+'（此前已保存）']);
  return <><dl>{rows.map(([label,value])=><div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
    {effects&&<><Subtitle1 as="h3">确认后执行</Subtitle1><ul>
      <li>应用配置：{effects.restartRequired?'保存并重启应用，当前播放和任务会中断':'保存配置，无需重启'}</li>
      <li>本地服务：{effects.applyStartup?s.settings.autoStartCDN?'将启动':'不启动（启动设置已关闭）':s.running?'保持运行':'保持关闭'}</li>
      <li>房间队列预加载：{effects.applyStartup?s.settings.autoStartQueue?'将开启':'不开启（启动设置已关闭）':s.queue.desired?'保持开启':'保持关闭'}</li>
      <li>游戏连接：{effects?.restartRequired&&s.activeSettings.restoreHostsOnExit&&s.hosts.exitRestorePrepared?'重启时恢复直连，完成后请点击「开始加速」重新接入':s.hosts.ready?'已配置，本次确认不再修改':'未配置，完成后请在首页点击「开始加速」'}</li>
    </ul><p>{effects.applyStartup?'按已保存的启动设置执行；启动失败时会在首页显示原因。':'沿用当前服务状态，不会重新开启已手动关闭的功能。'}</p></>}
    {source&&<p>{effects?.restartRequired?'重启后开始导入。':'确认后开始导入。'}关闭网页不影响导入，重启程序会中止导入。</p>}
    <p>存储、网络和导入选择在点击「确认并应用」后生效。条款确认和上方游戏连接操作已即时生效，放弃草稿不会撤销。</p></>;
}

export function Setup({accepted,onAccepted,onExit,guard,feedback}: {accepted:boolean;onAccepted:()=>void;onExit:()=>void;guard: React.RefObject<(() => Promise<boolean>) | undefined>;feedback:React.ReactNode}) {
  const app=useApp(), s=app.status.data;
  const [step,setStep]=useState<Step>('welcome'), [terms,setTerms]=useState<Terms>(), [termsOpen,setTermsOpen]=useState(bootstrap.termsGate?.step==='terms'), [termsLoading,setTermsLoading]=useState(false);
  const [draft,setDraft]=useState<Draft>(), [busy,setBusy]=useState(false), [proxyPending,setProxyPending]=useState(false), [message,setMessage]=useState('');
  const [diagnosticUpstream,setDiagnosticUpstream]=useState('direct');
  const [source,setSource]=useState(''), [importEnabled,setImportEnabled]=useState(false), [custom,setCustom]=useState(false);
  const [storage,setStorage]=useState<Storage>(), [storageMessage,setStorageMessage]=useState(''), [storageBusy,setStorageBusy]=useState(false), [logs,setLogs]=useState<Logs>(), [logsBusy,setLogsBusy]=useState(false);
  const [application,setApplication]=useState<Application>(), [recovering,setRecovering]=useState(false), [restartError,setRestartError]=useState(''), [folderBusy,setFolderBusy]=useState(false);
  const [effects,setEffects]=useState<SetupEffects>(), [reviewError,setReviewError]=useState('');
  const reviewSequence=useRef(0), reported=useRef(false);
  const form=useRef<HTMLFormElement>(null), heading=useRef<HTMLHeadingElement>(null), base=useRef(''), baseline=useRef(''), applicationID=useRef(savedApplication());
  const initialized=useRef(false), sequence=useRef(0), storageSequence=useRef(0), folder=useRef<AbortController | undefined>(undefined), progressWrites=useRef(Promise.resolve());
  const mounted=useRef(true), draftRef=useRef(draft), consumed=useRef(false), busyLock=useRef(false);draftRef.current=draft;
  useEffect(()=>()=>{mounted.current=false;sequence.current++;storageSequence.current++;folder.current?.abort();if(!consumed.current)void releaseImport(draftRef.current?.proxyImportID || '');},[]);
  const update=(patch:Partial<Draft>)=>setDraft(d=>d?{...d,...patch}:d);
  const plan=draft&&s?setupPlan(draft,s,base.current,importEnabled?source:''):undefined;
  const history=useVRCX(draft?.storageDir || '',accepted&&!!draft,busy||proxyPending||!!application||recovering,true);
  const restoreFocusTarget=useRestoreFocusTarget();
  const locked=busy || proxyPending || history.pending || history.targetPending&&!!draft || !!application || recovering;
  const dirty=!!draft && (fingerprint(draft)!==baseline.current || importEnabled || !!source || history.changed);
  const canContinue=accepted && !!terms?.accepted && !!s && !!draft;
  const persist=(state='active', next:Step=step)=>{
    if(!accepted)return Promise.resolve();
    progressWrites.current=progressWrites.current.then(()=>request('setup',{schemaVersion:1,step:next,state})).then(()=>{},()=>{});
    return progressWrites.current;
  };
  async function loadTerms() {
    setTermsLoading(true);
    try {const current=await request<Terms>('terms/current');if(!mounted.current)return;setTerms(current);if(current.needsConfirmation&&!current.accepted)setTermsOpen(true);if(current.accepted)onAccepted();}
    catch(e){if(mounted.current)setMessage('条款暂时无法载入：'+String(e));}
    finally{if(mounted.current)setTermsLoading(false);}
  }
  useEffect(()=>{void loadTerms();},[]);
  useEffect(()=>{
    if(!accepted || !s || initialized.current)return;
    initialized.current=true;const d=draftFrom(s.settings);setDraft(d);baseline.current=fingerprint(d);base.current=s.setupSettingsVersion;
    setCustom(pathKey(d.storageDir)!==pathKey(s.defaultStorageDir));
    if(applicationID.current){setStep('finish');void reconcile();return;}
    if(bootstrap.termsGate)return;
    const revision=sequence.current;
    void request<{step:string;state:string}>('setup').then(p=>{
      const next=({terms:'welcome',import:'storage',direct:'proxy',environment:'finish'}[p.step] || p.step) as Step;
      if(mounted.current && revision===sequence.current && p.state==='active' && steps.includes(next))setStep(next);
    }).catch(e=>{if(mounted.current)setMessage('无法读取上次引导进度，可以重新开始。'+String(e));});
  },[accepted,s]);
  useEffect(()=>{
    if(applicationID.current && s?.setupApplication?.id===applicationID.current && s.setupApplication.phase) setApplication(s.setupApplication);
  },[s?.setupApplication]);
  useEffect(()=>{
    if(application?.configSaved && !consumed.current) {consumed.current=true;app.discardSettings();}
    if(application?.phase==='completed' && !busy && !recovering && !reported.current) {
      reported.current=true;rememberApplication('');void persist('completed','finish');
      void request<Status>('status').then(status=>{
        app.setMessage(`配置已应用。${accelerationLabel(status)}；房间队列预加载${status.queue.running?'已开启':status.queue.desired?'尚未就绪，请检查首页提示':'已关闭'}。`+(application.vrcx?'VRCX 歌曲偏好：'+application.vrcx.message:''),'home');
      }).catch(()=>app.setMessage('配置已应用，暂未确认服务启动状态，请查看首页状态。','home')).finally(()=>{if(mounted.current)onExit();});
    }
    if(application?.phase==='restart' && !recovering && !restartError)void recoverRestart();
  },[application,busy,recovering,restartError]);
  async function leave(confirm=true) {
    if(locked && !restartError && application?.phase!=='uncertain' && application?.phase!=='missing' && application?.phase!=='failed')return false;
    if(!accepted){await app.action('exit');return false;}
    if(confirm && dirty && !await app.confirm('放弃引导更改？','尚未应用的存储、网络和导入选择将丢弃。已确认的条款和已执行的游戏连接操作会保留。'))return false;
    if(application?.phase==='missing'||application?.phase==='failed')rememberApplication('');
    void persist('skipped');return true;
  }
  guard.current=()=>leave();
  useEffect(()=>()=>{guard.current=undefined;},[guard]);
  useEffect(()=>{
    const warn=(event:BeforeUnloadEvent)=>{if(dirty&&!application&&!app.stopped)event.preventDefault();};addEventListener('beforeunload',warn);return()=>removeEventListener('beforeunload',warn);
  },[dirty,application,app.stopped]);
  useEffect(()=>{
    folder.current?.abort();folder.current=undefined;setFolderBusy(false);
    if(step==='storage'&&draft)void checkStorage(draft.storageDir);
    if(step==='finish'&&canContinue&&!application&&!history.targetPending)void checkAccess();
  },[step,!!draft,history.targetPending]);
  async function checkStorage(path:string) {
    const revision=++storageSequence.current;setStorage(undefined);setStorageBusy(true);setStorageMessage('');
    try{const next=await request<Storage>('setup/storage',{path});if(mounted.current&&revision===storageSequence.current){setStorage(next);setStorageMessage([next.writable,next.library].filter(x=>x.state!=='ok').map(x=>x.message).join('；'));}}
    catch(e){if(mounted.current&&revision===storageSequence.current)setStorageMessage('目录无法确认：'+String(e));}
    finally{if(mounted.current&&revision===storageSequence.current)setStorageBusy(false);}
  }
  async function chooseFolder(kind:'storage'|'import') {
    if(folder.current || locked || !draft)return;
    const controller=folder.current=new AbortController();setFolderBusy(true);setMessage('');
    try {
      const result=await request<{path:string;canceled:boolean}>('setup/folder',{kind,path:kind==='storage'?draft.storageDir:source},AbortSignal.any([controller.signal,AbortSignal.timeout(300000)]));
      if(!mounted.current||controller.signal.aborted)return;
      if(!result.canceled&&result.path){if(kind==='storage'){update({storageDir:result.path});setCustom(pathKey(result.path)!==pathKey(s!.defaultStorageDir));void checkStorage(result.path);}else setSource(result.path);}
    }catch(e){if(mounted.current&&!controller.signal.aborted)setMessage('未选择文件夹：'+String(e)+'，请重试。');}
    finally{if(folder.current===controller){folder.current=undefined;if(mounted.current)setFolderBusy(false);}}
  }
  async function checkLogs() {
    setLogsBusy(true);app.refresh();
    try{const result=await request<Logs>('setup/logs',{});if(mounted.current)setLogs(result);}
    catch(e){if(mounted.current)setLogs({state:'unknown',message:String(e),directory:''});}
    finally{if(mounted.current)setLogsBusy(false);}
  }
  async function checkAccess() {
    const revision=++reviewSequence.current;setEffects(undefined);setReviewError('');
    await Promise.all([checkLogs(),history.inspect(),(async()=>{
      try{const result=await request<SetupEffects>('setup/validate',plan);if(mounted.current&&revision===reviewSequence.current)setEffects(result);}
      catch(e){if(mounted.current&&revision===reviewSequence.current)setReviewError('无法核对完成后的动作：'+String(e));}
    })()]);
  }
  async function go(next:Step) {
    if(locked || !canContinue || !form.current?.reportValidity())return;
    if(step==='storage'&&importEnabled&&!source){setMessage('请选择导入文件夹，或取消勾选。');return;}
    if(step==='proxy'&&!draft?.upstreamMode){setMessage('请至少选择一种连接方式。');return;}
    if(step==='proxy'&&draft?.proxyImportText.trim()){setMessage('请先检查节点内容，或清除本次导入草稿。');return;}
    sequence.current++;setBusy(true);setMessage('');folder.current?.abort();
    try{if(step==='storage'||step==='proxy')await request('setup/validate',{...plan,page:step});setStep(next);if(next==='welcome')await loadTerms();void persist('active',next);heading.current?.focus({preventScroll:true});}
    catch(e){setMessage('本页尚未通过检查：'+String(e));}
    finally{setBusy(false);}
  }
  async function acceptTerms() {
    if(!terms || busyLock.current)return;
    busyLock.current=true;setBusy(true);setMessage('');
    try{await request('terms/accept',{version:terms.version,sha256:terms.sha256,agree:true,contentRights:true});setTerms({...terms,accepted:true});setTermsOpen(false);onAccepted();}
    catch(e){setMessage('条款确认未保存：'+String(e));}
    finally{busyLock.current=false;setBusy(false);}
  }
  async function reconcile() {
    const id=applicationID.current;if(!id||busyLock.current)return;
    busyLock.current=true;setBusy(true);
    try {const receipt=await request<Application>('setup/receipt',{id});if(receipt.id!==id)throw new Error('回执 ID 不匹配');if(mounted.current)setApplication(receipt);}
    catch(e){if(mounted.current)setApplication({id,phase:e instanceof APIError&&e.status===404?'missing':'uncertain',error:'原应用回执尚未确认：'+String(e)+'。不会重复应用或导入。'});}
    finally{busyLock.current=false;if(mounted.current)setBusy(false);}
  }
  async function recoverRestart() {
    if(recovering)return;setRecovering(true);setRestartError('');
    try{await reconnect();}catch(e){if(mounted.current)setRestartError(String(e));}
    finally{if(mounted.current)setRecovering(false);}
  }
  async function apply() {
    if(locked||busyLock.current||!canContinue||!plan||!effects||app.unavailable)return;
    busyLock.current=true;setMessage('');
    try{
      if(history.enabled&&!await history.confirmOverwrite())return;
      if(!mounted.current)return;
      setBusy(true);
      const id=applicationID.current ||= crypto.randomUUID();rememberApplication(id);
      await progressWrites.current;
      const historyPreview=history.enabled&&history.preview?.id&&history.preview.events>0?history.preview:undefined;
      const result=await request<Application>('setup/apply',{...plan,id,vrcxEnabled:history.enabled,
        vrcxPreviewID:historyPreview?.id || '',vrcxOverwrite:!!historyPreview?.existingSongs},AbortSignal.timeout(45000));
      setApplication(result);app.refresh();
    }
    catch(e){if(e instanceof APIError){applicationID.current='';rememberApplication('');setMessage('尚未应用：'+e.message);}else{setApplication({id:applicationID.current,phase:'uncertain',error:'结果尚未确认。请核对应用结果，不会自动重复创建清单。'});app.refresh();}}
    finally{busyLock.current=false;setBusy(false);}
  }
  function reset() {
    if(!draft||!s)return;folder.current?.abort();
    if(step==='storage'){setCustom(false);setImportEnabled(false);setSource('');history.reset();update({storageDir:s.defaultStorageDir,videoStorageMode:'none',maxCacheGiB:'0'});void checkStorage(s.defaultStorageDir);}
    if(step==='proxy'){void releaseImport(draft.proxyImportID);update({upstreamMode:'direct',proxyBackend:'external-socks5',socks5Address:'',socks5Username:'',socks5Password:'',proxyImportID:'',proxyImportText:'',proxyReport:undefined});}
  }
  return <div className="wizard">{feedback}<header><Brand /><div className="actions"><Link href="/help" target="_blank" rel="noopener">使用帮助（新窗口）</Link><Button disabled={busy||recovering||application?.phase==='importing'} onClick={async()=>{if(await leave(false))onExit();}}>{accepted?'放弃草稿并退出引导':'退出'}</Button></div></header>
    <NavigationLayout label="引导步骤" selected={step} steps items={steps.map((name,i)=>({value:name,label:`${i+1}. ${names[name]}`,disabled:locked||(name!=='welcome'&&!canContinue),onClick:()=>void go(name)}))}>
    <Text as="h1" size={600} weight="semibold" ref={heading} tabIndex={-1}>{names[step]}</Text>
    <form className="wizard-form" ref={form} onSubmit={e=>{e.preventDefault();if(step==='finish')void apply();else void go(steps[steps.indexOf(step)+1]);}}>
      <div className="wizard-content" key={step}>
      {step==='welcome'&&<><Subtitle1 as="h2">Still Wanna Dance (SWD)</Subtitle1><p>Load failed? But I still wanna dance!</p><p>专为 VRChat WannaDance 设计的高性能视频加速器</p><ul><li>本地视频</li><li>队列预加载</li><li>直连优化</li><li>自定义代理</li></ul><p>本工具自动后台运行，使用 WebUI 操作，可通过系统托盘图标管理。</p><p>技术原理：通过修改系统 hosts 劫持 HTTP 视频请求</p>
        <div className="actions"><Button onClick={()=>{setTermsOpen(true);void loadTerms();}}>{terms?.accepted?'查看完整条款':'阅读并同意条款'}</Button><Badge color={terms?.accepted?'success':'informative'}>{terms?.accepted?'条款已确认':'尚未确认条款'}</Badge></div><p>在弹窗中同意后立即保存；退出引导不会撤销条款确认。</p>
        {accepted&&!s&&<Spinner label="正在连接设置…"/>}<Details title="修复已有安装"><p>移除本程序的 hosts 映射，需要管理员确认，立即生效。</p><Button disabled={app.busy||app.stopped} onClick={()=>void app.action('hosts/disable')}>恢复游戏直连</Button></Details>
      </>}
      {step==='storage'&&draft&&s&&<div className="form-grid">
        <CacheCapacityFields mode={draft.videoStorageMode} value={draft.maxCacheGiB} disabled={locked||folderBusy} onChange={update} />
        <StorageDirectoryFields mode={draft.videoStorageMode} disabled={locked||folderBusy}>{directoryDisabled => <>
          <Checkbox checked={custom} disabled={directoryDisabled} label="指定其他位置（程序文件夹外）" onChange={(_,v)=>{setCustom(!!v.checked);if(!v.checked){update({storageDir:s.defaultStorageDir});void checkStorage(s.defaultStorageDir);}}}/>
          <Field label="本地存储目录："><Button className="path-button" disabled={directoryDisabled||!custom} onClick={()=>void chooseFolder('storage')}>{draft.storageDir || '选择存储文件夹'}</Button></Field>
          <div className="actions"><Button disabled={directoryDisabled} onClick={()=>{setCustom(false);update({storageDir:s.defaultStorageDir});void checkStorage(s.defaultStorageDir);}}>默认位置</Button><Button disabled={locked||storageBusy} onClick={()=>void checkStorage(draft.storageDir)}>重新检查</Button></div>
          <p role="status">{storageBusy?'正在检查目录…':storage?.space.state==='ok'?`可用 ${gib(storage.freeBytes)} / ${gib(storage.totalBytes)}`:storage?.space.message || '容量待确认'}</p>
        </>}</StorageDirectoryFields>
        <Feedback error>{storageMessage}</Feedback>
        {pathKey(draft.storageDir)!==pathKey(s.settings.storageDir)&&<Feedback>更换目录不会搬迁已有数据。</Feedback>}
        <p>完整数据约 430 GiB。空间不足时，建议将视频存储预算设为 40 GiB 以内，以获得最佳空间收益。</p><Checkbox label="导入已有视频" checked={importEnabled} disabled={locked} onChange={(_,v)=>{setImportEnabled(!!v.checked);if(!v.checked){folder.current?.abort();setSource('');}}}/>
        {importEnabled&&<><Field label="导入视频文件夹"><Button className="path-button" disabled={locked||folderBusy} onClick={()=>void chooseFolder('import')}>{source||'点击选择文件夹'}</Button></Field><p>可选择存有 WannaDance 视频或旧版保存的视频的文件夹，包含子文件夹，无需特定目录结构。</p><p>请放心，我们不会修改来源文件，并且会尽量使用硬链接来避免双份空间占用。导入后请勿修改源视频。</p></>}
        <VRCXOption model={history}/>
      </div>}
      {step==='proxy'&&draft&&s&&<><Subtitle1 as="h2">连接方式</Subtitle1><NetworkFields wizard draft={draft} update={update} disabled={locked} status={s} onPending={setProxyPending}/><Details title="检测网络连接与下载速度" open><Diagnostics visible={true} proxy={{...plan,page:'proxy',upstreamMode:'proxy'}} upstream={diagnosticUpstream} onUpstreamChange={setDiagnosticUpstream}/></Details></>}
      {step==='finish'&&draft&&s&&<><Panel title="接入检查"><p>游戏连接：{s.hosts.ready?'已配置':s.hosts.conflict?s.hosts.message:'未配置'}</p><p>下面的连接操作会立即修改 hosts，可能需要管理员权限。放弃引导草稿不会撤销；需要撤销时点击「恢复直连」。</p><div className="actions"><Button disabled={app.unavailable||(s.hosts.ready&&(!s.activeSettings.restoreHostsOnExit||!s.hosts.managed||s.hosts.exitRestorePrepared))} onClick={()=>void app.action('hosts/enable')}>配置游戏连接</Button><Button disabled={app.unavailable} onClick={()=>void app.action('hosts/disable')}>恢复直连</Button><Button disabled={logsBusy||locked||app.unavailable} onClick={()=>void checkAccess()}>刷新接入检查</Button></div>
        <Details title="端口与日志检查"><p>80 (HTTP) 端口：{s.portOK?'可用':s.portOwner?`被 ${s.portOwner.name}（PID ${s.portOwner.pid}）占用`:'被占用或不可用'}</p><p>443 (HTTPS) 端口：{s.httpsPortOK?'可用':s.httpsPortOwner?`被 ${s.httpsPortOwner.name}（PID ${s.httpsPortOwner.pid}）占用`:'被占用或不可用'}</p>
        <p>VRChat 日志：{logsBusy?'正在检测…':logs?.path||logs?.directory||'未定位'}</p><p>{logs?.message}</p></Details><Feedback error>{s.actionErrors?.hosts}</Feedback>
        <VRCXCheck model={history}/>
        {(!s.portOK||!s.httpsPortOK)&&<Feedback error>接入检查未通过：端口被占用，请关闭占用程序后重试。</Feedback>}
      {s.portOK&&s.httpsPortOK&&logs?.state!=='ok'&&<p>视频接入条件已就绪；日志尚未确认，不影响本地视频读取和按需下载。</p>}</Panel><Panel title="应用配置"><SetupReview draft={draft} status={s} source={importEnabled?source:''} effects={effects}/>{!effects&&!reviewError&&<p role="status">正在核对完成后的动作…</p>}<Feedback error>{reviewError || application?.error || restartError || s.setupSettingsError}</Feedback><p role="status">{application?.configSaved&&'配置已保存。'}{recovering?'正在重启并等待新进程…':application?.phase==='importing'?'正在启动导入…':''}</p></Panel></>}
      </div>
      <Feedback error>{message}</Feedback>
      <div className="wizard-footer"><div className="actions">{['storage','proxy'].includes(step)&&!application&&<Button disabled={locked} onClick={reset}>恢复本页默认</Button>}</div><div className="actions">
        {!application&&<Button disabled={locked||step==='welcome'} onClick={()=>void go(steps[Math.max(0,steps.indexOf(step)-1)])}>上一步</Button>}
        {application ? application.phase==='uncertain'?<Button disabled={busy} onClick={()=>void reconcile()}>核对应用结果</Button>:restartError?<Button disabled={recovering} onClick={()=>void recoverRestart()}>重新连接并核对</Button>:['failed','missing'].includes(application.phase)?<Button onClick={async()=>{if(await leave(false))onExit();}}>返回控制台</Button>:<Spinner size="tiny" label="正在应用…"/> : <Button {...restoreFocusTarget} type="submit" appearance="primary" disabled={locked||!canContinue||(step==='finish'&&(app.unavailable||!effects))}>{step==='finish'?'确认并应用':'下一步'}</Button>}
      </div></div>
    </form></NavigationLayout>
    <Dialog open={termsOpen} onOpenChange={(_,v)=>setTermsOpen(v.open)}><DialogSurface><DialogBody><DialogTitle>完整条款</DialogTitle><DialogContent>
      {terms?.needsConfirmation&&!terms.accepted&&<Feedback>之前的同意记录无法验证，请重新阅读并确认。</Feedback>}
      <Card role="article" appearance="outline" size="large" className="terms-text" tabIndex={0} aria-label="完整使用条款">{termsLoading?'正在读取条款…':terms?.text || '条款暂时无法载入，请重试或使用下方链接阅读完整文本。'}</Card><Feedback error>{message}</Feedback>
      <p>同意后立即保存。</p><Link href="/terms.txt" target="_blank" rel="noopener">查看／保存纯文本</Link>
    </DialogContent><DialogActions><Button onClick={()=>setTermsOpen(false)}>关闭条款</Button><Button appearance="primary" disabled={busy||termsLoading||terms?.accepted} onClick={()=>terms?void acceptTerms():void loadTerms()}>{terms?.accepted?'已确认条款':terms?'同意完整条款':'重新读取条款'}</Button></DialogActions></DialogBody></DialogSurface></Dialog>
  </div>;
}
