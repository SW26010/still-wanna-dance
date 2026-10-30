import {useEffect,useRef,useState} from 'react';
import {Button,Card,Dialog,DialogActions,DialogBody,DialogContent,DialogSurface,DialogTitle,Link} from '@fluentui/react-components';
import {request} from './api';
import {useApp} from './hooks';
import {Feedback} from './ui';

interface Terms {text:string;version:string;sha256:string;accepted:boolean;needsConfirmation?:boolean}

export function TermsConsent({onAccepted}:{onAccepted:()=>void}) {
  const app=useApp(), [terms,setTerms]=useState<Terms>(), [loading,setLoading]=useState(true), [busy,setBusy]=useState(false), [error,setError]=useState('');
  const mounted=useRef(true), lock=useRef(false);
  async function load() {
    setLoading(true);setError('');
    try {
      const current=await request<Terms>('terms/current');
      if(!mounted.current)return;
      if(current.accepted)onAccepted();else setTerms(current);
    } catch(e) {if(mounted.current)setError('条款暂时无法载入：'+String(e));}
    finally {if(mounted.current)setLoading(false);}
  }
  useEffect(()=>{mounted.current=true;void load();return()=>{mounted.current=false;};},[]);
  async function accept() {
    if(!terms||lock.current)return;
    lock.current=true;setBusy(true);setError('');
    try {
      const result=await request<{warning?:string}>('terms/accept',{version:terms.version,sha256:terms.sha256,agree:true,contentRights:true});
      if(mounted.current){if(result.warning)app.setMessage(result.warning,undefined,true);onAccepted();}
    } catch(e) {if(mounted.current)setError('条款确认未保存：'+String(e));}
    finally {lock.current=false;if(mounted.current)setBusy(false);}
  }
  return <Dialog open modalType="alert"><DialogSurface><DialogBody>
    <DialogTitle>使用条款</DialogTitle><DialogContent>
      {terms?.needsConfirmation&&<Feedback>之前的同意记录无法验证，请重新阅读并确认。</Feedback>}
      <Card role="article" appearance="outline" size="large" className="terms-text" tabIndex={0} aria-label="完整使用条款">{loading?'正在读取条款…':terms?.text||'条款暂时无法载入，请重试。'}</Card>
      <p>同意后立即保存，并按启动设置运行。开启视频加速可能需要管理员授权。</p>
      <div className="actions"><Link href="/terms.txt" target="_blank" rel="noopener">查看／保存纯文本</Link><Link href="/help" target="_blank" rel="noopener">使用帮助（新窗口）</Link></div>
      <Feedback error>{error||app.messageError&&app.message}</Feedback>
      {app.stopped&&<p role="status">正在退出程序。</p>}
    </DialogContent><DialogActions>
      <Button disabled={busy||app.busy||app.stopped} onClick={()=>void app.action('exit')}>不同意并退出</Button>
      {!terms&&<Button disabled={loading||app.stopped} onClick={()=>void load()}>重新读取条款</Button>}
      <Button appearance="primary" disabled={busy||loading||!terms||app.busy||app.stopped} onClick={()=>void accept()}>同意完整条款</Button>
    </DialogActions>
  </DialogBody></DialogSurface></Dialog>;
}
