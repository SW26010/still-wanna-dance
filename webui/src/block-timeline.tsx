import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type MouseEvent } from 'react';
import { Badge, Button, Caption1, Popover, PopoverSurface, Slider, Spinner, Switch, Text } from '@fluentui/react-components';
import { Add20Regular, Subtract20Regular, ArrowSync20Regular, ChevronLeft20Regular, ChevronRight20Regular, Dismiss20Regular, Pin20Regular } from '@fluentui/react-icons';
import { scaleLinear } from 'd3-scale';
import { select } from 'd3-selection';
import { zoom, zoomIdentity, zoomTransform } from 'd3-zoom';
import { Panel, Feedback } from './ui';
import { useApp } from './hooks';
import { absoluteTime, blockNumber, blockStateNames, sourceName, videoLabel, type BlockView, type TimelineData } from './block-timeline-model';
import { useBlockDetail, useBlockStream, useDocumentVisible } from './block-timeline-stream';
import { blockLane, blockY, drawTimeline, hitBlocks, timelineHeight, timelineLayout, type TimelineFrame, type TimelinePalette } from './block-timeline-canvas';
import { BlockDemandView } from './block-demand-view';

type View = { follow: boolean; center: number; span: number; offset: number };
type Selection = { ids: string[]; index: number; x: number; y: number; pinned: boolean };
const minimumSpan = 1000, maximumSpan = 7 * 86400000;

export function BlockTimeline({ visible, paused }: { visible: boolean; paused: boolean }) {
  const app = useApp(), documentVisible = useDocumentVisible();
  const container = useRef<HTMLDivElement>(null);
  const [inView, setInView] = useState(false);
  const [expanded, setExpanded] = useState(false);
  useEffect(() => {
    // A resize/scroll can enqueue multiple transitions for this same element.
    // Apply the latest entry, including when a long panel crosses the viewport.
    const observer = new IntersectionObserver(entries => setInView(entries[entries.length - 1].isIntersecting));
    observer.observe(container.current!);
    return () => observer.disconnect();
  }, []);
  const enabled = expanded && visible && inView && documentVisible && !paused && !app.stopped;
  const { data, connection } = useBlockStream(enabled, app.revision);
  const active = enabled && connection === 'live';
  return <div ref={container}><Panel id="block-demands" title="块需求时间轴" action={<Button appearance={expanded ? 'subtle' : 'secondary'} aria-expanded={expanded} aria-controls="block-timeline-content" onClick={() => setExpanded(value => !value)}>{expanded ? '收起时间轴' : '展开时间轴'}</Button>}>
    <Caption1 className="hint">沿时间查看块的需求、下载与本地保护。</Caption1>
    {expanded && <div id="block-timeline-content" className="block-timeline-content">
      <div className="timeline-status"><Badge appearance="tint" color={active ? 'success' : 'informative'}>{!enabled ? '展示已暂停' : connection === 'live' ? '事件已连接' : connection === 'error' ? '正在重新连接' : connection === 'offline' ? '引擎未运行' : '正在连接'}</Badge><Caption1>{data ? `需求占用 ${data.used.toLocaleString()} / ${data.capacity || 4096} · 执行 ${data.active} / 额度 ${data.open}` : '等待当前状态'}</Caption1></div>
      {connection === 'error' && enabled && <Feedback error>事件连接中断，保留最后状态，恢复后自动同步。</Feedback>}
      {!data && enabled && connection === 'connecting' && <Spinner size="tiny" label="正在读取块需求" />}
      {data && <TimelineViewport data={data} active={active} />}
    </div>}
  </Panel></div>;
}

function readPalette(element: HTMLElement): TimelinePalette {
  const css = getComputedStyle(element);
  const color = (name: string) => css.getPropertyValue('--timeline-' + name).trim();
  const videos = Array.from({ length: 8 }, (_, i) => color('video-' + i));
  const videoEdges = videos.map(value => /^#[\da-f]{6}$/i.test(value)
    ? '#' + value.slice(1).match(/../g)!.map(channel => Math.round(parseInt(channel, 16) * .78).toString(16).padStart(2, '0')).join('')
    : value); // System colors retain their contrast in forced-colors mode.
  return { background: color('background'), foreground: color('foreground'), muted: color('muted'), grid: color('grid'), blue: color('blue'), blueWash: color('blue-wash'), green: color('green'), amber: color('amber'), amberWash: color('amber-wash'), selected: color('selected'), onAccent: color('on-accent'), videos, videoEdges };
}

