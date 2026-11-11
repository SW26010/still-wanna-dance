import { useLayoutEffect, useMemo, useRef, useState, type MouseEvent } from 'react';
import { Caption1, Text } from '@fluentui/react-components';
import { compareDemands, demandLabel, videoLabel, type BlockDemand, type BlockView, type TimelineData } from './block-timeline-model';
import { drawBlock, videoColor, type TimelinePalette } from './block-timeline-canvas';
import { useFocusRecovery } from './hooks';

type Props = {
  data: TimelineData; size: number; palette: TimelinePalette; active: boolean; selected?: string;
  onPick: (blocks: BlockView[], x: number, y: number, pinned: boolean, target: HTMLCanvasElement) => void;
  onLeave: (event: MouseEvent<HTMLElement>) => void;
};
export function BlockDemandView({ showHeading, ...props }: Props & { showHeading: boolean }) {
  const focus = useFocusRecovery();
  const demands = useMemo(() => {
    const all = [...props.data.demands.values()];
    const readingSongs = new Set(all.filter(demand => demand.kind === 'get' && demand.songID).map(demand => demand.songID));
    return all.filter(demand => demand.kind !== 'handoff' && !(demand.kind === 'queue' && demand.position === 1 && readingSongs.has(demand.songID))).sort(compareDemands);
  }, [props.data.demands]);
  return <section {...focus} tabIndex={-1} className="block-demand-view" aria-label="视频需求视图">
    <div className="demand-view-heading" hidden={!showHeading}><Text weight="semibold" size={300}>需求视图</Text><Caption1>{demands.length} 项需求</Caption1></div>
    {demands.length ? <div className="demand-list" role="list" aria-label="视频需求列表">
      {demands.map((demand, index) => <DemandRow key={demand.id} {...props} demand={demand} position={index + 1} total={demands.length} />)}
    </div> : <p className="demand-empty">当前没有使用需求。</p>}
  </section>;
}

function DemandRow({ demand, position, total, ...props }: Props & { demand: BlockDemand; position: number; total: number }) {
  const strip = useRef<HTMLDivElement>(null), canvas = useRef<HTMLCanvasElement>(null);
  const [width, setWidth] = useState(0), [focused, setFocused] = useState(0);
  const inView = useRef(false), repaint = useRef((_force = false) => {});
  const blocks = useMemo(() => demand.blocks.flatMap(id => { const b = props.data.blocks.get(id); return b ? [b] : []; }).sort((a, b) => a.block - b.block), [demand.blocks, props.data.blocks]);
  const videoBlocks = Math.max(demand.totalBlocks || 0, (blocks.at(-1)?.block ?? -1) + 1);
  // Fit the complete video to the row, allowing overlap without shrinking squares.
  // With unknown length, scale to the observed extent until the total is available.
  const pitch = Math.max(0, width - 24 - props.size) / Math.max(1, videoBlocks - 1);
  const inset = videoBlocks > 1 ? 12 : (width - props.size) / 2;
  const height = props.size + 18; // 12 px above for the reader dot, 6 px below for selection.
  const title = videoLabel(demand);
  const available = useMemo(() => blocks.filter(block => block.state === 'available' || block.state === 'protected').length, [blocks]);
  const percent = Math.floor(100 * available / (blocks.length || 1));
  // Keep every row in normal page flow, but only allocate and paint visible
  // canvases. Measure new rows before paint so live replacements appear together.
  useLayoutEffect(() => {
    const element = strip.current!;
    const resize = new ResizeObserver(() => setWidth(element.clientWidth));
    const updateVisibility = () => repaint.current();
    const visibility = new IntersectionObserver(updateVisibility);
    resize.observe(element); visibility.observe(element); setWidth(element.clientWidth);
    // Scroll events run before paint; drawing here avoids a blank frame while
    // waiting for IntersectionObserver after a page jump or a large removal.
    addEventListener('scroll', updateVisibility, { passive: true });
    return () => { resize.disconnect(); visibility.disconnect(); removeEventListener('scroll', updateVisibility); };
  }, []);
  useLayoutEffect(() => {
    repaint.current = (force = false) => {
      const element = canvas.current!, bounds = strip.current!.getBoundingClientRect();
      const visible = bounds.bottom > 0 && bounds.top < innerHeight && bounds.right > 0 && bounds.left < innerWidth;
      if (!visible || !width) {
        inView.current = false;
        if (element.width || element.height) { element.width = 0; element.height = 0; }
        return;
      }
      if (inView.current && !force) return;
      inView.current = true;
      const ctx = element.getContext('2d');
      if (!ctx) return;
      const dpr = devicePixelRatio || 1;
      if (element.width !== Math.round(width * dpr)) element.width = Math.round(width * dpr);
      if (element.height !== Math.round(height * dpr)) element.height = Math.round(height * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0); ctx.clearRect(0, 0, width, height);
      for (let i = blocks.length - 1; i >= 0; i--) {
        const block = blocks[i], x = inset + block.block * pitch;
        drawBlock(ctx, block, x, 12, props.size, props.palette, block.id === props.selected);
      }
    };
    repaint.current(true);
  }, [blocks, width, height, pitch, inset, props.size, props.palette, props.selected]);
  useLayoutEffect(() => { repaint.current(); });
  const pointer = (event: MouseEvent<HTMLCanvasElement>, pinned: boolean) => {
    if (!props.active) return;
    const rect = event.currentTarget.getBoundingClientRect(), x = event.clientX - rect.left - inset, y = event.clientY - rect.top - 12;
    const hits = y >= 0 && y <= props.size ? blocks.filter(block => x >= block.block * pitch && x <= block.block * pitch + props.size) : [];
    if (hits.length) setFocused(blocks.indexOf(hits[0]));
    props.onPick(hits, event.clientX, event.clientY, pinned, event.currentTarget);
  };
  return <div className="demand-row-slot" role="listitem" aria-posinset={position} aria-setsize={total}>
    <div className="demand-row" data-demand-id={demand.id} style={{ borderColor: videoColor(demand, props.palette) }}>
      <div className="demand-row-heading"><Caption1 className="demand-source">{demandLabel(demand)}</Caption1><div className="demand-video-heading"><Text size={300} className={'demand-video-title'+(!demand.title&&!demand.songID?' resource-id':'')} title={title}>{title}</Text><Caption1 className="demand-progress" title={`当前需求中 ${available} / ${blocks.length} 块本地可用`} aria-label={`${demandLabel(demand)}，${title}，需求就绪进度 ${percent}%`}>{percent}%</Caption1></div><Caption1 title={demand.totalBlocks > 0 ? `视频共 ${demand.totalBlocks} 块` : '视频总块数尚未知'}>{blocks.length} 块</Caption1></div>
      <div ref={strip} className="demand-block-strip">
        <canvas ref={canvas} width={0} height={0} className="demand-block-canvas" style={{ width, height }} tabIndex={0} role="group" aria-label={`${demandLabel(demand)}，${title}，需要 ${blocks.length} 个块，${demand.totalBlocks > 0 ? `视频共 ${demand.totalBlocks} 块` : '总块数尚未知'}。左右键选择，Home 和 End 跳到首尾。`}
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
