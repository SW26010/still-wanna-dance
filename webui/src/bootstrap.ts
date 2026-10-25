import type { ThemePreference } from './types';
export interface Bootstrap {
  theme?: ThemePreference;
  page: 'about' | 'help' | 'terms' | 'console';
  token: string;
  termsGate?: { pageID: string } | null;
  paths?: Record<string, string>;
  terms?: { text: string; version: string };
  build?: { kind: string; version: string; revision: string; dirty: boolean; builtAtUtc: string; toolchain: string; target: string };
}
export const bootstrap: Bootstrap = JSON.parse(document.getElementById('swd-bootstrap')?.textContent || '{}');
