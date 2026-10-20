import { useLayoutEffect, useMemo, useRef, useState, type MouseEvent } from 'react';
import { Badge, Caption1, Text } from '@fluentui/react-components';
import { compareDemands, demandLabel, videoLabel, type BlockDemand, type BlockView, type TimelineData } from './block-timeline-model';
import { drawBlock, videoColor, type TimelinePalette } from './block-timeline-canvas';
import { useFocusRecovery } from './hooks';

type Props = {
  data: TimelineData; size: number; palette: TimelinePalette; active: boolean; selected?: string;
  onPick: (blocks: BlockView[], x: number, y: number, pinned: boolean, target: HTMLCanvasElement) => void;
  onLeave: (event: MouseEvent<HTMLElement>) => void;
};
const rowHeight = 128, listHeight = 440;

export function BlockDemandView(props: Props) {
  const focus = useFocusRecovery();
  const demands = useMemo(() => [...props.data.demands.values()].sort(compareDemands), [props.data.demands]);
  const list = useRef<HTMLDivElement>(null), [scroll, setScroll] = useState(0);
  const height = Math.min(listHeight, demands.length * rowHeight);
  // Clamp before slicing: removals can leave the previous scroll position past
  // the new list end. Never commit an empty virtual window between two updates.
  const maxScroll = Math.max(0, demands.length * rowHeight - height), visibleScroll = Math.min(scroll, maxScroll);
  const first = Math.max(0, Math.floor(visibleScroll / rowHeight) - 1), last = Math.min(demands.length, Math.ceil((visibleScroll + height) / rowHeight) + 1);
  useLayoutEffect(() => {
    if (list.current) { list.current.scrollTop = Math.min(list.current.scrollTop, maxScroll); setScroll(list.current.scrollTop); }
  }, [maxScroll]);
  return <section {...focus} tabIndex={-1} className="block-demand-view" aria-label="视频需求视图">
    <div className="demand-view-heading"><Text weight="semibold" size={400}>需求视图</Text><Caption1>{demands.length} 项需求</Caption1></div>
    <Caption1 as="p" className="demand-view-help">GET → 短期交接 → 队列 → 手动下载 · 块按视频内位置排列，无需求的区间留空</Caption1>
    {demands.length ? <div ref={list} className="demand-list" style={{ height }} onScroll={event => setScroll(event.currentTarget.scrollTop)} role="list" aria-label="视频需求列表">
      <div style={{ height: first * rowHeight }} aria-hidden="true" />
      {demands.slice(first, last).map((demand, index) => <DemandRow key={demand.id} {...props} demand={demand} position={first + index + 1} total={demands.length} />)}
      <div style={{ height: (demands.length - last) * rowHeight }} aria-hidden="true" />
    </div> : <p className="demand-empty">当前没有使用需求；保护期内的本地块仍显示在时间轴中。</p>}
  </section>;
}

