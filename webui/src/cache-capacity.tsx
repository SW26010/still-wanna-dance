import { Field, Input, Radio, RadioGroup } from '@fluentui/react-components';
import type { Settings } from './types';

export type CapacityDraft = Pick<Settings, 'videoStorageMode'> & { maxCacheGiB: string };
export function capacityPayload(d: CapacityDraft) {
  return {videoStorageMode: d.videoStorageMode, maxCacheBytes: Math.round(Number(d.maxCacheGiB) * 1073741824)};
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
