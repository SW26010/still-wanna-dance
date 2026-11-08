import {MessageBar,MessageBarBody,MessageBarTitle} from '@fluentui/react-components';
import type {ReactNode} from 'react';
import { useEffect, useRef, useState } from 'react';
import { request, withRequestTimeout } from './api';
import { Feedback } from './ui';
import { PathField, samePath, usePathPicker } from './path-picker';
import { displayStoragePath } from './storage-path';
import type { Settings } from './types';
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
export function StorageLocationFields({section, value, update, disabled, status, validation, onValidation, onPending}: {
  section: 'storage' | 'cache'; value: Locations; update: (value: Partial<Locations>) => void; disabled: boolean; status: SettingsSnapshot;
  validation: LocationValidation; onValidation: (value: LocationValidation) => void; onPending: (pending: boolean) => void;
}) {
  const [message, setMessage] = useState(''), [errorField, setErrorField] = useState<LocationChoice>();
  const picker = usePathPicker(onPending), locked = disabled || picker.pending;
  const current = useRef({value,status,validation}); current.current = {value,status,validation};
  const lastChoice = useRef<LocationChoice>(section === 'cache' ? 'cache' : 'database');
  const key = locationValidationKey(value,status.settings);

  // Saved settings can change independently while this page holds a draft.
  // Discard a response whenever either the displayed draft or its baseline moved.
  useEffect(() => {
    if (picker.pending || !validation.checked || validation.checked === key) return;
    const controller = new AbortController(), choice = lastChoice.current;
    const next = {databasePath:value.databasePath,videoDir:value.videoDir,blockCacheDir:value.blockCacheDir};
    void (async () => {
      try {
        const check = await request<LocationCheck>('storage/validate-location', {...next,selection:choice}, withRequestTimeout(controller.signal));
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
    let selected = path, prepared = false, preparingDatabase = false;
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
        let check: LocationCheck;
        try { check = await request<LocationCheck>('storage/validate-location', {...next,selection:choice}, withRequestTimeout(signal)); }
        catch (error) { if (candidate().key !== key && !signal.aborted) continue; throw error; }
        if (signal.aborted) return;
        if (candidate().key !== key) continue;
        if (check.valid && choice === 'database' && prepare && !prepared) {
          preparingDatabase = true;
          const result = await request<{path: string; created: boolean}>('storage/prepare-database', next, withRequestTimeout(signal));
          if (signal.aborted) return;
          selected = result.path; prepared = true; preparingDatabase = false;
          setMessage(result.created?'数据库已新建。保存设置并重启后使用。':'已选择数据库。保存设置并重启后使用。');
          if (candidate().key !== key) continue;
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
      const message = locationRequestError(error,preparingDatabase);
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
  function warning(choice: LocationChoice) {
    // Keep cross-page location errors attached to a field the user can adjust here.
    const field = section === 'cache' ? 'cache' : validation.warning?.field === 'cache' ? lastChoice.current : validation.warning?.field;
    const message = field === choice ? validation.warning?.message : '';
    return message ? `${message.replace(/[。！？.!?]+$/, '')}。请调整后保存。` : undefined;
  }
  return <div className="form-grid">
    {section === 'storage' && <><PathField name="databasePath" label="数据库文件" hint="保存歌曲资料、收藏、历史和累计统计，请优先备份。点击选择已有数据库，或输入新文件名建库。" value={value.databasePath} defaultValue={status.defaultDatabasePath} displayValue={displayStoragePath(value.databasePath || status.defaultDatabasePath,status)} warning={warning('database')} disabled={locked}
      onChoose={() => choose('database')} onReset={() => reset('database')}/>
    <Feedback error>{errorField === 'database' && picker.error}</Feedback>
    <PathField name="videoDir" label="本地视频目录" hint="SWD 使用所选目录保存完整视频，首次使用建议新建专用空目录。按视频保留策略管理，按需备份；更换位置不搬移原文件。" value={value.videoDir} defaultValue={status.defaultVideoDir} displayValue={displayStoragePath(value.videoDir || status.defaultVideoDir,status)} warning={warning('video')} disabled={locked}
      onChoose={() => choose('video')} onReset={() => reset('video')}/>
    <Feedback error>{errorField === 'video' && picker.error}</Feedback></>}
    {section === 'cache' && <><PathField name="blockCacheDir" label="临时视频缓存目录" hint="SWD 使用所选目录存放播放和预加载缓存，首次使用建议新建专用空目录。不能与视频目录相互包含。" value={value.blockCacheDir} defaultValue={status.defaultBlockCacheDir} displayValue={displayStoragePath(value.blockCacheDir || status.defaultBlockCacheDir,status)} isDefault={!value.blockCacheDir} warning={warning('cache')} disabled={locked}
      onChoose={() => choose('cache')} onReset={() => reset('cache')}/>
    <Feedback error>{errorField === 'cache' && picker.error}</Feedback></>}
    <p role="status">{message}</p>
  </div>;
}

export function StorageConfirmationNotice({message,action}:{message?:string;action?:ReactNode}) {
  return message?<MessageBar intent="warning"><MessageBarBody><MessageBarTitle>待确认存储</MessageBarTitle><p className="wrap">{message}</p>{action}</MessageBarBody></MessageBar>:null;
}
