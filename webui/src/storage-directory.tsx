import { useEffect, useRef, useState } from 'react';
import { Button, Field, Input } from '@fluentui/react-components';
import { request } from './api';
import { Feedback } from './ui';
import type { Settings, Status } from './types';

type Locations = Pick<Settings, 'databasePath' | 'videoDir'>;
export function StorageLocationFields({value, update, disabled, status, onPending}: {
  value: Locations; update: (value: Partial<Locations>) => void; disabled: boolean; status: Status; onPending: (pending: boolean) => void;
}) {
  const [message, setMessage] = useState(''), [error, setError] = useState('');
  const lock = useRef(false), mounted = useRef(true), controller = useRef<AbortController | undefined>(undefined);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; controller.current?.abort(); onPending(false); };
  }, [onPending]);
  async function database(create: boolean) {
    if (disabled || lock.current) return;
    lock.current = true; onPending(true); setError(''); setMessage('');
    const abort = create ? undefined : new AbortController(); controller.current = abort;
    try {
      const result = await request<{path:string;canceled?:boolean}>(create?'storage/create-database':'storage/select-database',
        {databasePath: value.databasePath.trim() || status.defaultDatabasePath, videoDir:value.videoDir.trim()}, abort?.signal);
      if (mounted.current && !abort?.signal.aborted && !result.canceled) {
        update({databasePath:result.path, ...(!value.videoDir ? {videoDir:status.defaultVideoDir} : {})});
        setMessage(create?'数据库已新建。保存设置并重启后使用。':'已选择数据库。保存设置并重启后使用。');
      }
    } catch (e) { if (mounted.current && !abort?.signal.aborted) setError(String(e)); }
    finally { lock.current = false; if (mounted.current) onPending(false); }
  }
  return <div className="form-grid">
    <Field label="数据库文件" hint="保存歌曲资料、收藏、历史和累计统计，请优先备份。">
      <Input disabled={disabled} value={value.databasePath} placeholder={status.defaultDatabasePath} onChange={(_,v)=>{setMessage('');setError('');update({databasePath:v.value});}} />
    </Field>
    <div className="actions"><Button type="button" disabled={disabled} onClick={()=>void database(false)}>打开已有数据库…</Button><Button type="button" disabled={disabled} onClick={()=>void database(true)}>{value.databasePath.trim()?'在此新建数据库':'在默认位置新建数据库'}</Button></div>
    <Field label="完整视频目录" hint="保存完整视频，按视频保留策略管理；按需备份。保存新位置时会创建缺失的目录，不搬移原文件。">
      <Input disabled={disabled} value={value.videoDir} placeholder={status.defaultVideoDir} onChange={(_,v)=>update({videoDir:v.value})} />
    </Field>
    <p className="secondary wrap">当前数据库：{status.activeSettings.databasePath||'尚未指定'}<br/>当前完整视频目录：{status.activeSettings.videoDir||'尚未指定'}</p>
    <p role="status">{message}</p><Feedback error>{error}</Feedback>
  </div>;
}
