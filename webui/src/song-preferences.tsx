import { useState } from 'react';
import { Button, Field, Input, Select } from '@fluentui/react-components';

import type {SongPolicy} from './types';
export const policyLabels:Record<SongPolicy,string>={normal:'普通',favorite:'收藏',blocked:'屏蔽'};
export function SongPreferenceTools({disabled,save}:{disabled:boolean;save:(id:string,policy:SongPolicy)=>void}) {
  const [id,setID]=useState(''),[policy,setPolicy]=useState<SongPolicy>('favorite');
  return <>
  <form className="actions" onSubmit={e=>{e.preventDefault();save(id,policy);}}><Field label="歌曲 ID" required><Input required inputMode="numeric" pattern="[1-9][0-9]*" maxLength={19} value={id} onChange={(_,v)=>setID(v.value)}/></Field><Field label="歌曲偏好"><Select value={policy} onChange={(_,v)=>setPolicy(v.value as SongPolicy)}>{Object.entries(policyLabels).map(([value,label])=><option key={value} value={value}>{label}</option>)}</Select></Field><Button type="submit" disabled={disabled}>保存标记</Button></form><p className="secondary">收藏仅作标记；屏蔽会拒绝新的播放和下载请求。</p></>;
}
