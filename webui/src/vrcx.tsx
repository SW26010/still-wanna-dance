import { createContext, useContext, useEffect, useRef, useState, type PropsWithChildren } from 'react';
import { Button, Checkbox, Caption1, useRestoreFocusTarget } from '@fluentui/react-components';
import { APIError, request } from './api';
import { useApp } from './hooks';
import { Feedback, Notification, Panel } from './ui';
import { PathField, usePathPicker } from './path-picker';

interface Preview {
  id: string; source: string; databasePath: string; total: number; recognized: number;
  events: number; duplicates: number; invalid: number; unrelated: number; skipped: number;
  songs: number; firstAt: number; lastAt: number; existingSongs: number; imported: boolean;
}
const release = async (id?: string) => {
  if (id) try { await request('vrcx/discard', {id}); } catch { /* Server expires the bounded preview. */ }
};
function useVRCXImport(target: string) {
  const app = useApp();
  const picker = usePathPicker();
  const [selected, setSelected] = useState<boolean>(), [existingSongs, setExistingSongs] = useState<number>();
  const [path, setPath] = useState(''), [defaultPath, setDefaultPath] = useState(''), [found, setFound] = useState<boolean>(), [targetPending, setTargetPending] = useState(true), [targetError, setTargetError] = useState('');
  const [preview, setPreview] = useState<Preview>(), [pending, setPending] = useState(false), [message, setMessage] = useState('');
  const [detectedSuggestion, setSuggested] = useState(false), [notify,setNotify] = useState(false);
  const suggested = detectedSuggestion && !!app.status.data?.vrcxImportAvailable;
  const mounted = useRef(true), controller = useRef<AbortController | undefined>(undefined), current = useRef<Preview | undefined>(undefined), lock = useRef(false), revision = useRef(0);
  const enabled = selected ?? existingSongs === 0;
  const locked = app.unavailable || pending || picker.pending || targetPending;
  const busy = (value: boolean) => { lock.current = value; setPending(value); };
  function clear() {
    revision.current++; controller.current?.abort();
    void release(current.current?.id); current.current = undefined; setPreview(undefined); setMessage('');
  }
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; controller.current?.abort(); void release(current.current?.id); }; }, []);
  useEffect(() => { clear(); setSelected(undefined); }, [target]);
  useEffect(() => {
    let canceled = false;
    setTargetPending(true); setExistingSongs(undefined); setTargetError('');
    setSuggested(false);
    void request<{path:string;found:boolean;suggested?:boolean;notify?:boolean;existingSongs?:number;targetError?:string}>('vrcx/detect', {}).then(result => {
      if (!canceled) { setDefaultPath(result.path); setFound(result.found); setExistingSongs(result.existingSongs); setTargetError(result.targetError || ''); setSuggested(!!result.suggested); setNotify(!!result.notify); }
    }).catch(() => { if (!canceled) { setFound(false); setTargetError('无法检查目标歌曲统计'); } })
      .finally(() => { if(!canceled)setTargetPending(false); });
    return () => { canceled = true; };
  }, [target]);
  async function choose() {
    if (lock.current || locked) return;
    await picker.choose('vrcx-database', path || defaultPath, value => { clear(); setPath(value); });
  }
  async function commit(p:Preview) {
    const result = await request<Preview>('vrcx/apply', {id:p.id, overwrite:p.existingSongs > 0}, AbortSignal.timeout(35000));
    if (mounted.current) { setPreview(result); current.current = result; setSuggested(false); app.refresh(); }
    return result;
  }
  async function inspect(initialize = false) {
    if ((!enabled && !initialize) || lock.current || locked) return;
    clear(); busy(true);
    if (initialize) setSelected(true);
    const checkedRevision = revision.current, abort = new AbortController(); controller.current = abort;
    let applying = false;
    try {
      const result = await request<Preview>('vrcx/preview', {path:initialize?'':path}, AbortSignal.any([abort.signal, AbortSignal.timeout(35000)]));
      if (!mounted.current || checkedRevision !== revision.current) { void release(result.id); return; }
      setPreview(result); current.current = result;
      if (initialize) {
        setPath('');
        if (!result.id || result.events <= 0 || result.existingSongs > 0) return {review:true};
        applying = true;
        return {imported:await commit(result)};
      }
    } catch (e) {
      if (mounted.current && checkedRevision === revision.current) {
        if (applying && e instanceof APIError && e.status === 409) clear();
        setMessage(e instanceof APIError ? e.message : applying?'导入结果尚未确认。可再次点击原导入按钮核对，同一预览不会重复导入。':'检查未完成，请重试；尚未导入。');
        if (applying) return {review:true};
      }
    } finally { if (mounted.current) busy(false); }
  }
  async function dismiss() {
    if (lock.current || locked) return;
    busy(true); setMessage('');
    try { await request('vrcx/dismiss', {}); if (mounted.current) setSuggested(false); }
    catch { if (mounted.current) setMessage('未能忽略提示，请重试。'); }
    finally { if (mounted.current) busy(false); }
  }
  async function confirmOverwrite() {
    const p = current.current;
    return !p?.id || p.events <= 0 || !p.existingSongs || app.confirm('覆盖全部歌曲需求统计？',
      <><p>现有 {p.existingSongs} 首歌曲的需求次数、分数和最后需求时间将整体被替换，不会与 VRCX 结果累加。未出现在有效历史中的旧统计也会删除。</p><p>已下载视频、歌曲资料和设置保持不变。导入到：{p.databasePath}</p></>,
      '强制导入并覆盖');
  }
  async function apply() {
    const p = current.current;
    if (!p?.id || p.imported || lock.current || locked) return;
    lock.current = true; setMessage('');
    try {
      if (!await confirmOverwrite() || !mounted.current) return;
      busy(true);
      await commit(p);
    } catch (e) {
      if (mounted.current) {
        if (e instanceof APIError && e.status === 409) clear();
        setMessage(e instanceof APIError ? e.message : '导入结果尚未确认。可再次点击原导入按钮核对，同一预览不会重复导入。');
      }
    } finally { if (mounted.current) busy(false); }
  }
  return {target, enabled, path, defaultPath, found, suggested, notify, preview, pending, targetPending, targetError, message, pickerError:picker.error, locked, choose, inspect, apply, dismiss, confirmOverwrite,
    resetPath: () => { clear(); setPath(''); },
    toggle: (value:boolean) => { clear(); setSelected(value); }};
}
type VRCXModel = ReturnType<typeof useVRCXImport>;
const VRCXContext = createContext<VRCXModel | null>(null);
export function useVRCX() {
  const model = useContext(VRCXContext);
  if (!model) throw new Error('VRCXProvider is required');
  return model;
}
export function VRCXProvider({target,page,children}:PropsWithChildren<{target:string;page:string}>) {
  const model = useVRCXImport(target), app = useApp();
  const announced = useRef(false), [notice,setNotice] = useState(false);
  useEffect(() => {
    if (!model.suggested || !model.notify || page === 'library' || app.stopped) setNotice(false);
    if (!model.suggested || !model.notify || announced.current || app.stopped) return;
    announced.current = true;
    // Like other startup notices, remember only a tab-local acknowledgement.
    try {
      if (sessionStorage.getItem('swd-vrcx-suggestion')) return;
      sessionStorage.setItem('swd-vrcx-suggestion','shown');
    } catch { /* Optional storage; in-page deduplication still applies. */ }
    if (page !== 'library') setNotice(true);
  },[model.suggested,model.notify,page,app.stopped]);
  return <VRCXContext.Provider value={model}>
    {notice&&<Notification message="检测到 VRCX 数据库" body="可在曲库导入历史，安排歌曲下载与保留顺序。确认前不会读取内容。" action={{href:'#/library',label:'前往曲库'}}/>}
    {children}
  </VRCXContext.Provider>;
}

