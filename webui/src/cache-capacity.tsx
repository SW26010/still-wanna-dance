import { Field, Input, Radio, RadioGroup } from '@fluentui/react-components';

type Capacity = { cacheUnlimited: boolean; maxCacheGiB: string };
export function CacheCapacityFields({ unlimited, value, disabled, onChange, anchor }: {
  unlimited: boolean; value: string; disabled: boolean; onChange: (patch: Partial<Capacity>) => void; anchor?: string;
}) {
  return <div id={anchor} tabIndex={-1} className="form-grid">
    <Field label="缓存空间上限"><RadioGroup value={unlimited ? 'unlimited' : 'limited'} disabled={disabled}
      onChange={(_, v) => onChange({cacheUnlimited: v.value === 'unlimited',
        ...(v.value === 'unlimited' && (!value.trim() || !Number.isFinite(Number(value)) || Number(value) < 0 || Number(value) > 8388607) ? {maxCacheGiB: '40'} : {})})}>
      <Radio value="unlimited" label="不限制" /><Radio value="limited" label="限制为" />
    </RadioGroup></Field>
    <Field label="缓存容量（GiB）" required={!unlimited} hint="0 表示不持久保留缓存。播放、队列和交接保护中的视频仍可复用，保护解除后自动清理；已有缓存也按此规则回收。使用中的视频和临时文件可能使占用暂时超限。">
      <Input type="number" min="0" max="8388607" step="0.01" required={!unlimited} disabled={disabled || unlimited}
        value={value} onChange={(_, v) => onChange({maxCacheGiB: v.value})} />
    </Field>
  </div>;
}
