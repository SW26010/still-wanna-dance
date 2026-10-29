import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Button, Field, Input, type LabelProps, type SlotRenderFunction } from '@fluentui/react-components';
import { request } from './api';
import { Feedback } from './ui';

type PathPurpose = 'database' | 'vrcx-database' | 'video-directory' | 'import-directory' | 'log-directory';

export function usePathPicker(onPending?: (pending: boolean) => void) {
  const [pending, setPending] = useState(false), [error, setError] = useState('');
  const controller = useRef<AbortController | undefined>(undefined), notify = useRef(onPending);
  const mounted = useRef(true);
  notify.current = onPending;
  useEffect(() => {
    mounted.current = true;
    const cancel = () => controller.current?.abort();
    addEventListener('pagehide', cancel);
    return () => { mounted.current = false; cancel(); removeEventListener('pagehide', cancel); notify.current?.(false); };
  }, []);
  async function choose(purpose: PathPurpose, path: string, apply: (path: string, signal: AbortSignal) => void | Promise<void>) {
    if (controller.current) return;
    const abort = new AbortController(); controller.current = abort;
    setPending(true); notify.current?.(true); setError('');
    try {
      const result = await request<{path: string; canceled: boolean}>('path/select', {purpose, path}, abort.signal);
      if (!abort.signal.aborted && !result.canceled) await apply(result.path, abort.signal);
    } catch (e) { if (!abort.signal.aborted) setError(String(e)); }
    finally {
      controller.current = undefined;
      if (mounted.current) { setPending(false); notify.current?.(false); }
    }
  }
  return {pending, error, choose};
}

// Keep the selected path readable/copyable; all edits go through the OS dialog.
export function PathField({label, labelSuffix, value, defaultValue = '', isDefault = samePath(value, defaultValue), hint, disabled, onChoose, onReset}: {
  label: string; labelSuffix?: ReactNode; value: string; defaultValue?: string; isDefault?: boolean; hint?: string; disabled: boolean;
  onChoose: () => void; onReset: () => void;
}) {
  const renderLabel: SlotRenderFunction<LabelProps> = (Label, props) => <div className="inline" style={{alignItems:'baseline'}}><Label {...props}>{label}</Label>{labelSuffix}</div>;
  return <Field label={labelSuffix ? {children: renderLabel} : label} hint={hint}><div className="path-field" data-default={isDefault}>
    <Input readOnly disabled={disabled} value={value || defaultValue} placeholder="点击选择…" aria-haspopup="dialog"
      onClick={() => { if (!disabled) onChoose(); }}
      onKeyDown={event => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); if (!disabled && !event.repeat) onChoose(); } }}/>
    <Button type="button" disabled={disabled || isDefault} aria-label={`${label}：恢复默认`} onClick={onReset}>恢复默认</Button>
  </div></Field>;
}

function samePath(value: string, defaultValue: string) {
  const normalize = (path: string) => path.replaceAll('/', '\\').replace(/\\+$/, '').toLowerCase();
  return !value || normalize(value) === normalize(defaultValue);
}

export function DirectoryField({purpose, label, labelSuffix, value, defaultValue = '', isDefault, hint, disabled, onChange, onReset, onPending}: {
  purpose: 'video-directory' | 'import-directory' | 'log-directory'; label: string; value: string; defaultValue?: string; isDefault?: boolean;
  labelSuffix?: ReactNode; hint?: string; disabled: boolean; onChange: (path: string) => void; onReset?: () => void; onPending?: (pending: boolean) => void;
}) {
  const picker = usePathPicker(onPending);
  return <>
    <PathField label={label} labelSuffix={labelSuffix} value={value} defaultValue={defaultValue} isDefault={isDefault} hint={hint} disabled={disabled || picker.pending}
      onChoose={() => void picker.choose(purpose, value || defaultValue, onChange)} onReset={onReset || (() => onChange(defaultValue))}/>
    <Feedback error>{picker.error}</Feedback>
  </>;
}
