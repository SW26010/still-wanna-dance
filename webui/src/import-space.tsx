import { useEffect, useState } from 'react';
import { request } from './api';

type SpaceMode = 'same-volume' | 'cross-volume' | 'unknown';
const descriptions = {
  'same-volume': '预计不额外占用视频空间',
  'cross-volume': '预计需要额外空间',
  unknown: '空间占用待确认',
  pending: '正在判断空间占用…',
  empty: '选择来源后显示空间预估',
} as const;

export function ImportSpaceHint({source, videoDir, unavailable}: {source: string; videoDir: string; unavailable: boolean}) {
  const [result, setResult] = useState<{source: string; videoDir: string; mode: SpaceMode}>();
  useEffect(() => {
    if (!source || unavailable) { setResult(undefined); return; }
    const controller = new AbortController();
    setResult(undefined);
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(15000)]);
    void request<{mode: SpaceMode; videoDir: string}>('import/space?' + new URLSearchParams({source}), undefined, signal)
      .then(value => {
        if (!controller.signal.aborted) setResult({source, videoDir, mode: value.videoDir === videoDir ? value.mode : 'unknown'});
      })
      .catch(() => { if (!controller.signal.aborted) setResult({source, videoDir, mode: 'unknown'}); });
    return () => controller.abort();
  }, [source, videoDir, unavailable]);
  const mode = !source ? 'empty' : unavailable ? 'unknown' : result?.source === source && result.videoDir === videoDir ? result.mode : 'pending';
  return <span aria-live="polite" className="inline secondary" style={{display:'inline-flex'}}>{descriptions[mode]}</span>;
}
