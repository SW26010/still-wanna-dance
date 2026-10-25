import { useCallback, useEffect, useLayoutEffect, useRef, useState, createContext, useContext, type ReactNode, type FocusEvent } from 'react';
import { Button, Dialog, DialogSurface, DialogBody, DialogTitle, DialogContent, DialogActions } from '@fluentui/react-components';
import { APIError, request, hostsActionTimeout, hostsActionPending, unconfirmedAction } from './api';
import { bootstrap } from './bootstrap';
import { active, type Status } from './types';
import { usePageFeedback } from './page-feedback';

// When an update removes the focused row, return focus to its labelled region.
export function useFocusRecovery() {
  const ref=useRef<HTMLDivElement>(null), focused=useRef<HTMLElement>(null);
  useLayoutEffect(()=>{
    if(focused.current && !focused.current.isConnected){
      if(document.activeElement===document.body)ref.current?.focus();
      focused.current=null;
    }
  });
  return {ref,onFocusCapture:(event:FocusEvent<HTMLDivElement>)=>{focused.current=event.target as HTMLElement;}};
}

// One request at a time, abort stale reads on route/save/pause, suspend hidden tabs.
export function useResource<T>(path: string, enabled = true, revision = 0, interval = 5000, retain = false) {
  const [data, setData] = useState<T>(), [error, setError] = useState(''), [loading, setLoading] = useState(false);
  const [loadedRevision, setLoadedRevision] = useState<number>();
  const [refreshKey, setRefreshKey] = useState(0);
  const refresh = useCallback(() => setRefreshKey(v => v + 1), []);
  useEffect(() => {
    if (!enabled) return;
    let disposed = false, pending = false, rerun = false, timer: ReturnType<typeof setTimeout>;
    const controller = new AbortController();
    const run = async () => {
      clearTimeout(timer);
      if (disposed || document.hidden) return;
      if (pending) { rerun = true; return; }
      pending = true; setLoading(true);
      try {
        const next = await request<T>(path, undefined, AbortSignal.any([controller.signal, AbortSignal.timeout(35000)]));
        if (!disposed) { setData(next); setLoadedRevision(revision); setError(''); }
      } catch (e) { if (!disposed) { setError(String(e)); if (!retain) setData(undefined); } }
      finally {
        pending = false;
        if (!disposed) { setLoading(false); if (rerun) { rerun = false; void run(); } else if (interval > 0) timer = setTimeout(run, interval); }
      }
    };
    const visibility = () => { if (document.hidden) clearTimeout(timer); else void run(); };
    void run(); document.addEventListener('visibilitychange', visibility);
    return () => { disposed = true; clearTimeout(timer); controller.abort(); document.removeEventListener('visibilitychange', visibility); };
  }, [path, enabled, revision, refreshKey, interval, retain]);
  return { data, error, loading, refresh, loadedRevision };
}

export function useConfirmation() {
  const [prompt, setPrompt] = useState<{title: string; content: ReactNode; confirmLabel:string}>();
  const resolve = useRef<(answer: boolean) => void>(() => {});
  const confirm = (title: string, content: ReactNode, confirmLabel='确认') => new Promise<boolean>(done => { resolve.current(false); resolve.current = done; setPrompt({title, content, confirmLabel}); });
  const close = (answer: boolean) => { resolve.current(answer); setPrompt(undefined); };
  useEffect(() => () => resolve.current(false), []);
  const dialog = <Dialog open={!!prompt} onOpenChange={(_, d) => { if (!d.open) close(false); }}><DialogSurface>
    <DialogBody><DialogTitle>{prompt?.title}</DialogTitle><DialogContent>{prompt?.content}</DialogContent>
    <DialogActions><Button onClick={() => close(false)}>取消</Button><Button appearance="primary" onClick={() => close(true)}>{prompt?.confirmLabel}</Button></DialogActions></DialogBody>
  </DialogSurface></Dialog>;
  return {confirm, dialog};
}

export async function reconnect() {
  const end = Date.now() + 60000;
  while (Date.now() < end) {
    try {
      const response = await fetch('/', {cache: 'no-store', signal: AbortSignal.timeout(2000)});
      const doc = new DOMParser().parseFromString(await response.text(), 'text/html');
      const token = doc.querySelector<HTMLMetaElement>('meta[name="swd-token"]')?.content;
      if (response.ok && token && token !== bootstrap.token) { location.reload(); return; }
    } catch { /* Listener is unavailable during restart. */ }
    await new Promise(r => setTimeout(r, 500));
  }
  throw new Error('60 秒内未连接到新进程。请刷新页面并核对日志，勿重复提交原操作。');
}