function TimelineViewport({ data, active }: { data: TimelineData; active: boolean }) {
  const root = useRef<HTMLDivElement>(null), canvas = useRef<HTMLCanvasElement>(null);
  const surface = useRef<HTMLDivElement>(null), pointerAt = useRef({ x: 0, y: 0 });
  const [width, setWidth] = useState(700), [untimedOffset, setUntimedOffset] = useState(0);
  const [selection, setSelection] = useState<Selection>();
  const [canvasInView, setCanvasInView] = useState(false);
  const [blockSize, setBlockSize] = useState(18), [layers, setLayers] = useState(false);
  const demandHover = useRef<{ ids: string[]; target: HTMLCanvasElement } | undefined>(undefined);
  const selectionTarget = useRef<HTMLCanvasElement | null>(null);
  const defaultSpan = Math.max(120000, data.prepareAheadMS * 2 || 1000000);
  const [view, setView] = useState<View>({ follow: true, center: data.at || Date.now(), span: defaultSpan, offset: 0 });
  const [reducedMotion, setReducedMotion] = useState(() => matchMedia('(prefers-reduced-motion: reduce)').matches);
  const [palette, setPalette] = useState<TimelinePalette>();
  const timed = useMemo(() => [...data.blocks.values()].filter(b => b.at).sort((a, b) => a.at - b.at || a.id.localeCompare(b.id)), [data.blocks]);
  const untimed = useMemo(() => [...data.blocks.values()].filter(b => !b.at).sort((a, b) => a.resource.localeCompare(b.resource) || a.block - b.block), [data.blocks]);
  const untimedRows = useMemo(() => layers ? [0, 1, 2].map(row => untimed.filter(b => blockLane(b) === row)) : [untimed], [untimed, layers]);
  const untimedCount = Math.max(...untimedRows.map(row => row.length));
  const latest = useRef({ data, active, view, timed, untimed, untimedRows, untimedOffset, selection, width, palette, reducedMotion, blockSize, layers });
  latest.current = { data, active, view, timed, untimed, untimedRows, untimedOffset, selection, width, palette, reducedMotion, blockSize, layers };
  const frame = useRef<TimelineFrame | undefined>(undefined), frozenNow = useRef(data.at || Date.now());
  const animation = useRef(0), repaint = useRef<(force?: boolean) => void>(() => {}), interacting = useRef(false);
  const hoverTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const closeTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const controls = useRef({ zoom: (_factor: number) => {}, home: () => {}, pan: (_center: number) => {} });
  const now = () => {
    const current = latest.current;
    if (current.active) frozenNow.current = current.data.at + performance.now() - current.data.receivedAt;
    return frozenNow.current;
  };
  const clearHoverTimers = () => { clearTimeout(hoverTimer.current); clearTimeout(closeTimer.current); };
  const closeSelection = () => { clearHoverTimers(); setSelection(undefined); };
  const scheduleClose = (event: { clientX: number; clientY: number }) => {
    clearHoverTimers();
    pointerAt.current = { x: event.clientX, y: event.clientY };
    if (!latest.current.selection?.pinned) closeTimer.current = setTimeout(() => {
      // Loading source details can reposition the surface under a stationary
      // pointer. Keep it open while either the chosen block or surface is hovered.
      if (surface.current?.matches(':hover')) return;
      const rect = canvas.current?.getBoundingClientRect(), chosen = latest.current.selection;
      if (chosen && demandHover.current?.target.matches(':hover') && demandHover.current.ids.includes(chosen.ids[chosen.index])) return;
      if (rect && chosen && canvas.current?.matches(':hover') && frame.current && hitBlocks(frame.current, pointerAt.current.x - rect.left, pointerAt.current.y - rect.top).some(b => b.id === chosen.ids[chosen.index])) return;
      setSelection(old => old?.pinned ? old : undefined);
    }, 350);
  };
  const selectedID = selection?.ids[selection.index];
  const selectedBlock = selectedID ? data.blocks.get(selectedID) : undefined;

  useEffect(() => {
    // The demand list can remain visible after the main canvas leaves the
    // viewport. Keep its shared stream, but stop painting the offscreen canvas.
    const observer = new IntersectionObserver(entries => setCanvasInView(entries[entries.length - 1].isIntersecting));
    observer.observe(canvas.current!);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    const element = root.current!;
    const measure = () => { setWidth(Math.max(160, element.clientWidth)); setPalette(readPalette(element)); };
    const observer = new ResizeObserver(measure); observer.observe(element); measure();
    const dark = matchMedia('(prefers-color-scheme: dark)'), contrast = matchMedia('(forced-colors: active)'), motion = matchMedia('(prefers-reduced-motion: reduce)');
    const changeMotion = () => setReducedMotion(motion.matches);
    dark.addEventListener('change', measure); contrast.addEventListener('change', measure); motion.addEventListener('change', changeMotion);
    return () => { observer.disconnect(); dark.removeEventListener('change', measure); contrast.removeEventListener('change', measure); motion.removeEventListener('change', changeMotion); clearHoverTimers(); };
  }, []);

  useEffect(() => {
    // Visibility and connection interruptions hide the popover and release its
    // stream. Preserve an explicit pin; the fresh snapshot validates its IDs.
    if (!active) { clearHoverTimers(); setSelection(old => old?.pinned ? old : undefined); }
  }, [active]);
  useEffect(() => {
    setSelection(old => {
      if (!old) return old;
      const ids = old.ids.filter(id => data.blocks.has(id));
      if (ids.length === old.ids.length) return old;
      return ids.length ? { ...old, ids, index: Math.max(0, ids.indexOf(old.ids[old.index])) } : undefined;
    });
  }, [data.blocks]);
  useEffect(() => { setUntimedOffset(value => Math.min(value, Math.max(0, untimedCount * (blockSize + 6) - width + 48))); }, [untimedCount, width, blockSize]);

  useEffect(() => {
    const element = canvas.current!, ctx = element.getContext('2d');
    if (!ctx || !palette) return;
    let disposed = false;
    const paint = (force = false) => {
      if (disposed || !canvasInView && !force) return;
      const c = latest.current, currentTime = now();
      const { height } = timelineLayout(c.layers, !!c.untimed.length), dpr = window.devicePixelRatio || 1;
      if (element.width !== Math.round(c.width * dpr) || element.height !== Math.round(height * dpr)) {
        element.width = Math.round(c.width * dpr); element.height = Math.round(height * dpr);
      }
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      const currentFrame: TimelineFrame = { data: c.data, timed: c.timed, untimed: c.untimed, untimedRows: c.untimedRows, center: c.view.follow ? currentTime + c.view.offset : c.view.center, now: currentTime, span: c.view.span, width: c.width, untimedOffset: c.untimedOffset, selected: c.selection?.ids[c.selection.index], palette: c.palette!, blockSize: c.blockSize, layers: c.layers };
      frame.current = currentFrame;
      drawTimeline(ctx, currentFrame);
    };
    const animate = () => { paint(); animation.current = requestAnimationFrame(animate); };
    repaint.current = paint; paint();
    if (active && canvasInView && !reducedMotion) animation.current = requestAnimationFrame(animate);
    return () => { disposed = true; cancelAnimationFrame(animation.current); repaint.current = () => {}; };
  }, [active, reducedMotion, palette, canvasInView]);
  useEffect(() => { repaint.current(); }, [data, width, view, selection, untimedOffset, blockSize, layers]);

  useEffect(() => {
    if (!active) return;
    const element = canvas.current!, target = select(element);
    const base = scaleLinear().domain([-defaultSpan / 2, defaultSpan / 2]).range([24, width - 24]);
    let anchor = now(), quiet = true;
    const centerOffset = (transform: ReturnType<typeof zoomTransform>) => { const domain = transform.rescaleX(base).domain(); return (domain[0] + domain[1]) / 2; };
    const behavior = zoom<HTMLCanvasElement, unknown>().clickDistance(4).scaleExtent([defaultSpan / maximumSpan, defaultSpan / minimumSpan]).extent([[24, 0], [width - 24, timelineHeight]])
      .filter(event => !event.ctrlKey && !event.button && event.type !== 'wheel')
      .on('start.timeline', () => {
        if (quiet) return;
        const current = latest.current.view;
        anchor = (current.follow ? now() + current.offset : current.center) - centerOffset(zoomTransform(element));
        interacting.current = true; clearHoverTimers();
        if (!latest.current.selection?.pinned) setSelection(undefined);
      })
      .on('zoom.timeline', event => {
        if (quiet) return;
        const domain = event.transform.rescaleX(base).domain();
        const next = { follow: false, center: anchor + (domain[0] + domain[1]) / 2, span: domain[1] - domain[0], offset: 0 };
        latest.current.view = next; setView(next); repaint.current();
      }).on('end.timeline', () => { interacting.current = false; });
    const synchronize = (next: View) => {
      quiet = true; anchor = next.center;
      const k = defaultSpan / next.span;
      target.call(behavior.transform, zoomIdentity.translate(width / 2 * (1 - k), 0).scale(k));
      quiet = false; latest.current.view = next; setView(next); repaint.current();
    };
    const zoomBy = (factor: number) => {
      const current = latest.current.view;
      synchronize({ ...current, center: current.follow ? now() + current.offset : current.center, span: Math.max(minimumSpan, Math.min(maximumSpan, current.span / factor)) });
    };
    target.call(behavior); target.on('dblclick.zoom', null).on('wheel.zoom', null);
    // Wheel gestures preserve the view mode and scale around its center.
    // Horizontal scrolling in follow mode moves the offset, not the clock.
    target.on('wheel.timeline', (event: WheelEvent) => {
      if (event.ctrlKey || !(event.deltaX || event.deltaY)) return;
      event.preventDefault(); clearHoverTimers();
      if (!latest.current.selection?.pinned) setSelection(undefined);
      if (event.shiftKey || Math.abs(event.deltaX) > Math.abs(event.deltaY)) {
        const current = latest.current.view;
        const pixels = (event.deltaX || event.deltaY) * (event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? width - 48 : 1);
        const shift = pixels * current.span / (width - 48);
        synchronize({ ...current, center: (current.follow ? now() + current.offset : current.center) + shift, offset: current.follow ? current.offset + shift : 0 });
      } else {
        zoomBy(2 ** behavior.wheelDelta().call(element, event, 0, [element]));
      }
    }, { passive: false });
    synchronize({ ...latest.current.view, center: latest.current.view.follow ? now() + latest.current.view.offset : latest.current.view.center });
    controls.current = {
      zoom: zoomBy,
      home: () => synchronize({ follow: true, center: now(), span: latest.current.view.span, offset: 0 }),
      pan: center => synchronize({ ...latest.current.view, follow: false, center, offset: 0 }),
    };
    return () => { target.on('.zoom', null).on('.timeline', null); interacting.current = false; controls.current = { zoom: () => {}, home: () => {}, pan: () => {} }; };
  }, [active, width, defaultSpan]);

  const showBlocks = (blocks: BlockView[], x: number, y: number, pinned: boolean) => {
    if (!blocks.length) { if (!selection?.pinned) closeSelection(); return; }
    clearHoverTimers();
    const ids = blocks.map(b => b.id);
    setSelection(old => old?.pinned && !pinned ? old : { ids, index: pinned && old && ids.includes(old.ids[old.index]) ? ids.indexOf(old.ids[old.index]) : 0, x, y, pinned });
  };
  const hoverBlocks = (blocks: BlockView[], x: number, y: number) => {
    // Keep a stable popup while the hit stack is unchanged. Once open, update
    // immediately when entering/leaving an overlap and select its top block.
    if (selection?.index === 0 && selection.ids.length === blocks.length && blocks.every((block, index) => block.id === selection.ids[index])) return;
    if (selection) { showBlocks(blocks, x, y, false); return; }
    hoverTimer.current = setTimeout(() => showBlocks(blocks, x, y, false), 100);
  };
  const pointer = (event: MouseEvent<HTMLCanvasElement>, pinned = false) => {
    if (!active || interacting.current || selection?.pinned && !pinned) return;
    // A pointer event can arrive immediately after scrolling back, before the
    // intersection callback. Refresh the frame before hit-testing in that gap.
    if (!canvasInView) repaint.current(true);
    if (!frame.current) return;
    const rect = event.currentTarget.getBoundingClientRect(), x = event.clientX - rect.left, y = event.clientY - rect.top;
    const blocks = hitBlocks(frame.current, x, y);
    selectionTarget.current = event.currentTarget;
    clearHoverTimers();
    if (pinned) { showBlocks(blocks, event.clientX, event.clientY, true); return; }
    if (!blocks.length) { scheduleClose(event); return; }
    hoverBlocks(blocks, event.clientX, event.clientY);
  };
  const navigate = (event: KeyboardEvent<HTMLCanvasElement>) => {
    if (!active) return;
    if (event.key === 'Escape') { closeSelection(); return; }
    if (event.key === '+' || event.key === '=') { event.preventDefault(); controls.current.zoom(1.5); return; }
    if (event.key === '-') { event.preventDefault(); controls.current.zoom(1 / 1.5); return; }
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End', 'Enter'].includes(event.key)) return;
    event.preventDefault();
    const blocks = [...timed, ...untimed]; if (!blocks.length) return;
    const index = blocks.findIndex(b => b.id === selectedID);
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? blocks.length - 1 : Math.max(0, Math.min(blocks.length - 1, index + (event.key === 'ArrowLeft' ? -1 : event.key === 'Enter' ? 0 : 1)));
    const block = blocks[next];
    if (block.at) {
      const visibleFrame = frame.current;
      if (!visibleFrame || Math.abs(block.at - visibleFrame.center) > view.span * .4) controls.current.pan(block.at);
    } else { setUntimedOffset(Math.max(0, untimedRows[layers ? blockLane(block) : 0].findIndex(b => b.id === block.id) * (blockSize + 6) - width / 2)); }
    const rect = canvas.current!.getBoundingClientRect();
    selectionTarget.current = canvas.current;
    const y = block.at ? blockY(block, layers, blockSize) : timelineLayout(layers, true).untimedY + (layers ? blockLane(block) * 65 : 0);
    showBlocks([block], rect.left + width / 2, rect.top + y + blockSize / 2, true);
  };
  const baseNow = data.at || Date.now();
  const rangeMin = Math.min(baseNow - Math.max(data.downloadGraceMS * 2, view.span / 2), timed[0]?.at ?? baseNow);
  const rangeMax = Math.max(baseNow + Math.max(data.prepareAheadMS, view.span / 2), timed.at(-1)?.at ?? baseNow);
  const center = view.follow ? baseNow + view.offset : view.center;
  const virtualTarget = useMemo(() => ({ getBoundingClientRect: () => new DOMRect(selection?.x || 0, selection?.y || 0, 1, 1) }), [selection?.x, selection?.y]);
  const summary = useMemo(() => {
    const counts = { waiting: 0, downloading: 0, available: 0, protected: 0, stopping: 0 };
    for (const block of data.blocks.values()) counts[block.state]++;
    return counts;
  }, [data.blocks]);

  return <div ref={root} className="block-timeline" data-follow={view.follow} data-span={Math.round(view.span)} data-block-size={blockSize} data-layers={layers ? 3 : 1}>
    <div className="timeline-toolbar"><div className="timeline-window-keys"><span className="timeline-window-key grace">延续 {data.downloadGraceMS / 1000} 秒</span><span className="timeline-window-key ahead">提前 {data.prepareAheadMS / 1000} 秒</span></div><div className="timeline-controls">
      <Button size="small" icon={<Subtract20Regular />} aria-label="缩小时间轴" disabled={!active || view.span >= maximumSpan} onClick={() => controls.current.zoom(1 / 1.5)} />
      <Button size="small" icon={<Add20Regular />} aria-label="放大时间轴" disabled={!active || view.span <= minimumSpan} onClick={() => controls.current.zoom(1.5)} />
      <Button size="small" icon={<ArrowSync20Regular />} appearance={view.follow && !view.offset ? 'subtle' : 'primary'} disabled={!active} onClick={() => controls.current.home()}>{view.follow && !view.offset ? '跟随现在' : '回到现在'}</Button>
    </div></div>
    <div className="timeline-layout-controls"><Switch label="按状态分三层" checked={layers} onChange={(_, value) => setLayers(value.checked)} /><label className="timeline-size-control"><Caption1>块大小</Caption1><Slider aria-label="块大小" min={4} max={36} step={1} value={blockSize} onChange={(_, value) => setBlockSize(value.value)} /><Caption1>{blockSize} px</Caption1></label></div>
    <canvas ref={canvas} className="timeline-canvas" style={{ height: timelineLayout(layers, !!untimed.length).height }} tabIndex={0} role="group" aria-label={`块需求时间轴，${data.blocks.size} 个块，${layers ? '三层' : '单层'}。左右键选择块，Home 和 End 跳到首尾，加减号缩放，Escape 关闭详情。`} onPointerMove={event => pointer(event)} onPointerLeave={scheduleClose} onClick={event => pointer(event, true)} onKeyDown={navigate} />
    {untimed.length > 0 && <Slider className="timeline-untimed-scroll" aria-label="无期限块横向浏览" min={0} max={Math.max(1, untimedCount * (blockSize + 6) - width + 48)} value={untimedOffset} disabled={!active || untimedCount * (blockSize + 6) <= width - 48} onChange={(_, value) => setUntimedOffset(value.value)} />}
    <Slider className="timeline-scroll" aria-label="时间轴横向浏览" min={rangeMin} max={rangeMax} step={1000} value={Math.min(rangeMax, Math.max(rangeMin, center))} disabled={!active} onChange={(_, value) => controls.current.pan(value.value)} />
    <div className="timeline-legend">{(Object.entries(summary) as [keyof typeof summary, number][]).filter(([state, count]) => state !== 'stopping' || count > 0).map(([state, count]) => <span key={state}><i className={'timeline-swatch ' + state} aria-hidden="true" />{blockStateNames[state]} <strong>{count}</strong></span>)}<span><i className="timeline-swatch blocking" aria-hidden="true" />读取急需</span></div>
    <Caption1 as="p" className="timeline-help">颜色区分视频，形态区分状态 · 纵向滚轮缩放 · 横向滚轮平移 · 拖动或滑条浏览 · 悬浮查看来源 · 点击固定详情{reducedMotion && ' · 已减少动态效果'}</Caption1>
    {palette && <BlockDemandView data={data} size={blockSize} palette={palette} active={active} selected={selectedID} onLeave={scheduleClose} onPick={(blocks, x, y, pinned, target) => {
      demandHover.current = { ids: blocks.map(b => b.id), target };
      if (!active || selection?.pinned && !pinned) return;
      clearHoverTimers();
      selectionTarget.current = target;
      if (!blocks.length) { if (pinned) closeSelection(); else scheduleClose({ clientX: x, clientY: y }); return; }
      if (pinned) { showBlocks(blocks, x, y, true); return; }
      hoverBlocks(blocks, x, y);
    }} />}
    <span className="timeline-selection-status" role="status" aria-atomic="true">{selectedBlock && `${videoLabel(selectedBlock)}，块 ${blockNumber(selectedBlock)}，${blockStateNames[selectedBlock.state]}，need-by ${selectedBlock.needBy ? absoluteTime(selectedBlock.needBy) : '无期限'}。`}</span>
    <Popover open={!!selectedBlock && active} onOpenChange={(event, value) => {
      // A pinned selection survives toolbar clicks (including pause). Escape
      // and the close button still dismiss it explicitly.
      if (!value.open && (!selection?.pinned || event.type === 'keydown' && 'key' in event && event.key === 'Escape')) closeSelection();
    }} positioning={{ target: virtualTarget, position: 'above', align: 'center', offset: 16 }} withArrow unstable_disableAutoFocus>
      <PopoverSurface ref={surface} className="timeline-popover" aria-label="块需求详情" onPointerEnter={clearHoverTimers} onPointerLeave={scheduleClose}>
        {selection && selectedID && <><div className="timeline-detail-toolbar"><Text weight="semibold">块 {selectedBlock ? blockNumber(selectedBlock) : '已退出'}</Text><div>
          {!selection.pinned && <Button size="small" appearance="subtle" icon={<Pin20Regular />} aria-label="固定块详情" onClick={() => setSelection({ ...selection, pinned: true })} />}
          <Button size="small" appearance="subtle" icon={<Dismiss20Regular />} aria-label="关闭块详情" onClick={() => { closeSelection(); (selectionTarget.current?.isConnected ? selectionTarget.current : canvas.current)?.focus(); }} /></div></div>
          {selection.ids.length > 1 && <div className="timeline-overlap"><Button size="small" icon={<ChevronLeft20Regular />} aria-label="上一个重叠块" onClick={() => setSelection({ ...selection, index: (selection.index + selection.ids.length - 1) % selection.ids.length, pinned: true })} /><span>重叠 {selection.ids.length} 块 · {selection.index + 1} / {selection.ids.length}</span><Button size="small" icon={<ChevronRight20Regular />} aria-label="下一个重叠块" onClick={() => setSelection({ ...selection, index: (selection.index + 1) % selection.ids.length, pinned: true })} /></div>}
          <BlockProperties key={selectedID} id={selectedID} block={selectedBlock} active={active && !!selectedBlock} />
        </>}
      </PopoverSurface>
    </Popover>
  </div>;
}

