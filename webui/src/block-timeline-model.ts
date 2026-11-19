export type BlockState = 'waiting' | 'downloading' | 'available' | 'protected' | 'stopping';
interface RollingTime { rollingBase?: number; rollingOffset?: number }
export interface BlockView extends RollingTime {
  id: string; resource: string; songID: string; colorKey?: string; block: number; byteOffset: number; totalBlocks: number; bytes: number; title: string;
  at: number; timeBasis: 'need-by' | 'last-need-by' | 'ended' | 'untimed';
  needBy: number; deadline: number; protectionStart: number; protectionUntil: number;
  fixedNeedBy?: number;
  protected: boolean; inUse: boolean; sources: number; readers: number; state: BlockState;
  windowClosed: boolean; opportunistic: boolean; eligible: boolean;
}
export interface BlockSource extends RollingTime {
  id: number; kind: 'get' | 'queue' | 'batch' | 'formal' | 'handoff'; songID: string; title: string;
  position: number; needBy: number; expiresAt: number; windowClosed: boolean;
}
export interface BlockDetail { block: BlockView; sources: BlockSource[] }
export interface PlaybackClock { zeroAt: number; endAt: number }
export interface BlockDemand {
  id: string; sourceID: number; kind: BlockSource['kind']; songID: string; title: string;
  position: number; resource: string; colorKey?: string; totalBlocks: number; blocks: string[];
  gameSongID?: string; playback?: PlaybackClock;
}
export interface BlockEvent {
  at: number; running: boolean; reset: boolean; capacity: number; used: number;
  active: number; open: number; prepareAheadMS: number; downloadGraceMS: number;
  blocks: BlockView[]; removed: string[];
  demands: BlockDemand[]; removedDemands: string[];
}
export interface TimelineData extends Omit<BlockEvent, 'blocks' | 'removed' | 'reset' | 'demands' | 'removedDemands'> {
  blocks: Map<string, BlockView>; demands: Map<string, BlockDemand>; demandRows: BlockDemand[]; receivedAt: number;
}
export function applyBlockEvent(previous: TimelineData | undefined, event: BlockEvent): TimelineData {
  // Keep the first assigned color for each visible video, including reconnects
  // and changes in its song/source metadata. No history outlives the view data.
  let colors: Map<string, string> | undefined;
  const colorKey = (video: { resource: string; songID: string }) => {
    if (!colors) {
      colors = new Map();
      for (const demand of previous?.demands.values() || []) colors.set(demand.resource, demand.colorKey || demand.songID || demand.resource);
      for (const block of previous?.blocks.values() || []) colors.set(block.resource, block.colorKey || block.songID || block.resource);
    }
    if (!colors.has(video.resource)) colors.set(video.resource, video.songID || video.resource);
    return colors.get(video.resource)!;
  };
  const blocks = event.reset || !previous || event.blocks.length || event.removed.length
    ? new Map<string, BlockView>(event.reset ? undefined : previous?.blocks) : previous.blocks;
  for (const id of event.removed) blocks.delete(id);
  for (const block of event.blocks) blocks.set(block.id, { ...block, colorKey: colorKey(block) });
  const demands = event.reset || !previous || event.demands?.length || event.removedDemands?.length
    ? new Map<string, BlockDemand>(event.reset ? undefined : previous?.demands) : previous.demands;
  for (const id of event.removedDemands || []) demands.delete(id);
  for (const demand of event.demands || []) demands.set(demand.id, { ...demand, colorKey: colorKey(demand) });
  const demandRows = previous && previous.demands === demands
    ? previous.demandRows : projectDemandRows(demands, previous?.demandRows);
  return { ...event, blocks, demands, demandRows, receivedAt: performance.now() };
}

function projectDemandRows(demands: Map<string, BlockDemand>, previous: BlockDemand[] | undefined) {
  const all = [...demands.values()];
  const formal = all.find(demand => demand.playback);
  const gameBlocks = new Set<string>();
  const standalone = all.filter(demand => {
    if (formal && demand.gameSongID === formal.songID && demand.resource === formal.resource) {
      for (const id of demand.blocks) gameBlocks.add(id);
      return false;
    }
    return demand.kind !== 'formal' && demand.kind !== 'handoff';
  });
  const readingSongs = new Set(standalone.filter(demand => demand.kind === 'get' && demand.gameSongID).map(demand => demand.gameSongID));
  const rows = standalone.filter(demand => !(demand.kind === 'queue' && demand.position === 1 && readingSongs.has(demand.songID)));
  if (formal) {
    const previousBlocks = previous?.find(row => row.id === formal.id)?.blocks;
    // The canvas sorts by block number. Changes to source order preserve membership.
    const blocks = previousBlocks?.length === gameBlocks.size && previousBlocks.every(id => gameBlocks.has(id))
      ? previousBlocks : [...gameBlocks];
    rows.push({ ...formal, blocks });
  }
  return rows.sort(compareDemands);
}
export function videoLabel(video: { title: string; songID: string; resource: string }) {
  return video.title || (video.songID ? `歌曲 ${video.songID}` : `视频 ${video.resource.slice(0, 12)}`);
}
export function blockNumber(block: BlockView) {
  return `${block.block + 1}/${block.totalBlocks > 0 ? block.totalBlocks : '?'}`;
}
export function rollingNeedBy(value: RollingTime, now: number) {
  return value.rollingBase ? Math.max(value.rollingBase, now) + (value.rollingOffset ?? 0) : 0;
}
export function needByAt(value: RollingTime & { needBy: number; fixedNeedBy?: number }, now: number) {
  const rolling = rollingNeedBy(value, now);
  return rolling ? Math.min(value.fixedNeedBy || Infinity, rolling) : value.needBy;
}
export function demandLabel(demand: BlockDemand) {
  if (demand.kind === 'formal') return '正式播放';
  return demand.kind === 'get' ? `读取 #${demand.sourceID}` : demand.kind === 'queue' ? `队列 #${demand.position}` : demand.kind === 'batch' ? '手动下载' : '短期交接';
}
export function compareDemands(a: BlockDemand, b: BlockDemand) {
  const rank = { formal: -1, get: 0, handoff: 1, queue: 2, batch: 3 };
  return rank[a.kind] - rank[b.kind] || (a.kind === 'queue' ? a.position - b.position : 0) || a.sourceID - b.sourceID || a.resource.localeCompare(b.resource);
}
export const blockStateNames: Record<BlockState, string> = {
  waiting: '等待下载', downloading: '正在下载', available: '本地可用', protected: '延续保护', stopping: '正在结束',
};
export function sourceName(source: BlockSource) {
  if (source.kind === 'formal') return '正式播放';
  return source.kind === 'get' ? `播放请求 #${source.id}` : source.kind === 'queue' ? `房间队列 · 第 ${source.position} 首` : source.kind === 'batch' ? '手动下载' : '短期交接';
}
const clockFormat = new Intl.DateTimeFormat('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' });
const preciseClockFormat = new Intl.DateTimeFormat('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3 });
export function timeLabel(at: number, precise = false) { return at ? (precise ? preciseClockFormat : clockFormat).format(at) : '无期限'; }
export function absoluteTime(at: number) {
  return at ? new Date(at).toLocaleString('zh-CN', { hour12: false }) : '—';
}
