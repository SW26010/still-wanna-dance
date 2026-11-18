import { scaleUtc } from 'd3-scale';
import { blockNumber, rollingNeedBy, timeLabel, type BlockView, type TimelineData } from './block-timeline-model';

const laneNames = ['待下载', '下载中', '本地'];
const readerDotOffset = 9, readerDotRadius = 2.5;
const selectionPadding = 3, selectionStroke = 2;
const selectionOutset = selectionPadding + selectionStroke / 2;

export function timelineLayout(layers: boolean, timed: boolean, untimed: boolean, size: number) {
  const plotTop = 16, timedY = 28;
  const lanePitch = Math.ceil(size + selectionOutset + readerDotOffset + readerDotRadius);
  const lanesHeight = size + (layers ? 2 * lanePitch : 0);
  const axis = timedY + lanesHeight + 12, untimedY = timed ? axis + 64 : 40;
  return { plotTop, timedY, lanePitch, axis, untimedY, height: untimed ? untimedY + size + 12 : timed ? axis + 40 : 0 };
}
export function blockLane(block: BlockView) { return block.state === 'waiting' ? 0 : block.state === 'downloading' || block.state === 'stopping' ? 1 : 2; }
export function blockY(block: BlockView, layers: boolean, size: number) {
  const { timedY, lanePitch } = timelineLayout(layers, true, false, size);
  return timedY + (layers ? blockLane(block) * lanePitch : 0);
}
export interface TimelinePalette {
  background: string; foreground: string; muted: string; grid: string;
  blue: string; blueWash: string; green: string; amber: string; amberWash: string;
  selected: string; onAccent: string;
  videos: string[];
  videoEdges: string[];
}
export interface TimelineFrame {
  data: TimelineData; timed: TimedBlocks; untimed: BlockView[];
  center: number; now: number; span: number; width: number; untimedOffset: number;
  selected?: string; palette: TimelinePalette;
  blockSize: number; layers: boolean;
}
export function timelineRange(width: number, layers: boolean): [number, number] {
  return [layers ? 120 : 24, width - 24];
}
export function untimedLayout(width: number, count: number, size: number) {
  const [start, end] = timelineRange(width, false), available = end - start, minimumPitch = size / 2;
  const pitch = Math.max(minimumPitch, Math.min(size + 6, (available - size) / Math.max(1, count - 1)));
  const maxOffset = Math.max(0, (count ? (count - 1) * minimumPitch + size : 0) - available);
  return { start, end, pitch, maxOffset };
}
export function timeScale(frame: Pick<TimelineFrame, 'center' | 'span' | 'width' | 'layers'>) {
  return scaleUtc().domain([frame.center - frame.span / 2, frame.center + frame.span / 2]).range(timelineRange(frame.width, frame.layers));
}
interface TimedPoint { block: BlockView; at: number }
interface TimedBlocks { length: number; fixed: TimedPoint[]; rolling: Map<number, TimedPoint[]> }
const comparePoints = (a: TimedPoint, b: TimedPoint) => a.at - b.at || a.block.id.localeCompare(b.block.id);

export function indexTimed(blocks: Iterable<BlockView>): TimedBlocks {
  const index: TimedBlocks = { length: 0, fixed: [], rolling: new Map() };
  for (const block of blocks) {
    if (!block.at) continue;
    index.length++;
    const fixed = block.rollingBase ? block.fixedNeedBy : block.at;
    if (fixed) index.fixed.push({ block, at: fixed });
    if (block.rollingBase) {
      let group = index.rolling.get(block.rollingBase);
      if (!group) index.rolling.set(block.rollingBase, group = []);
      group.push({ block, at: block.rollingOffset ?? 0 });
    }
  }
  index.fixed.sort(comparePoints);
  for (const group of index.rolling.values()) group.sort(comparePoints);
  return index;
}

