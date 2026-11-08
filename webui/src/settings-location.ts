import {useEffect,useRef,useState} from 'react';
import {request,withRequestTimeout} from './api';
import {samePath,usePathPicker} from './path-picker';
import type {Settings} from './types';
import type {SettingsSnapshot} from './settings-source';

type Locations = Pick<Settings, 'databasePath' | 'videoDir' | 'blockCacheDir'>;
type LocationChoice = 'database' | 'video' | 'cache';
type LocationWarning = {field: LocationChoice; message: string};
type LocationCheck = {valid: boolean; field?: string; message?: string};
export type LocationValidation = {warning?: LocationWarning; pendingDatabase?: string; databaseError?: string; checked?: string};
export function locationValidationKey(value: Locations, saved: Locations) {
  return JSON.stringify([value.databasePath,value.videoDir,value.blockCacheDir,saved.databasePath,saved.videoDir,saved.blockCacheDir]);
}
function checkWarning(check: LocationCheck, choice: LocationChoice, previous?: LocationWarning): LocationWarning {
  let field = check.field === 'database' || check.field === 'video' || check.field === 'cache' ? check.field
    : previous && previous.message === check.message ? previous.field : choice;
  if (previous && previous.field !== choice && field !== choice) field = previous.field;
  return {field,message:check.message || '所选存储位置不可用'};
}
export function locationRequestError(error: unknown, preparingDatabase = false) {
  if (error instanceof DOMException && error.name === 'TimeoutError') return preparingDatabase
    ? '数据库准备超时，结果尚未确认，请重新选择并核对' : '检查存储位置超时，请重试';
  return error instanceof Error ? error.message : String(error);
}
class LocationFailure extends Error {
 constructor(error:unknown,readonly preparingDatabase:boolean){super(locationRequestError(error,preparingDatabase));}
}
export function isDatabasePreparationError(error:unknown){return error instanceof LocationFailure&&error.preparingDatabase;}
function checkLocation(value:Locations,choice:LocationChoice,signal?:AbortSignal){
 return request<LocationCheck>('storage/validate-location',{databasePath:value.databasePath,videoDir:value.videoDir,blockCacheDir:value.blockCacheDir,selection:choice},signal?withRequestTimeout(signal):undefined);
}
export async function checkAndPrepareLocation(value:Locations,choice:LocationChoice,prepare:boolean,signal?:AbortSignal,isCurrent=()=>true) {
 let preparing=false;
 try {
   const check=await checkLocation(value,choice,signal);
   if(signal?.aborted||!isCurrent())return undefined;
   if(!check.valid||!prepare)return {check};
   preparing=true;
   const database=await request<{path:string;created:boolean}>('storage/prepare-database',{databasePath:value.databasePath,videoDir:value.videoDir,blockCacheDir:value.blockCacheDir},signal?withRequestTimeout(signal):undefined);
   if(signal?.aborted)return undefined;
   return {check,database};
 } catch(error){throw new LocationFailure(error,preparing);}
}
export function useSettingsLocations({value,update,disabled,status,validation,onValidation,onPending}: {
 value:Locations; update:(patch:Partial<Locations>)=>void; disabled:boolean; status:SettingsSnapshot;
 validation:LocationValidation; onValidation:(value:LocationValidation)=>void; onPending:(pending:boolean)=>void;
}) {
  const [message, setMessage] = useState(''), [errorField, setErrorField] = useState<LocationChoice>();
  const picker = usePathPicker(onPending), locked = disabled || picker.pending;
  const current = useRef({value,status,validation}); current.current = {value,status,validation};
  const lastChoice = useRef<LocationChoice>('database');
  const lastStorageChoice = useRef<LocationChoice>('database');
  const key = locationValidationKey(value,status.settings);

  // Saved settings can change independently while this page holds a draft.
  // Discard a response whenever either the displayed draft or its baseline moved.
  useEffect(() => {
    if (picker.pending || !validation.checked || validation.checked === key) return;
    const controller = new AbortController(), choice = lastChoice.current;
    const next = {databasePath:value.databasePath,videoDir:value.videoDir,blockCacheDir:value.blockCacheDir};
    void (async () => {
      try {
        const check = await checkLocation(next,choice,controller.signal);
        if (controller.signal.aborted || locationValidationKey(current.current.value,current.current.status.settings) !== key) return;
        const previous = current.current.validation;
        const warning = !check.valid ? checkWarning(check,choice,previous.warning)
          : previous.databaseError ? {field:'database' as const,message:previous.databaseError} : undefined;
        onValidation({...previous,warning,checked:key});
      } catch (error) {
        if (controller.signal.aborted || locationValidationKey(current.current.value,current.current.status.settings) !== key) return;
        const previous = current.current.validation;
        onValidation({...previous,warning:{field:choice,message:locationRequestError(error)},checked:key});
      }
    })();
    return () => controller.abort();
  }, [key,picker.pending,validation.checked]);

  async function apply(choice: LocationChoice, path: string, prepare: boolean, signal: AbortSignal) {
    lastChoice.current = choice;
    if(choice!=='cache')lastStorageChoice.current=choice;
    let selected = path, prepared = false;
    const candidate = () => {
      const {value,status} = current.current;
      const patch: Partial<Locations> = choice === 'database'
        ? {databasePath:selected, ...(!value.videoDir && prepare ? {videoDir:status.defaultVideoDir} : {})}
        : choice === 'video' ? {videoDir:selected} : {blockCacheDir:samePath(selected, status.defaultBlockCacheDir) ? '' : selected};
      const next = {databasePath:value.databasePath,videoDir:value.videoDir,blockCacheDir:value.blockCacheDir,...patch};
      return {patch,next,key:locationValidationKey(next,status.settings)};
    };
    try {
      while (!signal.aborted) {
        const {patch,next,key} = candidate();
        let result:Awaited<ReturnType<typeof checkAndPrepareLocation>>;
        try { result=await checkAndPrepareLocation(next,choice,choice==='database'&&prepare&&!prepared,signal,()=>candidate().key===key); }
        catch(error){if(!isDatabasePreparationError(error)&&candidate().key!==key&&!signal.aborted)continue;throw error;}
        if(signal.aborted)return;
        if(!result)continue;
        const {check,database}=result;
        if(database){
          selected=database.path;prepared=true;
          setMessage(database.created?'数据库已新建。保存设置并重启后使用。':'已选择数据库。保存设置并重启后使用。');
          if(candidate().key!==key)continue;
        }
        const previous = current.current.validation;
        const pendingDatabase = choice === 'database' ? (prepare && !prepared ? selected : undefined) : previous.pendingDatabase;
        const databaseError = choice === 'database' ? undefined : previous.databaseError;
        const warning = !check.valid ? checkWarning(check,choice,previous.warning)
          : databaseError ? {field:'database' as const,message:databaseError} : undefined;
        update(patch);
        onValidation({warning,pendingDatabase,databaseError,checked:key});
        return;
      }
    } catch (error) {
      if (signal.aborted) return;
      const {patch,key} = candidate(), previous = current.current.validation;
      const pendingDatabase = choice === 'database' ? (prepare && !prepared ? selected : undefined) : previous.pendingDatabase;
      const databaseError = choice === 'database' ? undefined : previous.databaseError;
      update(patch);
      const preparingDatabase=isDatabasePreparationError(error);
      const message = locationRequestError(error);
      onValidation({warning:{field:choice,message},pendingDatabase,databaseError:preparingDatabase ? message : databaseError,checked:key});
    }
  }
  function choose(choice: LocationChoice) {
    if (locked) return;
    setMessage(''); setErrorField(choice);
    const purpose = choice === 'database' ? 'database' : choice === 'video' ? 'video-directory' : 'block-cache-directory';
    const initial = choice === 'database' ? value.databasePath || status.defaultDatabasePath
      : choice === 'video' ? value.videoDir || status.defaultVideoDir : value.blockCacheDir || status.defaultBlockCacheDir;
    void picker.choose(purpose, initial, (path, signal) => apply(choice, path, true, signal));
  }
  function reset(choice: LocationChoice) {
    if (locked) return;
    setMessage(''); setErrorField(choice);
    const path = choice === 'database' ? status.defaultDatabasePath : choice === 'video' ? status.defaultVideoDir : '';
    void picker.run(signal => apply(choice, path, false, signal));
  }
  function warning(section: 'storage'|'cache', choice: LocationChoice) {
    // Keep cross-page location errors attached to a field the user can adjust here.
    const field = section === 'cache' ? 'cache' : validation.warning?.field === 'cache' ? lastStorageChoice.current : validation.warning?.field;
    const message = field === choice ? validation.warning?.message : '';
    return message ? `${message.replace(/[。！？.!?]+$/, '')}。请调整后保存。` : undefined;
  }
  return {choose,reset,warning,locked,message,errorField,error:picker.error};
}
