import {useEffect,useRef,useState} from 'react';
import {hasProxyImport,importProxyConfiguration,importedProxyDraft,releaseImport,type Draft} from './settings-draft';

export function useSettingsProxy(d:Draft,update:(patch:Partial<Draft>)=>void,disabled:boolean,onPending:(pending:boolean)=>void) {
  const [pending, setPending] = useState(false), [error, setError] = useState('');
  useEffect(()=>setError(''),[d.proxyImportID,d.proxyImportText,d.proxyImportSource,d.proxyBackend,d.upstreamMode]);
  const generation = useRef(0), current = useRef(d); current.current = d;
  useEffect(() => () => { generation.current++; }, []);
  const busy = (value: boolean) => { setPending(value); onPending(value); };
  async function readFile(file?: File) {
    if (!file) return;
    const id = ++generation.current; busy(true); setError('');
    try { if (file.size > 2 * 1024 * 1024) throw new Error('配置文件超过 2 MiB。'); const text = await file.text(); if (id === generation.current) update({proxyImportText: text,proxyImportName:file.name,proxyImportSource: 'config'}); }
    catch (e) { if (id === generation.current) setError(String(e)); }
    finally { if (id === generation.current) busy(false); }
  }
  async function inspect() {
    if (pending || disabled || !hasProxyImport(d)) return;
    const id = ++generation.current, content = d.proxyImportText; busy(true); setError('');
    try {
      const report = await importProxyConfiguration(d);
      if (id !== generation.current || current.current.proxyImportText !== content) { void releaseImport(report.id || ''); return; }
      void releaseImport(current.current.proxyImportID);
      update(importedProxyDraft(report));
    } catch (e) { if (id === generation.current) setError(String(e)); }
    finally { if (id === generation.current) busy(false); }
  }
  return {pending,error,readFile,inspect,clearError:()=>setError('')};
}
