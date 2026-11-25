export type BlockState = 'waiting' | 'downloading' | 'available' | 'protected' | 'stopping';
interface RollingTime { rollingBase?: number; rollingOffset?: number }
export interface BlockView extends RollingTime {
  id: string; resource: string; songID: string; block: number; byteOffset: number; totalBlocks: number; bytes: number; title: string;
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
export interface BlockDetail { block: BlockView; sources: BlockSource[]; upstreamIP?: string; viaProxy?: boolean }
export interface PlaybackClock { zeroAt: number; endAt: number }
export interface BlockAvailability {
  totalBytes: number; availableBytes: number;
  availableRanges?: { start: number; end: number }[];
}
export interface BlockDemand {
  id: string; sourceID: number; kind: BlockSource['kind']; songID: string; title: string;
  position: number; resource: string; totalBlocks: number; blocks: string[];
  gameSongID?: string; playback?: PlaybackClock;
}
export interface BlockEvent {
  at: number; running: boolean; reset: boolean; capacity: number; used: number;
  active: number; open: number; prepareAheadMS: number; downloadGraceMS: number;
  blocks: BlockView[]; removed: string[];
  demands: BlockDemand[]; removedDemands: string[];
  availability?: Record<string, BlockAvailability>; removedAvailability?: string[];
}
export interface TimelineData extends Omit<BlockEvent, 'blocks' | 'removed' | 'reset' | 'demands' | 'removedDemands' | 'availability' | 'removedAvailability'> {
  blocks: Map<string, BlockView>; demands: Map<string, BlockDemand>; demandRows: BlockDemand[]; receivedAt: number;
  availability: Map<string, BlockAvailability>;
  colorPool: VideoColorPool;
}
export const videoColorCount = 9;
interface VideoColorPool {
  order: readonly number[];
  byResource: ReadonlyMap<string, number>;
}
const initialColorPool: VideoColorPool = {
  order: Array.from({ length: videoColorCount }, (_, index) => index),
  byResource: new Map(),
};

function updateColorPool(previous: VideoColorPool, rows: BlockDemand[], blocks: Iterable<BlockView>): VideoColorPool {
  const resources = new Set(rows.map(row => row.resource));
  for (const block of blocks) resources.add(block.resource);
  if (resources.size === previous.byResource.size && [...resources].every(resource => previous.byResource.has(resource))) return previous;

  const byResource = new Map(previous.byResource), order = [...previous.order];
  const occupied = new Set<number>(), released = new Set<number>();
  const moveToBack = (color: number) => {
    order.splice(order.indexOf(color), 1);
    order.push(color);
  };
  for (const [resource, color] of byResource) {
    if (resources.has(resource)) {
      occupied.add(color);
    } else {
      byResource.delete(resource);
      released.add(color);
    }
  }
  // Preserve the prior order for colors released in the same snapshot.
  for (const color of previous.order) {
    if (released.has(color) && !occupied.has(color)) moveToBack(color);
  }
  for (const resource of resources) {
    if (byResource.has(resource)) continue;
    const color = order.find(index => !occupied.has(index)) ?? order[0];
    byResource.set(resource, color);
    occupied.add(color);
    moveToBack(color);
  }
  return { order, byResource };
}

export function applyBlockEvent(previous: TimelineData | undefined, event: BlockEvent): TimelineData {
  const blocks = event.reset || !previous || event.blocks.length || event.removed.length
    ? new Map<string, BlockView>(event.reset ? undefined : previous?.blocks) : previous.blocks;
  for (const id of event.removed) blocks.delete(id);
  for (const block of event.blocks) blocks.set(block.id, block);
  const demands = event.reset || !previous || event.demands?.length || event.removedDemands?.length
    ? new Map<string, BlockDemand>(event.reset ? undefined : previous?.demands) : previous.demands;
  for (const id of event.removedDemands || []) demands.delete(id);
  for (const demand of event.demands || []) demands.set(demand.id, demand);
  const demandRows = previous && previous.demands === demands
    ? previous.demandRows : projectDemandRows(demands, previous?.demandRows);
  const colorPool = previous && previous.blocks === blocks && previous.demandRows === demandRows
    ? previous.colorPool : updateColorPool(previous?.colorPool ?? initialColorPool, demandRows, blocks.values());
  const availabilityUpdates = Object.entries(event.availability ?? {});
  const availability = event.reset || !previous || availabilityUpdates.length || event.removedAvailability?.length
    ? new Map(event.reset ? undefined : previous?.availability) : previous.availability;
  for (const resource of event.removedAvailability ?? []) availability.delete(resource);
  for (const [resource, view] of availabilityUpdates) availability.set(resource, view);
  return { ...event, blocks, demands, demandRows, colorPool, availability, receivedAt: performance.now() };
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
export function blockNeedByDisplay(block: BlockView, now: number) {
  if (block.timeBasis === 'last-need-by') return { label: '原 need-by', time: block.at };
  return { label: 'need-by', time: needByAt(block, now) };
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
