import { useState } from 'react';
import { request } from './api';
import { Feedback } from './ui';
import { DirectoryField, PathField, usePathPicker } from './path-picker';
import type { Settings, Status } from './types';

type Locations = Pick<Settings, 'databasePath' | 'videoDir'>;
export function StorageLocationFields({value, update, disabled, status, onPending}: {
  value: Locations; update: (value: Partial<Locations>) => void; disabled: boolean; status: Status; onPending: (pending: boolean) => void;
}) {
  const [message, setMessage] = useState(''), picker = usePathPicker(onPending);
  function database() {
    if (disabled) return;
    setMessage('');
    void picker.choose('database', value.databasePath || status.defaultDatabasePath, async (path, signal) => {
      const result = await request<{path: string; created: boolean}>('storage/prepare-database', {databasePath: path, videoDir: value.videoDir}, signal);
      if (signal.aborted) return;
      update({databasePath:result.path, ...(!value.videoDir ? {videoDir:status.defaultVideoDir} : {})});
      setMessage(result.created?'数据库已新建。保存设置并重启后使用。':'已选择数据库。保存设置并重启后使用。');
    });
  }
  return <div className="form-grid">
    <PathField label="数据库文件" hint="保存歌曲资料、收藏、历史和累计统计，请优先备份。点击选择已有数据库，或输入新文件名建库。" value={value.databasePath} defaultValue={status.defaultDatabasePath} disabled={disabled || picker.pending}
      onChoose={database} onReset={() => { setMessage(''); update({databasePath:status.defaultDatabasePath}); }}/>
    <DirectoryField purpose="video-directory" label="完整视频目录" hint="保存完整视频，按视频保留策略管理；按需备份。不搬移原文件。" value={value.videoDir} defaultValue={status.defaultVideoDir} disabled={disabled} onChange={videoDir=>update({videoDir})} onPending={onPending}/>
    <p className="secondary wrap">当前数据库：{status.activeSettings.databasePath||'尚未指定'}<br/>当前完整视频目录：{status.activeSettings.videoDir||'尚未指定'}</p>
    <p role="status">{message}</p><Feedback error>{picker.error}</Feedback>
  </div>;
}
