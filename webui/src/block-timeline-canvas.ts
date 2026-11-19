import { scaleUtc } from 'd3-scale';
import { blockNumber, rollingNeedBy, timeLabel, type BlockView, type TimelineData } from './block-timeline-model';

const laneNames = ['待下载', '下载中', '本地'];
const canvasInset = 12;
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
  videos: { fill: string; edge: string; protectedFill: string; protectedEdge: string }[];
}
export interface TimelineFrame {
  data: TimelineData; timed: TimedBlocks; untimed: BlockView[];
  center: number; now: number; span: number; width: number; untimedOffset: number;
  selected?: string; palette: TimelinePalette;
  blockSize: number; layers: boolean;
}
export function timelineRange(width: number): [number, number] {
  return [60, width - 2 * canvasInset];
}
export function timelineClipRange(width: number): [number, number] {
  return [canvasInset, width - canvasInset];
}
export function untimedLayout(width: number, count: number, size: number) {
  const start = 2 * canvasInset, end = width - start, available = end - start, minimumPitch = size / 2;
  const pitch = Math.max(minimumPitch, Math.min(size + 6, (available - size) / Math.max(1, count - 1)));
  const maxOffset = Math.max(0, (count ? (count - 1) * minimumPitch + size : 0) - available);
  return { start, end, pitch, maxOffset };
}
export function timeScale(frame: Pick<TimelineFrame, 'center' | 'span' | 'width'>) {
  return scaleUtc().domain([frame.center - frame.span / 2, frame.center + frame.span / 2]).range(timelineRange(frame.width));
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
  const [left, right] = timelineClipRange(frame.width);
  if (x < left || x > right) return [];
  const scale = timeScale(frame), first = +scale.invert(x - size / 2), last = +scale.invert(x + size / 2);
  const hits: BlockView[] = [];
  for (const { block } of timedBetween(frame.timed, frame.now, first, last)) {
    const top = blockY(block, layers, size);
    if (y >= top && y <= top + size) hits.push(block);
  }
  return hits; // Earlier times are painted on top; keep covered blocks reachable.
}
type BlockPaintContext = CanvasRenderingContext2D | OffscreenCanvasRenderingContext2D;