export function VRCXOption({model:m}: {model:VRCXModel}) {
  return <div className="content-stack">
    <Checkbox label="从 VRCX 导入历史偏好" checked={m.enabled} disabled={m.locked} onChange={(_, data) => m.toggle(!!data.checked)} />
    <Caption1 as="p" className="hint">根据 VRCX 历史，优先下载和保留你常用的歌曲。</Caption1>
  </div>;
}

export function VRCXImport({model:m}: {model:VRCXModel}) {
  const restoreFocusTarget = useRestoreFocusTarget(), p = m.preview;
  return <Panel id="vrcx-import" title="导入 VRCX 历史">
    <VRCXOption model={m}/>
    {m.enabled && <>
      {m.found === false && <p>未找到默认位置的 VRCX 数据库，可选择其他位置或跳过。</p>}
      <PathField label="VRCX 数据库路径" hint="默认查找当前用户的 VRCX.sqlite3；支持自定义位置。" value={m.path} defaultValue={m.defaultPath} isDefault={!m.path} disabled={m.locked}
        onChoose={() => void m.choose()} onReset={m.resetPath}/>
      <p className="wrap">导入到当前数据库：{m.target}</p>
      <div className="actions"><Button type="button" disabled={m.locked} onClick={() => void m.inspect()}>读取并预览</Button></div>
      {p && <div className="content-stack">
        <p className="wrap">数据源：{p.source}</p>
        <dl>
          <div><dt>可识别 WannaDance 事件</dt><dd>{p.recognized}</dd></div>
          <div><dt>去重后有效需求 / 歌曲</dt><dd>{p.events} 次 / {p.songs} 首</dd></div>
          <div><dt>时间范围</dt><dd>{p.events ? new Date(p.firstAt).toLocaleString()+' ～ '+new Date(p.lastAt).toLocaleString() : '无有效记录'}</dd></div>
          <div><dt>跳过记录</dt><dd>{p.skipped} 条（重复 {p.duplicates} · 无效 {p.invalid} · 无关 {p.unrelated}）</dd></div>
        </dl>
        {p.imported ? <p role="status">已导入 {p.events} 次历史需求、{p.songs} 首歌曲，跳过 {p.skipped} 条记录。</p>
          : !p.events ? <Feedback>没有有效历史需求，原统计保持不变。</Feedback>
          : <><p>{p.existingSongs ? `已有 ${p.existingSongs} 首歌曲的需求统计。导入须确认整体覆盖，不会累加。` : '当前歌曲需求统计为空，可以直接导入。'}预览 15 分钟内有效；导入立即生效。</p>
            <Button {...restoreFocusTarget} type="button" appearance="primary" disabled={m.locked} onClick={() => void m.apply()}>{p.existingSongs ? '导入并覆盖…' : '导入历史偏好'}</Button></>}
      </div>}
    </>}
    <p role="status">{m.pending ? '正在处理 VRCX 历史偏好…' : ''}</p>
    <Feedback error>{m.message}</Feedback>
    <Feedback error>{m.pickerError}</Feedback>
  </Panel>;
}
