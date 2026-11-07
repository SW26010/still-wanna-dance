import { Field, Link, Radio, RadioGroup, SpinButton, Tooltip } from '@fluentui/react-components';
import { useId, useRef } from 'react';
import { flushSync } from 'react-dom';
import { date, gib, type Settings } from './types';
import {ValidationField} from './field-validation';

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

export type CapacityDraft = Pick<Settings, 'videoStorageMode' | 'aggressiveBlockReclamation'> & { videoBudgetGiB: string };
export function capacityPayload(d: CapacityDraft) {
  return {videoStorageMode: d.videoStorageMode, aggressiveBlockReclamation: d.aggressiveBlockReclamation, videoBudgetBytes: Math.round(Number(d.videoBudgetGiB) * 1073741824)};
}
export function CacheCapacityFields({ mode, value, disabled, onChange, anchor }: {
  mode: CapacityDraft['videoStorageMode']; value: string; disabled: boolean; onChange: (patch: Partial<CapacityDraft>) => void; anchor?: string;
}) {
  const limited = mode === 'limited';
  const estimate = limited ? estimatedCoverage(value) : undefined;
  const estimateId = useId();
  const budgetInput = useRef<HTMLInputElement>(null);
  // Fluent commits typed text and steps in the same mouse event. Flush the blur
  // first so its step starts from the current draft, including live edits.
  const commitBeforeStep = () => flushSync(() => budgetInput.current?.blur());
  return <div id={anchor} tabIndex={-1} className="form-grid">
    <Field label="保留下载的视频" hint={estimate === undefined ? undefined : {id:estimateId,children:`预计请求命中率：约 ${estimate}`}}><RadioGroup layout="horizontal" className="retention-options" value={mode} disabled={disabled}
      onChange={(_, v) => {
        if (v.value !== 'none' && v.value !== 'limited' && v.value !== 'unlimited') return;
        onChange({videoStorageMode: v.value,
          ...(!value.trim() || !Number.isFinite(Number(value)) || Number(value) < 0 || Number(value) > 8388607 || v.value === 'limited' && Number(value) === 0 ? {videoBudgetGiB: v.value === 'none' ? '0' : '40'} : {})});
      }}>
      <Radio value="none" label="不保留" />
      <div className="retention-option"><Radio value="limited" label="限制为" />
      <ValidationField className="retention-input budget-field" name="videoBudgetBytes" label="视频存储预算（GiB）" required={limited}>
        <div className="budget-input"><SpinButton ref={budgetInput} name="videoBudgetBytes" min={0.01} max={8388607} step={1} precision={2} required={limited} disabled={disabled || !limited}
          aria-describedby={estimate === undefined ? undefined : estimateId}
          value={mode === 'none' ? 0 : value.trim() && Number.isFinite(Number(value)) ? Number(value) : null}
          displayValue={mode === 'none' ? '0' : value}
          input={{inputMode:'decimal',onChange:e=>onChange({videoBudgetGiB:e.target.value})}}
          incrementButton={{'aria-label':'增加视频预算',onMouseDownCapture:commitBeforeStep}} decrementButton={{'aria-label':'减少视频预算',onMouseDownCapture:commitBeforeStep}}
          onChange={(_, v) => onChange({videoBudgetGiB:v.displayValue ?? String(v.value ?? '')})} /><span className="secondary">GiB</span></div>
      </ValidationField></div>
      <Radio value="unlimited" label="不限制" />
    </RadioGroup></Field>
  </div>;
}
export function CacheReclamationFields({ mode, reclamation, disabled, onChange }: {
  mode: CapacityDraft['videoStorageMode']; reclamation: CapacityDraft['aggressiveBlockReclamation']; disabled: boolean; onChange: (patch: Partial<CapacityDraft>) => void;
}) {
  return <Field hint={<span aria-live="polite">{reclamation === 'auto' ? `不保留视频时开启。当前${mode === 'none' ? '开启' : '关闭'}` : reclamation === 'on' ? '可能导致视频未能及时保存到本地' : '可能导致大量缓存未及时回收'}</span>} label={<Tooltip content="减少缓存占用，可能重复下载" relationship="description"><span tabIndex={0}>激进的缓存回收策略</span></Tooltip>}>
      <RadioGroup layout="horizontal" value={reclamation} disabled={disabled} onChange={(_, v) => {
        if (v.value === 'auto' || v.value === 'on' || v.value === 'off') onChange({aggressiveBlockReclamation: v.value});
      }}>
        <Radio value="auto" label="自动（默认）" /><Radio value="on" label="开启" /><Radio value="off" label="关闭" />
      </RadioGroup>
    </Field>;
}
