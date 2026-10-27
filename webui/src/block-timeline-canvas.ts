import { scaleUtc } from 'd3-scale';
import { blockNumber, timeLabel, type BlockView, type TimelineData } from './block-timeline-model';

export function timelineLayout(layers: boolean, untimed: boolean, size: number) {
  const plotTop = 16, timedY = layers ? 44 : 28, lanePitch = size + 32;
  const lanesHeight = size + (layers ? 2 * lanePitch : 0);
  const axis = timedY + lanesHeight + 12, untimedY = axis + 64;
  return { plotTop, timedY, lanePitch, axis, untimedY, height: untimed ? untimedY + lanesHeight + 12 : axis + 40 };
}
export function blockLane(block: BlockView) { return block.state === 'waiting' ? 0 : block.state === 'downloading' || block.state === 'stopping' ? 1 : 2; }
export function blockY(block: BlockView, layers: boolean, size: number) {
  const { timedY, lanePitch } = timelineLayout(layers, false, size);
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
  data: TimelineData; timed: BlockView[]; untimed: BlockView[];
  untimedRows: BlockView[][];
  center: number; now: number; span: number; width: number; untimedOffset: number;
  selected?: string; palette: TimelinePalette;
  blockSize: number; layers: boolean;
}
export function timeScale(frame: Pick<TimelineFrame, 'center' | 'span' | 'width'>) {
  return scaleUtc().domain([frame.center - frame.span / 2, frame.center + frame.span / 2]).range([24, frame.width - 24]);
}
function lowerBound(blocks: BlockView[], at: number) {
  let low = 0, high = blocks.length;
  while (low < high) { const middle = (low + high) >>> 1; if (blocks[middle].at < at) low = middle + 1; else high = middle; }
  return low;
}
export function hitBlocks(frame: TimelineFrame, x: number, y: number) {
  const { blockSize: size, layers } = frame, { untimedY, lanePitch } = timelineLayout(layers, !!frame.untimed.length, size), pitch = size + 6;
  const row = Math.floor((y - untimedY) / lanePitch);
  if (row >= 0 && row < frame.untimedRows.length && y <= untimedY + row * lanePitch + size) {
    const index = Math.floor((x - 24 + frame.untimedOffset) / pitch);
    const blocks = frame.untimedRows[row];
    return index >= 0 && index < blocks.length && (x - 24 + frame.untimedOffset) % pitch <= size ? [blocks[index]] : [];
  }
  const scale = timeScale(frame), first = +scale.invert(x - size / 2), last = +scale.invert(x + size / 2);
  const hits: BlockView[] = [];
  for (let i = lowerBound(frame.timed, first); i < frame.timed.length && frame.timed[i].at <= last; i++) {
    const block = frame.timed[i], top = blockY(block, layers, size);
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
    ctx.fillStyle = palette.amber; ctx.beginPath(); ctx.arc(x + size / 2, y - 9, 2.5, 0, Math.PI * 2); ctx.fill();
  }
  if (selected) {
    ctx.strokeStyle = palette.selected; ctx.lineWidth = 2;
    roundRect(ctx, x - 3, y - 3, size + 6, size + 6, 5); ctx.stroke();
  }
}

