import { useState } from 'react';
import { Button, Field, Input, Select } from '@fluentui/react-components';
import { Details } from './ui';
import type {SongPolicy} from './types';
export const policyLabels:Record<SongPolicy,string>={normal:'普通',favorite:'收藏',blocked:'屏蔽'};
export function SongPreferenceTools({disabled,save}:{disabled:boolean;save:(id:string,policy:SongPolicy)=>void}) {
  const [id,setID]=useState(''),[policy,setPolicy]=useState<SongPolicy>('favorite');
  return <Details title="歌曲偏好说明与按 ID 设置"><p>收藏只做标记，不开始下载；开启「优先保留收藏视频」时，完整收藏视频可以突破预算保留。关闭该选项后按普通预算处理。屏蔽会拒绝新的播放请求，并跳过预加载和下载补齐；已开始的任务继续。</p><p>多首歌曲共用视频时，只要仍有未屏蔽的关联歌曲，就保留其正常使用资格。收藏不保证视频块收齐，文件删除或丢失不会自动补下载。</p>
  <form className="actions" onSubmit={e=>{e.preventDefault();save(id,policy);}}><Field label="歌曲 ID" required><Input required inputMode="numeric" pattern="[1-9][0-9]*" maxLength={19} value={id} onChange={(_,v)=>setID(v.value)}/></Field><Field label="歌曲偏好"><Select value={policy} onChange={(_,v)=>setPolicy(v.value as SongPolicy)}>{Object.entries(policyLabels).map(([value,label])=><option key={value} value={value}>{label}</option>)}</Select></Field><Button type="submit" disabled={disabled}>保存标记</Button></form></Details>;
}
