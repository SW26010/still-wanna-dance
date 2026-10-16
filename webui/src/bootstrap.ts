export interface Bootstrap {
  page: 'about' | 'help' | 'terms' | 'console';
  token: string;
  termsGate?: { pageID: string; step: string } | null;
  paths?: Record<string, string>;
  terms?: { text: string; version: string };
}
export const bootstrap: Bootstrap = JSON.parse(document.getElementById('swd-bootstrap')?.textContent || '{}');