function lowerBound(blocks: TimedPoint[], at: number) {
  let low = 0, high = blocks.length;
  while (low < high) { const middle = (low + high) >>> 1; if (blocks[middle].at < at) low = middle + 1; else high = middle; }
  return low;
}
// Each rolling group moves by the same amount. Search its visible range using
// the current origin, then compare with independent fixed deadlines.
export function timedBetween(index: TimedBlocks, now: number, first: number, last: number) {
  const points: TimedPoint[] = [];
  for (let i = lowerBound(index.fixed, first); i < index.fixed.length && index.fixed[i].at <= last; i++) {
    const point = index.fixed[i], rolling = rollingNeedBy(point.block, now);
    if (!rolling || point.at <= rolling) points.push(point);
  }
  for (const [base, group] of index.rolling) {
    const origin = Math.max(base, now);
    for (let i = lowerBound(group, first - origin); i < group.length && group[i].at <= last - origin; i++) {
      const { block, at: offset } = group[i], at = origin + offset;
      if (!block.fixedNeedBy || at < block.fixedNeedBy) points.push({ block, at });
    }
  }
  return index.rolling.size ? points.sort(comparePoints) : points;
}
export function hitBlocks(frame: TimelineFrame, x: number, y: number) {
  const { blockSize: size, layers } = frame, { untimedY } = timelineLayout(layers, !!frame.timed.length, !!frame.untimed.length, size);
  if (y >= untimedY && y <= untimedY + size) {
    const { start, end, pitch } = untimedLayout(frame.width, frame.untimed.length, size);
    if (x < start - 12 || x > end + 12) return [];
    const local = x - start + frame.untimedOffset, first = Math.max(0, Math.ceil((local - size) / pitch)), last = Math.min(frame.untimed.length - 1, Math.floor(local / pitch));
    if (last < first) return [];
    return frame.untimed.slice(first, last + 1);
  }
  const [plotStart, plotEnd] = timelineRange(frame.width, layers);
  if (x < plotStart - 12 || x > plotEnd + 12) return [];
  const scale = timeScale(frame), first = +scale.invert(x - size / 2), last = +scale.invert(x + size / 2);
  const hits: BlockView[] = [];
  for (const { block } of timedBetween(frame.timed, frame.now, first, last)) {
    const top = blockY(block, layers, size);
    if (y >= top && y <= top + size) hits.push(block);
  }
  return hits; // Earlier times are painted on top; keep covered blocks reachable.
}
function roundRect(ctx: CanvasRenderingContext2D, x: number, y: number, width: number, height: number, radius: number) {
  ctx.beginPath(); ctx.roundRect(x, y, width, height, radius);
}
function videoColorIndex(video: { songID: string; resource: string; colorKey?: string }, palette: TimelinePalette) {
  let hash = 2166136261;
  for (const char of video.colorKey || video.songID || video.resource) hash = Math.imul(hash ^ char.charCodeAt(0), 16777619);
  return (hash >>> 0) % palette.videos.length;
}
export function videoColor(video: { songID: string; resource: string; colorKey?: string }, palette: TimelinePalette) {
  return palette.videos[videoColorIndex(video, palette)];
}
export function drawBlock(ctx: CanvasRenderingContext2D, block: BlockView, x: number, y: number, size: number, palette: TimelinePalette, selected: boolean) {
  const index = videoColorIndex(block, palette), color = palette.videos[index];
  ctx.save();
  if (block.state === 'protected') ctx.globalAlpha = .35;
  roundRect(ctx, x, y, size, size, Math.min(3, size / 6));
  ctx.fillStyle = block.state === 'waiting' ? palette.background : color;
  ctx.fill(); ctx.strokeStyle = color; ctx.lineWidth = 1.5; ctx.stroke();
  // A thin outer edge separates same-color blocks when their squares overlap.
  // Keep the original path for hatching; avoid blur/shadow work per frame.
  ctx.strokeStyle = palette.videoEdges[index]; ctx.lineWidth = .75; ctx.stroke();
  ctx.restore(); // Selection and reader markers remain fully visible.
  if (block.state === 'downloading' || block.state === 'stopping') {
    ctx.save(); ctx.clip(); ctx.strokeStyle = palette.background; ctx.globalAlpha = .55; ctx.lineWidth = 2;
    ctx.beginPath(); for (let offset = -size; offset < size; offset += 7) { ctx.moveTo(x + offset, y + size); ctx.lineTo(x + offset + size, y); } ctx.stroke(); ctx.restore();
  }
  if (block.state === 'stopping') {
    ctx.strokeStyle = palette.foreground; ctx.setLineDash([2, 2]);
    ctx.strokeRect(x - 2, y - 2, size + 4, size + 4); ctx.setLineDash([]);
  }
  if (block.readers > 0) {
    ctx.strokeStyle = palette.amber; ctx.lineWidth = 2;
    roundRect(ctx, x - 2, y - 2, size + 4, size + 4, 5); ctx.stroke();
    ctx.fillStyle = palette.amber; ctx.beginPath(); ctx.arc(x + size / 2, y - readerDotOffset, readerDotRadius, 0, Math.PI * 2); ctx.fill();
  }
  if (selected) {
    ctx.strokeStyle = palette.selected; ctx.lineWidth = selectionStroke;
    roundRect(ctx, x - selectionPadding, y - selectionPadding, size + 2 * selectionPadding, size + 2 * selectionPadding, 5); ctx.stroke();
  }
}

