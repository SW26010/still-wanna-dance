import {MessageBar,MessageBarBody,MessageBarTitle} from '@fluentui/react-components';
import type {ReactNode} from 'react';
import {Feedback} from './ui';
import {PathField} from './path-picker';
import {displayStoragePath} from './storage-path';
import type {SettingsEditor} from './settings-source';

export function StorageLocationFields({section,editor}:{section:'storage'|'cache';editor:SettingsEditor}) {
 const {values:value,data:status,locations}=editor;
 const {choose,reset,locked,message,errorField}=locations;
  return <div className="form-grid">
    {section === 'storage' && <><PathField name="databasePath" label="数据库文件" hint="保存歌曲资料、收藏、历史和累计统计，请优先备份。点击选择已有数据库，或输入新文件名建库。" value={value.databasePath} defaultValue={status.defaultDatabasePath} displayValue={displayStoragePath(value.databasePath || status.defaultDatabasePath,status)} warning={locations.warning(section,'database')} disabled={locked}
      onChoose={() => choose('database')} onReset={() => reset('database')}/>
    <Feedback error>{errorField === 'database' && locations.error}</Feedback>
    <PathField name="videoDir" label="本地视频目录" hint="SWD 使用所选目录保存完整视频，首次使用建议新建专用空目录。按视频保留策略管理，按需备份；更换位置不搬移原文件。" value={value.videoDir} defaultValue={status.defaultVideoDir} displayValue={displayStoragePath(value.videoDir || status.defaultVideoDir,status)} warning={locations.warning(section,'video')} disabled={locked}
      onChoose={() => choose('video')} onReset={() => reset('video')}/>
    <Feedback error>{errorField === 'video' && locations.error}</Feedback></>}
    {section === 'cache' && <><PathField name="blockCacheDir" label="临时视频缓存目录" hint="SWD 使用所选目录存放播放和预加载缓存，首次使用建议新建专用空目录。不能与视频目录相互包含。" value={value.blockCacheDir} defaultValue={status.defaultBlockCacheDir} displayValue={displayStoragePath(value.blockCacheDir || status.defaultBlockCacheDir,status)} isDefault={!value.blockCacheDir} warning={locations.warning(section,'cache')} disabled={locked}
      onChoose={() => choose('cache')} onReset={() => reset('cache')}/>
    <Feedback error>{errorField === 'cache' && locations.error}</Feedback></>}
    <p role="status">{section==='storage'?message:''}</p>
  </div>;
}

export function StorageConfirmationNotice({message,action}:{message?:string;action?:ReactNode}) {
  return message?<MessageBar intent="warning"><MessageBarBody><MessageBarTitle>待确认存储</MessageBarTitle><p className="wrap">{message}</p>{action}</MessageBarBody></MessageBar>:null;
}
