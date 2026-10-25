import type { ReactNode } from 'react';
import type { Settings } from './types';

export function StorageDirectoryFields({ mode, disabled, children }: {
  mode: Settings['videoStorageMode']; disabled: boolean; children: (disabled: boolean) => ReactNode;
}) {
  const retained = mode !== 'none';
  return <div className="form-grid">{children(disabled || !retained)}</div>;
}
