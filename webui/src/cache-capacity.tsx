import { Field, Input, Radio, RadioGroup } from '@fluentui/react-components';
import type { Settings } from './types';

export type CapacityDraft = Pick<Settings, 'videoStorageMode' | 'aggressiveCacheReclamation'> & { maxCacheGiB: string };
export function capacityPayload(d: CapacityDraft) {
  return {videoStorageMode: d.videoStorageMode, aggressiveCacheReclamation: d.aggressiveCacheReclamation, maxCacheBytes: Math.round(Number(d.maxCacheGiB) * 1073741824)};
}
export function reclamationLabel(s: Pick<Settings, 'videoStorageMode' | 'aggressiveCacheReclamation'>) {
  return s.aggressiveCacheReclamation === 'auto' ? `自动（当前${s.videoStorageMode === 'none' ? '开启' : '关闭'}）` : s.aggressiveCacheReclamation === 'on' ? '开启' : '关闭';
}
export function CacheCapacityFields({ mode, value, disabled, onChange, anchor }: {
  mode: CapacityDraft['videoStorageMode']; value: string; disabled: boolean; onChange: (patch: Partial<CapacityDraft>) => void; anchor?: string;
}) {
  const limited = mode === 'limited';
  return <div id={anchor} tabIndex={-1} className="form-grid">
    <Field label="视频存储预算"><RadioGroup value={mode} disabled={disabled}
      onChange={(_, v) => {
        if (v.value !== 'none' && v.value !== 'limited' && v.value !== 'unlimited') return;
        onChange({videoStorageMode: v.value,
          ...(!value.trim() || !Number.isFinite(Number(value)) || Number(value) < 0 || Number(value) > 8388607 || v.value === 'limited' && Number(value) === 0 ? {maxCacheGiB: v.value === 'none' ? '0' : '40'} : {})});
      }}>
      <Radio value="none" label="不保留" /><Radio value="limited" label="限制为" /><Radio value="unlimited" label="不限制" />
    </RadioGroup></Field>
    <Field label="视频存储预算（GiB）" required={limited} hint="本地视频持久化存储预算，用于跨运行保留完整视频。选择「不保留」时预算为 0，不保留持久化视频文件，仍可使用视频块缓存。已有本地视频在保护结束后回收。实际磁盘占用可能超过预算。">
      <Input type="number" min="0.01" max="8388607" step="0.01" required={limited} disabled={disabled || !limited}
        value={mode === 'none' ? '0' : value} onChange={(_, v) => onChange({maxCacheGiB: v.value})} />
    </Field>
  </div>;
}
export function CacheReclamationFields({ mode, reclamation, disabled, onChange }: {
  mode: CapacityDraft['videoStorageMode']; reclamation: CapacityDraft['aggressiveCacheReclamation']; disabled: boolean; onChange: (patch: Partial<CapacityDraft>) => void;
}) {
  return <div className="form-grid">
    <Field label="激进的缓存回收策略" hint="通常可以最小化硬盘和内存占用，但可能增加反复下载消耗的流量，若希望边下边存，请关闭此选项。">
      <RadioGroup value={reclamation} disabled={disabled} onChange={(_, v) => {
        if (v.value === 'auto' || v.value === 'on' || v.value === 'off') onChange({aggressiveCacheReclamation: v.value});
      }}>
        <Radio value="auto" label="自动（默认）" /><Radio value="on" label="开启" /><Radio value="off" label="关闭" />
      </RadioGroup>
    </Field>
    <p className="hint" aria-live="polite">{reclamation === 'auto' ? `${reclamationLabel({videoStorageMode: mode, aggressiveCacheReclamation: reclamation})}：选择「不保留」时开启，选择「限制为」或「不限制」时关闭。` : '手动选择不随视频存储预算变化。'}关闭后仍按视频存储预算保留完整视频；选择「不保留」时，视频块缓存仅在本次运行内复用。</p>
  </div>;
}