function BlockProperties({ id, block, active }: { id: string; block?: BlockView; active: boolean }) {
  const { detail, error } = useBlockDetail(id, active);
  const b = detail?.block || block;
  if (!b) return <p>这个块已退出当前视图。</p>;
  return <div className="timeline-properties">
    <p className="timeline-block-title">{videoLabel(b)} <Badge appearance="tint" color={b.state === 'available' ? 'success' : 'informative'}>{blockStateNames[b.state]}</Badge></p>
    <dl className="timeline-properties-grid">
      <dt>视频内编号</dt><dd>{blockNumber(b)}{b.totalBlocks <= 0 && '（总数未知）'}{b.songID && ` · 歌曲 ${b.songID}`}</dd>
      <dt>need-by</dt><dd>{b.needBy ? absoluteTime(b.needBy) : '无有限期限'}</dd>
      <dt>下载截止</dt><dd>{absoluteTime(b.deadline)}</dd>
      <dt>下载资格</dt><dd>{!b.inUse ? '使用需求已结束' : b.readers ? '读取急需' : b.windowClosed ? '来源尚未开放窗口' : b.eligible ? '可参与调度' : '等待窗口或有效需求'}</dd>
      <dt>读取等待</dt><dd>{b.readers ? `${b.readers} 个读取者` : '无'}</dd>
      <dt>本地保护</dt><dd>{b.protected ? b.protectionUntil && !b.inUse ? `保护至 ${absoluteTime(b.protectionUntil)}` : '保护中' : b.protectionStart ? `开始于 ${absoluteTime(b.protectionStart)}` : '未保护'}</dd>
      <dt>字节范围</dt><dd>{(b.block * 1048576).toLocaleString()} – {b.bytes ? (b.block * 1048576 + b.bytes - 1).toLocaleString() : '末端待定'}</dd>
    </dl>
    {b.timeBasis === 'ended' && <Caption1 as="p">按需求结束时间定位；此前的 need-by 未记录。</Caption1>}
    {b.timeBasis === 'last-need-by' && <Caption1 as="p">沿用最后一次 need-by 的位置；当前已无需求。</Caption1>}
    {b.opportunistic && b.inUse && <Caption1 as="p">同时允许机会下载。</Caption1>}
    <Text weight="semibold" className="timeline-source-heading">需求来源 · {b.sources}</Text>
    {error ? <Feedback error>来源连接中断，正在重连。</Feedback> : detail === undefined ? <Spinner size="tiny" label="读取来源" /> : detail === null ? <p>需求已结束。</p> : detail.sources.length ? <ul className="timeline-sources">{detail.sources.map(source => <li key={source.id}><strong>{sourceName(source)}</strong>{(source.title || source.songID) && <span>{source.title || `歌曲 ${source.songID}`}</span>}<span>need-by：{source.needBy ? absoluteTime(source.needBy) : '无期限'}{source.windowClosed ? ' · 尚未入窗' : ''}</span>{source.expiresAt > 0 && <span>交接截止：{absoluteTime(source.expiresAt)}</span>}</li>)}</ul> : <p className="hint">已无使用需求，本地数据仍在延续保护期。</p>}
    <Caption1 as="p" className="timeline-resource">资源 {b.resource}</Caption1>
  </div>;
}