function roundRect(ctx: BlockPaintContext, x: number, y: number, width: number, height: number, radius: number) {
  ctx.beginPath(); ctx.roundRect(x, y, width, height, radius);
}
function videoColorIndex(video: { songID: string; resource: string; colorKey?: string }, palette: TimelinePalette) {
  let hash = 2166136261;
  for (const char of video.colorKey || video.songID || video.resource) hash = Math.imul(hash ^ char.charCodeAt(0), 16777619);
  return (hash >>> 0) % palette.videos.length;
}
export function videoColor(video: { songID: string; resource: string; colorKey?: string }, palette: TimelinePalette) {
  return palette.videos[videoColorIndex(video, palette)].fill;
}
function blockStyle(block: BlockView, palette: TimelinePalette) {
  return { videoIndex: videoColorIndex(block, palette), state: block.state, protectionOnly: block.state === 'protected' && !block.inUse };
}
function drawBlockBody(ctx: BlockPaintContext, { videoIndex, state, protectionOnly }: ReturnType<typeof blockStyle>, x: number, y: number, size: number, palette: TimelinePalette) {
  const video = palette.videos[videoIndex];
  const color = protectionOnly ? video.protectedFill : video.fill;
  ctx.save();
  if (state === 'protected') ctx.globalAlpha = protectionOnly ? .25 : .35;
  roundRect(ctx, x, y, size, size, Math.min(3, size / 6));
  ctx.fillStyle = state === 'waiting' ? palette.background : color;
  ctx.fill(); ctx.strokeStyle = color; ctx.lineWidth = 1.5; ctx.stroke();
  // A thin outer edge separates same-color blocks when their squares overlap.
  // Keep the original path for hatching; avoid blur/shadow work per frame.
  ctx.strokeStyle = protectionOnly ? video.protectedEdge : video.edge; ctx.lineWidth = .75; ctx.stroke();
  ctx.restore(); // Selection and reader markers remain fully visible.
  if (state === 'downloading' || state === 'stopping') {
    ctx.save(); ctx.clip(); ctx.strokeStyle = palette.background; ctx.globalAlpha = .55; ctx.lineWidth = 2;
    ctx.beginPath(); for (let offset = -size; offset < size; offset += 7) { ctx.moveTo(x + offset, y + size); ctx.lineTo(x + offset + size, y); } ctx.stroke(); ctx.restore();
  }
}
export function drawBlock(ctx: CanvasRenderingContext2D, block: BlockView, x: number, y: number, size: number, palette: TimelinePalette, selected: boolean) {
  drawBlockBody(ctx, blockStyle(block, palette), x, y, size, palette);
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

type DrawTimelineBlock = (block: BlockView, x: number, y: number, selected: boolean) => void;
const timelineBlockPainters = new WeakMap<CanvasRenderingContext2D, { palette: TimelinePalette; size: number; scale: number; draw: DrawTimelineBlock }>();

function timelineBlockPainter(ctx: CanvasRenderingContext2D, size: number, palette: TimelinePalette): DrawTimelineBlock {
  const scale = ctx.getTransform().a;
  const previous = timelineBlockPainters.get(ctx);
  if (previous?.palette === palette && previous.size === size && previous.scale === scale) return previous.draw;

  const direct: DrawTimelineBlock = (block, x, y, selected) => drawBlock(ctx, block, x, y, size, palette, selected);
  // Keep system colors tied to the visible canvas in forced-colors mode.
  if (typeof OffscreenCanvas === 'undefined' || matchMedia('(forced-colors: active)').matches) {
    timelineBlockPainters.set(ctx, { palette, size, scale, draw: direct });
    return direct;
  }

  // Allow for the 1.5px outline and its antialiasing on each edge of the block body.
  const paddingPixels = Math.ceil(2 * scale);
  const padding = paddingPixels / scale;
  const pixels = Math.ceil(size * scale) + 2 * paddingPixels;
  const extent = pixels / scale;
  const styles = new Map<string, OffscreenCanvas>();
  const blocks = new WeakMap<BlockView, OffscreenCanvas>();
  const draw: DrawTimelineBlock = (block, x, y, selected) => {
    if (selected || block.readers > 0 || block.state === 'stopping') return direct(block, x, y, selected);
    let image = blocks.get(block);
    if (!image) {
      const style = blockStyle(block, palette);
      const key = `${style.videoIndex}:${style.state}:${style.protectionOnly}`;
      image = styles.get(key);
      if (!image) {
        image = new OffscreenCanvas(pixels, pixels);
        const brush = image.getContext('2d');
        if (!brush) return direct(block, x, y, selected);
        brush.scale(scale, scale);
        drawBlockBody(brush, style, padding, padding, size, palette);
        styles.set(key, image);
      }
      blocks.set(block, image);
    }
    ctx.drawImage(image, x - padding, y - padding, extent, extent);
  };
  timelineBlockPainters.set(ctx, { palette, size, scale, draw });
  return draw;
}

export function drawTimeline(ctx: CanvasRenderingContext2D, frame: TimelineFrame) {
  const { width, data, palette: p, now, span, timed, untimed, blockSize: size, layers } = frame;
  const draw = timelineBlockPainter(ctx, size, p);
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
    const [plotStart, plotEnd] = timelineRange(width), plotWidth = plotEnd - plotStart;
    const x = timeScale(frame), [left, right] = timelineClipRange(width);
    const cutoffX = x(now - data.downloadGraceMS), nowX = x(now), aheadX = x(now + data.prepareAheadMS);
    ctx.save(); ctx.beginPath(); ctx.rect(left, plotTop, right - left, axis - plotTop - 8); ctx.clip();
    ctx.fillStyle = p.amberWash; ctx.fillRect(cutoffX, plotTop, nowX - cutoffX, axis - plotTop - 8);
    ctx.fillStyle = p.blueWash; ctx.fillRect(nowX, plotTop, aheadX - nowX, axis - plotTop - 8);
    drawWindowEdge(nowX, p.amber);
    drawWindowEdge(nowX, p.blue);
    drawWindowEdge(aheadX, p.blue);
    ctx.restore();
    const showNow = nowX >= left && nowX <= right;
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
      draw(block, x(at) - size / 2, blockY(block, layers, size), block.id === frame.selected);
    }
    ctx.restore();
    if (layers) {
      ctx.font = captionFont; ctx.textAlign = 'left';
      laneNames.forEach((label, row) => drawLaneLabel(label, timedY + row * lanePitch + size / 2, cutoffX));
    }
    if (cutoffX >= left && cutoffX <= right) drawWindowEdge(cutoffX, p.amber);
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
      draw(untimed[i], px, untimedY, untimed[i].id === frame.selected);
    }
    ctx.restore();
  }
  function drawWindowEdge(x: number, color: string) {
    ctx.save();
    ctx.strokeStyle = color; ctx.lineWidth = 1; ctx.globalAlpha = .35; ctx.setLineDash([3, 5]);
    ctx.beginPath(); ctx.moveTo(x, plotTop); ctx.lineTo(x, axis - 8); ctx.stroke();
    ctx.restore();
  }
  function drawLaneLabel(label: string, centerY: number, cutoffX: number) {
    const { actualBoundingBoxLeft, actualBoundingBoxRight, actualBoundingBoxAscent, actualBoundingBoxDescent } = ctx.measureText(label);
    const inkRight = canvasInset + actualBoundingBoxRight;
    const left = canvasInset - actualBoundingBoxLeft - 4, right = Math.min(inkRight + 4, cutoffX - 1);
    const height = actualBoundingBoxAscent + actualBoundingBoxDescent + 4;
    const y = centerY + (actualBoundingBoxAscent - actualBoundingBoxDescent) / 2;
    ctx.save();
    if (right > canvasInset) {
      ctx.globalAlpha = .85; ctx.fillStyle = p.background;
      roundRect(ctx, left, centerY - height / 2, right - left, height, 4); ctx.fill();
      ctx.globalAlpha = 1;
    }
    if (inkRight + 1 > cutoffX) {
      ctx.strokeStyle = p.background; ctx.lineWidth = 2; ctx.lineJoin = 'round';
      ctx.strokeText(label, canvasInset, y);
    }
    ctx.fillStyle = p.muted; ctx.fillText(label, canvasInset, y);
    ctx.restore();
  }
}
