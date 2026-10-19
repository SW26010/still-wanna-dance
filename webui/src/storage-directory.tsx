import type { ReactNode } from 'react';
import type { Settings } from './types';
import { Details } from './ui';

export function StorageDirectoryFields({ mode, disabled, children }: {
  mode: Settings['videoStorageMode']; disabled: boolean; children: (disabled: boolean) => ReactNode;
}) {
  const retained = mode !== 'none';
  return <Details key={retained ? 'retained' : 'none'} title="本地存储目录" open={retained}>
    <div className="form-grid">{children(disabled || !retained)}</div>
  </Details>;
}
