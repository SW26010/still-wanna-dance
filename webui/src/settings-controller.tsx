import {useCallback,useEffect,useRef,useState} from 'react';
import {APIError,request} from './api';
import {useSettingsProxy} from './settings-proxy';
import {useApp} from './hooks';
import {samePath} from './path-picker';
import {displayStoragePath} from './storage-path';
import {useSettingsLocations,checkAndPrepareLocation,locationRequestError,locationValidationKey,type LocationValidation} from './settings-location';
import {draftFrom,fingerprint,settingsPayload,hasProxyImport,importProxyConfiguration,importedProxyDraft,releaseImport,type Draft} from './settings-draft';
import type {Settings} from './types';
import type {SettingsSnapshot} from './settings-source';
import type {SettingsSection} from './settings';

// Forms supply validation/focus adapters. Editing does not depend on page DOM,
// tab nesting, or callers coordinating individual state setters.
export interface SettingsFormAdapter {
 validate:()=>Record<string,string>;
 fieldError:(name:string)=>string;
 hasField:(name:string)=>boolean;
 focus:(name:string)=>void;
}
export function useSettingsController(s:SettingsSnapshot,loadedRevision:number|undefined,unavailable:boolean,scope:'section'|'workspace') {
 const app=useApp();
 const [draft,setDraft]=useState<Partial<Draft>>();
 const [pending,setPending]=useState(false),[awaitingRevision,setAwaitingRevision]=useState<number>();
 const [locationError,setLocationError]=useState(''),[errors,setErrors]=useState<Record<string,string>>(s.fields||{});
 const [locationValidation,setLocationValidation]=useState<LocationValidation>({});
 const adapters=useRef(new Map<SettingsSection,SettingsFormAdapter>());
 const registerForm=useCallback((section:SettingsSection,adapter:SettingsFormAdapter)=>{
   adapters.current.set(section,adapter);
   return ()=>{adapters.current.delete(section);};
 },[]);
 const syncing=awaitingRevision!==undefined;
 const d={...draftFrom(s.settings),...draft};
 const dirty=!syncing&&fingerprint(d)!==fingerprint(draftFrom(s.settings));
 const disabled=unavailable||pending||syncing||loadedRevision!==app.revision;
 const locationChecking=!!locationValidation.checked&&locationValidation.checked!==locationValidationKey(d,s.settings);
 useEffect(()=>{
   if(awaitingRevision!==undefined&&loadedRevision!==undefined&&loadedRevision>=awaitingRevision){setDraft(undefined);setAwaitingRevision(undefined);setErrors({});}
 },[awaitingRevision,loadedRevision]);
 useEffect(()=>{if(!draft)setErrors(s.fields||{});},[s]);
 useEffect(()=>{
   const warn=(event:BeforeUnloadEvent)=>{if(dirty&&!app.stopped)event.preventDefault();};
   addEventListener('beforeunload',warn);return ()=>removeEventListener('beforeunload',warn);
 },[dirty,app.stopped]);
 function discard(){void releaseImport(draft?.proxyImportID||'');setDraft(undefined);setPending(false);setAwaitingRevision(undefined);setLocationValidation({});setErrors(s.fields||{});setLocationError('');}
 function focusInvalid(forms:SettingsFormAdapter[],name:string){forms.find(form=>form.hasField(name))?.focus(name);}
 function validateField(name:string,message:string){setErrors(previous=>{const next={...previous};if(message)next[name]=message;else delete next[name];return next;});}
  const update = (patch: Partial<Draft>) => {
    setDraft(value=>({...value,...patch}));
    setErrors(previous=>{
      const next={...previous};
      for(const key of Object.keys(patch)) {
        const name=key==='videoBudgetGiB'?'videoBudgetBytes':key;
        if(!(name in next))continue;
        const message=Array.from(adapters.current.values()).find(form=>form.hasField(name))?.fieldError(name)||'';
        if(message)next[name]=message;else delete next[name];
      }
      if('proxyImportID' in patch||'proxyImportSource' in patch||'proxyImportText' in patch)delete next.proxyImportText;
      if('upstreamMode' in patch||'proxyBackend' in patch)for(const name of ['socks5Address','socks5Username','socks5Password','proxyImportText'])delete next[name];
      if('requestRetentionMode' in patch)delete next.requestRetentionDays;
      if('videoStorageMode' in patch)delete next.videoBudgetBytes;
      return next;
    });
  };
 const proxy=useSettingsProxy(d,update,disabled,setPending);
 const locations=useSettingsLocations({value:d,update,disabled,status:s,validation:locationValidation,onValidation:setLocationValidation,onPending:setPending});
  async function save(section:SettingsSection, confirmedStorage?: Draft) {
    if ((!dirty&&!confirmedStorage) || disabled || locationChecking) return;
    const candidate=confirmedStorage||d;
    if(confirmedStorage)update(confirmedStorage);
    const checked:Record<string,string>={};
    const sections:SettingsSection[]=scope==='workspace'?['application','storage','downloads','network']:[section];
    const forms=sections.flatMap(section=>{const form=adapters.current.get(section);return form?[form]:[];});
    for(const form of forms)Object.assign(checked,form.validate());
    setErrors(checked);
    if(Object.keys(checked).length) {
      focusInvalid(forms,Object.keys(checked)[0]);
      return;
    }
    if(locationValidation.warning)return;
    setPending(true); setLocationError('');
    try {
      let ready=candidate;
      if((scope==='workspace'||section==='network')&&d.upstreamMode==='proxy'&&d.proxyBackend==='mihomo'&&hasProxyImport(d)) {
        const report=await importProxyConfiguration(d);
        void releaseImport(d.proxyImportID);
        const imported=importedProxyDraft(report);
        ready={...d,...imported};update(imported);
      }
      const saved = settingsPayload(draftFrom(s.settings)), values = settingsPayload(ready);
      const patch = Object.fromEntries(Object.entries(values).filter(([key,value]) => value !== saved[key as keyof typeof saved]));
      const pendingDatabase=confirmedStorage&&!s.settings.databasePath?values.databasePath:locationValidation.pendingDatabase;
      if (pendingDatabase) {
        if (!samePath(pendingDatabase, values.databasePath)) {
          setLocationValidation(current=>({...current,warning:{field:'database',message:'数据库位置已变化，请重新选择'}}));
          return;
        }
        try {
          const result=await checkAndPrepareLocation(values,'database',true);
          if(!result)return;
          const {check}=result;
          if (!check.valid) {
            const field = check.field === 'video' || check.field === 'cache' ? check.field : 'database';
            setLocationValidation(current=>({...current,warning:{field,message:check.message || '所选存储位置不可用'}}));
            return;
          }
          setLocationValidation(current=>({...current,warning:undefined,pendingDatabase:undefined,databaseError:undefined}));
        } catch (error) {
          const message = locationRequestError(error);
          setLocationValidation(current=>({...current,warning:{field:'database',message},databaseError:message,pendingDatabase}));
          return;
        }
      }
      let confirmed: Pick<Settings,'databasePath'|'videoDir'|'videoStorageMode'|'videoBudgetBytes'|'retainFavorites'> | undefined;
      if (ready.databasePath !== s.settings.databasePath || ready.videoDir !== s.settings.videoDir) {
        const check = await request<{confirmVideoPolicy:boolean}>('storage/check', {databasePath:ready.databasePath,videoDir:ready.videoDir,blockCacheDir:ready.blockCacheDir});
        if (check.confirmVideoPolicy) {
          const accepted = await app.confirm('确认此目录的视频保留方式', <><p className="wrap">视频目录：{displayStoragePath(ready.videoDir,s)}</p><p className="wrap">数据库：{displayStoragePath(ready.databasePath,s)||'尚未指定'}</p><p>此目录的视频将按本次设置管理：{ready.videoStorageMode==='none'?'不保留普通视频':ready.videoStorageMode==='unlimited'?'不限制视频空间':ready.videoBudgetGiB+' GiB 视频预算'}。不符合保留策略的视频可能被删除。</p><p>{ready.retainFavorites?'优先保留收藏视频。':'收藏不提供额外保留保护。'}</p></>, '确认并保存');
          if (!accepted) return;
          const {databasePath,videoDir,videoStorageMode,videoBudgetBytes,retainFavorites} = values;
          confirmed = {databasePath,videoDir,videoStorageMode,videoBudgetBytes,retainFavorites};
        }
      }
      if (await app.action('settings', {...patch,confirmVideoPolicy:confirmed},error=>{
        if(error.code!=='settings_validation'||!error.fields)return false;
        const name=Object.keys(error.fields).find(name=>forms.some(form=>form.hasField(name)));
        if(!name)return false;
        setErrors(error.fields);focusInvalid(forms,name);return true;
      })) setAwaitingRevision(app.revision + 1);
    } catch (e) {
      if(e instanceof APIError&&e.code==='settings_validation'&&e.fields)setErrors(e.fields);
      else setLocationError(String(e));
    }
    finally { setPending(false); }
  }
 return {values:d,pending,syncing,dirty,disabled,errors,locationError,locationWarning:!!locationValidation.warning,locationChecking,locations,proxy,
   update,discard,save,registerForm,validateField,pathPending:setPending};
}