function DemandRow({ demand, position, total, ...props }: Props & { demand: BlockDemand; position: number; total: number }) {
  const strip = useRef<HTMLDivElement>(null), canvas = useRef<HTMLCanvasElement>(null);
  const [width, setWidth] = useState(0), [focused, setFocused] = useState(0);
  const blocks = useMemo(() => demand.blocks.flatMap(id => { const b = props.data.blocks.get(id); return b ? [b] : []; }).sort((a, b) => a.block - b.block), [demand.blocks, props.data.blocks]);
  const videoBlocks = Math.max(demand.totalBlocks || 0, (blocks.at(-1)?.block ?? -1) + 1);
  // Fit the complete video to the row, allowing overlap without shrinking squares.
  // With unknown length, scale to the observed extent until the total is available.
  const pitch = Math.max(0, width - 24 - props.size) / Math.max(1, videoBlocks - 1);
  const inset = videoBlocks > 1 ? 12 : (width - props.size) / 2;
  const height = props.size + 24;
  const title = videoLabel(demand);
  // Measure and draw new/replaced virtual rows before the browser paints them,
  // so the heading, row bounds and blocks appear together at their final width.
  useLayoutEffect(() => {
    const element = strip.current!, observer = new ResizeObserver(() => setWidth(element.clientWidth));
    observer.observe(element); setWidth(element.clientWidth); return () => observer.disconnect();
  }, []);
  useLayoutEffect(() => {
    const element = canvas.current!, ctx = element.getContext('2d');
    if (!ctx || !width) return;
    const dpr = devicePixelRatio || 1;
    if (element.width !== Math.round(width * dpr)) element.width = Math.round(width * dpr);
    if (element.height !== Math.round(height * dpr)) element.height = Math.round(height * dpr);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0); ctx.clearRect(0, 0, width, height);
    for (let i = blocks.length - 1; i >= 0; i--) {
      const block = blocks[i], x = inset + block.block * pitch;
      drawBlock(ctx, block, x, 12, props.size, props.palette, block.id === props.selected);
    }
  }, [blocks, width, height, pitch, inset, props.size, props.palette, props.selected]);
  const pointer = (event: MouseEvent<HTMLCanvasElement>, pinned: boolean) => {
    if (!props.active) return;
    const rect = event.currentTarget.getBoundingClientRect(), x = event.clientX - rect.left - inset, y = event.clientY - rect.top - 12;
    const hits = y >= 0 && y <= props.size ? blocks.filter(block => x >= block.block * pitch && x <= block.block * pitch + props.size) : [];
    if (hits.length) setFocused(blocks.indexOf(hits[0]));
    props.onPick(hits, event.clientX, event.clientY, pinned, event.currentTarget);
  };
  return <div className="demand-row-slot" role="listitem" aria-posinset={position} aria-setsize={total}>
    <div className={`demand-row${demand.kind === 'handoff' ? ' demand-handoff' : ''}`} data-demand-id={demand.id} style={{ borderColor: demand.kind === 'handoff' ? '#fff' : videoColor(demand, props.palette) }}>
      <div className="demand-row-heading"><Badge appearance="tint" color="informative">{demandLabel(demand)}</Badge><Text weight="semibold" className="demand-video-title" title={title}>{title}</Text><Caption1 title={demand.totalBlocks > 0 ? `视频共 ${demand.totalBlocks} 块` : '视频总块数尚未知'}>{blocks.length} 块</Caption1></div>
      <div ref={strip} className="demand-block-strip">
        <canvas ref={canvas} className="demand-block-canvas" style={{ width, height }} tabIndex={0} role="group" aria-label={`${demandLabel(demand)}，${title}，需要 ${blocks.length} 个块，${demand.totalBlocks > 0 ? `视频共 ${demand.totalBlocks} 块` : '总块数尚未知'}。左右键选择，Home 和 End 跳到首尾。`}
          onPointerMove={event => pointer(event, false)} onPointerLeave={props.onLeave} onClick={event => pointer(event, true)}
          onKeyDown={event => {
            if (event.key === 'Escape') { props.onPick([], 0, 0, true, event.currentTarget); return; }
            if (!props.active || !blocks.length || !['ArrowLeft', 'ArrowRight', 'Home', 'End', 'Enter'].includes(event.key)) return;
            event.preventDefault();
            const index = event.key === 'Home' ? 0 : event.key === 'End' ? blocks.length - 1 : Math.max(0, Math.min(blocks.length - 1, focused + (event.key === 'ArrowLeft' ? -1 : event.key === 'ArrowRight' ? 1 : 0)));
            setFocused(index);
            const center = inset + blocks[index].block * pitch + props.size / 2;
            const rect = canvas.current!.getBoundingClientRect();
            props.onPick([blocks[index]], rect.left + center, rect.top + 12 + props.size / 2, true, event.currentTarget);
          }} />
      </div>
    </div>
  </div>;
}
