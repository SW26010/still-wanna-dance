import { Field, Input, Link, Radio, RadioGroup, Tooltip } from '@fluentui/react-components';
import { date, gib, type Settings } from './types';
import { Feedback, Hint } from './ui';

export function StorageBudgetNotice({settings,usedBytes,checkedAt}: {settings:Pick<Settings,'videoStorageMode'|'videoBudgetBytes'|'retainFavorites'>;usedBytes?:number;checkedAt?:number}) {
  const budget=settings.videoStorageMode==='none'?'不保留普通视频':settings.videoStorageMode==='unlimited'?'不限制':`预算 ${gib(settings.videoBudgetBytes)}`;
  return <div className="content-stack">
    <div className="inline secondary"><Tooltip relationship="description" content={<>
      <p>仅统计已保存的完整视频，同一资源只计一次；不含临时文件和数据库，不代表磁盘实际占用。收藏和使用中的视频可能使占用超过预算。</p>
      <p>{usedBytes===undefined?'占用尚未取得，可在曲库概览检查本地视频。':`最后检查时间：${date(checkedAt)}`}</p>
    </>}><span tabIndex={0}>本地视频：已用 {usedBytes===undefined?'未知':gib(usedBytes)} / {budget}</span></Tooltip><Link href="#videoBudgetGiB">前往设置</Link></div>
    {settings.videoStorageMode==='none'&&<Feedback>如需保留普通视频，请调整设置，保存后重启生效。{settings.retainFavorites&&'已开启优先保留收藏视频，收藏视频仍可保留。'}</Feedback>}
  </div>;
}

export type CapacityDraft = Pick<Settings, 'videoStorageMode' | 'aggressiveBlockReclamation'> & { videoBudgetGiB: string };
export function capacityPayload(d: CapacityDraft) {
  return {videoStorageMode: d.videoStorageMode, aggressiveBlockReclamation: d.aggressiveBlockReclamation, videoBudgetBytes: Math.round(Number(d.videoBudgetGiB) * 1073741824)};
}
export function reclamationLabel(s: Pick<Settings, 'videoStorageMode' | 'aggressiveBlockReclamation'>) {
  return s.aggressiveBlockReclamation === 'auto' ? `自动（当前${s.videoStorageMode === 'none' ? '开启' : '关闭'}）` : s.aggressiveBlockReclamation === 'on' ? '开启' : '关闭';
}
export function CacheCapacityFields({ mode, value, disabled, onChange, anchor }: {
  mode: CapacityDraft['videoStorageMode']; value: string; disabled: boolean; onChange: (patch: Partial<CapacityDraft>) => void; anchor?: string;
}) {
  const limited = mode === 'limited';
  return <div id={anchor} tabIndex={-1} className="form-grid">
    <Field label="是否保留下载的视频"><RadioGroup layout="horizontal" value={mode} disabled={disabled}
      onChange={(_, v) => {
        if (v.value !== 'none' && v.value !== 'limited' && v.value !== 'unlimited') return;
        onChange({videoStorageMode: v.value,
          ...(!value.trim() || !Number.isFinite(Number(value)) || Number(value) < 0 || Number(value) > 8388607 || v.value === 'limited' && Number(value) === 0 ? {videoBudgetGiB: v.value === 'none' ? '0' : '40'} : {})});
      }}>
      <Radio value="none" label="不保留" /><Radio value="limited" label="限制为" /><Radio value="unlimited" label="不限制" />
    </RadioGroup></Field>
    <p>{mode==='none'?'普通视频不长期保留；开启“优先保留收藏视频”时，收藏视频仍会保留。':'保存完整视频供下次播放，优先保留常用歌曲。'}</p>
    <Field label="视频存储预算（GiB）" required={limited}>
      <Input type="number" min="0.01" max="8388607" step="0.01" required={limited} disabled={disabled || !limited}
        value={mode === 'none' ? '0' : value} onChange={(_, v) => onChange({videoBudgetGiB: v.value})} />
    </Field>
    <div className="inline secondary">预算用于完整视频<Hint label="空间占用说明">1 GiB 约为 1.07 GB。使用中的视频、临时文件与优先保留的收藏可能使实际占用超过预算。</Hint></div>
  </div>;
}
export function CacheReclamationFields({ mode, reclamation, disabled, onChange }: {
  mode: CapacityDraft['videoStorageMode']; reclamation: CapacityDraft['aggressiveBlockReclamation']; disabled: boolean; onChange: (patch: Partial<CapacityDraft>) => void;
}) {
  return <div className="form-grid">
    <Field label="激进的缓存回收策略" hint="减少缓存占用，可能增加重复下载。">
      <RadioGroup layout="horizontal" value={reclamation} disabled={disabled} onChange={(_, v) => {
        if (v.value === 'auto' || v.value === 'on' || v.value === 'off') onChange({aggressiveBlockReclamation: v.value});
      }}>
        <Radio value="auto" label="自动（默认）" /><Radio value="on" label="开启" /><Radio value="off" label="关闭" />
      </RadioGroup>
    </Field>
    <span className="secondary" aria-live="polite">{reclamation === 'auto' ? reclamationLabel({videoStorageMode:mode,aggressiveBlockReclamation:reclamation})+'：不保留视频时开启。' : '手动选择不随存储模式变化。'}</span>
  </div>;
}
