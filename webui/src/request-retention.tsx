import { Radio, RadioGroup } from '@fluentui/react-components';
import { useId } from 'react';
import type { Settings } from './types';
import {ValidationField} from './field-validation';
import {NumberInput} from './number-input';

type RetentionDraft = Pick<Settings, 'requestRetentionMode'> & { requestRetentionDays: string };

export function RequestRetentionFields({ mode, value, disabled, onChange }: {
  mode: Settings['requestRetentionMode']; value: string; disabled: boolean; onChange: (patch: Partial<RetentionDraft>) => void;
}) {
  const repair=!['none','limited','unlimited'].includes(mode)||Number(value)<0||Number(value)>36500;
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
      {repair&&!['none','limited','unlimited'].includes(mode)&&<Radio disabled value={mode} label={'无效值：'+mode}/>}
      <Radio value="none" label="不保留" />
      <div className="retention-option"><Radio value="limited" label="保留指定天数" />
    <ValidationField className="retention-input" name="requestRetentionDays" label="请求明细保留天数" required={limited}>
      <NumberInput name="requestRetentionDays" unit="天" stepLabel="请求明细保留天数" min={limited?1:0} max={36500} required={limited} disabled={disabled || (!limited&&!repair)}
        value={limited||repair ? value : mode === 'none' ? '0' : ''} onChange={value => onChange({requestRetentionDays:value})} />
    </ValidationField></div>
      <Radio value="unlimited" label="永久保留" />
    </RadioGroup>
  </fieldset>;
}
