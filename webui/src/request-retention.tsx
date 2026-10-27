import { Field, Input, Radio, RadioGroup } from '@fluentui/react-components';
import type { Settings } from './types';

type RetentionDraft = Pick<Settings, 'requestRetentionMode'> & { requestRetentionDays: string };

export function RequestRetentionFields({ mode, value, disabled, onChange }: {
  mode: Settings['requestRetentionMode']; value: string; disabled: boolean; onChange: (patch: Partial<RetentionDraft>) => void;
}) {
  const limited = mode === 'limited';
  return <div className="form-grid">
    <Field label="是否保留历史请求明细"><RadioGroup layout="horizontal" style={{flexWrap:'wrap'}} value={mode} disabled={disabled}
      onChange={(_, v) => {
        if (v.value !== 'none' && v.value !== 'limited' && v.value !== 'unlimited') return;
        const days = Number(value);
        onChange({requestRetentionMode:v.value,
          ...(!value.trim() || !Number.isInteger(days) || days < 0 || days > 36500 || v.value === 'limited' && days === 0
            ? {requestRetentionDays:v.value === 'limited' ? '30' : '0'} : {})});
      }}>
      <Radio value="none" label="不保留" /><Radio value="limited" label="保留指定天数" /><Radio value="unlimited" label="永久保留" />
    </RadioGroup></Field>
    <Field label="请求明细保留天数" required={limited}>
      <Input type="number" min="1" max="36500" step="1" required={limited} disabled={disabled || !limited}
        value={limited ? value : mode === 'none' ? '0' : ''} onChange={(_, v) => onChange({requestRetentionDays:v.value})} />
    </Field>
    <span className="secondary">{mode === 'none' ? '重启后清理已有请求明细，并停止保留新明细；累计统计不受影响。' : '用于「历史」页面的请求明细，不影响累计统计和日志。'}</span>
  </div>;
}
