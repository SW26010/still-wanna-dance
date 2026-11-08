import { Field, Link, Radio, RadioGroup, Text, Tooltip } from '@fluentui/react-components';
import { useId } from 'react';
import { date, gib, type Settings } from './types';
import {ValidationField} from './field-validation';
import {NumberInput} from './number-input';

declare const __VIDEO_COVERAGE_REFERENCE__: {maxBudgetGiB: number; budgets: number[]; values: number[]};
// A 0.01 GiB budget is stored as a whole number of bytes and can round down.
const minimumBudgetGiB = Math.round(0.01 * 1073741824) / 1073741824;

function estimatedCoverage(value: string) {
  const budget = Number(value);
  if (!value.trim() || !Number.isFinite(budget) || budget < minimumBudgetGiB || budget > 8388607) return undefined;
  const {maxBudgetGiB, budgets, values} = __VIDEO_COVERAGE_REFERENCE__;
  const referenceBudget = Math.min(budget, maxBudgetGiB);
  const upper = budgets.findIndex(value => value >= referenceBudget), lower = Math.max(upper - 1, 0);
  const fraction = upper === lower ? 0 : (referenceBudget - budgets[lower]) / (budgets[upper] - budgets[lower]);
  const coverage = values[lower] + (values[upper] - values[lower]) * fraction;
  return `${Math.round(coverage * 100)}%${budget > maxBudgetGiB ? '+' : ''}`;
}

export function StorageBudgetNotice({settings,usedBytes,checkedAt}: {settings:Pick<Settings,'videoStorageMode'|'videoBudgetBytes'>;usedBytes?:number;checkedAt?:number}) {
  const budget=settings.videoStorageMode==='none'?'不保留普通视频':settings.videoStorageMode==='unlimited'?'不限制':`预算 ${gib(settings.videoBudgetBytes)}`;
  return <div className="content-stack">
    <div className="inline secondary"><Tooltip relationship="description" content={<>
      <p>仅统计已保存的完整视频。</p>
      <p>{usedBytes===undefined?'占用尚未取得，可在曲库概览检查本地视频。':`最后检查时间：${date(checkedAt)}`}</p>
    </>}><span tabIndex={0}>本地视频：已用 {usedBytes===undefined?'未知':gib(usedBytes)} / {budget}</span></Tooltip><Link href="#videoBudgetGiB">前往设置</Link></div>
  </div>;
}

export type CapacityDraft = Pick<Settings, 'videoStorageMode'> & { videoBudgetGiB: string };
export function capacityPayload(d: CapacityDraft) {
  return {videoStorageMode: d.videoStorageMode, videoBudgetBytes: Math.round(Number(d.videoBudgetGiB) * 1073741824)};
}
export function CacheCapacityFields({ mode, value, disabled, onChange, anchor }: {
  mode: CapacityDraft['videoStorageMode']; value: string; disabled: boolean; onChange: (patch: Partial<CapacityDraft>) => void; anchor?: string;
}) {
  const repair=!['none','limited','unlimited'].includes(mode)||Number(value)<0;
  const limited = mode === 'limited';
  const estimate = limited ? estimatedCoverage(value) : undefined;
  const estimateId = useId();
  const labelId = useId();
  return <div id={anchor} tabIndex={-1} className="form-grid">
    <Field label={{className:'inline',children:<><span id={labelId}>保留下载的视频</span>{estimate !== undefined && <Text id={estimateId} size={200} weight="regular" className="secondary">预计请求命中率：约 {estimate}</Text>}</>}}><RadioGroup aria-labelledby={labelId} aria-describedby={estimate === undefined ? undefined : estimateId} layout="horizontal" className="retention-options" value={mode} disabled={disabled}
      onChange={(_, v) => {
        if (v.value !== 'none' && v.value !== 'limited' && v.value !== 'unlimited') return;
        onChange({videoStorageMode: v.value,
          ...(!value.trim() || !Number.isFinite(Number(value)) || Number(value) < 0 || Number(value) > 8388607 || v.value === 'limited' && Number(value) === 0 ? {videoBudgetGiB: v.value === 'none' ? '0' : '40'} : {})});
      }}>
      {repair&&!['none','limited','unlimited'].includes(mode)&&<Radio disabled value={mode} label={'无效值：'+mode}/>}
      <Radio value="none" label="不保留" />
      <div className="retention-option"><Radio value="limited" label="限制为" />
      <ValidationField className="retention-input" name="videoBudgetBytes" label="视频存储预算（GiB）" required={limited}>
        <NumberInput name="videoBudgetBytes" min={limited?0.01:0} max={8388607} precision={2} unit="GiB" stepLabel="视频预算" required={limited} disabled={disabled || (!limited&&!repair)}
          aria-describedby={estimate === undefined ? undefined : estimateId}
          value={mode === 'none'&&!repair ? '0' : value}
          onChange={value => onChange({videoBudgetGiB:value})} />
      </ValidationField></div>
      <Radio value="unlimited" label="不限制" />
    </RadioGroup></Field>
  </div>;
}
