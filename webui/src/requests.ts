import type { RequestRecord, Download } from './types';
export function requestOutcome(r: RequestRecord) {
  if (r.outcome === 'canceled') return '客户端取消 / 请求中断';
  if (r.outcome === 'aborted') return '传输中止（原因未确定）';
  if (r.outcome === 'incomplete') return '传输不完整';
  if (r.outcome === 'failed') return r.status >= 400 ? '请求失败' : '写入失败 / 连接中断';
  if (r.outcome === 'completed') return [200,206].includes(r.status) ? r.method === 'HEAD' ? '响应头处理完成' : '请求处理完成' : 'HTTP 响应结束';
  return '结果未知';
}

export function requestTitle(r: Pick<Download,'resource'|'songs'|'moreSongs'>) {
  const songs=r.songs || [];
  if(!songs.length)return '未知歌名 · 资源 '+r.resource;
  return (songs.length>1||r.moreSongs?'关联曲目（播放歌曲未确定）：':'关联曲目：')+songs.map(s=>(s.title||'未知歌名')+'（ID '+s.id+'）').join(' / ')+(r.moreSongs?' / …更多关联曲目':'');
}