const actionMessages: Record<string,string> = {
  settings:'设置已保存。',
  'settings/theme':'主题已应用。',
  'activation/enable':'视频加速已开启。',
  'batch/scan':'已开始检查缺失视频。', 'batch/start':'已开始下载缺失视频。',
  'batch/cancel':'已请求停止批量任务。', 'inventory/scan':'已开始更新清单并统计覆盖率。',
  'import/start':'已开始扫描并导入视频。', 'import/cleanup':'已处理源文件清理，请查看导入结果。',
};
export function useConsole(enabled: boolean, page: string) {
  const [revision, setRevision] = useState(0), [busy, setBusy] = useState(false), [stopped, setStopped] = useState(false);
  const feedback = usePageFeedback<{text:string;error:boolean}>(page);
  const setMessage = (text:string,destination?:string,error=false)=>feedback.setValue({text,error},destination);
  const message = feedback.value?.text || '';
  const messageError=feedback.value?.error || false;
  const [dirtySettings,setDirtySettings]=useState<Set<string>>(()=>new Set());
  const setSettingsDirty=useCallback((section:string,dirty:boolean)=>setDirtySettings(previous=>{
    if(previous.has(section)===dirty)return previous;
    const next=new Set(previous);if(dirty)next.add(section);else next.delete(section);return next;
  }),[]);
  const lock = useRef(false);
  const status = useResource<Status>('status', enabled && !stopped, revision, 5000, true);
  const refresh = () => { setRevision(v => v + 1); };
  const confirmation = useConfirmation();
  async function action(path: string, body: unknown = {}): Promise<boolean> {
    if (lock.current || stopped) return false;
    if (path === 'restart' && status.data?.import.running && !await confirmation.confirm('导入仍在运行', '导入仍在运行。现在重启会中断导入且不会自动续传。建议等导入结束再重启；确定现在中断并重启吗？')) return false;
    if (lock.current || stopped) return false;
    lock.current = true; setBusy(true);
    const hosts = path.startsWith('hosts/') || path === 'activation/enable' || path === 'start';
    const restorePermission = path === 'settings' && typeof body === 'object' && body !== null && 'restoreHostsOnExit' in body && body.restoreHostsOnExit === true && status.data?.settings.restoreHostsOnExit === false;
    setMessage(hosts ? hostsActionPending : restorePermission ? '正在准备退出恢复权限，请留意管理员授权。' : '正在处理…');
    try {
      await request(path, body, AbortSignal.timeout(hosts || restorePermission ? hostsActionTimeout : 30000));
      if (path === 'exit') { setStopped(true); setMessage('正在退出应用并处理游戏连接。'); return true; }
      if (path === 'restart') { setStopped(true); setMessage('正在重启应用，连接恢复后自动刷新。'); await reconnect(); return true; }
      setMessage(actionMessages[path] || (path.startsWith('upstream/check') ? '网络检测已开始，请查看检测结果。' : '请求已接受，请查看当前任务状态。')); return true;
    } catch (e) {
      if (path === 'restart' && !(e instanceof APIError)) {
        setStopped(true); setMessage('重启响应未确认，正在等待新进程…');
        try { await reconnect(); } catch (problem) { setMessage(String(problem),undefined,true); } return false;
      }
      setMessage(e instanceof APIError ? e.message : unconfirmedAction,undefined,true); return false;
    } finally { lock.current = false; setBusy(false); refresh(); }
  }
  return { status, revision, busy, stopped, dirtySettings, setSettingsDirty, message, messageError, setMessage, refresh, action, confirm: confirmation.confirm, dialog: confirmation.dialog,
    unavailable: busy || stopped || !status.data || !!status.error || active(status.data) };
}
type ConsoleModel = ReturnType<typeof useConsole>;
export const ConsoleContext = createContext<ConsoleModel | null>(null);
export const useApp = () => { const model = useContext(ConsoleContext); if (!model) throw new Error('Missing console provider'); return model; };