export function drawTimeline(ctx: CanvasRenderingContext2D, frame: TimelineFrame) {
  const { width, data, palette: p, now, span, timed, untimed, blockSize: size, layers } = frame;
  const { plotTop, timedY, lanePitch, axis, untimedY, height } = timelineLayout(layers, !!untimed.length, size), pitch = size + 6;
  const x = timeScale(frame), left = 12, right = width - 12;
  ctx.clearRect(0, 0, width, height);
  ctx.fillStyle = p.background; ctx.fillRect(0, 0, width, height);
  ctx.font = '12px "Segoe UI", "Microsoft YaHei", sans-serif';
  ctx.textBaseline = 'middle';
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
  const nowLabelLeft = Math.max(4, Math.min(width - 110, nowX - 53));
  const ticks = x.ticks(Math.max(2, Math.floor((width - 48) / 105)));
  for (const tick of ticks) {
    const px = x(tick);
    ctx.strokeStyle = p.grid; ctx.lineWidth = 1; ctx.beginPath(); ctx.moveTo(px, plotTop); ctx.lineTo(px, axis + 4); ctx.stroke();
    ctx.fillStyle = p.muted; ctx.textAlign = 'center';
    const label = span >= 86400000 ? tick.toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', hour12: false }) : timeLabel(+tick, span < 10000);
    const half = ctx.measureText(label).width / 2;
    // The current time shares the tick band; omit tick labels it would cover.
    if (!showNow || px + half < nowLabelLeft - 8 || px - half > nowLabelLeft + 114) ctx.fillText(label, px, axis + 21);
  }
  ctx.strokeStyle = p.grid; ctx.beginPath(); ctx.moveTo(12, axis); ctx.lineTo(right, axis); ctx.stroke();
  if (layers) {
    ctx.textAlign = 'left';
    ['未安排', '下载中', '本地 / 已完成'].forEach((label, row) => {
      ctx.fillStyle = p.muted; ctx.fillText(label, 24, timedY - 20 + row * lanePitch);
    });
  }
  if (showNow) {
    ctx.strokeStyle = p.blue; ctx.lineWidth = 1.5; ctx.beginPath(); ctx.moveTo(nowX, plotTop); ctx.lineTo(nowX, axis); ctx.stroke();
    ctx.fillStyle = p.blue; ctx.beginPath(); ctx.moveTo(nowX - 5, axis + 4); ctx.lineTo(nowX + 5, axis + 4); ctx.lineTo(nowX, axis - 3); ctx.fill();
    roundRect(ctx, nowLabelLeft, axis + 8, 106, 26, 13); ctx.fill();
    ctx.fillStyle = p.onAccent; ctx.textAlign = 'center'; ctx.font = '600 12px "Segoe UI", sans-serif';
    ctx.fillText('现在 ' + timeLabel(now), nowLabelLeft + 53, axis + 21);
  }
  ctx.save(); ctx.beginPath(); ctx.rect(left, timedY - 13, right - left, axis - timedY + 9); ctx.clip();
  const min = +x.invert(left - size), max = +x.invert(right + size);
  let selected: BlockView | undefined;
  const first = lowerBound(timed, min);
  let end = lowerBound(timed, max);
  while (end < timed.length && timed[end].at === max) end++;
  for (let i = end - 1; i >= first; i--) {
    const b = timed[i];
    if (b.id === frame.selected) selected = b;
    drawBlock(ctx, b, x(b.at) - size / 2, blockY(b, layers, size), size, p, b.id === frame.selected);
  }
  ctx.restore();
  if (selected) {
    const label = `块 ${blockNumber(selected)} · ${selected.timeBasis === 'ended' ? '需求结束' : selected.timeBasis === 'last-need-by' ? '原 need-by' : 'need-by'} ${timeLabel(selected.at)}`;
    ctx.fillStyle = p.foreground; ctx.textAlign = 'center'; ctx.font = '12px "Segoe UI", "Microsoft YaHei", sans-serif';
    const half = ctx.measureText(label).width / 2;
    ctx.fillText(label, Math.max(left + half, Math.min(right - half, x(selected.at))), 8, width - 24);
  }
  if (!timed.length) {
    ctx.fillStyle = p.muted; ctx.textAlign = 'center'; ctx.font = '14px "Segoe UI", "Microsoft YaHei", sans-serif';
    ctx.fillText(data.running ? '有期限的块需求将在这里出现' : '引擎尚未运行', width / 2, timedY + size / 2);
  }
  if (untimed.length) {
    ctx.fillStyle = p.muted; ctx.textAlign = 'left'; ctx.font = '12px "Segoe UI", "Microsoft YaHei", sans-serif';
    frame.untimedRows.forEach((blocks, row) => {
      const y = untimedY + row * lanePitch;
      ctx.fillStyle = p.muted;
      ctx.fillText(`无期限${layers ? ' · ' + ['未安排', '下载中', '本地 / 已完成'][row] : ''} · ${blocks.length} 块`, 24, y - 20);
      ctx.save(); ctx.beginPath(); ctx.rect(left, y - 12, right - left, size + 20); ctx.clip();
      const start = Math.max(0, Math.floor(frame.untimedOffset / pitch) - 1);
      for (let i = start; i < blocks.length; i++) {
        const px = 24 + i * pitch - frame.untimedOffset; if (px > right) break;
        drawBlock(ctx, blocks[i], px, y, size, p, blocks[i].id === frame.selected);
      }
      ctx.restore();
    });
  }
}
