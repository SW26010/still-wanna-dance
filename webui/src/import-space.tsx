import { useEffect, useState } from 'react';
import { Tooltip } from '@fluentui/react-components';
import { InfoRegular } from '@fluentui/react-icons';
import { request } from './api';

type SpaceMode = 'same-volume' | 'cross-volume' | 'unknown';
const descriptions = {
  'same-volume': ['预计不额外占用视频空间', '来源与目标在同一存储卷，硬链接共享原视频内容，无需再保存一份，仅增加少量文件记录。以逐文件导入结果为准。'],
  'cross-volume': ['预计需要额外空间', '来源与目标跨存储卷，将复制需要导入的视频，源文件默认保留；已经存在的视频会跳过。实际占用取决于导入结果。'],
  unknown: ['空间占用待确认', '暂时无法确认来源与目标的存储卷。导入时逐文件判断：硬链接不复制内容，跨卷复制需要额外空间。'],
  pending: ['正在判断空间占用…', '正在读取来源与目标目录的存储卷信息，不扫描视频内容。'],
  empty: ['选择来源后显示空间预估', '根据来源与当前导入目标判断是否需要额外保存一份视频，不预先计算具体容量。'],
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
  const [label, detail] = descriptions[mode];
  return <span aria-live="polite"><Tooltip content={detail} relationship="description">
    <span className="inline secondary" style={{display:'inline-flex'}} tabIndex={0}>{label}<InfoRegular aria-hidden="true" /></span>
  </Tooltip></span>;
}
