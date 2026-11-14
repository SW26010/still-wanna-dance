import type { RequestRecord, Download } from './types';
function requestResult(r: RequestRecord): [string,string] {
  if (r.outcome === 'blocked') return ['歌曲已屏蔽','该歌曲在请求时被设为屏蔽，SWD 已拒绝播放。'];
  if (r.outcome === 'expired') return ['等待超时','已超过本次播放所需数据的下载截止时间，SWD 主动结束了请求。'];
  if (r.outcome === 'playback_info_failed') return ['信息获取失败','未能取得有效的歌曲播放地址或必要信息，SWD 已结束请求。'];
  if (r.outcome === 'canceled') return ['请求中断','检测到客户端取消或连接断开；换歌、跳转进度也可能触发。'];
  if (r.outcome === 'aborted') return ['处理异常','响应传输被中止，未记录更具体的原因。'];
  if (r.outcome === 'incomplete') return ['处理异常','实际写出的视频字节少于本次响应应交付的字节。'];
  if (r.outcome === 'failed') return ['处理异常','本次请求未能正常完成，未记录更具体的原因。'];
  // Older records cannot distinguish a download deadline from other timeouts.
  if (r.outcome === 'timeout') return ['处理异常','记录表明处理发生超时，未记录具体类别。'];
  if (r.outcome === 'completed') return ['完成',[200,206].includes(r.status) ? r.method === 'HEAD' ? '响应头已处理完成。' : '本次请求的视频响应已处理完成。' : '本次 HTTP 响应已结束。'];
  return ['结果未知','记录中没有可识别的结果。'];
}

export const requestOutcome = (r: RequestRecord) => requestResult(r)[0];
export const requestOutcomeDescription = (r: RequestRecord) => requestResult(r)[1];

export function requestTitle(r: Pick<Download,'resource'|'songs'|'moreSongs'>) {
  const songs=r.songs || [];
  if(!songs.length)return '未知歌名 · 资源 '+r.resource;
  return (songs.length>1||r.moreSongs?'关联曲目（播放歌曲未确定）：':'关联曲目：')+songs.map(s=>(s.title||'未知歌名')+'（ID '+s.id+'）').join(' / ')+(r.moreSongs?' / …更多关联曲目':'');
}
