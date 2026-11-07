import { Input, Radio, RadioGroup } from '@fluentui/react-components';
import { useId } from 'react';
import type { Settings } from './types';
import {ValidationField} from './field-validation';

type RetentionDraft = Pick<Settings, 'requestRetentionMode'> & { requestRetentionDays: string };

export function RequestRetentionFields({ mode, value, disabled, onChange }: {
  mode: Settings['requestRetentionMode']; value: string; disabled: boolean; onChange: (patch: Partial<RetentionDraft>) => void;
}) {
  const limited = mode === 'limited';
  const labelId = useId();
  return <fieldset><legend id={labelId}>历史请求明细</legend>
    <RadioGroup aria-labelledby={labelId} layout="horizontal" className="retention-options" value={mode} disabled={disabled}
      onChange={(_, v) => {
        if (v.value !== 'none' && v.value !== 'limited' && v.value !== 'unlimited') return;
        const days = Number(value);
        onChange({requestRetentionMode:v.value,
          ...(!value.trim() || !Number.isInteger(days) || days < 0 || days > 36500 || v.value === 'limited' && days === 0
            ? {requestRetentionDays:v.value === 'limited' ? '30' : '0'} : {})});
      }}>
      <Radio value="none" label="不保留" />
      <div className="retention-option"><Radio value="limited" label="保留指定天数" />
    <ValidationField className="retention-input" name="requestRetentionDays" label="请求明细保留天数" required={limited}>
      <Input name="requestRetentionDays" contentAfter="天" type="number" min="1" max="36500" step="1" required={limited} disabled={disabled || !limited}
        value={limited ? value : mode === 'none' ? '0' : ''} onChange={(_, v) => onChange({requestRetentionDays:v.value})} />
    </ValidationField></div>
      <Radio value="unlimited" label="永久保留" />
    </RadioGroup>
  </fieldset>;
}