export function drawTimeline(ctx: CanvasRenderingContext2D, frame: TimelineFrame) {
  const { width, data, palette: p, now, span, timed, untimed, blockSize: size, layers } = frame;
  const { plotTop, timedY, lanePitch, axis, untimedY, height } = timelineLayout(layers, !!timed.length, !!untimed.length, size);
  const textStyle = getComputedStyle(ctx.canvas), textSizeFamily = `${textStyle.fontSize} ${textStyle.fontFamily}`;
  const captionSizeFamily = `${textStyle.getPropertyValue('--fontSizeBase200').trim()} ${textStyle.fontFamily}`;
  const font = `${textStyle.fontWeight} ${textSizeFamily}`, captionFont = `${textStyle.fontWeight} ${captionSizeFamily}`;
  const strongCaptionFont = `${textStyle.getPropertyValue('--fontWeightSemibold').trim()} ${captionSizeFamily}`;
  ctx.clearRect(0, 0, width, height);
  ctx.fillStyle = p.background; ctx.fillRect(0, 0, width, height);
  ctx.font = captionFont;
  ctx.textBaseline = 'middle';
  if (timed.length) {
    const [plotStart, plotEnd] = timelineRange(width, layers), plotWidth = plotEnd - plotStart;
    const x = timeScale(frame), left = plotStart - 12, right = plotEnd + 12;
    ctx.save(); ctx.beginPath(); ctx.rect(left, plotTop, right - left, axis - plotTop - 8); ctx.clip();
    const band = (start: number, end: number, fill: string, stroke: string) => {
      const a = x(start), b = x(end);
      ctx.fillStyle = fill; ctx.fillRect(a, plotTop, b - a, axis - plotTop - 8);
      ctx.strokeStyle = stroke; ctx.globalAlpha = .35; ctx.setLineDash([3, 5]);
      ctx.beginPath(); ctx.moveTo(a, plotTop); ctx.lineTo(a, axis - 8); ctx.moveTo(b, plotTop); ctx.lineTo(b, axis - 8); ctx.stroke(); ctx.setLineDash([]); ctx.globalAlpha = 1;
    };
    band(now - data.downloadGraceMS, now, p.amberWash, p.amber);
    band(now, now + data.prepareAheadMS, p.blueWash, p.blue);
    ctx.restore();
    const nowX = x(now), showNow = nowX >= left && nowX <= right;
    const nowLabel = '现在 ' + timeLabel(now);
    ctx.font = strongCaptionFont; ctx.textAlign = 'center';
    const nowText = ctx.measureText(nowLabel);
    const paddingX = parseFloat(textStyle.getPropertyValue('--spacingHorizontalSNudge')), paddingY = parseFloat(textStyle.getPropertyValue('--spacingVerticalXS'));
    const nowLabelWidth = Math.ceil(nowText.width) + 2 * paddingX;
    const nowLabelHeight = Math.ceil(nowText.actualBoundingBoxAscent + nowText.actualBoundingBoxDescent) + 2 * paddingY;
    const nowLabelLeft = Math.max(4, Math.min(width - nowLabelWidth - 4, nowX - nowLabelWidth / 2));
    // Canvas alignment uses font metrics; center the frame on the visible glyphs.
    const nowLabelTop = axis + 21 + (nowText.actualBoundingBoxDescent - nowText.actualBoundingBoxAscent) / 2 - nowLabelHeight / 2;
    const nowTextX = nowLabelLeft + nowLabelWidth / 2 + (nowText.actualBoundingBoxLeft - nowText.actualBoundingBoxRight) / 2;
    ctx.font = captionFont;
    const ticks = x.ticks(Math.max(2, Math.floor(plotWidth / 105)));
    let lastLabelRight = left - 8;
    for (const tick of ticks) {
      const px = x(tick);
      ctx.strokeStyle = p.grid; ctx.lineWidth = 1; ctx.beginPath(); ctx.moveTo(px, plotTop); ctx.lineTo(px, axis + 4); ctx.stroke();
      ctx.fillStyle = p.muted; ctx.textAlign = 'center';
      const label = span >= 86400000 ? tick.toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', hour12: false }) : timeLabel(+tick, span < 10000);
      const half = ctx.measureText(label).width / 2;
      // Measure the current font so Fluent typography never crowds adjacent labels.
      if (px - half >= lastLabelRight + 8 && px + half <= right && (!showNow || px + half < nowLabelLeft - 8 || px - half > nowLabelLeft + nowLabelWidth + 8)) {
        ctx.fillText(label, px, axis + 21); lastLabelRight = px + half;
      }
    }
    ctx.strokeStyle = p.grid; ctx.beginPath(); ctx.moveTo(left, axis); ctx.lineTo(right, axis); ctx.stroke();
    if (layers) {
      ctx.textAlign = 'left';
      laneNames.forEach((label, row) => {
        ctx.fillStyle = p.muted; drawLaneLabel(label, timedY + row * lanePitch + size / 2);
      });
    }
    if (showNow) {
      ctx.strokeStyle = p.blue; ctx.lineWidth = 1.5; ctx.beginPath(); ctx.moveTo(nowX, plotTop); ctx.lineTo(nowX, axis); ctx.stroke();
      ctx.fillStyle = p.blue; ctx.beginPath(); ctx.moveTo(nowX - 5, axis + 4); ctx.lineTo(nowX + 5, axis + 4); ctx.lineTo(nowX, axis - 3); ctx.fill();
      roundRect(ctx, nowLabelLeft, nowLabelTop, nowLabelWidth, nowLabelHeight, nowLabelHeight / 2); ctx.fill();
      ctx.fillStyle = p.onAccent; ctx.textAlign = 'center'; ctx.font = strongCaptionFont;
      ctx.fillText(nowLabel, nowTextX, axis + 21);
    }
    ctx.save(); ctx.beginPath(); ctx.rect(left, timedY - 13, right - left, axis - timedY + 9); ctx.clip();
    const min = +x.invert(left - size), max = +x.invert(right + size);
    let selected: TimedPoint | undefined;
    const visible = timedBetween(timed, now, min, max);
    for (let i = visible.length - 1; i >= 0; i--) {
      const { block, at } = visible[i];
      if (block.id === frame.selected) selected = visible[i];
      drawBlock(ctx, block, x(at) - size / 2, blockY(block, layers, size), size, p, block.id === frame.selected);
    }
    ctx.restore();
    if (selected) {
      const { block, at } = selected;
      const label = `块 ${blockNumber(block)} · ${block.timeBasis === 'ended' ? '需求结束' : block.timeBasis === 'last-need-by' ? '原 need-by' : 'need-by'} ${timeLabel(at)}`;
      ctx.fillStyle = p.foreground; ctx.textAlign = 'center'; ctx.font = font;
      const half = ctx.measureText(label).width / 2;
      ctx.fillText(label, Math.max(12 + half, Math.min(width - 12 - half, x(at))), 8, width - 24);
    }
  }
  if (untimed.length) {
    const { start, end, pitch } = untimedLayout(width, untimed.length, size);
    ctx.fillStyle = p.muted; ctx.textAlign = 'left'; ctx.font = captionFont;
    ctx.fillText(`无期限 · ${untimed.length} 块`, start, untimedY - 20);
    ctx.save(); ctx.beginPath(); ctx.rect(start - 12, untimedY - 12, end - start + 24, size + 20); ctx.clip();
    const first = Math.max(0, Math.floor((frame.untimedOffset - size - 12) / pitch)), last = Math.min(untimed.length - 1, Math.ceil((end - start + frame.untimedOffset + 12) / pitch));
    for (let i = last; i >= first; i--) {
      const px = start + i * pitch - frame.untimedOffset;
      drawBlock(ctx, untimed[i], px, untimedY, size, p, untimed[i].id === frame.selected);
    }
    ctx.restore();
  }
  function drawLaneLabel(label: string, centerY: number) {
    const metrics = ctx.measureText(label);
    ctx.fillText(label, 12, centerY + (metrics.actualBoundingBoxAscent - metrics.actualBoundingBoxDescent) / 2);
  }
}
