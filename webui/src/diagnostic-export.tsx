import {useEffect,useLayoutEffect,useRef,useState} from 'react';
import {Button,Field,Input,Textarea,MessageBar,MessageBarBody} from '@fluentui/react-components';
import {bootstrap} from './bootstrap';
import {decode} from './api';
import {NumberInput} from './number-input';

const localTime=(at:number)=>{const d=new Date(at);return new Date(at-d.getTimezoneOffset()*60000).toISOString().slice(0,19);};

export function DiagnosticExport() {
  const [from,setFrom]=useState(()=>localTime(Date.now()-86400000)),[to,setTo]=useState(()=>localTime(Date.now())),[note,setNote]=useState('');
  const [hours,setHours]=useState<string|null>('24');
  const [busy,setBusy]=useState(false),[message,setMessage]=useState(''),[error,setError]=useState(false);
  const controller=useRef<AbortController|null>(null);
  const downloadButton=useRef<HTMLButtonElement>(null),focusedCancel=useRef<HTMLButtonElement|null>(null);
  useLayoutEffect(()=>{
    if(!busy&&focusedCancel.current){
      if(!focusedCancel.current.isConnected&&document.activeElement===document.body&&downloadButton.current?.getClientRects().length)downloadButton.current.focus();
      focusedCancel.current=null;
    }
  },[busy]);
  useEffect(()=>()=>controller.current?.abort(),[]);
  function recent(value:string) {
    setHours(value);
    const count=Number(value),now=Date.now();
    if(Number.isFinite(count)&&count>0&&count<now/3600000){setFrom(localTime(now-count*3600000));setTo(localTime(now));}
  }
  async function download() {
    const count=hours===null?null:Number(hours);
    if(count!==null&&(!Number.isFinite(count)||count<=0||count>=Date.now()/3600000)){setError(true);setMessage('请输入有效的最近小时数，必须大于 0。');return;}
    const end=count===null?new Date(to).getTime():Date.now(),start=count===null?new Date(from).getTime():end-count*3600000;
    if(count!==null){setFrom(localTime(start));setTo(localTime(end));}
    if(!Number.isFinite(start)||!Number.isFinite(end)||start<=0||end<=start||end>Date.now()+60000){setError(true);setMessage('请选择有效的起止时间，结束时间应晚于开始且不晚于现在。');return;}
    const abort=new AbortController();controller.current=abort;setBusy(true);setError(false);setMessage('正在收集诊断信息…');
    try {
      const response=await fetch('/api/diagnostics/export',{method:'POST',headers:{'Content-Type':'application/json','X-SWD-Token':bootstrap.token},body:JSON.stringify({from:start,to:end,note}),signal:AbortSignal.any([abort.signal,AbortSignal.timeout(150000)]),cache:'no-store'});
      if(!response.ok)await decode<never>(response);
      const url=URL.createObjectURL(await response.blob()),link=document.createElement('a');
      link.href=url;link.download='swd-diagnostics.zip';link.click();setTimeout(()=>URL.revokeObjectURL(url),1000);
      setMessage(response.headers.get('X-SWD-Diagnostics-Partial')==='true'?'诊断包已生成，部分资料未收集，请查看包内说明和 manifest.json。':'诊断包已生成，已交给浏览器下载。');
    } catch(e) {if(!abort.signal.aborted){setError(true);setMessage(e instanceof Error?e.message:'诊断包生成失败，请重试。');}}
    finally {setBusy(false);controller.current=null;}
  }
  return <section className="form-grid diagnostic-export" aria-label="收集诊断信息">
      <div className="diagnostic-range">
        <div className="diagnostic-dates">
          <Field label="开始时间"><Input type="datetime-local" step={1} value={from} disabled={busy} onChange={(_,v)=>{setFrom(v.value);setHours(null);}}/></Field>
          <Field label="结束时间"><Input type="datetime-local" step={1} value={to} disabled={busy} onChange={(_,v)=>{setTo(v.value);setHours(null);}}/></Field>
        </div>
        <Field className="diagnostic-hours" label="最近" orientation="horizontal"><NumberInput aria-label="最近小时数" value={hours??''} min={0.01} precision={2} unit="小时" stepLabel="最近小时数" placeholder="自定义" disabled={busy} onChange={recent}/></Field>
      </div>
      <Field label="问题说明（可选）"><Textarea value={note} maxLength={2000} disabled={busy} placeholder="例如：发生了什么、当时正在做什么" onChange={(_,v)=>setNote(v.value)}/></Field>
      <p className="secondary">分享前请检查包内的个人路径、玩家、房间和链接等信息，勿分享密码或代理凭据。</p>
      {message&&<MessageBar intent={error?'error':'info'}><MessageBarBody><span role="status">{message}</span></MessageBarBody></MessageBar>}
      <div className="actions"><Button ref={downloadButton} appearance="primary" disabled={busy} onClick={()=>void download()}>{busy?'正在收集…':'下载诊断包'}</Button>{busy&&<Button onFocus={event=>{focusedCancel.current=event.currentTarget;}} onClick={()=>{controller.current?.abort();setMessage('已取消收集。');}}>取消</Button>}</div>
  </section>;
}
