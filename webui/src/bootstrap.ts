import type { Settings, ThemePreference } from './types';
export type DownloadNumberField = 'prepareAheadSeconds' | 'downloadConcurrencySoftLimit' | 'downloadGrowIntervalMS' | 'localProtectionGraceSeconds';
export interface Bootstrap {
  settingsDefaults: Pick<Settings, DownloadNumberField>;
  theme?: ThemePreference;
  page: 'about' | 'help' | 'terms' | 'console';
  token: string;
  recovery?: { reason: string };
  termsGate?: { pageID: string } | null;
  paths?: Record<string, string>;
  terms?: { text: string; version: string };
  build?: { kind: string; version: string; revision: string; dirty: boolean; builtAtUtc: string; toolchain: string; target: string };
}
export const bootstrap: Bootstrap = JSON.parse(document.getElementById('swd-bootstrap')?.textContent || '{}');
