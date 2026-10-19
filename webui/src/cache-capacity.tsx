import { Field, Input, Radio, RadioGroup } from '@fluentui/react-components';

type Capacity = { cacheUnlimited: boolean; maxCacheGiB: string };
export function CacheCapacityFields({ unlimited, value, disabled, onChange, anchor }: {
  unlimited: boolean; value: string; disabled: boolean; onChange: (patch: Partial<Capacity>) => void; anchor?: string;
}) {
  return <div id={anchor} tabIndex={-1} className="form-grid">
    <Field label="视频存储预算"><RadioGroup value={unlimited ? 'unlimited' : 'limited'} disabled={disabled}
      onChange={(_, v) => onChange({cacheUnlimited: v.value === 'unlimited',
        ...(v.value === 'unlimited' && (!value.trim() || !Number.isFinite(Number(value)) || Number(value) < 0 || Number(value) > 8388607) ? {maxCacheGiB: '40'} : {})})}>
      <Radio value="unlimited" label="不限制" /><Radio value="limited" label="限制为" />
    </RadioGroup></Field>
    <Field label="视频存储预算（GiB）" required={!unlimited} hint="本地视频持久化存储预算，用于跨运行保留完整视频。设为 0 时不持久保留视频，仍可使用视频块缓存。实际磁盘占用可能超过预算。">
      <Input type="number" min="0" max="8388607" step="0.01" required={!unlimited} disabled={disabled || unlimited}
        value={value} onChange={(_, v) => onChange({maxCacheGiB: v.value})} />
    </Field>
  </div>;
}
