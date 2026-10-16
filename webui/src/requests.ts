import type { RequestRecord } from './types';
export type RequestGroup = {id:number; range:boolean; first:number; last:number; end:number; bytes:number; requests:RequestRecord[]};
export function groupRequests(requests: RequestRecord[]) {
  type Group = {range: boolean; first: number; last: number; end: number; bytes: number; requests: RequestRecord[]};
  const groups: Group[] = [], latest = new Map<string,Group>();
  for (const r of requests) {
    const range = r.method === 'GET' && !!r.range;
    let group = latest.get(r.resource);
    if (!range || !group || !group.range || group.first-r.at > 30000) {
      group = {range,first:r.at,last:r.at,end:r.at,bytes:0,requests:[]}; groups.push(group);
    }
    group.first = r.at; group.end = Math.max(group.end,r.at+r.elapsedMS); group.bytes += r.bytes; group.requests.push(r); latest.set(r.resource,group);
  }
  return groups;
}

// Reuse each prior group's identity once, preferring the largest shared request set.
export function reconcileRequestGroups(requests: RequestRecord[], previous: RequestGroup[]): RequestGroup[] {
  const groups = groupRequests(requests), owners = new Map<string,number>();
  previous.forEach((group,index) => group.requests.forEach(r => owners.set(`${r.resource}:${r.id}`,index)));
  const matches: {next:number; prior:number; overlap:number}[] = [];
  groups.forEach((group,next) => {
    const counts = new Map<number,number>();
    group.requests.forEach(r => { const prior = owners.get(`${r.resource}:${r.id}`); if (prior !== undefined) counts.set(prior,(counts.get(prior) || 0)+1); });
    counts.forEach((overlap,prior) => matches.push({next,prior,overlap}));
  });
  const assigned = new Map<number,number>(), used = new Set<number>();
  for (const {next,prior} of matches.sort((a,b) => b.overlap-a.overlap)) {
    if (!assigned.has(next) && !used.has(prior)) { assigned.set(next,previous[prior].id); used.add(prior); }
  }
  let nextID = Math.max(0,...previous.map(g => g.id)) + 1;
  return groups.map((group,index) => ({...group,id:assigned.get(index) ?? nextID++}));
}
export function requestOutcome(r: RequestRecord) {
  if (r.outcome === 'canceled') return '客户端取消 / 请求中断';
  if (r.outcome === 'aborted') return '传输中止（原因未确定）';
  if (r.outcome === 'incomplete') return '传输不完整';
  if (r.outcome === 'failed') return r.status >= 400 ? '请求失败' : '写入失败 / 连接中断';
  if (r.outcome === 'completed') return [200,206].includes(r.status) ? r.method === 'HEAD' ? '响应头处理完成' : '请求处理完成' : 'HTTP 响应结束';
  return '结果未知';
}
export const requestCache = (r: RequestRecord) => ({HIT:'缓存命中',MISS:'回源',WAIT:'等待共享下载',UNKNOWN:'缓存状态未知'}[r.cache] || '缓存状态未知');

export function requestTitle(r: {resource:string;songs?:{id:number;title?:string}[];moreSongs?:boolean}) {
  const songs=r.songs || [];
  if(!songs.length)return '未知歌名 · 资源 '+r.resource;
  return (songs.length>1||r.moreSongs?'关联曲目（播放歌曲未确定）：':'关联曲目：')+songs.map(s=>(s.title||'未知歌名')+'（ID '+s.id+'）').join(' / ')+(r.moreSongs?' / …更多关联曲目':'');
}
