import {bootstrap,type DownloadNumberField} from './bootstrap';
import {decode,request} from './api';
import type {Settings} from './types';
import {capacityPayload,type CapacityDraft} from './cache-capacity';

const numeric = ['requestRetentionDays','prepareAheadSeconds','downloadConcurrencySoftLimit','downloadGrowIntervalMS','localProtectionGraceSeconds'] as const;
type NumberField = typeof numeric[number];
export interface ProxyReport { id: string; nodes: number; groups: number; name: string; importedAt?: string }
export type Draft = Omit<Settings, NumberField | 'videoBudgetBytes' | 'theme'> & Record<NumberField, string> & CapacityDraft & { theme?: Settings['theme']; proxyImportID: string; proxyImportText: string; proxyImportName: string; proxyImportSource: 'config'|'url'|'base64'; proxyReport?: ProxyReport };
export function draftFrom(s: Settings): Draft {
  const {videoBudgetBytes, theme: _theme, ...values} = s;
  // The backend supplies complete, normalized settings; drafts only format them.
  return { ...values, socks5Password: '',
    requestRetentionDays: String(s.requestRetentionDays),
    prepareAheadSeconds: String(s.prepareAheadSeconds),
    downloadConcurrencySoftLimit: String(s.downloadConcurrencySoftLimit), downloadGrowIntervalMS: String(s.downloadGrowIntervalMS),
    localProtectionGraceSeconds: String(s.localProtectionGraceSeconds),
    videoBudgetGiB: String(videoBudgetBytes / 1073741824), proxyImportID: '', proxyImportText: '', proxyImportName: '', proxyImportSource: 'config' };
}
export function settingsPayload(d: Draft) {
  const { videoStorageMode, videoBudgetGiB, proxyImportText: _text, proxyImportName: _name, proxyReport: _report, proxyImportSource: _source, socks5Password, ...values } = d;
  return { ...values, ...Object.fromEntries(numeric.map(k => [k, k === 'requestRetentionDays' ? Number(d[k]) : downloadNumber(d, k)])), ...capacityPayload({videoStorageMode, videoBudgetGiB}),
    ...(socks5Password || !d.socks5Username ? {socks5Password: socks5Password || ''} : {}) };
}
export function downloadNumber(d: Draft, key: DownloadNumberField) {
  return Number(d[key].trim() || bootstrap.settingsDefaults[key]);
}
export function fingerprint(d: Draft) {
  return JSON.stringify({...settingsPayload(d), emptyNumbers: numeric.filter(k => k !== 'requestRetentionDays' && !d[k].trim()), proxyImportText: d.proxyImportText, proxyImportName: d.proxyImportName, proxyImportSource: d.proxyImportText ? d.proxyImportSource : ''});
}
export const releaseImport = async (id: string) => { if (id) try { await request('proxy/import/discard', {id}); } catch { /* Local discard remains available offline; server expiry is the fallback. */ } };

export function hasProxyImport(d: Draft) {
  return d.proxyImportSource==='config'?!!d.proxyImportName:!!d.proxyImportText.trim();
}
// Explicit import and Save settings share this request and draft transition.
export async function importProxyConfiguration(d: Draft): Promise<ProxyReport> {
  const headers:Record<string,string>={'Content-Type':'text/plain','X-SWD-Token':bootstrap.token,'X-SWD-Import-Source':d.proxyImportSource};
  if(d.proxyImportSource==='config'&&d.proxyImportName)headers['Content-Disposition']="attachment; filename*=UTF-8''"+encodeURIComponent(d.proxyImportName).replaceAll("'",'%27');
  return decode<ProxyReport>(await fetch('/api/proxy/import',{method:'POST',headers,body:d.proxyImportText,signal:AbortSignal.timeout(100000)}));
}
export function importedProxyDraft(report:ProxyReport) {
  return {proxyImportID:report.id,proxyImportText:'',proxyImportName:'',proxyReport:report};
}
